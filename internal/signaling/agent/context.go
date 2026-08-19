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
// knowledge base runs to hundreds of lines and the operative instruction is
// simply too far from the generation point to compete. Restating it last costs
// nothing.
//
// The internal case reads the way it does because a colleague's call only
// reaches the supervisor when deterministic resolution could NOT connect it —
// the extension is unregistered, the parking slot was empty, the target is
// unknown. Telling the model to "just dial it" there would be telling it to
// retry the thing that already failed.
func (c CallContext) FirstTurnDirective() string {
	switch c.Direction {
	case DirectionInternal:
		return "# Right now\n" +
			"This is an INTERNAL call: a colleague dialed " + c.Callee + " and the system could not " +
			"connect it — that is why you are on this call.\n" +
			"Do not greet them and do not introduce yourself; they are staff and they expected a phone " +
			"to ring. Say in one short sentence that it did not go through, then offer the next useful " +
			"thing: another person in the same area, or to take a message.\n"
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
