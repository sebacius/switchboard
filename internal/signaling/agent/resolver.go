package agent

import (
	"log/slog"
	"strings"

	"github.com/sebas/switchboard/internal/signaling/dialplan"
	"github.com/sebas/switchboard/internal/signaling/parking"
)

// The resolver is the deterministic answer to "does this call have exactly one
// correct destination?". It runs after the router has produced direction and
// tenant, and is consulted by the flow engine.
//
// Why it exists is history worth keeping: the archived llm-pbx-supervisor change
// routed every INVITE through a model, and its own smoke testing measured a 57s
// first turn against a 30s deadline — and an internal extension call answered
// with a greeting instead of a dial. Deterministic resolution took that class of
// call away from the model, and outlived the model itself.
//
// What keeps this from becoming the dialplan again: the resolvable set is CLOSED
// (four shapes, below), and the resolver only ever sees the DIALED TARGET. It
// decides nothing about a call it cannot answer in one hop; it declines, and the
// flow engine decides.
//
// What keeps this from becoming a second trust path: a resolved destination is
// adjudicated by the same Policy that adjudicates every other dial. The routing
// table is data, never authority.

// DestinationKind is the shape of a resolved destination.
type DestinationKind int

const (
	// DestinationHandOff means resolution declined the call: whoever asked
	// decides what to do with it. This is the zero value, so an unset
	// Destination is a hand-off.
	DestinationHandOff DestinationKind = iota
	// DestinationEndpoint is a single concrete dial target ("user/110").
	DestinationEndpoint
	// DestinationGroup is a named ring group in the tenant's routing table.
	DestinationGroup
	// DestinationRetrieve is a *7XX pickup of an occupied parking slot.
	DestinationRetrieve
)

// String renders the kind for logs.
func (k DestinationKind) String() string {
	switch k {
	case DestinationEndpoint:
		return "endpoint"
	case DestinationGroup:
		return "group"
	case DestinationRetrieve:
		return "retrieve"
	default:
		return "handoff"
	}
}

// Destination is what resolution produced. A DestinationHandOff carries a Reason
// explaining why nothing resolved, which is the log line that answers "why did
// this call not take the direct path?".
type Destination struct {
	Kind DestinationKind

	// Target is the concrete dial target for DestinationEndpoint.
	Target string

	// GroupName and Group describe a DestinationGroup, defaults already applied.
	GroupName string
	Group     dialplan.RingGroup

	// Slot is the parking slot for DestinationRetrieve, digits only.
	Slot string

	// Reason explains the outcome, resolved or not. It is always set.
	Reason string
}

// handOff builds a declining result with its reason.
func handOff(reason string) Destination {
	return Destination{Kind: DestinationHandOff, Reason: reason}
}

// ParkingLookup is the read-only slice of parking the resolver needs: whether a
// slot currently holds a call. It is separate from ParkingService (which the
// park and unpark paths use) so the resolver depends only on what it reads.
type ParkingLookup interface {
	Get(slotID string) (*parking.ParkSlot, bool)
}

// Resolver answers the deterministic routing question for one call. It holds no
// per-call state and is safe for concurrent use.
type Resolver struct {
	routing dialplan.RoutingSource
	dir     Directory
	parking ParkingLookup
	log     *slog.Logger
}

// NewResolver builds a Resolver. A nil routing source or directory disables
// deterministic resolution entirely (everything hands off), which is the correct
// degradation: the flow engine still routes, it just gets no shortcut.
func NewResolver(routing dialplan.RoutingSource, dir Directory, park ParkingLookup, log *slog.Logger) *Resolver {
	if log == nil {
		log = slog.Default()
	}
	return &Resolver{
		routing: routing,
		dir:     dir,
		parking: park,
		log:     log.With("component", "call-resolver"),
	}
}

// Resolve decides whether this call has exactly one correct destination. It
// returns the destination and ok=true only for the four resolvable shapes; every
// other outcome is a hand-off with a reason.
//
// Order matters: the retrieval prefix is checked before the extension table so a
// tenant cannot accidentally shadow "*701" with an extension entry, and the DID
// table is only consulted for inbound calls so a directory user dialing a string
// that happens to look like a DID is not silently re-routed.
func (r *Resolver) Resolve(cc CallContext) (Destination, bool) {
	if r == nil || r.routing == nil {
		return handOff("no routing source configured"), false
	}

	table, ok := r.routing.TenantRouting(cc.Tenant)
	if !ok {
		return handOff("tenant has no routing table"), false
	}

	callee := strings.TrimSpace(cc.Callee)
	if callee == "" {
		return handOff("no dialed target"), false
	}

	// 1. Call retrieval (*7XX). Internal callers only: an outside caller who
	//    guessed a slot number must not be able to pick up a held call.
	if prefix := table.RetrievalPrefixOrDefault(); strings.HasPrefix(callee, prefix) {
		if cc.Direction != DirectionInternal {
			return handOff("call retrieval is internal-only"), false
		}
		return r.resolveRetrieval(callee, prefix)
	}

	// 2. Table lookup: DIDs for inbound, extensions for internal/outbound.
	dest, found := lookupDestination(table, cc.Direction, callee)
	if !found {
		return handOff("dialed target is not in the tenant routing table"), false
	}

	return r.resolveDestination(table, dest)
}

// resolveRetrieval turns a *7XX dial into a retrieval of an OCCUPIED slot. An
// empty slot deliberately does NOT resolve: declining sends the call on through
// the entry mapping and, failing that, to the tenant operator, which is a better
// answer than bridging a caller into an empty slot.
func (r *Resolver) resolveRetrieval(callee, prefix string) (Destination, bool) {
	slot := normalizeSlotID(strings.TrimPrefix(callee, prefix))
	if slot == "" {
		return handOff("retrieval code carries no slot number"), false
	}
	if r.parking == nil {
		return handOff("parking is not available"), false
	}
	if _, occupied := r.parking.Get(slot); !occupied {
		return handOff("parking slot " + slot + " is empty"), false
	}
	return Destination{
		Kind:   DestinationRetrieve,
		Slot:   slot,
		Reason: "retrieving parked call in slot " + slot,
	}, true
}

// resolveDestination interprets a routing-table value: a ring group or a
// concrete endpoint.
func (r *Resolver) resolveDestination(table *dialplan.RoutingTable, dest string) (Destination, bool) {
	dest = strings.TrimSpace(dest)

	if name, isGroup := dialplan.IsGroupTarget(dest); isGroup {
		group, ok := table.Group(name)
		if !ok {
			// The loader validates group references, so reaching here means the
			// table changed under us; decline rather than ring nothing.
			return handOff("ring group " + name + " is not defined"), false
		}
		return Destination{
			Kind:      DestinationGroup,
			GroupName: name,
			Group:     group,
			Reason:    "ring group " + name,
		}, true
	}

	// A concrete endpoint only resolves when it is actually registered. An
	// extension that exists on paper but has no phone online is exactly the case
	// to decline, so the caller's route is decided by the graph rather than by a
	// forward into a dead end.
	if !r.isRegistered(dest) {
		return handOff("destination " + dest + " is not registered"), false
	}
	return Destination{
		Kind:   DestinationEndpoint,
		Target: dest,
		Reason: "registered endpoint " + dest,
	}, true
}

// isRegistered reports whether a "user/NNN" target has a live registration.
// A non-directory target (an external number a tenant put in its table) is not
// something the resolver claims: external reach is the policy layer's business,
// not a silent deterministic forward.
func (r *Resolver) isRegistered(target string) bool {
	if r.dir == nil {
		return false
	}
	user, ok := strings.CutPrefix(target, "user/")
	if !ok || user == "" {
		return false
	}
	return r.dir.IsRegistered("", user)
}

// lookupDestination reads the right table for the call's direction, matching a
// DID both literally and by its digits so "+15558001200" and "15558001200" are
// the same number.
func lookupDestination(table *dialplan.RoutingTable, dir Direction, callee string) (string, bool) {
	if dir == DirectionInbound {
		// MatchDID handles the leading-'+' inconsistency itself.
		return table.MatchDID(callee)
	}

	return table.MatchExtension(callee)
}

// LogDecision records what resolution decided for a call. It is deliberately one
// line per call at info level: "why did this call not take the direct path?" is
// the first question asked when a deterministic route does not happen, and it
// should be answerable from the log without turning on debug.
func (r *Resolver) LogDecision(cc CallContext, dest Destination, resolved bool) {
	r.log.Info("call resolution",
		"tenant", cc.Tenant,
		"direction", cc.Direction,
		"caller", cc.Caller,
		"callee", cc.Callee,
		"resolved", resolved,
		"kind", dest.Kind.String(),
		"reason", dest.Reason,
	)
}
