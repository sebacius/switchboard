package b2bua

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/sebas/switchboard/services/signaling/transport"
)

// bridgeImpl is the concrete implementation of the Bridge interface.
type bridgeImpl struct {
	mu sync.RWMutex

	// Identity
	id string

	// Legs
	legA Leg
	legB Leg

	// State
	state            BridgeState
	terminationCause TerminationCause
	terminatedBy     string // "leg_a", "leg_b", or "local"

	// Media
	codec              string
	transcodingEnabled bool
	transport          transport.Transport // RTP Manager transport for media bridging
	mediaBridgeID      string              // RTP Manager bridge ID

	// Timing
	createdAt    time.Time
	startedAt    time.Time
	terminatedAt time.Time

	// Statistics
	packetsA2B int64
	packetsB2A int64
	bytesA2B   int64
	bytesB2A   int64

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc

	// Options
	autoHangup bool

	// Callbacks
	terminatedCallbacks  []func(cause TerminationCause)
	callbackMu           sync.Mutex
	terminationWaiters   chan struct{}
	terminationWaitersMu sync.Mutex
}

// NewBridge creates a new bridge between two legs.
func NewBridge(legA, legB Leg, opts ...BridgeOption) (Bridge, error) {
	if legA == nil || legB == nil {
		return nil, ErrInvalidState
	}

	options := &bridgeOptions{
		autoHangup: true, // Default: hangup legs when bridge terminates
	}
	for _, opt := range opts {
		opt(options)
	}

	id := options.id
	if id == "" {
		id = "bridge-" + uuid.New().String()
	}

	ctx, cancel := context.WithCancel(context.Background())

	bridge := &bridgeImpl{
		id:                 id,
		legA:               legA,
		legB:               legB,
		state:              BridgeStateCreated,
		createdAt:          time.Now(),
		autoHangup:         options.autoHangup,
		transport:          options.transport,
		ctx:                ctx,
		cancel:             cancel,
		terminationWaiters: make(chan struct{}),
	}

	if options.onTerminated != nil {
		bridge.terminatedCallbacks = append(bridge.terminatedCallbacks, options.onTerminated)
	}

	return bridge, nil
}

// --- Identity Methods ---

func (b *bridgeImpl) ID() string {
	return b.id
}

func (b *bridgeImpl) LegA() Leg {
	return b.legA
}

func (b *bridgeImpl) LegB() Leg {
	return b.legB
}

// --- State Methods ---

func (b *bridgeImpl) GetState() BridgeState {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.state
}

// --- Info Method ---

func (b *bridgeImpl) Info() *BridgeInfo {
	b.mu.RLock()
	defer b.mu.RUnlock()

	return &BridgeInfo{
		ID:                 b.id,
		LegAID:             b.legA.ID(),
		LegBID:             b.legB.ID(),
		State:              b.state,
		TerminationCause:   b.terminationCause,
		TerminatedBy:       b.terminatedBy,
		Codec:              b.codec,
		TranscodingEnabled: b.transcodingEnabled,
		CreatedAt:          b.createdAt,
		StartedAt:          b.startedAt,
		TerminatedAt:       b.terminatedAt,
		PacketsA2B:         b.packetsA2B,
		PacketsB2A:         b.packetsB2A,
		BytesA2B:           b.bytesA2B,
		BytesB2A:           b.bytesB2A,
	}
}

// --- Lifecycle Operations ---

func (b *bridgeImpl) Start(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.state != BridgeStateCreated {
		return ErrBridgeActive
	}

	// Verify both legs are answered
	if b.legA.GetState() != LegStateAnswered {
		return ErrLegNotAnswered
	}
	if b.legB.GetState() != LegStateAnswered {
		return ErrLegNotAnswered
	}

	// Get session IDs from both legs
	sessionAID := b.legA.SessionID()
	sessionBID := b.legB.SessionID()

	// Bridge media at RTP Manager level if transport is configured
	if b.transport != nil && sessionAID != "" && sessionBID != "" {
		bridgeID, err := b.transport.BridgeMedia(ctx, sessionAID, sessionBID)
		if err != nil {
			slog.Error("[Bridge] Failed to bridge media",
				"bridge_id", b.id,
				"session_a", sessionAID,
				"session_b", sessionBID,
				"error", err,
			)
			return fmt.Errorf("bridge media: %w", err)
		}
		b.mediaBridgeID = bridgeID
		slog.Info("[Bridge] Media bridged",
			"bridge_id", b.id,
			"media_bridge_id", bridgeID,
			"session_a", sessionAID,
			"session_b", sessionBID,
		)
	} else if b.transport == nil {
		slog.Warn("[Bridge] No transport configured - media bridging skipped",
			"bridge_id", b.id,
		)
	}

	b.state = BridgeStateActive
	b.startedAt = time.Now()

	// Set up leg termination monitoring
	b.legA.OnTerminated(func(cause TerminationCause) {
		b.handleLegTerminated("leg_a", cause)
	})
	b.legB.OnTerminated(func(cause TerminationCause) {
		b.handleLegTerminated("leg_b", cause)
	})

	slog.Info("[Bridge] Started",
		"bridge_id", b.id,
		"leg_a", b.legA.ID(),
		"leg_b", b.legB.ID(),
	)

	return nil
}

func (b *bridgeImpl) Stop(hangupLegs bool) error {
	b.mu.Lock()
	if b.state == BridgeStateTerminated {
		b.mu.Unlock()
		return nil // Already terminated
	}

	b.state = BridgeStateTerminating
	mediaBridgeID := b.mediaBridgeID
	transport := b.transport
	b.mu.Unlock()

	// Unbridge media at RTP Manager level
	if transport != nil && mediaBridgeID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := transport.UnbridgeMedia(ctx, mediaBridgeID); err != nil {
			slog.Warn("[Bridge] Failed to unbridge media",
				"bridge_id", b.id,
				"media_bridge_id", mediaBridgeID,
				"error", err,
			)
			// Continue with teardown even if unbridging fails
		} else {
			slog.Info("[Bridge] Media unbridged",
				"bridge_id", b.id,
				"media_bridge_id", mediaBridgeID,
			)
		}
		cancel()
	}

	// Hangup legs if requested
	if hangupLegs {
		ctx := context.Background()
		if b.legA.GetState() == LegStateAnswered {
			b.legA.Hangup(ctx, TerminationCauseBridgePeer)
		}
		if b.legB.GetState() == LegStateAnswered {
			b.legB.Hangup(ctx, TerminationCauseBridgePeer)
		}
	}

	b.mu.Lock()
	b.state = BridgeStateTerminated
	b.terminatedAt = time.Now()
	if b.terminatedBy == "" {
		b.terminatedBy = "local"
	}
	if b.terminationCause == TerminationCauseNone {
		b.terminationCause = TerminationCauseNormal
	}
	cause := b.terminationCause
	b.mu.Unlock()

	// Cancel context to signal goroutines
	b.cancel()

	// Notify waiters
	b.terminationWaitersMu.Lock()
	close(b.terminationWaiters)
	b.terminationWaiters = make(chan struct{}) // Reset for next use
	b.terminationWaitersMu.Unlock()

	b.notifyTerminated(cause)

	slog.Info("[Bridge] Stopped",
		"bridge_id", b.id,
		"cause", cause.String(),
		"terminated_by", b.terminatedBy,
	)

	return nil
}

func (b *bridgeImpl) WaitForTermination(ctx context.Context) (TerminationCause, error) {
	b.mu.RLock()
	if b.state == BridgeStateTerminated {
		cause := b.terminationCause
		b.mu.RUnlock()
		return cause, nil
	}
	b.mu.RUnlock()

	b.terminationWaitersMu.Lock()
	waitCh := b.terminationWaiters
	b.terminationWaitersMu.Unlock()

	select {
	case <-ctx.Done():
		return TerminationCauseNone, ctx.Err()
	case <-waitCh:
		b.mu.RLock()
		cause := b.terminationCause
		b.mu.RUnlock()
		return cause, nil
	}
}

// --- Event Callbacks ---

func (b *bridgeImpl) OnTerminated(fn func(cause TerminationCause)) {
	b.callbackMu.Lock()
	b.terminatedCallbacks = append(b.terminatedCallbacks, fn)
	b.callbackMu.Unlock()
}

// --- Internal Methods ---

func (b *bridgeImpl) handleLegTerminated(legName string, cause TerminationCause) {
	b.mu.Lock()
	if b.state == BridgeStateTerminated || b.state == BridgeStateTerminating {
		b.mu.Unlock()
		return
	}

	// Set state to Terminating immediately under lock to prevent race
	// where both legs terminate simultaneously and both try to Stop()
	b.state = BridgeStateTerminating
	b.terminatedBy = legName
	b.terminationCause = TerminationCauseBridgePeer
	b.mu.Unlock()

	slog.Info("[Bridge] Leg terminated",
		"bridge_id", b.id,
		"leg", legName,
		"cause", cause.String(),
	)

	// Stop the bridge (which will hangup the other leg if autoHangup is true)
	b.Stop(b.autoHangup)
}

func (b *bridgeImpl) notifyTerminated(cause TerminationCause) {
	b.callbackMu.Lock()
	callbacks := make([]func(cause TerminationCause), len(b.terminatedCallbacks))
	copy(callbacks, b.terminatedCallbacks)
	b.callbackMu.Unlock()

	for _, fn := range callbacks {
		fn(cause)
	}
}

// Ensure bridgeImpl implements Bridge
var _ Bridge = (*bridgeImpl)(nil)
