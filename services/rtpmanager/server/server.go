package server

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/sebas/switchboard/services/rtpmanager/media"
	"github.com/sebas/switchboard/services/rtpmanager/portpool"
	"github.com/sebas/switchboard/services/rtpmanager/session"
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
}

// Server implements the RTPManagerService gRPC server
type Server struct {
	rtpv1.UnimplementedRTPManagerServiceServer
	sessionMgr *session.Manager
	portPool   *portpool.PortPool
	config     *Config
}

// NewServer creates a new RTP Manager gRPC server
func NewServer(cfg *Config) (*Server, error) {
	// Create port pool
	pool := portpool.NewPortPool(cfg.RTPPortMin, cfg.RTPPortMax)

	// Create media service
	mediaService := media.NewLocalService()

	// Create session manager
	sessionMgr := session.NewManager(pool, mediaService, cfg.AdvertiseAddr)

	return &Server{
		sessionMgr: sessionMgr,
		portPool:   pool,
		config:     cfg,
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
	slog.Info("[gRPC] PlayAudio", "session_id", req.SessionId, "file", req.FilePath)

	// Create event channel
	eventCh := make(chan *rtpv1.PlaybackEvent, 10)

	// Start playback in background
	if err := s.sessionMgr.PlayAudio(req.SessionId, req.FilePath, eventCh); err != nil {
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

// Close cleans up resources
func (s *Server) Close() error {
	s.sessionMgr.CloseAll()
	return nil
}
