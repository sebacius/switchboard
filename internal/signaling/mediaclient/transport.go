package mediaclient

import (
	"context"
)

// CodecOffer is one offered format together with the rtpmap that gives it
// meaning. A bare payload-type number is enough for a static codec such as PCMU
// but not for a dynamic one: telephone-event may be 101 in one offer and 96 in
// another, and only the rtpmap says which. Carrying it is what makes DTMF
// negotiable at all.
type CodecOffer struct {
	PayloadType  int
	EncodingName string // e.g. "PCMU", "telephone-event"; empty for a static type
	ClockRate    int
	FMTP         string // e.g. "0-15"
}

// SessionInfo contains parameters for creating a media session
type SessionInfo struct {
	CallID        string   // SIP Call-ID for correlation
	RemoteAddr    string   // Client IP address from SDP
	RemotePort    int      // Client RTP port from SDP
	OfferedCodecs []string // Payload types offered by client
	Offered       []CodecOffer
}

// SessionResult contains the result of session creation
type SessionResult struct {
	SessionID     string // Unique session identifier
	LocalAddr     string // Address for SDP
	LocalPort     int    // Port for SDP
	SDPBody       []byte // Complete SDP answer
	SelectedCodec string // Negotiated codec
	// TelephoneEventPT is the negotiated RFC 4733 payload type, or 0 when the
	// offer carried none. Zero means this leg has no DTMF transport.
	TelephoneEventPT int
}

// CollectPrompt is what the caller hears during a digit collection.
type CollectPrompt struct {
	Text  string
	Voice string
	File  string
	Files []string
}

// CollectRequest parameterises one digit collection.
type CollectRequest struct {
	SessionID string
	// Prompt is played while collecting. Nil collects in silence.
	Prompt *CollectPrompt
	// Interruptible lets the first digit stop the prompt.
	Interruptible bool
	MaxDigits     int
	Terminators   string
	// FirstDigitTimeoutMs is measured from the END of the prompt, so a caller is
	// never timed out while still being told their options.
	FirstDigitTimeoutMs int
	InterDigitTimeoutMs int
	OverallTimeoutMs    int
	// FlushBuffer discards digits pressed before this collection started.
	FlushBuffer bool
}

// CollectReason says why a collection ended.
type CollectReason int

const (
	CollectReasonUnspecified CollectReason = iota
	CollectReasonTerminator
	CollectReasonMaxDigits
	CollectReasonInterDigitTimeout
	CollectReasonFirstDigitTimeout
	CollectReasonNoInput
	CollectReasonCanceled
	// CollectReasonNoDTMFTransport means the leg negotiated no telephone-event,
	// so no digit could ever arrive. Distinct from silence: the flow must
	// degrade rather than keep waiting.
	CollectReasonNoDTMFTransport
	CollectReasonError
)

// String renders the reason for logs and CDR hops.
func (r CollectReason) String() string {
	switch r {
	case CollectReasonTerminator:
		return "terminator"
	case CollectReasonMaxDigits:
		return "max_digits"
	case CollectReasonInterDigitTimeout:
		return "inter_digit_timeout"
	case CollectReasonFirstDigitTimeout:
		return "first_digit_timeout"
	case CollectReasonNoInput:
		return "no_input"
	case CollectReasonCanceled:
		return "canceled"
	case CollectReasonNoDTMFTransport:
		return "no_dtmf_transport"
	case CollectReasonError:
		return "error"
	default:
		return "unspecified"
	}
}

// CollectResult is what a collection gathered.
type CollectResult struct {
	Digits            string
	Reason            CollectReason
	PromptInterrupted bool
	Err               string
}

// PlayRequest contains audio playback parameters
type PlayRequest struct {
	SessionID  string
	AudioFile  string   // Single file (for backwards compatibility)
	Files      []string // Playlist of files (preferred over AudioFile)
	Loop       bool
	OnComplete func(sessionID string) // Called when playback completes
}

// TTSRequest contains text-to-speech playback parameters
type TTSRequest struct {
	SessionID  string
	Text       string // Text to synthesize
	Voice      string // Voice name (e.g., "alloy", "echo", "nova")
	OnComplete func(sessionID string)
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

// ListenRequest contains parameters for listening to caller audio
type ListenRequest struct {
	SessionID        string
	MaxDurationMs    int // Maximum recording duration (default 10000ms)
	SilenceTimeoutMs int // Stop after this much silence (default 1500ms)
}

// ListenResult contains the result of listening
type ListenResult struct {
	Text       string // Transcribed text from ASR
	DurationMs int    // How long caller spoke
	Timeout    bool   // True if max_duration was reached
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

	// CreateSessionPendingRemoteOnNode creates a session on the same node as another session.
	// Used for B2BUA B-leg to ensure bridging is possible (both legs on same RTP manager).
	CreateSessionPendingRemoteOnNode(ctx context.Context, peerSessionID, callID string, codecs []string) (*SessionResult, error)

	// UpdateSessionRemote updates the remote endpoint for a session.
	// Used when SDP answer arrives after session creation (B2BUA scenario).
	UpdateSessionRemote(ctx context.Context, sessionID, remoteAddr string, remotePort int) error

	// DestroySession releases resources
	DestroySession(ctx context.Context, sessionID string, reason TerminateReason) error

	// PlayAudio streams audio, returning a channel for status updates
	PlayAudio(ctx context.Context, req PlayRequest) (<-chan PlayStatus, error)

	// StopAudio cancels ongoing playback
	StopAudio(ctx context.Context, sessionID string) error

	// PlayTTS synthesizes text and streams audio
	PlayTTS(ctx context.Context, req TTSRequest) (<-chan PlayStatus, error)

	// BridgeMedia connects two sessions for bidirectional RTP relay.
	// Returns a bridge ID for later unbridging.
	BridgeMedia(ctx context.Context, sessionAID, sessionBID string) (string, error)

	// UnbridgeMedia disconnects two bridged sessions.
	UnbridgeMedia(ctx context.Context, bridgeID string) error

	// Listen captures audio from caller and returns transcribed text via ASR
	Listen(ctx context.Context, req ListenRequest) (*ListenResult, error)

	// CollectDigits plays a prompt and collects DTMF digits in one operation.
	CollectDigits(ctx context.Context, req CollectRequest) (*CollectResult, error)

	// Ready checks if transport is connected and healthy
	Ready() bool

	// Close releases transport resources
	Close() error
}
