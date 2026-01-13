package b2bua

import (
	"context"
	"time"

	"github.com/sebas/switchboard/internal/signaling/dialog"
)

// Leg represents one side of a call in a B2BUA scenario.
//
// A Leg encapsulates:
//   - SIP dialog state (via Dialog)
//   - Media session (via SessionID)
//   - Lifecycle management (state transitions, hangup)
//
// Legs are created in two ways:
//   - Inbound (A-leg): Adopted from an existing dialog via CallService.AdoptInboundLeg
//   - Outbound (B-leg): Created via CallService.CreateOutboundLeg or Dial
//
// Thread Safety: All methods are safe for concurrent use.
type Leg interface {
	// ID returns the unique identifier for this leg.
	// Format: "leg-{uuid}" for generated IDs or custom ID if provided.
	ID() string

	// CallID returns the SIP Call-ID for this leg.
	// Each leg has its own Call-ID (B2BUA, not proxy).
	CallID() string

	// Direction returns whether this is an inbound or outbound leg.
	Direction() LegDirection

	// GetState returns the current state of the leg.
	GetState() LegState

	// GetTerminationCause returns why the leg was terminated.
	// Returns TerminationCauseNone if not yet terminated.
	GetTerminationCause() TerminationCause

	// WaitForState blocks until the leg reaches the target state or context is canceled.
	// Returns immediately if already in or past the target state.
	// Returns error if the leg reaches a terminal state before the target.
	WaitForState(ctx context.Context, target LegState) error

	// Dialog returns the underlying SIP dialog.
	// Use with caution - prefer Leg methods for state changes.
	Dialog() *dialog.Dialog

	// SessionID returns the RTP Manager session ID for this leg.
	// Empty string if no media session is established.
	SessionID() string

	// Context returns the leg's context.
	// Canceled when the leg is destroyed.
	Context() context.Context

	// Info returns detailed information about this leg.
	Info() *LegInfo

	// --- Lifecycle Operations ---

	// Answer sends 200 OK for an inbound leg (no-op for outbound).
	// Transitions state from Ringing to Answered.
	// Returns error if leg is not in Ringing state.
	Answer(ctx context.Context) error

	// Hangup terminates the leg with BYE (if answered) or CANCEL (if ringing).
	// Transitions state to Destroyed after cleanup.
	// Safe to call multiple times.
	Hangup(ctx context.Context, cause TerminationCause) error

	// Destroy releases all resources without SIP signaling.
	// Use for cleanup after receiving BYE/CANCEL.
	// Safe to call multiple times.
	Destroy()

	// --- Event Callbacks ---

	// OnStateChange registers a callback for state transitions.
	// Callback is invoked synchronously - keep handlers fast.
	// Returns a function to unregister the callback.
	OnStateChange(fn func(old, new LegState)) func()

	// OnTerminated registers a callback for termination.
	// Called once when leg reaches Destroyed state.
	OnTerminated(fn func(cause TerminationCause))
}

// LegInfo contains detailed information about a leg.
type LegInfo struct {
	// Identity
	ID        string       `json:"id"`
	CallID    string       `json:"call_id"`
	Direction LegDirection `json:"direction"`

	// SIP addressing
	LocalURI  string `json:"local_uri"`  // Our contact URI
	RemoteURI string `json:"remote_uri"` // Peer's contact URI
	FromURI   string `json:"from_uri"`   // From header
	ToURI     string `json:"to_uri"`     // To header

	// Media
	SessionID       string `json:"session_id,omitempty"`
	LocalRTPAddr    string `json:"local_rtp_addr,omitempty"`
	LocalRTPPort    int    `json:"local_rtp_port,omitempty"`
	RemoteRTPAddr   string `json:"remote_rtp_addr,omitempty"`
	RemoteRTPPort   int    `json:"remote_rtp_port,omitempty"`
	NegotiatedCodec string `json:"negotiated_codec,omitempty"`

	// State
	State            LegState         `json:"state"`
	TerminationCause TerminationCause `json:"termination_cause,omitempty"`

	// Timing
	CreatedAt    time.Time `json:"created_at"`
	RingingAt    time.Time `json:"ringing_at,omitempty"`
	AnsweredAt   time.Time `json:"answered_at,omitempty"`
	TerminatedAt time.Time `json:"terminated_at,omitempty"`

	// SIP response (for failed outbound legs)
	SIPCode   int    `json:"sip_code,omitempty"`
	SIPReason string `json:"sip_reason,omitempty"`
}

// Duration returns the total duration from creation to termination.
// Returns 0 if not yet terminated.
func (i *LegInfo) Duration() time.Duration {
	if i.TerminatedAt.IsZero() {
		return 0
	}
	return i.TerminatedAt.Sub(i.CreatedAt)
}

// RingDuration returns how long the leg was in Ringing state.
// Returns 0 if never rang or still ringing.
func (i *LegInfo) RingDuration() time.Duration {
	if i.RingingAt.IsZero() {
		return 0
	}
	end := i.AnsweredAt
	if end.IsZero() {
		end = i.TerminatedAt
	}
	if end.IsZero() {
		return 0
	}
	return end.Sub(i.RingingAt)
}

// TalkDuration returns how long the leg was in Answered state.
// Returns 0 if never answered or still talking.
func (i *LegInfo) TalkDuration() time.Duration {
	if i.AnsweredAt.IsZero() {
		return 0
	}
	end := i.TerminatedAt
	if end.IsZero() {
		return 0
	}
	return end.Sub(i.AnsweredAt)
}

// LegOption configures leg creation.
type LegOption func(*legOptions)

type legOptions struct {
	id         string
	earlyMedia bool
	sdpOffer   []byte
	onRinging  func(Leg)
	onAnswered func(Leg)
	callerID   string
	callerName string
	onTeardown func(Leg) // Called when leg is being torn down (before state change)
}

// WithLegID sets a custom leg ID instead of generating one.
func WithLegID(id string) LegOption {
	return func(o *legOptions) {
		o.id = id
	}
}

// WithEarlyMedia enables early media (183 Session Progress).
// When enabled, media session is created before 200 OK.
func WithEarlyMedia(enable bool) LegOption {
	return func(o *legOptions) {
		o.earlyMedia = enable
	}
}

// WithSDPOffer provides the SDP offer for outbound legs.
// If not provided, a new SDP will be generated.
func WithSDPOffer(sdp []byte) LegOption {
	return func(o *legOptions) {
		o.sdpOffer = sdp
	}
}

// WithOnRinging sets a callback when leg enters Ringing state.
func WithOnRinging(fn func(Leg)) LegOption {
	return func(o *legOptions) {
		o.onRinging = fn
	}
}

// WithOnAnswered sets a callback when leg enters Answered state.
func WithOnAnswered(fn func(Leg)) LegOption {
	return func(o *legOptions) {
		o.onAnswered = fn
	}
}

// WithCallerID sets the caller ID (From URI user part) for outbound legs.
// This is typically the caller's phone number or extension.
func WithCallerID(callerID string) LegOption {
	return func(o *legOptions) {
		o.callerID = callerID
	}
}

// WithCallerName sets the caller display name (From header display name) for outbound legs.
func WithCallerName(callerName string) LegOption {
	return func(o *legOptions) {
		o.callerName = callerName
	}
}

// WithTeardownHandler sets a callback invoked when the leg is being torn down.
// This is called BEFORE the state changes to Destroyed, allowing the handler
// to send SIP signaling (BYE) before the leg is marked as terminated.
// For A-leg: handler should call dialogMgr.Terminate() to send BYE to caller.
// For B-leg: handler should call originator.SendBYE() to send BYE to callee.
func WithTeardownHandler(fn func(Leg)) LegOption {
	return func(o *legOptions) {
		o.onTeardown = fn
	}
}
