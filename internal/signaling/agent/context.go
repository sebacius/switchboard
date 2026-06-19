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
