package bridge

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"

	"github.com/google/uuid"
)

// Endpoint represents one side of a bridge (A or B leg).
type Endpoint struct {
	SessionID  string
	LocalAddr  string
	LocalPort  int
	RemoteAddr string
	RemotePort int
	conn       *net.UDPConn
}

// Bridge represents a bidirectional RTP relay between two sessions.
type Bridge struct {
	ID       string
	SessionA *Endpoint
	SessionB *Endpoint

	ctx    context.Context
	cancel context.CancelFunc
	active atomic.Bool

	// Statistics
	packetsA2B atomic.Int64
	packetsB2A atomic.Int64
	bytesA2B   atomic.Int64
	bytesB2A   atomic.Int64
}

// Stats returns current bridge statistics.
type Stats struct {
	PacketsA2B int64
	PacketsB2A int64
	BytesA2B   int64
	BytesB2A   int64
}

// Manager manages active bridges.
type Manager struct {
	bridges    map[string]*Bridge // bridgeID -> Bridge
	sessionMap map[string]string  // sessionID -> bridgeID
	mu         sync.RWMutex
}

// NewManager creates a new bridge manager.
func NewManager() *Manager {
	return &Manager{
		bridges:    make(map[string]*Bridge),
		sessionMap: make(map[string]string),
	}
}

// CreateBridge establishes bidirectional RTP forwarding between two sessions.
func (m *Manager) CreateBridge(endpointA, endpointB *Endpoint) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if either session is already bridged
	if bridgeID, exists := m.sessionMap[endpointA.SessionID]; exists {
		return "", fmt.Errorf("session %s is already in bridge %s", endpointA.SessionID, bridgeID)
	}
	if bridgeID, exists := m.sessionMap[endpointB.SessionID]; exists {
		return "", fmt.Errorf("session %s is already in bridge %s", endpointB.SessionID, bridgeID)
	}

	// Validate endpoints have remote info
	if endpointA.RemoteAddr == "" || endpointA.RemotePort == 0 {
		return "", fmt.Errorf("session A (%s) has no remote endpoint", endpointA.SessionID)
	}
	if endpointB.RemoteAddr == "" || endpointB.RemotePort == 0 {
		return "", fmt.Errorf("session B (%s) has no remote endpoint", endpointB.SessionID)
	}

	bridgeID := "bridge-" + uuid.New().String()
	ctx, cancel := context.WithCancel(context.Background())

	bridge := &Bridge{
		ID:       bridgeID,
		SessionA: endpointA,
		SessionB: endpointB,
		ctx:      ctx,
		cancel:   cancel,
	}

	// Bind UDP sockets for each endpoint
	if err := bridge.bindSockets(); err != nil {
		cancel()
		return "", fmt.Errorf("failed to bind sockets: %w", err)
	}

	bridge.active.Store(true)

	// Start relay goroutines
	go bridge.relayAtoB()
	go bridge.relayBtoA()

	m.bridges[bridgeID] = bridge
	m.sessionMap[endpointA.SessionID] = bridgeID
	m.sessionMap[endpointB.SessionID] = bridgeID

	slog.Info("[Bridge] Created",
		"bridge_id", bridgeID,
		"session_a", endpointA.SessionID,
		"session_a_local", fmt.Sprintf("%s:%d", endpointA.LocalAddr, endpointA.LocalPort),
		"session_a_remote", fmt.Sprintf("%s:%d", endpointA.RemoteAddr, endpointA.RemotePort),
		"session_b", endpointB.SessionID,
		"session_b_local", fmt.Sprintf("%s:%d", endpointB.LocalAddr, endpointB.LocalPort),
		"session_b_remote", fmt.Sprintf("%s:%d", endpointB.RemoteAddr, endpointB.RemotePort),
	)

	return bridgeID, nil
}

// bindSockets binds UDP sockets for both endpoints.
// Note: These sockets listen on the same ports allocated for the sessions.
func (b *Bridge) bindSockets() error {
	// Validate remote endpoints before binding - ParseIP returns nil for invalid IPs
	remoteIPA := net.ParseIP(b.SessionA.RemoteAddr)
	if remoteIPA == nil {
		return fmt.Errorf("session A has invalid remote IP: %q", b.SessionA.RemoteAddr)
	}
	remoteIPB := net.ParseIP(b.SessionB.RemoteAddr)
	if remoteIPB == nil {
		return fmt.Errorf("session B has invalid remote IP: %q", b.SessionB.RemoteAddr)
	}

	// Bind A's local port (receives packets from A's remote party)
	addrA := &net.UDPAddr{Port: b.SessionA.LocalPort, IP: net.IPv4zero}
	connA, err := net.ListenUDP("udp", addrA)
	if err != nil {
		return fmt.Errorf("bind A port %d: %w", b.SessionA.LocalPort, err)
	}
	b.SessionA.conn = connA

	// Bind B's local port (receives packets from B's remote party)
	addrB := &net.UDPAddr{Port: b.SessionB.LocalPort, IP: net.IPv4zero}
	connB, err := net.ListenUDP("udp", addrB)
	if err != nil {
		_ = connA.Close()
		return fmt.Errorf("bind B port %d: %w", b.SessionB.LocalPort, err)
	}
	b.SessionB.conn = connB

	return nil
}

// relayAtoB forwards packets from A's remote party to B's remote party.
func (b *Bridge) relayAtoB() {
	buf := make([]byte, 1500) // MTU-sized buffer

	// Parse destination IP once at start (validated in bindSockets)
	destIP := net.ParseIP(b.SessionB.RemoteAddr)
	destAddr := &net.UDPAddr{
		IP:   destIP,
		Port: b.SessionB.RemotePort,
	}

	slog.Debug("[Bridge] Relay A->B started",
		"bridge_id", b.ID,
		"read_from", fmt.Sprintf("0.0.0.0:%d", b.SessionA.LocalPort),
		"write_to", destAddr.String(),
	)

	for b.active.Load() {
		select {
		case <-b.ctx.Done():
			slog.Debug("[Bridge] Relay A->B context done", "bridge_id", b.ID)
			return
		default:
		}

		// Read from A's local port (packets from A's remote party)
		n, srcAddr, err := b.SessionA.conn.ReadFromUDP(buf)
		if err != nil {
			if b.ctx.Err() != nil {
				return // Context canceled
			}
			slog.Debug("[Bridge] Read error A->B", "bridge_id", b.ID, "error", err)
			continue
		}

		// Log first packet for debugging
		count := b.packetsA2B.Load()
		if count == 0 {
			slog.Info("[Bridge] First packet A->B",
				"bridge_id", b.ID,
				"from", srcAddr.String(),
				"to", destAddr.String(),
				"size", n,
			)
		}

		// Forward to B's remote party using B's socket (so source is B's local port)
		if _, err := b.SessionB.conn.WriteToUDP(buf[:n], destAddr); err != nil {
			slog.Debug("[Bridge] Write error A->B", "bridge_id", b.ID, "error", err)
			continue
		}

		b.packetsA2B.Add(1)
		b.bytesA2B.Add(int64(n))
	}
}

// relayBtoA forwards packets from B's remote party to A's remote party.
func (b *Bridge) relayBtoA() {
	buf := make([]byte, 1500)

	// Parse destination IP once at start (validated in bindSockets)
	destIP := net.ParseIP(b.SessionA.RemoteAddr)
	destAddr := &net.UDPAddr{
		IP:   destIP,
		Port: b.SessionA.RemotePort,
	}

	slog.Debug("[Bridge] Relay B->A started",
		"bridge_id", b.ID,
		"read_from", fmt.Sprintf("0.0.0.0:%d", b.SessionB.LocalPort),
		"write_to", destAddr.String(),
	)

	for b.active.Load() {
		select {
		case <-b.ctx.Done():
			slog.Debug("[Bridge] Relay B->A context done", "bridge_id", b.ID)
			return
		default:
		}

		// Read from B's local port (packets from B's remote party)
		n, srcAddr, err := b.SessionB.conn.ReadFromUDP(buf)
		if err != nil {
			if b.ctx.Err() != nil {
				return
			}
			slog.Debug("[Bridge] Read error B->A", "bridge_id", b.ID, "error", err)
			continue
		}

		// Log first packet for debugging
		count := b.packetsB2A.Load()
		if count == 0 {
			slog.Info("[Bridge] First packet B->A",
				"bridge_id", b.ID,
				"from", srcAddr.String(),
				"to", destAddr.String(),
				"size", n,
			)
		}

		// Forward to A's remote party using A's socket (so source is A's local port)
		if _, err := b.SessionA.conn.WriteToUDP(buf[:n], destAddr); err != nil {
			slog.Debug("[Bridge] Write error B->A", "bridge_id", b.ID, "error", err)
			continue
		}

		b.packetsB2A.Add(1)
		b.bytesB2A.Add(int64(n))
	}
}

// GetStats returns the current statistics for a bridge.
func (b *Bridge) GetStats() Stats {
	return Stats{
		PacketsA2B: b.packetsA2B.Load(),
		PacketsB2A: b.packetsB2A.Load(),
		BytesA2B:   b.bytesA2B.Load(),
		BytesB2A:   b.bytesB2A.Load(),
	}
}

// DestroyBridge tears down an active bridge.
func (m *Manager) DestroyBridge(bridgeID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	bridge, exists := m.bridges[bridgeID]
	if !exists {
		return fmt.Errorf("bridge not found: %s", bridgeID)
	}

	m.destroyBridgeLocked(bridge)
	return nil
}

// DestroyBySession finds and destroys the bridge containing a session.
func (m *Manager) DestroyBySession(sessionID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	bridgeID, exists := m.sessionMap[sessionID]
	if !exists {
		return "", nil // Not bridged, nothing to do
	}

	bridge, exists := m.bridges[bridgeID]
	if !exists {
		return bridgeID, nil
	}

	m.destroyBridgeLocked(bridge)
	return bridgeID, nil
}

// destroyBridgeLocked tears down a bridge (must hold lock).
func (m *Manager) destroyBridgeLocked(bridge *Bridge) {
	bridge.active.Store(false)
	bridge.cancel()

	if bridge.SessionA.conn != nil {
		_ = bridge.SessionA.conn.Close()
	}
	if bridge.SessionB.conn != nil {
		_ = bridge.SessionB.conn.Close()
	}

	delete(m.sessionMap, bridge.SessionA.SessionID)
	delete(m.sessionMap, bridge.SessionB.SessionID)
	delete(m.bridges, bridge.ID)

	stats := bridge.GetStats()
	slog.Info("[Bridge] Destroyed",
		"bridge_id", bridge.ID,
		"packets_a2b", stats.PacketsA2B,
		"packets_b2a", stats.PacketsB2A,
		"bytes_a2b", stats.BytesA2B,
		"bytes_b2a", stats.BytesB2A,
	)
}

// GetBridge returns a bridge by ID.
func (m *Manager) GetBridge(bridgeID string) (*Bridge, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	bridge, ok := m.bridges[bridgeID]
	return bridge, ok
}

// GetBridgeBySession returns the bridge containing a session.
func (m *Manager) GetBridgeBySession(sessionID string) (*Bridge, bool) {
	m.mu.RLock()
	bridgeID, exists := m.sessionMap[sessionID]
	if !exists {
		m.mu.RUnlock()
		return nil, false
	}
	bridge, ok := m.bridges[bridgeID]
	m.mu.RUnlock()
	return bridge, ok
}

// IsSessionBridged checks if a session is part of a bridge.
func (m *Manager) IsSessionBridged(sessionID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, exists := m.sessionMap[sessionID]
	return exists
}

// Count returns the number of active bridges.
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.bridges)
}

// CloseAll destroys all active bridges.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, bridge := range m.bridges {
		m.destroyBridgeLocked(bridge)
	}
}
