package transport

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"

	rtpv1 "github.com/sebas/switchboard/pkg/rtpmanager/v1"
)

// GRPCConfig holds gRPC client configuration
type GRPCConfig struct {
	Address           string
	ConnectTimeout    time.Duration
	KeepaliveInterval time.Duration
	KeepaliveTimeout  time.Duration
}

// DefaultGRPCConfig returns sensible defaults
func DefaultGRPCConfig() GRPCConfig {
	return GRPCConfig{
		Address:           "localhost:9090",
		ConnectTimeout:    10 * time.Second,
		KeepaliveInterval: 30 * time.Second,
		KeepaliveTimeout:  10 * time.Second,
	}
}

// GRPCTransport implements Transport using gRPC to remote RTP Manager
type GRPCTransport struct {
	conn          *grpc.ClientConn
	client        rtpv1.RTPManagerServiceClient
	mu            sync.RWMutex
	ready         bool
	callToSession map[string]string // callID -> sessionID mapping
}

// NewGRPCTransport creates a new gRPC transport client
func NewGRPCTransport(cfg GRPCConfig) (*GRPCTransport, error) {
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                cfg.KeepaliveInterval,
			Timeout:             cfg.KeepaliveTimeout,
			PermitWithoutStream: true,
		}),
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.ConnectTimeout)
	defer cancel()

	conn, err := grpc.DialContext(ctx, cfg.Address, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to RTP Manager at %s: %w", cfg.Address, err)
	}

	t := &GRPCTransport{
		conn:          conn,
		client:        rtpv1.NewRTPManagerServiceClient(conn),
		ready:         true,
		callToSession: make(map[string]string),
	}

	slog.Info("[gRPC] Connected to RTP Manager", "address", cfg.Address)
	return t, nil
}

// CreateSession implements Transport.CreateSession
func (t *GRPCTransport) CreateSession(ctx context.Context, info SessionInfo) (*SessionResult, error) {
	req := &rtpv1.CreateSessionRequest{
		CallId:        info.CallID,
		RemoteAddr:    info.RemoteAddr,
		RemotePort:    int32(info.RemotePort),
		OfferedCodecs: info.OfferedCodecs,
	}

	resp, err := t.client.CreateSession(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("CreateSession RPC failed: %w", err)
	}

	if resp.Status != nil && resp.Status.State == rtpv1.SessionState_SESSION_STATE_ERROR {
		return nil, fmt.Errorf("session creation failed: %s", resp.Status.ErrorMessage)
	}

	// Cache the call->session mapping
	t.mu.Lock()
	t.callToSession[info.CallID] = resp.SessionId
	t.mu.Unlock()

	return &SessionResult{
		SessionID:     resp.SessionId,
		LocalAddr:     resp.LocalAddr,
		LocalPort:     int(resp.LocalPort),
		SDPBody:       resp.SdpBody,
		SelectedCodec: resp.SelectedCodec,
	}, nil
}

// DestroySession implements Transport.DestroySession
func (t *GRPCTransport) DestroySession(ctx context.Context, sessionID string, reason TerminateReason) error {
	req := &rtpv1.DestroySessionRequest{
		SessionId: sessionID,
		Reason:    rtpv1.TerminateReason(reason),
	}

	_, err := t.client.DestroySession(ctx, req)
	if err != nil {
		return fmt.Errorf("DestroySession RPC failed: %w", err)
	}

	// Remove from cache
	t.mu.Lock()
	for callID, sid := range t.callToSession {
		if sid == sessionID {
			delete(t.callToSession, callID)
			break
		}
	}
	t.mu.Unlock()

	return nil
}

// PlayAudio implements Transport.PlayAudio
func (t *GRPCTransport) PlayAudio(ctx context.Context, req PlayRequest) (<-chan PlayStatus, error) {
	grpcReq := &rtpv1.PlayAudioRequest{
		SessionId: req.SessionID,
		FilePath:  req.AudioFile,
		Loop:      req.Loop,
	}

	stream, err := t.client.PlayAudio(ctx, grpcReq)
	if err != nil {
		return nil, fmt.Errorf("PlayAudio RPC failed: %w", err)
	}

	statusCh := make(chan PlayStatus, 10)

	go func() {
		defer close(statusCh)

		for {
			msg, err := stream.Recv()
			if err == io.EOF {
				return
			}
			if err != nil {
				statusCh <- PlayStatus{
					SessionID: req.SessionID,
					State:     PlayStateError,
					Error:     err,
				}
				return
			}

			status := PlayStatus{SessionID: msg.SessionId}

			switch e := msg.Event.(type) {
			case *rtpv1.PlaybackEvent_Started:
				status.State = PlayStateStarted
			case *rtpv1.PlaybackEvent_Progress:
				status.State = PlayStateProgress
			case *rtpv1.PlaybackEvent_Completed:
				status.State = PlayStateCompleted
				statusCh <- status
				if req.OnComplete != nil {
					req.OnComplete(req.SessionID)
				}
				return
			case *rtpv1.PlaybackEvent_Stopped:
				status.State = PlayStateStopped
				statusCh <- status
				return
			case *rtpv1.PlaybackEvent_Error:
				status.State = PlayStateError
				status.Error = fmt.Errorf("%s: %s", e.Error.Code, e.Error.Message)
			}

			statusCh <- status
		}
	}()

	return statusCh, nil
}

// StopAudio implements Transport.StopAudio
func (t *GRPCTransport) StopAudio(ctx context.Context, sessionID string) error {
	req := &rtpv1.StopAudioRequest{
		SessionId: sessionID,
	}

	_, err := t.client.StopAudio(ctx, req)
	return err
}

// Ready implements Transport.Ready
func (t *GRPCTransport) Ready() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if !t.ready || t.conn == nil {
		return false
	}

	// Check actual connection via health endpoint
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := t.client.Health(ctx, &rtpv1.HealthRequest{})
	return err == nil && resp.Healthy
}

// Close implements Transport.Close
func (t *GRPCTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.ready = false
	if t.conn != nil {
		return t.conn.Close()
	}
	return nil
}

// GetSessionID returns the session ID for a call ID
func (t *GRPCTransport) GetSessionID(callID string) (string, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	sessionID, ok := t.callToSession[callID]
	return sessionID, ok
}
