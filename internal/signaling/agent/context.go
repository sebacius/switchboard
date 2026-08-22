package agent

// Direction classifies a call as a trust gradient: internal (a registered
// directory user), inbound (from a trunk peer), or outbound (a directory user
// dialing an external destination).
type Direction string

const (
	DirectionInternal Direction = "internal"
	DirectionInbound  Direction = "inbound"
	DirectionOutbound Direction = "outbound"
)

// CallContext is the per-call identity every layer shares: who is calling whom,
// the call's direction, and the resolved tenant. Routing, resolution, admission,
// policy, and flow execution all read the same value, so none of them re-derives
// it and they cannot disagree about whose call this is.
type CallContext struct {
	Caller    string // SIP From user part
	Callee    string // dialed destination (To user part)
	Direction Direction
	Tenant    string
}
