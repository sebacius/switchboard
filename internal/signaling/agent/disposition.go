package agent

// Disposition is the loop-control verdict a tool execution hands back to the
// runner. It is the seam that lets a tool drive the runner's lifecycle without
// the runner knowing the tool's internals: a tool says "keep going", "we're
// done", or "hold the call open" and the loop reacts.
type Disposition int

const (
	// DispositionContinue keeps the event loop draining events. The tool result
	// is fed back into the conversation; on an autonomous turn the runner
	// re-prompts the model (subject to the runaway breaker).
	DispositionContinue Disposition = iota

	// DispositionTerminal ends the call: the runner funnels into teardown(reason)
	// exactly once and HandleCall returns.
	DispositionTerminal

	// DispositionParked holds the call alive without driving further turns. The
	// loop keeps the call open (e.g. music-on-hold / a parking slot) until an
	// unpark event arrives or the call context is canceled.
	DispositionParked
)

// String renders a Disposition for logs.
func (d Disposition) String() string {
	switch d {
	case DispositionContinue:
		return "continue"
	case DispositionTerminal:
		return "terminal"
	case DispositionParked:
		return "parked"
	default:
		return "unknown"
	}
}
