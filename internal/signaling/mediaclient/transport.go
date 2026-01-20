package mediaclient

import (
	"context"
)

// SessionInfo contains parameters for creating a media session
type SessionInfo struct {
	CallID        string   // SIP Call-ID for correlation
	RemoteAddr    string   // Client IP address from SDP
	RemotePort    int      // Client RTP port from SDP
	OfferedCodecs []string // Payload types offered by client
}

// SessionResult contains the result of session creation
type SessionResult struct {
	SessionID     string // Unique session identifier
	LocalAddr     string // Address for SDP
	LocalPort     int    // Port for SDP
	SDPBody       []byte // Complete SDP answer
	SelectedCodec string // Negotiated codec
}

// PlayRequest contains audio playback parameters
type PlayRequest struct {
	SessionID  string
	AudioFile  string
	Loop       bool
	OnComplete func(sessionID string) // Called when playback completes
}

// PlayState represents the state of playback
type PlayState int

const (
	PlayStateStarted PlayState = iota
	PlayStateProgress
	PlayStateCompleted
	PlayStateStopped
	PlayStateError
)

// PlayStatus represents playback progress
type PlayStatus struct {
	SessionID string
	State     PlayState
	Error     error
}

// TerminateReason indicates why a session was terminated
type TerminateReason int

const (
	TerminateReasonNormal TerminateReason = iota
	TerminateReasonBYE
	TerminateReasonCancel
	TerminateReasonError
	TerminateReasonTimeout
)

// BridgeInfo contains information about an active bridge
type BridgeInfo struct {
	BridgeID   string
	SessionAID string
	SessionBID string
}

// StatsProvider provides pool statistics (optional interface)
type StatsProvider interface {
	Stats() PoolStats
}

// Transport abstracts media service communication.
// Implementations: LocalTransport (in-process), GRPCTransport (remote)
type Transport interface {
	// CreateSession allocates resources and returns SDP
	CreateSession(ctx context.Context, info SessionInfo) (*SessionResult, error)

	// CreateSessionPendingRemote allocates resources without remote endpoint.
	// Used for B2BUA B-leg where remote is set later via UpdateSessionRemote.
	CreateSessionPendingRemote(ctx context.Context, callID string, codecs []string) (*SessionResult, error)

	// UpdateSessionRemote updates the remote endpoint for a session.
	// Used when SDP answer arrives after session creation (B2BUA scenario).
	UpdateSessionRemote(ctx context.Context, sessionID, remoteAddr string, remotePort int) error

	// DestroySession releases resources
	DestroySession(ctx context.Context, sessionID string, reason TerminateReason) error

	// PlayAudio streams audio, returning a channel for status updates
	PlayAudio(ctx context.Context, req PlayRequest) (<-chan PlayStatus, error)

	// StopAudio cancels ongoing playback
	StopAudio(ctx context.Context, sessionID string) error

	// BridgeMedia connects two sessions for bidirectional RTP relay.
	// Returns a bridge ID for later unbridging.
	BridgeMedia(ctx context.Context, sessionAID, sessionBID string) (string, error)

	// UnbridgeMedia disconnects two bridged sessions.
	UnbridgeMedia(ctx context.Context, bridgeID string) error

	// Ready checks if transport is connected and healthy
	Ready() bool

	// Close releases transport resources
	Close() error
}
