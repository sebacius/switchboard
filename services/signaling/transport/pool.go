package transport

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// PoolConfig holds configuration for the RTP manager pool
type PoolConfig struct {
	Addresses             []string
	ConnectTimeout        time.Duration
	KeepaliveInterval     time.Duration
	KeepaliveTimeout      time.Duration
	HealthCheckInterval   time.Duration
	UnhealthyThreshold    int // Number of failed health checks before marking unhealthy
	HealthyThreshold      int // Number of successful health checks before marking healthy
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
	address      string
	transport    *GRPCTransport
	healthy      atomic.Bool
	failCount    atomic.Int32
	successCount atomic.Int32
}

// Pool manages multiple RTP managers with load balancing and health checking
type Pool struct {
	mu            sync.RWMutex
	members       []*poolMember
	sessionToAddr map[string]string // sessionID -> member address (affinity)
	nextIndex     atomic.Uint64     // for round-robin
	config        PoolConfig
	stopCh        chan struct{}
	wg            sync.WaitGroup
}

// NewPool creates a new RTP manager pool
func NewPool(cfg PoolConfig) (*Pool, error) {
	if len(cfg.Addresses) == 0 {
		return nil, fmt.Errorf("no RTP manager addresses provided")
	}

	p := &Pool{
		members:       make([]*poolMember, 0, len(cfg.Addresses)),
		sessionToAddr: make(map[string]string),
		config:        cfg,
		stopCh:        make(chan struct{}),
	}

	// Create connections to all RTP managers
	grpcCfg := GRPCConfig{
		ConnectTimeout:    cfg.ConnectTimeout,
		KeepaliveInterval: cfg.KeepaliveInterval,
		KeepaliveTimeout:  cfg.KeepaliveTimeout,
	}

	for _, addr := range cfg.Addresses {
		grpcCfg.Address = addr
		transport, err := NewGRPCTransport(grpcCfg)
		if err != nil {
			slog.Warn("[Pool] Failed to connect to RTP manager", "address", addr, "error", err)
			// Continue - we'll mark it unhealthy and retry via health checks
			member := &poolMember{
				address: addr,
			}
			member.healthy.Store(false)
			p.members = append(p.members, member)
			continue
		}

		member := &poolMember{
			address:   addr,
			transport: transport,
		}
		member.healthy.Store(true)
		p.members = append(p.members, member)
		slog.Info("[Pool] Connected to RTP manager", "address", addr)
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

// selectMember picks a healthy member using round-robin
func (p *Pool) selectMember() (*poolMember, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// Count healthy members
	healthyMembers := make([]*poolMember, 0)
	for _, m := range p.members {
		if m.healthy.Load() && m.transport != nil {
			healthyMembers = append(healthyMembers, m)
		}
	}

	if len(healthyMembers) == 0 {
		return nil, fmt.Errorf("no healthy RTP managers available")
	}

	// Round-robin selection
	idx := p.nextIndex.Add(1) % uint64(len(healthyMembers))
	return healthyMembers[idx], nil
}

// getMemberByAddress returns the member for a specific address
func (p *Pool) getMemberByAddress(addr string) *poolMember {
	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, m := range p.members {
		if m.address == addr {
			return m
		}
	}
	return nil
}

// getMemberForSession returns the member that owns a session (affinity)
func (p *Pool) getMemberForSession(sessionID string) (*poolMember, bool) {
	p.mu.RLock()
	addr, ok := p.sessionToAddr[sessionID]
	p.mu.RUnlock()

	if !ok {
		return nil, false
	}

	member := p.getMemberByAddress(addr)
	return member, member != nil
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

	// Track session affinity
	p.mu.Lock()
	p.sessionToAddr[result.SessionID] = member.address
	p.mu.Unlock()

	slog.Debug("[Pool] Session created",
		"session_id", result.SessionID,
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

	// Remove affinity tracking
	p.mu.Lock()
	delete(p.sessionToAddr, sessionID)
	p.mu.Unlock()

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
		ActiveSessions: len(p.sessionToAddr),
		Members:        make([]MemberStats, 0, len(p.members)),
	}

	for _, m := range p.members {
		memberStats := MemberStats{
			Address: m.address,
			Healthy: m.healthy.Load(),
		}
		if m.transport != nil {
			// Could add more stats like active sessions per member
		}
		if memberStats.Healthy {
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
	Address string
	Healthy bool
}
