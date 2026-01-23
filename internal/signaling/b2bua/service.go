package b2bua

import (
	"context"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/sebas/switchboard/internal/signaling/dialog"
	"github.com/sebas/switchboard/internal/signaling/mediaclient"
)

// CallService orchestrates B2BUA operations: lookup, origination, and bridging.
//
// It is the main entry point for:
//   - Resolving dial targets to SIP URIs
//   - Creating outbound call legs
//   - Adopting inbound legs from existing dialogs
//   - Bridging two answered legs
//
// Thread Safety: All methods are safe for concurrent use.
// Statelessness: CallService holds configuration but no per-call state.
// Each call creates its own Legs and Bridges which manage their lifecycles.
type CallService interface {
	// --- Target Resolution ---

	// Lookup resolves a dial target to one or more SIP URIs.
	// Supports multiple target formats:
	//   - "user/1001" or "1001": query location service
	//   - "gateway/carrier": resolve gateway config
	//   - "sip:user@host:port": passthrough
	// Returns ErrTargetNotFound if target cannot be resolved.
	Lookup(ctx context.Context, target string) (*LookupResult, error)

	// --- Leg Creation ---

	// AdoptInboundLeg wraps an existing dialog as an inbound (A) leg.
	// The dialog should already be in a ringing or answered state.
	// Takes ownership of the dialog lifecycle.
	AdoptInboundLeg(dlg *dialog.Dialog, sessionID string, opts ...LegOption) (Leg, error)

	// CreateOutboundLeg creates an outbound (B) leg to a resolved target.
	// Sends INVITE to the primary contact and returns immediately.
	// The leg will be in Created or Ringing state.
	// Caller must wait for Answered state before bridging.
	CreateOutboundLeg(ctx context.Context, target *LookupResult, opts ...LegOption) (Leg, error)

	// --- Bridging ---

	// CreateBridge creates a bridge between two legs.
	// Both legs should be in Answered state.
	// Returns the bridge in Created state - call Start() to activate.
	CreateBridge(legA, legB Leg, opts ...BridgeOption) (Bridge, error)

	// --- High-Level Operations ---

	// Dial combines Lookup + CreateOutboundLeg + wait for answer.
	// Blocks until the call is answered, fails, or times out.
	// Returns the leg in Answered state (or error if failed).
	Dial(ctx context.Context, target string, timeout time.Duration, opts ...LegOption) (Leg, error)

	// DialAndBridge is a convenience method for common B2BUA flow.
	// Given an answered A-leg, dials the target and bridges on answer.
	// Blocks until the bridge terminates.
	// Returns bridge info with timing and statistics.
	// Accepts LegOption to pass CallerID, CallerName, etc. to the outbound leg.
	DialAndBridge(ctx context.Context, legA Leg, target string, timeout time.Duration, opts ...LegOption) (*BridgeInfo, error)

	// --- Ring Group Support (Future) ---

	// DialParallel originates to multiple targets simultaneously.
	// First answer wins, remaining legs are canceled.
	// Returns the winning leg in Answered state.
	// Not yet implemented - returns ErrNotImplemented.
	DialParallel(ctx context.Context, targets []*LookupResult, timeout time.Duration, opts ...LegOption) (Leg, error)

	// --- B-leg BYE Handling ---

	// HandleIncomingBYE handles an incoming BYE request for outbound (B-leg) calls.
	// Returns true if the BYE was for a known B-leg and was handled.
	// Returns false if the Call-ID does not match any tracked B-leg.
	// This should be called before the dialog manager's HandleIncomingBYE.
	HandleIncomingBYE(req *sip.Request, tx sip.ServerTransaction) bool

	// --- Drain Support ---

	// GetBridgeMapper returns the BridgeMapper interface for drain migration.
	// This allows the drain coordinator to find B-leg dialogs for bridged calls.
	GetBridgeMapper() BridgeMapper
}

// CallServiceConfig contains dependencies for CallService.
type CallServiceConfig struct {
	// Client is the sipgo client for sending SIP requests.
	// Required.
	Client *sipgo.Client

	// Resolver resolves dial targets to SIP URIs.
	// Required.
	Resolver Resolver

	// DialogManager manages SIP dialogs.
	// Required.
	DialogManager dialog.DialogStore

	// Transport handles RTP Manager communication.
	// Required.
	Transport mediaclient.Transport

	// LocalContact is our SIP contact URI for outbound INVITEs.
	// Required.
	LocalContact string

	// AdvertiseAddr is the IP address to advertise in SIP.
	// Required.
	AdvertiseAddr string

	// Port is the SIP listening port.
	// Required.
	Port int

	// Logger for structured logging (optional).
	Logger Logger

	// DefaultDialTimeout is used when Dial() timeout is 0.
	// Default: 30 seconds.
	DefaultDialTimeout time.Duration

	// EarlyMedia enables 183 Session Progress for early media.
	// Default: true.
	EarlyMedia bool
}

// Logger is a minimal logging interface.
type Logger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

