package server

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/sebas/switchboard/internal/rtpmanager/asr"
	"github.com/sebas/switchboard/internal/rtpmanager/bridge"
	"github.com/sebas/switchboard/internal/rtpmanager/media"
	"github.com/sebas/switchboard/internal/rtpmanager/portpool"
	"github.com/sebas/switchboard/internal/rtpmanager/session"
	"github.com/sebas/switchboard/internal/rtpmanager/tts"
	rtpv1 "github.com/sebas/switchboard/pkg/rtpmanager/v1"
)

// Config holds RTP Manager configuration
type Config struct {
	GRPCPort      int
	GRPCBindAddr  string
	AdvertiseAddr string
	RTPPortMin    int
	RTPPortMax    int
	AudioBasePath string
	TTSServerURL  string
	ASRServerURL  string
}

// Server implements the RTPManagerService gRPC server
type Server struct {
	rtpv1.UnimplementedRTPManagerServiceServer
	sessionMgr *session.Manager
	bridgeMgr  *bridge.Manager
	portPool   *portpool.PortPool
	config     *Config
	ttsClient  *tts.Client
	asrClient  *asr.Client
}

// NewServer creates a new RTP Manager gRPC server
func NewServer(cfg *Config) (*Server, error) {
	// Create port pool
	pool := portpool.NewPortPool(cfg.RTPPortMin, cfg.RTPPortMax)

	// Create media service
	mediaService := media.NewLocalService()

	// Create session manager
	sessionMgr := session.NewManager(pool, mediaService, cfg.AdvertiseAddr)

	// Create bridge manager
	bridgeMgr := bridge.NewManager()

	// Create TTS client (optional - may not be configured)
	var ttsClient *tts.Client
	if cfg.TTSServerURL != "" {
		ttsClient = tts.NewClient(tts.Config{
			ServerURL: cfg.TTSServerURL,
		})
		slog.Info("[gRPC] TTS client configured", "server", cfg.TTSServerURL)
	}

	// Create ASR client (optional - may not be configured)
	var asrClient *asr.Client
	if cfg.ASRServerURL != "" {
		asrClient = asr.NewClient(asr.Config{
			ServerURL: cfg.ASRServerURL,
		})
		slog.Info("[gRPC] ASR client configured", "server", cfg.ASRServerURL)
	}

	return &Server{
		sessionMgr: sessionMgr,
		bridgeMgr:  bridgeMgr,
		portPool:   pool,
		config:     cfg,
		ttsClient:  ttsClient,
		asrClient:  asrClient,
	}, nil
}

// CreateSession implements RTPManagerService.CreateSession
func (s *Server) CreateSession(ctx context.Context, req *rtpv1.CreateSessionRequest) (*rtpv1.CreateSessionResponse, error) {
	slog.Info("[gRPC] CreateSession",
		"call_id", req.CallId,
		"remote", fmt.Sprintf("%s:%d", req.RemoteAddr, req.RemotePort),
		"codecs", req.OfferedCodecs)

	sess, sdpBody, err := s.sessionMgr.CreateSession(
		req.CallId,
		req.RemoteAddr,
		int(req.RemotePort),
		req.OfferedCodecs,
	)
	if err != nil {
		slog.Error("[gRPC] CreateSession failed", "error", err)
		return &rtpv1.CreateSessionResponse{
			Status: &rtpv1.SessionStatus{
				State:        rtpv1.SessionState_SESSION_STATE_ERROR,
				ErrorMessage: err.Error(),
			},
		}, nil
	}

	return &rtpv1.CreateSessionResponse{
		SessionId:     sess.ID,
		LocalAddr:     sess.LocalAddr,
		LocalPort:     int32(sess.LocalPort),
		SelectedCodec: sess.Codec,
		SdpBody:       sdpBody,
		Status: &rtpv1.SessionStatus{
			State: rtpv1.SessionState_SESSION_STATE_CREATED,
		},
	}, nil
}

// DestroySession implements RTPManagerService.DestroySession
func (s *Server) DestroySession(ctx context.Context, req *rtpv1.DestroySessionRequest) (*rtpv1.DestroySessionResponse, error) {
	slog.Info("[gRPC] DestroySession", "session_id", req.SessionId, "reason", req.Reason)

	err := s.sessionMgr.DestroySession(req.SessionId)
	if err != nil {
		slog.Warn("[gRPC] DestroySession failed", "error", err)
		return &rtpv1.DestroySessionResponse{
			SessionId: req.SessionId,
			Status: &rtpv1.SessionStatus{
				State:        rtpv1.SessionState_SESSION_STATE_ERROR,
				ErrorMessage: err.Error(),
			},
		}, nil
	}

	return &rtpv1.DestroySessionResponse{
		SessionId: req.SessionId,
		Status: &rtpv1.SessionStatus{
			State: rtpv1.SessionState_SESSION_STATE_TERMINATED,
		},
	}, nil
}

// PlayAudio implements RTPManagerService.PlayAudio (server streaming)
func (s *Server) PlayAudio(req *rtpv1.PlayAudioRequest, stream rtpv1.RTPManagerService_PlayAudioServer) error {
	slog.Info("[gRPC] PlayAudio",
		"session_id", req.SessionId,
		"file", req.FilePath,
		"files", req.Files,
		"loop", req.Loop,
	)

	// Create event channel
	eventCh := make(chan *rtpv1.PlaybackEvent, 10)

	// Build play request
	playReq := session.PlayAudioRequest{
		FilePath: req.FilePath,
		Files:    req.Files,
		Loop:     req.Loop,
	}

	// Start playback in background
	if err := s.sessionMgr.PlayAudio(req.SessionId, playReq, eventCh); err != nil {
		return err
	}

	// Stream events to client
	for event := range eventCh {
		if err := stream.Send(event); err != nil {
			slog.Error("[gRPC] Failed to send playback event", "error", err)
			return err
		}
	}

	return nil
}

// StopAudio implements RTPManagerService.StopAudio
func (s *Server) StopAudio(ctx context.Context, req *rtpv1.StopAudioRequest) (*rtpv1.StopAudioResponse, error) {
	slog.Info("[gRPC] StopAudio", "session_id", req.SessionId)

	wasPlaying, _ := s.sessionMgr.StopAudio(req.SessionId)

	return &rtpv1.StopAudioResponse{
		SessionId:  req.SessionId,
		WasPlaying: wasPlaying,
	}, nil
}

// Health implements RTPManagerService.Health
func (s *Server) Health(ctx context.Context, req *rtpv1.HealthRequest) (*rtpv1.HealthResponse, error) {
	return &rtpv1.HealthResponse{
		Healthy:        true,
		ActiveSessions: int32(s.sessionMgr.Count()),
		AvailablePorts: int32(s.portPool.Available()),
	}, nil
}

// UpdateSessionRemote implements RTPManagerService.UpdateSessionRemote
func (s *Server) UpdateSessionRemote(ctx context.Context, req *rtpv1.UpdateSessionRemoteRequest) (*rtpv1.UpdateSessionRemoteResponse, error) {
	slog.Info("[gRPC] UpdateSessionRemote",
		"session_id", req.SessionId,
		"remote", fmt.Sprintf("%s:%d", req.RemoteAddr, req.RemotePort),
	)

	if err := s.sessionMgr.UpdateRemoteEndpoint(req.SessionId, req.RemoteAddr, int(req.RemotePort)); err != nil {
		slog.Error("[gRPC] UpdateSessionRemote failed", "error", err)
		return &rtpv1.UpdateSessionRemoteResponse{
			SessionId: req.SessionId,
			Status: &rtpv1.SessionStatus{
				State:        rtpv1.SessionState_SESSION_STATE_ERROR,
				ErrorMessage: err.Error(),
			},
		}, nil
	}

	return &rtpv1.UpdateSessionRemoteResponse{
		SessionId: req.SessionId,
		Status: &rtpv1.SessionStatus{
			State: rtpv1.SessionState_SESSION_STATE_ACTIVE,
		},
	}, nil
}

// BridgeMedia implements RTPManagerService.BridgeMedia
func (s *Server) BridgeMedia(ctx context.Context, req *rtpv1.BridgeMediaRequest) (*rtpv1.BridgeMediaResponse, error) {
	slog.Info("[gRPC] BridgeMedia",
		"session_a", req.SessionAId,
		"session_b", req.SessionBId,
	)

	// Get endpoint info for session A
	localAddrA, localPortA, remoteAddrA, remotePortA, err := s.sessionMgr.GetSessionEndpoint(req.SessionAId)
	if err != nil {
		slog.Error("[gRPC] BridgeMedia failed", "error", fmt.Sprintf("session A: %v", err))
		return &rtpv1.BridgeMediaResponse{
			Status: &rtpv1.SessionStatus{
				State:        rtpv1.SessionState_SESSION_STATE_ERROR,
				ErrorMessage: fmt.Sprintf("session A: %v", err),
			},
		}, nil
	}

	// Get endpoint info for session B
	localAddrB, localPortB, remoteAddrB, remotePortB, err := s.sessionMgr.GetSessionEndpoint(req.SessionBId)
	if err != nil {
		slog.Error("[gRPC] BridgeMedia failed", "error", fmt.Sprintf("session B: %v", err))
		return &rtpv1.BridgeMediaResponse{
			Status: &rtpv1.SessionStatus{
				State:        rtpv1.SessionState_SESSION_STATE_ERROR,
				ErrorMessage: fmt.Sprintf("session B: %v", err),
			},
		}, nil
	}

	// Create bridge endpoints
	endpointA := &bridge.Endpoint{
		SessionID:  req.SessionAId,
		LocalAddr:  localAddrA,
		LocalPort:  localPortA,
		RemoteAddr: remoteAddrA,
		RemotePort: remotePortA,
	}
	endpointB := &bridge.Endpoint{
		SessionID:  req.SessionBId,
		LocalAddr:  localAddrB,
		LocalPort:  localPortB,
		RemoteAddr: remoteAddrB,
		RemotePort: remotePortB,
	}

	bridgeID, err := s.bridgeMgr.CreateBridge(endpointA, endpointB)
	if err != nil {
		slog.Error("[gRPC] BridgeMedia failed", "error", err)
		return &rtpv1.BridgeMediaResponse{
			Status: &rtpv1.SessionStatus{
				State:        rtpv1.SessionState_SESSION_STATE_ERROR,
				ErrorMessage: err.Error(),
			},
		}, nil
	}

	// Mark sessions as bridged (errors are non-fatal, sessions may already be in correct state)
	_ = s.sessionMgr.SetSessionBridged(req.SessionAId)
	_ = s.sessionMgr.SetSessionBridged(req.SessionBId)

	slog.Info("[gRPC] BridgeMedia success",
		"bridge_id", bridgeID,
		"session_a", req.SessionAId,
		"session_b", req.SessionBId,
	)

	return &rtpv1.BridgeMediaResponse{
		BridgeId: bridgeID,
		Status: &rtpv1.SessionStatus{
			State: rtpv1.SessionState_SESSION_STATE_BRIDGED,
		},
	}, nil
}

// UnbridgeMedia implements RTPManagerService.UnbridgeMedia
func (s *Server) UnbridgeMedia(ctx context.Context, req *rtpv1.UnbridgeMediaRequest) (*rtpv1.UnbridgeMediaResponse, error) {
	slog.Info("[gRPC] UnbridgeMedia",
		"bridge_id", req.BridgeId,
		"session_id", req.SessionId,
	)

	var err error
	bridgeID := req.BridgeId

	if bridgeID != "" {
		err = s.bridgeMgr.DestroyBridge(bridgeID)
	} else if req.SessionId != "" {
		bridgeID, err = s.bridgeMgr.DestroyBySession(req.SessionId)
	} else {
		return &rtpv1.UnbridgeMediaResponse{
			Status: &rtpv1.SessionStatus{
				State:        rtpv1.SessionState_SESSION_STATE_ERROR,
				ErrorMessage: "bridge_id or session_id required",
			},
		}, nil
	}

	if err != nil {
		slog.Error("[gRPC] UnbridgeMedia failed", "error", err)
		return &rtpv1.UnbridgeMediaResponse{
			BridgeId: bridgeID,
			Status: &rtpv1.SessionStatus{
				State:        rtpv1.SessionState_SESSION_STATE_ERROR,
				ErrorMessage: err.Error(),
			},
		}, nil
	}

	return &rtpv1.UnbridgeMediaResponse{
		BridgeId: bridgeID,
		Status: &rtpv1.SessionStatus{
			State: rtpv1.SessionState_SESSION_STATE_TERMINATED,
		},
	}, nil
}

// PlayTTS implements RTPManagerService.PlayTTS (server streaming)
func (s *Server) PlayTTS(req *rtpv1.PlayTTSRequest, stream rtpv1.RTPManagerService_PlayTTSServer) error {
	slog.Info("[gRPC] PlayTTS",
		"session_id", req.SessionId,
		"text", req.Text,
		"voice", req.Voice,
	)

	// Check if TTS is configured
	if s.ttsClient == nil {
		return fmt.Errorf("TTS server not configured")
	}

	// Synthesize speech
	audioData, err := s.ttsClient.Synthesize(stream.Context(), req.Text, req.Voice)
	if err != nil {
		slog.Error("[gRPC] TTS synthesis failed", "error", err)
		return fmt.Errorf("TTS synthesis failed: %w", err)
	}

	slog.Debug("[gRPC] TTS synthesis complete", "audio_size", len(audioData))

	// Create event channel
	eventCh := make(chan *rtpv1.PlaybackEvent, 10)

	// Play the synthesized audio via session manager
	if err := s.sessionMgr.PlayTTSAudio(req.SessionId, audioData, eventCh); err != nil {
		return err
	}

	// Stream events to client
	for event := range eventCh {
		if err := stream.Send(event); err != nil {
			slog.Error("[gRPC] Failed to send TTS playback event", "error", err)
			return err
		}
	}

	return nil
}

// Listen implements RTPManagerService.Listen
func (s *Server) Listen(ctx context.Context, req *rtpv1.ListenRequest) (*rtpv1.ListenResponse, error) {
	slog.Info("[gRPC] Listen",
		"session_id", req.SessionId,
		"max_duration_ms", req.MaxDurationMs,
		"silence_timeout_ms", req.SilenceTimeoutMs,
	)

	// Check if ASR is configured
	if s.asrClient == nil {
		return nil, fmt.Errorf("ASR server not configured")
	}

	// Set defaults
	maxDuration := int(req.MaxDurationMs)
	if maxDuration == 0 {
		maxDuration = 10000 // 10 seconds default
	}
	silenceTimeout := int(req.SilenceTimeoutMs)
	if silenceTimeout == 0 {
		silenceTimeout = 1500 // 1.5 seconds default
	}

	// Capture audio from session
	audioData, durationMs, timeout, err := s.sessionMgr.Listen(ctx, req.SessionId, maxDuration, silenceTimeout)
	if err != nil {
		slog.Error("[gRPC] Listen failed", "error", err)
		return nil, fmt.Errorf("listen failed: %w", err)
	}

	// If no audio captured, return empty
	if len(audioData) == 0 {
		return &rtpv1.ListenResponse{
			SessionId:  req.SessionId,
			Text:       "",
			DurationMs: 0,
			Timeout:    timeout,
		}, nil
	}

	// Transcribe audio
	text, err := s.asrClient.Transcribe(ctx, audioData)
	if err != nil {
		slog.Error("[gRPC] Transcription failed", "error", err)
		return nil, fmt.Errorf("transcription failed: %w", err)
	}

	slog.Info("[gRPC] Listen complete",
		"session_id", req.SessionId,
		"duration_ms", durationMs,
		"text", text,
	)

	return &rtpv1.ListenResponse{
		SessionId:  req.SessionId,
		Text:       text,
		DurationMs: int32(durationMs),
		Timeout:    timeout,
	}, nil
}

// Close cleans up resources
func (s *Server) Close() error {
	s.bridgeMgr.CloseAll()
	s.sessionMgr.CloseAll()
	return nil
}
