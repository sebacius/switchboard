package session

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/sebas/switchboard/internal/rtpmanager/media"
	"github.com/sebas/switchboard/internal/rtpmanager/portpool"
	"github.com/sebas/switchboard/internal/rtpmanager/sdp"
	rtpv1 "github.com/sebas/switchboard/pkg/rtpmanager/v1"
)

// Session represents an active media session
type Session struct {
	ID           string
	CallID       string
	LocalAddr    string
	LocalPort    int
	RTCPPort     int
	RemoteAddr   string
	RemotePort   int
	Codec        string
	State        rtpv1.SessionState
	CreatedAt    time.Time
	ctx          context.Context
	cancel       context.CancelFunc
	playbackDone chan struct{}
	mu           sync.RWMutex
}

// Manager manages media sessions
type Manager struct {
	mu            sync.RWMutex
	sessions      map[string]*Session // sessionID -> Session
	callToSession map[string]string   // callID -> sessionID
	portPool      *portpool.PortPool
	mediaService  *media.LocalService
	advertiseAddr string
}

// NewManager creates a new session manager
func NewManager(portPool *portpool.PortPool, mediaService *media.LocalService, advertiseAddr string) *Manager {
	return &Manager{
		sessions:      make(map[string]*Session),
		callToSession: make(map[string]string),
		portPool:      portPool,
		mediaService:  mediaService,
		advertiseAddr: advertiseAddr,
	}
}

// CreateSession creates a new media session
func (m *Manager) CreateSession(callID, remoteAddr string, remotePort int, offeredCodecs []string) (*Session, []byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if session already exists for this call
	if sessionID, exists := m.callToSession[callID]; exists {
		if sess, ok := m.sessions[sessionID]; ok {
			slog.Warn("[SessionMgr] Session already exists for call", "call_id", callID, "session_id", sessionID)
			sdpBody := sdp.BuildResponseSDP(m.advertiseAddr, sess.LocalPort, sess.Codec)
			return sess, sdpBody, nil
		}
	}

	// Allocate ports
	rtpPort, rtcpPort, err := m.portPool.Allocate()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to allocate ports: %w", err)
	}

	// Negotiate codec (only PCMU supported)
	selectedCodec := ""
	for _, codec := range offeredCodecs {
		if codec == "0" { // PCMU
			selectedCodec = "0"
			break
		}
	}
	if selectedCodec == "" {
		m.portPool.Release(rtpPort)
		return nil, nil, fmt.Errorf("no supported codec offered (PCMU required)")
	}

	// Create session
	ctx, cancel := context.WithCancel(context.Background())
	sess := &Session{
		ID:           uuid.New().String(),
		CallID:       callID,
		LocalAddr:    m.advertiseAddr,
		LocalPort:    rtpPort,
		RTCPPort:     rtcpPort,
		RemoteAddr:   remoteAddr,
		RemotePort:   remotePort,
		Codec:        selectedCodec,
		State:        rtpv1.SessionState_SESSION_STATE_CREATED,
		CreatedAt:    time.Now(),
		ctx:          ctx,
		cancel:       cancel,
		playbackDone: make(chan struct{}),
	}

	m.sessions[sess.ID] = sess
	m.callToSession[callID] = sess.ID

	// Build SDP
	sdpBody := sdp.BuildResponseSDP(m.advertiseAddr, rtpPort, selectedCodec)

	slog.Info("[SessionMgr] Session created",
		"session_id", sess.ID,
		"call_id", callID,
		"local_port", rtpPort,
		"remote", fmt.Sprintf("%s:%d", remoteAddr, remotePort))

	return sess, sdpBody, nil
}

// GetSession retrieves a session by ID
func (m *Manager) GetSession(sessionID string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	sess, ok := m.sessions[sessionID]
	return sess, ok
}

// UpdateRemoteEndpoint updates the remote RTP endpoint for a session.
// Used when SDP answer arrives after session creation (B2BUA scenario).
func (m *Manager) UpdateRemoteEndpoint(sessionID, remoteAddr string, remotePort int) error {
	m.mu.RLock()
	sess, ok := m.sessions[sessionID]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	sess.mu.Lock()
	sess.RemoteAddr = remoteAddr
	sess.RemotePort = remotePort
	if sess.State == rtpv1.SessionState_SESSION_STATE_PENDING_REMOTE {
		sess.State = rtpv1.SessionState_SESSION_STATE_ACTIVE
	}
	sess.mu.Unlock()

	slog.Info("[SessionMgr] Remote endpoint updated",
		"session_id", sessionID,
		"remote", fmt.Sprintf("%s:%d", remoteAddr, remotePort),
	)

	return nil
}

// GetSessionEndpoint returns endpoint info for bridging.
func (m *Manager) GetSessionEndpoint(sessionID string) (localAddr string, localPort int, remoteAddr string, remotePort int, err error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sess, ok := m.sessions[sessionID]
	if !ok {
		return "", 0, "", 0, fmt.Errorf("session not found: %s", sessionID)
	}

	sess.mu.RLock()
	defer sess.mu.RUnlock()

	return sess.LocalAddr, sess.LocalPort, sess.RemoteAddr, sess.RemotePort, nil
}

// CreateSessionPendingRemote creates a session without remote endpoint info.
// Used for B2BUA B-leg where remote is set later via UpdateRemoteEndpoint.
func (m *Manager) CreateSessionPendingRemote(callID string, offeredCodecs []string) (*Session, []byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if session already exists for this call
	if sessionID, exists := m.callToSession[callID]; exists {
		if sess, ok := m.sessions[sessionID]; ok {
			slog.Warn("[SessionMgr] Session already exists for call", "call_id", callID, "session_id", sessionID)
			sdpBody := sdp.BuildResponseSDP(m.advertiseAddr, sess.LocalPort, sess.Codec)
			return sess, sdpBody, nil
		}
	}

	// Allocate ports
	rtpPort, rtcpPort, err := m.portPool.Allocate()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to allocate ports: %w", err)
	}

	// Negotiate codec (only PCMU supported)
	selectedCodec := ""
	for _, codec := range offeredCodecs {
		if codec == "0" { // PCMU
			selectedCodec = "0"
			break
		}
	}
	if selectedCodec == "" {
		m.portPool.Release(rtpPort)
		return nil, nil, fmt.Errorf("no supported codec offered (PCMU required)")
	}

	// Create session with empty remote endpoint (pending)
	ctx, cancel := context.WithCancel(context.Background())
	sess := &Session{
		ID:           uuid.New().String(),
		CallID:       callID,
		LocalAddr:    m.advertiseAddr,
		LocalPort:    rtpPort,
		RTCPPort:     rtcpPort,
		RemoteAddr:   "", // Empty - to be set later
		RemotePort:   0,  // Empty - to be set later
		Codec:        selectedCodec,
		State:        rtpv1.SessionState_SESSION_STATE_PENDING_REMOTE,
		CreatedAt:    time.Now(),
		ctx:          ctx,
		cancel:       cancel,
		playbackDone: make(chan struct{}),
	}

	m.sessions[sess.ID] = sess
	m.callToSession[callID] = sess.ID

	// Build SDP (for outgoing INVITE)
	sdpBody := sdp.BuildResponseSDP(m.advertiseAddr, rtpPort, selectedCodec)

	slog.Info("[SessionMgr] Session created (pending remote)",
		"session_id", sess.ID,
		"call_id", callID,
		"local_port", rtpPort)

	return sess, sdpBody, nil
}

// SetSessionBridged marks a session as part of a bridge.
func (m *Manager) SetSessionBridged(sessionID string) error {
	m.mu.RLock()
	sess, ok := m.sessions[sessionID]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	sess.mu.Lock()
	sess.State = rtpv1.SessionState_SESSION_STATE_BRIDGED
	sess.mu.Unlock()

	return nil
}

// DestroySession destroys a session and releases resources
func (m *Manager) DestroySession(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	sess, ok := m.sessions[sessionID]
	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	// Cancel context to stop any playback
	sess.cancel()

	// Stop media playback
	m.mediaService.Stop(sess.CallID)

	// Release ports
	m.portPool.Release(sess.LocalPort)

	// Update state
	sess.mu.Lock()
	sess.State = rtpv1.SessionState_SESSION_STATE_TERMINATED
	sess.mu.Unlock()

	// Remove from maps
	delete(m.sessions, sessionID)
	delete(m.callToSession, sess.CallID)

	slog.Info("[SessionMgr] Session destroyed", "session_id", sessionID, "call_id", sess.CallID)
	return nil
}

// PlayAudio starts audio playback for a session
func (m *Manager) PlayAudio(sessionID, filePath string, eventCh chan<- *rtpv1.PlaybackEvent) error {
	m.mu.RLock()
	sess, ok := m.sessions[sessionID]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("session not found: %s", sessionID)
	}

	// Update state
	sess.mu.Lock()
	sess.State = rtpv1.SessionState_SESSION_STATE_ACTIVE
	sess.mu.Unlock()

	// Create play request
	playReq := media.PlayRequest{
		CallID:    sess.CallID,
		File:      filePath,
		Codec:     sess.Codec,
		LocalAddr: sess.LocalAddr,
		LocalPort: sess.LocalPort,
		Endpoint:  sess.RemoteAddr,
		Port:      sess.RemotePort,
		OnComplete: func(callID string, data interface{}) error {
			// Send completion event
			eventCh <- &rtpv1.PlaybackEvent{
				SessionId: sessionID,
				Event: &rtpv1.PlaybackEvent_Completed{
					Completed: &rtpv1.PlaybackCompleted{
						TotalFramesSent: 0, // TODO: track actual frames
					},
				},
			}
			close(eventCh)
			return nil
		},
		OnError: func(callID string, err error) {
			// Send error event and close channel
			eventCh <- &rtpv1.PlaybackEvent{
				SessionId: sessionID,
				Event: &rtpv1.PlaybackEvent_Error{
					Error: &rtpv1.PlaybackError{
						Code:    "PLAYBACK_FAILED",
						Message: err.Error(),
					},
				},
			}
			close(eventCh)
		},
	}

	// Send started event
	eventCh <- &rtpv1.PlaybackEvent{
		SessionId: sessionID,
		Event: &rtpv1.PlaybackEvent_Started{
			Started: &rtpv1.PlaybackStarted{},
		},
	}

	// Start playback
	if err := m.mediaService.Play(sess.ctx, playReq); err != nil {
		eventCh <- &rtpv1.PlaybackEvent{
			SessionId: sessionID,
			Event: &rtpv1.PlaybackEvent_Error{
				Error: &rtpv1.PlaybackError{
					Code:    "PLAYBACK_FAILED",
					Message: err.Error(),
				},
			},
		}
		close(eventCh)
		return err
	}

	return nil
}

// StopAudio stops audio playback for a session
func (m *Manager) StopAudio(sessionID string) (bool, error) {
	m.mu.RLock()
	sess, ok := m.sessions[sessionID]
	m.mu.RUnlock()

	if !ok {
		return false, nil // Idempotent
	}

	err := m.mediaService.Stop(sess.CallID)
	return err == nil, err
}

// Count returns the number of active sessions
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sessions)
}

// CloseAll destroys all sessions
func (m *Manager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, sess := range m.sessions {
		sess.cancel()
		m.mediaService.Stop(sess.CallID)
		m.portPool.Release(sess.LocalPort)
	}
	m.sessions = make(map[string]*Session)
	m.callToSession = make(map[string]string)
}
