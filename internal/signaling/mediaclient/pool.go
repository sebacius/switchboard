package mediaclient

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// DrainState represents the lifecycle state of a pool member
type DrainState uint32

const (
	// StateActive - member is accepting new sessions
	StateActive DrainState = iota
	// StateDraining - member is not accepting new sessions, migrating existing
	StateDraining
	// StateDisabled - member is fully drained and removed from rotation
	StateDisabled
)

// String returns the string representation of DrainState
func (s DrainState) String() string {
	switch s {
	case StateActive:
		return "active"
	case StateDraining:
		return "draining"
	case StateDisabled:
		return "disabled"
	default:
		return "unknown"
	}
}

// PoolConfig holds configuration for the RTP manager pool
type PoolConfig struct {
	// NodeAddresses maps node ID to address (e.g., "rtpmanager-0" -> "localhost:9090")
	// If empty, Addresses is used with auto-generated IDs
	NodeAddresses map[string]string

	// Addresses is deprecated, use NodeAddresses instead
	// If NodeAddresses is empty, these addresses get auto-generated IDs (node-0, node-1, etc.)
	Addresses           []string
	ConnectTimeout      time.Duration
	KeepaliveInterval   time.Duration
	KeepaliveTimeout    time.Duration
	HealthCheckInterval time.Duration
	UnhealthyThreshold  int // Number of failed health checks before marking unhealthy
	HealthyThreshold    int // Number of successful health checks before marking healthy
}

// DefaultPoolConfig returns sensible defaults
func DefaultPoolConfig() PoolConfig {
	return PoolConfig{
		ConnectTimeout:      10 * time.Second,
		KeepaliveInterval:   30 * time.Second,
		KeepaliveTimeout:    10 * time.Second,
		HealthCheckInterval: 5 * time.Second,
		UnhealthyThreshold:  3,
		HealthyThreshold:    2,
	}
}

// poolMember represents a single RTP manager in the pool
type poolMember struct {
	id           string // Node ID (e.g., "rtpmanager-0")
	address      string // Network address (e.g., "localhost:9090")
	transport    *GRPCTransport
	healthy      atomic.Bool
	drainState   atomic.Uint32 // DrainState
	failCount    atomic.Int32
	successCount atomic.Int32
}

// DrainState returns the current drain state
func (m *poolMember) DrainState() DrainState {
	return DrainState(m.drainState.Load())
}

// SetDrainState atomically updates drain state
func (m *poolMember) SetDrainState(state DrainState) {
	m.drainState.Store(uint32(state))
}

// Pool manages multiple RTP managers with load balancing and health checking
type Pool struct {
	mu             sync.RWMutex
	members        []*poolMember
	membersByID    map[string]*poolMember        // nodeID -> member (fast lookup)
	sessionToNode  map[string]string             // sessionID -> nodeID (affinity)
	nodeToSessions map[string]map[string]struct{} // nodeID -> set of sessionIDs (reverse index)
	nextIndex      atomic.Uint64                 // for round-robin
	config         PoolConfig
	stopCh         chan struct{}
	wg             sync.WaitGroup
}

// NewPool creates a new RTP manager pool
func NewPool(cfg PoolConfig) (*Pool, error) {
	// Build node addresses map - either from NodeAddresses or auto-generate from Addresses
	nodeAddresses := cfg.NodeAddresses
	if len(nodeAddresses) == 0 {
		if len(cfg.Addresses) == 0 {
			return nil, fmt.Errorf("no RTP manager addresses provided")
		}
		// Auto-generate node IDs
		nodeAddresses = make(map[string]string, len(cfg.Addresses))
		for i, addr := range cfg.Addresses {
			nodeAddresses[fmt.Sprintf("node-%d", i)] = addr
		}
	}

	p := &Pool{
		members:        make([]*poolMember, 0, len(nodeAddresses)),
		membersByID:    make(map[string]*poolMember, len(nodeAddresses)),
		sessionToNode:  make(map[string]string),
		nodeToSessions: make(map[string]map[string]struct{}),
		config:         cfg,
		stopCh:         make(chan struct{}),
	}

	// Create connections to all RTP managers
	grpcCfg := GRPCConfig{
		ConnectTimeout:    cfg.ConnectTimeout,
		KeepaliveInterval: cfg.KeepaliveInterval,
		KeepaliveTimeout:  cfg.KeepaliveTimeout,
	}

	for nodeID, addr := range nodeAddresses {
		grpcCfg.Address = addr
		transport, err := NewGRPCTransport(grpcCfg)
		if err != nil {
			slog.Warn("[Pool] Failed to connect to RTP manager", "node_id", nodeID, "address", addr, "error", err)
			// Continue - we'll mark it unhealthy and retry via health checks
			member := &poolMember{
				id:      nodeID,
				address: addr,
			}
			member.healthy.Store(false)
			p.members = append(p.members, member)
			p.membersByID[nodeID] = member
			continue
		}

		member := &poolMember{
			id:        nodeID,
			address:   addr,
			transport: transport,
		}
		member.healthy.Store(true)
		p.members = append(p.members, member)
		p.membersByID[nodeID] = member
		slog.Info("[Pool] Connected to RTP manager", "node_id", nodeID, "address", addr)
	}

	// Check we have at least one healthy member
	healthyCount := 0
	for _, m := range p.members {
		if m.healthy.Load() {
			healthyCount++
		}
	}
	if healthyCount == 0 {
		return nil, fmt.Errorf("no healthy RTP managers available")
	}

	// Start health checker
	p.wg.Add(1)
	go p.healthChecker()

	slog.Info("[Pool] RTP manager pool initialized",
		"total", len(p.members),
		"healthy", healthyCount,
	)

	return p, nil
}

// healthChecker periodically checks health of all members
func (p *Pool) healthChecker() {
	defer p.wg.Done()

	ticker := time.NewTicker(p.config.HealthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
			p.checkAllHealth()
		}
	}
}

// checkAllHealth checks health of all pool members
func (p *Pool) checkAllHealth() {
	for _, member := range p.members {
		healthy := p.checkMemberHealth(member)

		if healthy {
			member.failCount.Store(0)
			newSuccess := member.successCount.Add(1)

			// Mark healthy after threshold consecutive successes
			if !member.healthy.Load() && int(newSuccess) >= p.config.HealthyThreshold {
				member.healthy.Store(true)
				slog.Info("[Pool] RTP manager marked healthy", "address", member.address)
			}
		} else {
			member.successCount.Store(0)
			newFail := member.failCount.Add(1)

			// Mark unhealthy after threshold consecutive failures
			if member.healthy.Load() && int(newFail) >= p.config.UnhealthyThreshold {
				member.healthy.Store(false)
				slog.Warn("[Pool] RTP manager marked unhealthy", "address", member.address)
			}
		}
	}
}

// checkMemberHealth checks if a single member is healthy
func (p *Pool) checkMemberHealth(member *poolMember) bool {
	if member.transport == nil {
		// Try to reconnect
		grpcCfg := GRPCConfig{
			Address:           member.address,
			ConnectTimeout:    p.config.ConnectTimeout,
			KeepaliveInterval: p.config.KeepaliveInterval,
			KeepaliveTimeout:  p.config.KeepaliveTimeout,
		}
		transport, err := NewGRPCTransport(grpcCfg)
		if err != nil {
			return false
		}
		member.transport = transport
		slog.Info("[Pool] Reconnected to RTP manager", "address", member.address)
	}

	return member.transport.Ready()
}

// ErrNoAvailableMembers is returned when no RTP managers are available for new sessions
var ErrNoAvailableMembers = fmt.Errorf("no available RTP managers")

// selectMember picks a healthy, active member using round-robin
func (p *Pool) selectMember() (*poolMember, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// Filter to healthy, active members only (skip draining/disabled)
	availableMembers := make([]*poolMember, 0)
	for _, m := range p.members {
		if m.healthy.Load() && m.transport != nil && m.DrainState() == StateActive {
			availableMembers = append(availableMembers, m)
		}
	}

	if len(availableMembers) == 0 {
		return nil, ErrNoAvailableMembers
	}

	// Round-robin selection
	idx := p.nextIndex.Add(1) % uint64(len(availableMembers))
	return availableMembers[idx], nil
}

// getMemberForSession returns the member that owns a session (affinity)
func (p *Pool) getMemberForSession(sessionID string) (*poolMember, bool) {
	p.mu.RLock()
	nodeID, ok := p.sessionToNode[sessionID]
	p.mu.RUnlock()

	if !ok {
		return nil, false
	}

	member := p.GetMemberByID(nodeID)
	return member, member != nil
}

// GetMemberByID returns the member for a specific node ID
func (p *Pool) GetMemberByID(nodeID string) *poolMember {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.membersByID[nodeID]
}

// trackSession adds session tracking in both directions (requires lock held)
func (p *Pool) trackSession(sessionID, nodeID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.sessionToNode[sessionID] = nodeID

	if p.nodeToSessions[nodeID] == nil {
		p.nodeToSessions[nodeID] = make(map[string]struct{})
	}
	p.nodeToSessions[nodeID][sessionID] = struct{}{}
}

// untrackSession removes session tracking in both directions (requires lock held)
func (p *Pool) untrackSession(sessionID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if nodeID, ok := p.sessionToNode[sessionID]; ok {
		delete(p.sessionToNode, sessionID)
		if sessions, exists := p.nodeToSessions[nodeID]; exists {
			delete(sessions, sessionID)
			if len(sessions) == 0 {
				delete(p.nodeToSessions, nodeID)
			}
		}
	}
}

// SessionsOnNode returns all session IDs on a specific node
func (p *Pool) SessionsOnNode(nodeID string) []string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	sessions, ok := p.nodeToSessions[nodeID]
	if !ok {
		return nil
	}

	result := make([]string, 0, len(sessions))
	for sessionID := range sessions {
		result = append(result, sessionID)
	}
	return result
}

// StartDrain initiates drain for a node, marking it as draining
func (p *Pool) StartDrain(nodeID string) error {
	member := p.GetMemberByID(nodeID)
	if member == nil {
		return fmt.Errorf("node not found: %s", nodeID)
	}

	// Only allow transitioning from Active to Draining
	if member.DrainState() != StateActive {
		return fmt.Errorf("node %s is not in active state (current: %s)", nodeID, member.DrainState())
	}

	member.SetDrainState(StateDraining)
	slog.Info("[Pool] Node drain started", "node_id", nodeID)
	return nil
}

// CompleteDrain marks a node as fully drained (disabled)
func (p *Pool) CompleteDrain(nodeID string) error {
	member := p.GetMemberByID(nodeID)
	if member == nil {
		return fmt.Errorf("node not found: %s", nodeID)
	}

	if member.DrainState() != StateDraining {
		return fmt.Errorf("node %s is not in draining state (current: %s)", nodeID, member.DrainState())
	}

	member.SetDrainState(StateDisabled)
	slog.Info("[Pool] Node drain completed", "node_id", nodeID)
	return nil
}

// CancelDrain cancels drain and returns node to active state
// Also used to re-enable a disabled node
func (p *Pool) CancelDrain(nodeID string) error {
	member := p.GetMemberByID(nodeID)
	if member == nil {
		return fmt.Errorf("node not found: %s", nodeID)
	}

	currentState := member.DrainState()
	if currentState == StateActive {
		return fmt.Errorf("node %s is already active", nodeID)
	}

	member.SetDrainState(StateActive)
	if currentState == StateDraining {
		slog.Info("[Pool] Node drain cancelled", "node_id", nodeID)
	} else {
		slog.Info("[Pool] Node re-enabled", "node_id", nodeID)
	}
	return nil
}

// CreateSessionOnNode creates a session on a specific node (for migration)
func (p *Pool) CreateSessionOnNode(ctx context.Context, nodeID string, info SessionInfo) (*SessionResult, error) {
	member := p.GetMemberByID(nodeID)
	if member == nil {
		return nil, fmt.Errorf("node not found: %s", nodeID)
	}

	if !member.healthy.Load() || member.transport == nil {
		return nil, fmt.Errorf("node %s is not healthy", nodeID)
	}

	// Allow creating on draining nodes for migration purposes (only block disabled)
	if member.DrainState() == StateDisabled {
		return nil, fmt.Errorf("node %s is disabled", nodeID)
	}

	result, err := member.transport.CreateSession(ctx, info)
	if err != nil {
		member.failCount.Add(1)
		return nil, fmt.Errorf("CreateSession on %s failed: %w", member.address, err)
	}

	p.trackSession(result.SessionID, member.id)

	slog.Debug("[Pool] Session created on specific node",
		"session_id", result.SessionID,
		"node_id", member.id,
		"rtp_manager", member.address,
	)

	return result, nil
}

// ListNodes returns all node IDs in the pool
func (p *Pool) ListNodes() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	nodes := make([]string, 0, len(p.members))
	for _, m := range p.members {
		nodes = append(nodes, m.id)
	}
	return nodes
}

// CreateSession implements Transport.CreateSession with load balancing
func (p *Pool) CreateSession(ctx context.Context, info SessionInfo) (*SessionResult, error) {
	member, err := p.selectMember()
	if err != nil {
		return nil, err
	}

	result, err := member.transport.CreateSession(ctx, info)
	if err != nil {
		// Mark member as potentially unhealthy
		member.failCount.Add(1)
		return nil, fmt.Errorf("CreateSession on %s failed: %w", member.address, err)
	}

	// Track session affinity (both directions)
	p.trackSession(result.SessionID, member.id)

	slog.Debug("[Pool] Session created",
		"session_id", result.SessionID,
		"node_id", member.id,
		"rtp_manager", member.address,
	)

	return result, nil
}

// DestroySession implements Transport.DestroySession with affinity
func (p *Pool) DestroySession(ctx context.Context, sessionID string, reason TerminateReason) error {
	member, ok := p.getMemberForSession(sessionID)
	if !ok {
		return fmt.Errorf("no RTP manager found for session %s", sessionID)
	}

	err := member.transport.DestroySession(ctx, sessionID, reason)

	// Remove affinity tracking (both directions)
	p.untrackSession(sessionID)

	return err
}

// PlayAudio implements Transport.PlayAudio with affinity
func (p *Pool) PlayAudio(ctx context.Context, req PlayRequest) (<-chan PlayStatus, error) {
	member, ok := p.getMemberForSession(req.SessionID)
	if !ok {
		return nil, fmt.Errorf("no RTP manager found for session %s", req.SessionID)
	}

	return member.transport.PlayAudio(ctx, req)
}

// StopAudio implements Transport.StopAudio with affinity
func (p *Pool) StopAudio(ctx context.Context, sessionID string) error {
	member, ok := p.getMemberForSession(sessionID)
	if !ok {
		return fmt.Errorf("no RTP manager found for session %s", sessionID)
	}

	return member.transport.StopAudio(ctx, sessionID)
}

// CreateSessionPendingRemote implements Transport.CreateSessionPendingRemote with load balancing
func (p *Pool) CreateSessionPendingRemote(ctx context.Context, callID string, codecs []string) (*SessionResult, error) {
	member, err := p.selectMember()
	if err != nil {
		return nil, err
	}

	result, err := member.transport.CreateSessionPendingRemote(ctx, callID, codecs)
	if err != nil {
		member.failCount.Add(1)
		return nil, fmt.Errorf("CreateSessionPendingRemote on %s failed: %w", member.address, err)
	}

	// Track session affinity (both directions)
	p.trackSession(result.SessionID, member.id)

	slog.Debug("[Pool] Session created (pending remote)",
		"session_id", result.SessionID,
		"node_id", member.id,
		"rtp_manager", member.address,
	)

	return result, nil
}

// CreateSessionPendingRemoteOnNode creates a session on the same node as a peer session.
// Used for B2BUA B-leg to ensure both legs are on the same RTP manager for bridging.
func (p *Pool) CreateSessionPendingRemoteOnNode(ctx context.Context, peerSessionID, callID string, codecs []string) (*SessionResult, error) {
	// Find which node the peer session is on
	member, ok := p.getMemberForSession(peerSessionID)
	if !ok {
		// Peer session not found, fall back to round-robin
		slog.Warn("[Pool] Peer session not found, using round-robin",
			"peer_session_id", peerSessionID,
			"call_id", callID,
		)
		return p.CreateSessionPendingRemote(ctx, callID, codecs)
	}

	// Create session on the same node
	result, err := member.transport.CreateSessionPendingRemote(ctx, callID, codecs)
	if err != nil {
		member.failCount.Add(1)
		return nil, fmt.Errorf("CreateSessionPendingRemote on %s failed: %w", member.address, err)
	}

	// Track session affinity
	p.trackSession(result.SessionID, member.id)

	slog.Debug("[Pool] Session created on same node as peer",
		"session_id", result.SessionID,
		"peer_session_id", peerSessionID,
		"node_id", member.id,
		"rtp_manager", member.address,
	)

	return result, nil
}

// UpdateSessionRemote implements Transport.UpdateSessionRemote with affinity
func (p *Pool) UpdateSessionRemote(ctx context.Context, sessionID, remoteAddr string, remotePort int) error {
	member, ok := p.getMemberForSession(sessionID)
	if !ok {
		return fmt.Errorf("no RTP manager found for session %s", sessionID)
	}

	return member.transport.UpdateSessionRemote(ctx, sessionID, remoteAddr, remotePort)
}

// BridgeMedia implements Transport.BridgeMedia
func (p *Pool) BridgeMedia(ctx context.Context, sessionAID, sessionBID string) (string, error) {
	// Both sessions must be on the same RTP manager for bridging
	memberA, okA := p.getMemberForSession(sessionAID)
	memberB, okB := p.getMemberForSession(sessionBID)

	if !okA {
		return "", fmt.Errorf("no RTP manager found for session A: %s", sessionAID)
	}
	if !okB {
		return "", fmt.Errorf("no RTP manager found for session B: %s", sessionBID)
	}

	if memberA.address != memberB.address {
		return "", fmt.Errorf("sessions are on different RTP managers (%s vs %s) - cross-manager bridging not supported",
			memberA.address, memberB.address)
	}

	return memberA.transport.BridgeMedia(ctx, sessionAID, sessionBID)
}

// UnbridgeMedia implements Transport.UnbridgeMedia
func (p *Pool) UnbridgeMedia(ctx context.Context, bridgeID string) error {
	// We need to find which member has this bridge
	// For now, try all members until one succeeds
	p.mu.RLock()
	members := make([]*poolMember, len(p.members))
	copy(members, p.members)
	p.mu.RUnlock()

	for _, member := range members {
		if member.transport == nil || !member.healthy.Load() {
			continue
		}
		err := member.transport.UnbridgeMedia(ctx, bridgeID)
		if err == nil {
			return nil
		}
		// Try next member - bridge might be on a different one
	}

	return fmt.Errorf("bridge not found on any RTP manager: %s", bridgeID)
}

// Ready implements Transport.Ready
func (p *Pool) Ready() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, m := range p.members {
		if m.healthy.Load() {
			return true
		}
	}
	return false
}

// Close implements Transport.Close
func (p *Pool) Close() error {
	close(p.stopCh)
	p.wg.Wait()

	p.mu.Lock()
	defer p.mu.Unlock()

	var lastErr error
	for _, m := range p.members {
		if m.transport != nil {
			if err := m.transport.Close(); err != nil {
				lastErr = err
			}
		}
	}

	return lastErr
}

// Stats returns pool statistics
func (p *Pool) Stats() PoolStats {
	p.mu.RLock()
	defer p.mu.RUnlock()

	stats := PoolStats{
		TotalMembers:   len(p.members),
		ActiveSessions: len(p.sessionToNode),
		Members:        make([]MemberStats, 0, len(p.members)),
	}

	for _, m := range p.members {
		sessionCount := 0
		if sessions, ok := p.nodeToSessions[m.id]; ok {
			sessionCount = len(sessions)
		}

		memberStats := MemberStats{
			NodeID:       m.id,
			Address:      m.address,
			Healthy:      m.healthy.Load(),
			DrainState:   m.DrainState(),
			SessionCount: sessionCount,
		}
		if memberStats.Healthy && memberStats.DrainState == StateActive {
			stats.HealthyMembers++
		}
		stats.Members = append(stats.Members, memberStats)
	}

	return stats
}

// PoolStats holds pool statistics
type PoolStats struct {
	TotalMembers   int
	HealthyMembers int
	ActiveSessions int
	Members        []MemberStats
}

// MemberStats holds stats for a single pool member
type MemberStats struct {
	NodeID       string
	Address      string
	Healthy      bool
	DrainState   DrainState
	SessionCount int
}
