package drain

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/sebas/switchboard/internal/signaling/mediaclient"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

// MaxConcurrentMigrations limits parallel re-INVITE operations
const MaxConcurrentMigrations = 5

// Coordinator orchestrates the drain process for RTP manager nodes
type Coordinator struct {
	mu sync.RWMutex

	pool     *mediaclient.Pool
	migrator *Migrator

	// Active drains by node ID
	activeDrains map[string]*drainOperation
}

// drainOperation tracks a single node's drain progress
type drainOperation struct {
	status    DrainStatus
	cancel    context.CancelFunc
	completed chan struct{}
}

// NewCoordinator creates a new drain coordinator
func NewCoordinator(pool *mediaclient.Pool, migrator *Migrator) *Coordinator {
	return &Coordinator{
		pool:         pool,
		migrator:     migrator,
		activeDrains: make(map[string]*drainOperation),
	}
}

// StartDrain initiates drain for a node
func (c *Coordinator) StartDrain(ctx context.Context, req DrainRequest) (*DrainStatus, error) {
	c.mu.Lock()

	// Check if drain is already in progress
	if _, exists := c.activeDrains[req.NodeID]; exists {
		c.mu.Unlock()
		return nil, fmt.Errorf("drain already in progress for node %s", req.NodeID)
	}

	// Mark node as draining in the pool
	if err := c.pool.StartDrain(req.NodeID); err != nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("failed to start drain: %w", err)
	}

	// Set up the drain operation
	timeout := req.Timeout
	if timeout == 0 {
		timeout = DefaultDrainTimeout(req.Mode)
	}

	drainCtx, cancel := context.WithTimeout(ctx, timeout)
	sessions := c.pool.SessionsOnNode(req.NodeID)

	op := &drainOperation{
		status: DrainStatus{
			NodeID:        req.NodeID,
			State:         mediaclient.StateDraining,
			Mode:          req.Mode,
			StartedAt:     time.Now(),
			TotalSessions: len(sessions),
		},
		cancel:    cancel,
		completed: make(chan struct{}),
	}

	c.activeDrains[req.NodeID] = op
	c.mu.Unlock()

	slog.Info("[DrainCoordinator] Drain started",
		"node_id", req.NodeID,
		"mode", req.Mode,
		"total_sessions", len(sessions),
		"timeout", timeout)

	// Start drain in background
	go c.runDrain(drainCtx, op, req.NodeID, sessions)

	return &op.status, nil
}

// runDrain executes the drain process
func (c *Coordinator) runDrain(ctx context.Context, op *drainOperation, nodeID string, sessions []string) {
	defer close(op.completed)
	defer op.cancel()

	if len(sessions) == 0 {
		// No sessions to migrate, complete immediately
		c.completeDrain(nodeID, op)
		return
	}

	// Find a healthy target node
	targetNodeID, err := c.findTargetNode(nodeID)
	if err != nil {
		c.failDrain(nodeID, op, fmt.Errorf("no healthy target node: %w", err))
		return
	}

	slog.Info("[DrainCoordinator] Migrating sessions",
		"node_id", nodeID,
		"target_node", targetNodeID,
		"session_count", len(sessions))

	// Migrate sessions with bounded concurrency
	sem := semaphore.NewWeighted(MaxConcurrentMigrations)
	g, gCtx := errgroup.WithContext(ctx)

	var migratedCount, failedCount int
	var countMu sync.Mutex

	for _, sessionID := range sessions {
		sessionID := sessionID // capture for goroutine

		g.Go(func() error {
			// Acquire semaphore
			if err := sem.Acquire(gCtx, 1); err != nil {
				slog.Warn("[DrainCoordinator] Semaphore acquire failed",
					"session_id", sessionID,
					"error", err)
				return err
			}
			defer sem.Release(1)

			slog.Debug("[DrainCoordinator] Starting migration for session",
				"session_id", sessionID,
				"target_node", targetNodeID)

			// Attempt migration
			err := c.migrator.MigrateSession(gCtx, sessionID, targetNodeID)

			countMu.Lock()
			if err != nil {
				// Check if this is a B-leg that was skipped (will be migrated with A-leg)
				if err == ErrSkipBLeg {
					slog.Debug("[DrainCoordinator] B-leg session skipped (migrated with A-leg)",
						"session_id", sessionID)
					// Don't count as failed - it will be migrated with its A-leg
				} else {
					failedCount++
					op.status.FailedCount = failedCount
					op.status.Errors = append(op.status.Errors, SessionError{
						SessionID: sessionID,
						Error:     err.Error(),
						Timestamp: time.Now(),
					})
					slog.Warn("[DrainCoordinator] Session migration failed",
						"session_id", sessionID,
						"target_node", targetNodeID,
						"error", err)
				}
			} else {
				migratedCount++
				op.status.MigratedCount = migratedCount
				slog.Info("[DrainCoordinator] Session migrated successfully",
					"session_id", sessionID,
					"target_node", targetNodeID)
			}
			countMu.Unlock()

			// In graceful mode, continue even if one fails
			// In aggressive mode, we could return the error to stop
			return nil
		})
	}

	// Wait for all migrations to complete
	if err := g.Wait(); err != nil && err != context.Canceled {
		slog.Warn("[DrainCoordinator] Drain operation interrupted",
			"node_id", nodeID,
			"error", err)
	}

	// Check final state
	c.mu.Lock()
	remaining := c.pool.SessionsOnNode(nodeID)
	c.mu.Unlock()

	if len(remaining) == 0 {
		c.completeDrain(nodeID, op)
	} else {
		slog.Warn("[DrainCoordinator] Drain incomplete, sessions remaining",
			"node_id", nodeID,
			"remaining", len(remaining),
			"migrated", migratedCount,
			"failed", failedCount)
		// Keep node in draining state - operator can check status
	}
}

// findTargetNode finds a healthy, active node to migrate sessions to
func (c *Coordinator) findTargetNode(excludeNodeID string) (string, error) {
	stats := c.pool.Stats()

	for _, member := range stats.Members {
		if member.NodeID != excludeNodeID &&
			member.Healthy &&
			member.DrainState == mediaclient.StateActive {
			return member.NodeID, nil
		}
	}

	return "", fmt.Errorf("no healthy active nodes available")
}

// completeDrain marks drain as complete and disables the node
func (c *Coordinator) completeDrain(nodeID string, op *drainOperation) {
	if err := c.pool.CompleteDrain(nodeID); err != nil {
		slog.Error("[DrainCoordinator] Failed to complete drain",
			"node_id", nodeID,
			"error", err)
		return
	}

	c.mu.Lock()
	op.status.State = mediaclient.StateDisabled
	c.mu.Unlock()

	slog.Info("[DrainCoordinator] Drain completed successfully",
		"node_id", nodeID,
		"migrated", op.status.MigratedCount,
		"failed", op.status.FailedCount)
}

// failDrain records drain failure
func (c *Coordinator) failDrain(nodeID string, op *drainOperation, err error) {
	slog.Error("[DrainCoordinator] Drain failed",
		"node_id", nodeID,
		"error", err)

	// Cancel drain and return node to active
	if cancelErr := c.pool.CancelDrain(nodeID); cancelErr != nil {
		slog.Error("[DrainCoordinator] Failed to cancel drain",
			"node_id", nodeID,
			"error", cancelErr)
	}

	c.mu.Lock()
	op.status.State = mediaclient.StateActive
	op.status.Errors = append(op.status.Errors, SessionError{
		SessionID: "",
		Error:     err.Error(),
		Timestamp: time.Now(),
	})
	c.mu.Unlock()
}

// GetDrainStatus returns the current status of a drain operation
func (c *Coordinator) GetDrainStatus(nodeID string) (*DrainStatus, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	op, exists := c.activeDrains[nodeID]
	if !exists {
		// Check if node exists but isn't draining
		member := c.pool.GetMemberByID(nodeID)
		if member == nil {
			return nil, fmt.Errorf("node not found: %s", nodeID)
		}

		// Return current state from pool stats
		stats := c.pool.Stats()
		for _, m := range stats.Members {
			if m.NodeID == nodeID {
				return &DrainStatus{
					NodeID:        nodeID,
					State:         m.DrainState,
					TotalSessions: m.SessionCount,
				}, nil
			}
		}

		return nil, fmt.Errorf("node not found in stats: %s", nodeID)
	}

	// Return copy of status
	statusCopy := op.status
	return &statusCopy, nil
}

// CancelDrain cancels an in-progress drain and returns node to active
func (c *Coordinator) CancelDrain(nodeID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	op, exists := c.activeDrains[nodeID]
	if !exists {
		return fmt.Errorf("no drain in progress for node %s", nodeID)
	}

	// Cancel the context to stop migrations
	op.cancel()

	// Return node to active state
	if err := c.pool.CancelDrain(nodeID); err != nil {
		return fmt.Errorf("failed to cancel drain: %w", err)
	}

	// Remove from active drains
	delete(c.activeDrains, nodeID)

	slog.Info("[DrainCoordinator] Drain cancelled",
		"node_id", nodeID)

	return nil
}

// Cleanup removes completed drain operations from tracking
func (c *Coordinator) Cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for nodeID, op := range c.activeDrains {
		select {
		case <-op.completed:
			// Drain is complete, remove from tracking
			delete(c.activeDrains, nodeID)
		default:
			// Still in progress
		}
	}
}
