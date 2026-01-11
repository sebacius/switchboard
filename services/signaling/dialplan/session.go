package dialplan

import (
	"context"
	"time"
)

// CallSession provides actions access to call state and operations.
// This is the bridge between dialplan actions and the existing Dialog/Transport layers.
type CallSession interface {
	// Identity
	CallID() string
	Destination() string // Dialed number (To URI user part)
	CallerID() string    // Caller number (From URI user part)

	// Context returns the call's context. Canceled on BYE or timeout.
	Context() context.Context

	// Media operations
	PlayAudio(ctx context.Context, file string) error
	StopAudio() error

	// B2BUA operations (for dial action)
	// Dial initiates an outbound call to the target.
	// target can be "user/extension" or "sip:user@host:port"
	// Returns error if dial fails (timeout, rejected, user not found)
	Dial(ctx context.Context, target string, timeout time.Duration) error

	// Termination
	Hangup(reason string) error

	// State queries
	IsTerminated() bool
}

// DialResult contains the outcome of a dial action.
type DialResult struct {
	Success     bool
	BridgeID    string // If bridged
	SIPCode     int    // Response code if failed
	SIPReason   string
	Duration    time.Duration // Time until answer or failure
	TalkTime    time.Duration // Time after bridge until hangup
	EndedByLegA bool          // Which side hung up
}
