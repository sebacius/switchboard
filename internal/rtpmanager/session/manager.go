package session

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pion/rtp"
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

	// Stop media playback (best effort - error is not critical during cleanup)
	_ = m.mediaService.Stop(sess.CallID)

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

// PlayAudioRequest contains parameters for playing audio
type PlayAudioRequest struct {
	FilePath string   // Single file (for backwards compatibility)
	Files    []string // Playlist of files (preferred)
	Loop     bool     // Loop the playlist
}

// PlayAudio starts audio playback for a session
func (m *Manager) PlayAudio(sessionID string, req PlayAudioRequest, eventCh chan<- *rtpv1.PlaybackEvent) error {
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

	// Build file list (prefer Files over FilePath)
	files := req.Files
	if len(files) == 0 && req.FilePath != "" {
		files = []string{req.FilePath}
	}

	// Create play request
	playReq := media.PlayRequest{
		CallID:    sess.CallID,
		Files:     files,
		Loop:      req.Loop,
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

// PlayTTSAudio plays synthesized TTS audio (WAV data) for a session
func (m *Manager) PlayTTSAudio(sessionID string, audioData []byte, eventCh chan<- *rtpv1.PlaybackEvent) error {
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

	// Create play request with raw audio data
	playReq := media.PlayRequest{
		CallID:    sess.CallID,
		RawAudio:  audioData,
		Loop:      false,
		Codec:     sess.Codec,
		LocalAddr: sess.LocalAddr,
		LocalPort: sess.LocalPort,
		Endpoint:  sess.RemoteAddr,
		Port:      sess.RemotePort,
		OnComplete: func(callID string, data interface{}) error {
			eventCh <- &rtpv1.PlaybackEvent{
				SessionId: sessionID,
				Event: &rtpv1.PlaybackEvent_Completed{
					Completed: &rtpv1.PlaybackCompleted{
						TotalFramesSent: 0,
					},
				},
			}
			close(eventCh)
			return nil
		},
		OnError: func(callID string, err error) {
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
		_ = m.mediaService.Stop(sess.CallID)
		m.portPool.Release(sess.LocalPort)
	}
	m.sessions = make(map[string]*Session)
	m.callToSession = make(map[string]string)
}

// Listen captures incoming RTP audio from a session until silence is detected
// or max duration is reached. Returns WAV audio data suitable for Whisper (16kHz).
func (m *Manager) Listen(ctx context.Context, sessionID string, maxDurationMs, silenceTimeoutMs int) (audioData []byte, durationMs int, timeout bool, err error) {
	m.mu.RLock()
	sess, ok := m.sessions[sessionID]
	m.mu.RUnlock()

	if !ok {
		return nil, 0, false, fmt.Errorf("session not found: %s", sessionID)
	}

	// First stop any active playback on this session
	_ = m.mediaService.Stop(sess.CallID)

	// Wait a moment for the playback socket to be released
	time.Sleep(100 * time.Millisecond)

	slog.Info("[SessionMgr] Starting audio capture",
		"session_id", sessionID,
		"max_duration_ms", maxDurationMs,
		"silence_timeout_ms", silenceTimeoutMs,
		"local_port", sess.LocalPort,
	)

	// Create context with max duration timeout
	listenCtx, cancel := context.WithTimeout(ctx, time.Duration(maxDurationMs)*time.Millisecond)
	defer cancel()

	// Bind to local RTP port to receive incoming packets
	localAddr := &net.UDPAddr{
		Port: sess.LocalPort,
		IP:   net.IPv4zero,
	}

	conn, err := net.ListenUDP("udp", localAddr)
	if err != nil {
		return nil, 0, false, fmt.Errorf("failed to bind to local RTP port %d: %w", sess.LocalPort, err)
	}
	defer conn.Close()

	// Set read deadline based on silence timeout
	silenceTimeout := time.Duration(silenceTimeoutMs) * time.Millisecond

	// Buffer for receiving RTP packets
	buf := make([]byte, 1500)

	// Accumulated PCM data (8kHz, 16-bit)
	var pcmData []byte
	startTime := time.Now()
	lastVoiceTime := startTime
	framesReceived := 0

	// Silence detection threshold (empirical value for voice vs silence)
	// This is the squared RMS energy threshold
	const silenceThreshold = 100000.0 // Adjust based on testing

	for {
		select {
		case <-listenCtx.Done():
			// Max duration reached
			timeout = true
			slog.Info("[SessionMgr] Audio capture timeout",
				"session_id", sessionID,
				"frames_received", framesReceived,
			)
			goto done
		default:
		}

		// Set read deadline for silence detection
		conn.SetReadDeadline(time.Now().Add(silenceTimeout))

		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				// Silence timeout - no packets received
				if framesReceived > 0 {
					slog.Info("[SessionMgr] Silence detected (no packets)",
						"session_id", sessionID,
						"frames_received", framesReceived,
					)
					goto done
				}
				// No audio received yet, keep waiting
				continue
			}
			// Other error
			return nil, 0, false, fmt.Errorf("UDP read error: %w", err)
		}

		// Parse RTP packet
		packet := &rtp.Packet{}
		if err := packet.Unmarshal(buf[:n]); err != nil {
			slog.Warn("[SessionMgr] Failed to parse RTP packet", "error", err)
			continue
		}

		// Only process PCMU (payload type 0)
		if packet.PayloadType != 0 {
			continue
		}

		// Decode PCMU to PCM
		pcm := media.PCMUToPCM(packet.Payload)
		framesReceived++

		// Calculate energy for silence detection
		energy := media.CalculateEnergy(pcm)

		if energy > silenceThreshold {
			// Voice detected
			lastVoiceTime = time.Now()
			pcmData = append(pcmData, pcm...)
		} else {
			// Silence detected
			// Still append the audio to avoid cutting off words
			pcmData = append(pcmData, pcm...)

			// Check if silence has lasted long enough
			if time.Since(lastVoiceTime) > silenceTimeout && len(pcmData) > 0 {
				slog.Info("[SessionMgr] Silence detected (energy)",
					"session_id", sessionID,
					"frames_received", framesReceived,
					"energy", energy,
				)
				goto done
			}
		}
	}

done:
	if len(pcmData) == 0 {
		slog.Info("[SessionMgr] No audio captured", "session_id", sessionID)
		return nil, 0, timeout, nil
	}

	// Calculate duration
	durationMs = int(time.Since(startTime).Milliseconds())

	// Upsample from 8kHz to 16kHz for Whisper
	pcm16k := media.Upsample8to16(pcmData)

	// Build WAV file
	wavData := media.BuildWAVData(pcm16k, 16000)

	slog.Info("[SessionMgr] Audio capture complete",
		"session_id", sessionID,
		"frames_received", framesReceived,
		"duration_ms", durationMs,
		"pcm_8k_size", len(pcmData),
		"wav_16k_size", len(wavData),
	)

	return wavData, durationMs, timeout, nil
}
