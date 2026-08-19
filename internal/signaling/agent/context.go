package agent

import (
	"fmt"
	"strings"
)

// Direction classifies a call as a trust gradient: internal (a registered
// directory user), inbound (from a trunk peer), or outbound (a directory user
// dialing an external destination).
type Direction string

const (
	DirectionInternal Direction = "internal"
	DirectionInbound  Direction = "inbound"
	DirectionOutbound Direction = "outbound"
)

// CallContext is the per-call identity the supervisor needs: who is calling
// whom, the call's direction, and the resolved tenant. It is formatted into the
// system prompt so the model knows the situation without identity-getter tools.
type CallContext struct {
	Caller    string // SIP From user part
	Callee    string // dialed destination (To user part)
	Direction Direction
	Tenant    string
}

// FormatForPrompt renders the Call Context block prepended to the tenant prompt.
func (c CallContext) FormatForPrompt() string {
	var b strings.Builder
	b.WriteString("# Call Context\n")
	fmt.Fprintf(&b, "- Caller: %s\n", c.Caller)
	fmt.Fprintf(&b, "- Callee: %s\n", c.Callee)
	fmt.Fprintf(&b, "- Direction: %s\n", c.Direction)
	fmt.Fprintf(&b, "- Tenant: %s\n", c.Tenant)
	return b.String()
}

// FirstTurnDirective is the direction-specific instruction appended AFTER the
// tenant prompt, making it the last thing the model reads before it answers.
//
// Position is the whole point. The same rules live in settings.md, but a tenant
// knowledge base can run to hundreds of lines, and in live testing a 633-line
// tenant prompt was enough to make qwen3:8b greet an internal caller instead of
// routing them — the operative instruction was simply too far from the
// generation point to compete. Restating it last costs nothing and is what
// design decision #11 means by "output shaping, not bypass": the model still
// chooses the target, it is just reminded what shape the answer takes.
func (c CallContext) FirstTurnDirective() string {
	switch c.Direction {
	case DirectionInternal:
		return "# Right now\n" +
			"This is an INTERNAL call: a colleague dialed extension " + c.Callee + " and expects it to ring.\n" +
			"Respond with a single dial tool call for that extension and NO text whatsoever.\n" +
			"Do not greet. Do not say you are connecting them. Any text you produce is played " +
			"to the caller and takes away the ringing they expect.\n"
	case DirectionInbound:
		return "# Right now\n" +
			"This is an INBOUND call from outside: someone reached " + c.Callee + " from the " +
			"public network. Unless your instructions above say otherwise, greet them briefly, " +
			"find out what they need, then route them or answer their question.\n"
	case DirectionOutbound:
		return "# Right now\n" +
			"A colleague is calling " + c.Callee + ", which is not one of the phones registered " +
			"right now. Your instructions above say what that number is. It may be a service you " +
			"provide yourself — if so, handle the call and talk to them. It may be a destination " +
			"to route to — if so, dial it. If it is neither, tell them briefly that you cannot " +
			"place that call.\n"
	default:
		return ""
	}
}
