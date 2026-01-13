package b2bua

import (
	"context"
	"time"

	"github.com/sebas/switchboard/internal/signaling/transport"
)

// Bridge connects two call legs for bidirectional media exchange.
//
// A Bridge is created after both legs reach Answered state.
// It coordinates:
//   - RTP forwarding between the two media sessions
//   - Lifecycle monitoring (terminates if either leg hangs up)
//   - Optional features: recording, DTMF relay, etc.
//
// Thread Safety: All methods are safe for concurrent use.
type Bridge interface {
	// ID returns the unique identifier for this bridge.
	ID() string

	// LegA returns the inbound (A) leg.
	LegA() Leg

	// LegB returns the outbound (B) leg.
	LegB() Leg

	// GetState returns the current state of the bridge.
	GetState() BridgeState

	// Info returns detailed information about the bridge.
	Info() *BridgeInfo

	// --- Lifecycle Operations ---

	// Start activates media bridging between the two legs.
	// Both legs must be in Answered state.
	// Transitions bridge from Created to Active.
	// Returns error if either leg is not answered or bridge already active.
	Start(ctx context.Context) error

	// Stop terminates the bridge and optionally hangs up both legs.
	// If hangupLegs is true, sends BYE on both legs.
	// If hangupLegs is false, legs remain connected (for transfer scenarios).
	// Transitions bridge to Terminated.
	// Safe to call multiple times.
	Stop(hangupLegs bool) error

	// WaitForTermination blocks until the bridge terminates.
	// Returns the cause of termination.
	// Returns immediately if already terminated.
	WaitForTermination(ctx context.Context) (TerminationCause, error)

	// --- Event Callbacks ---

	// OnTerminated registers a callback for bridge termination.
	// Called once when bridge reaches Terminated state.
	OnTerminated(fn func(cause TerminationCause))
}

// BridgeInfo contains detailed information about a bridge.
type BridgeInfo struct {
	// Identity
	ID string `json:"id"`

	// Legs
	LegAID string `json:"leg_a_id"`
	LegBID string `json:"leg_b_id"`

	// State
	State            BridgeState      `json:"state"`
	TerminationCause TerminationCause `json:"termination_cause,omitempty"`
	TerminatedBy     string           `json:"terminated_by,omitempty"` // "leg_a", "leg_b", or "local"

	// Media
	Codec            string `json:"codec,omitempty"`
	TranscodingEnabled bool `json:"transcoding_enabled,omitempty"`

	// Timing
	CreatedAt    time.Time `json:"created_at"`
	StartedAt    time.Time `json:"started_at,omitempty"`     // When Start() was called
	TerminatedAt time.Time `json:"terminated_at,omitempty"`

	// Statistics (populated if RTP Manager provides them)
	PacketsA2B int64 `json:"packets_a_to_b,omitempty"` // Packets forwarded A -> B
	PacketsB2A int64 `json:"packets_b_to_a,omitempty"` // Packets forwarded B -> A
	BytesA2B   int64 `json:"bytes_a_to_b,omitempty"`
	BytesB2A   int64 `json:"bytes_b_to_a,omitempty"`
}

// Duration returns the total bridge duration (start to termination).
// Returns 0 if not yet terminated.
func (i *BridgeInfo) Duration() time.Duration {
	if i.StartedAt.IsZero() || i.TerminatedAt.IsZero() {
		return 0
	}
	return i.TerminatedAt.Sub(i.StartedAt)
}

// BridgeOption configures bridge creation.
type BridgeOption func(*bridgeOptions)

type bridgeOptions struct {
	id           string
	autoHangup   bool
	onTerminated func(TerminationCause)
	transport    transport.Transport
}

// WithBridgeID sets a custom bridge ID.
func WithBridgeID(id string) BridgeOption {
	return func(o *bridgeOptions) {
		o.id = id
	}
}

// WithAutoHangup configures whether legs should be hung up on termination.
// Default is true.
func WithAutoHangup(enable bool) BridgeOption {
	return func(o *bridgeOptions) {
		o.autoHangup = enable
	}
}

// WithBridgeOnTerminated sets a termination callback during creation.
func WithBridgeOnTerminated(fn func(TerminationCause)) BridgeOption {
	return func(o *bridgeOptions) {
		o.onTerminated = fn
	}
}

// WithTransport sets the RTP Manager transport for media bridging.
// Required for the bridge to establish RTP relay between sessions.
func WithTransport(t transport.Transport) BridgeOption {
	return func(o *bridgeOptions) {
		o.transport = t
	}
}
