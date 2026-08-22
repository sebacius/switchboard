package agent

import (
	"log/slog"
	"strings"

	"github.com/sebas/switchboard/internal/signaling/parking"
)

// The resolver is the deterministic answer to "does this call have exactly one
// correct destination?". It runs after the router has produced direction and
// tenant, and before the supervisor is started.
//
// Why it exists: the archived llm-pbx-supervisor change routed every INVITE
// through the model, and its own smoke testing measured a 57s first turn against
// a 30s deadline — and an internal extension call answered with a greeting
// instead of a dial. An 8B model was being asked to decide something with one
// correct answer, inside the INVITE transaction, while holding a business
// knowledge base as its prompt. Resolution removes that class of call from the
// model's job entirely.
//
// What keeps this from becoming the dialplan again: the resolvable set is CLOSED
// (four shapes, below), and the resolver only ever sees the DIALED TARGET, never
// an utterance. Anything expressed in speech is the supervisor's job by
// construction, because the resolver is not in that conversation.
//
// What keeps this from becoming a second trust path: a resolved destination is
// adjudicated by the same Policy that adjudicates a model-issued dial. The
// routing table is data, never authority.

// DestinationKind is the shape of a resolved destination.
type DestinationKind int

const (
	// DestinationHandOff means resolution declined the call: the supervisor
	// handles it. This is the zero value, so an unset Destination is a hand-off.
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
// the AI pick this call up?".
type Destination struct {
	Kind DestinationKind

	// Target is the concrete dial target for DestinationEndpoint.
	Target string

	// GroupName and Group describe a DestinationGroup, defaults already applied.
	GroupName string
	Group     RingGroup

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
// park/unpark tools use) so the resolver depends only on what it reads.
type ParkingLookup interface {
	Get(slotID string) (*parking.ParkSlot, bool)
}

// Resolver answers the deterministic routing question for one call. It holds no
// per-call state and is safe for concurrent use.
type Resolver struct {
	routing RoutingSource
	dir     Directory
	parking ParkingLookup
	log     *slog.Logger
}

// NewResolver builds a Resolver. A nil routing source or directory disables
// deterministic resolution entirely (everything hands off), which is the correct
// degradation: the supervisor still works, calls are just slower.
func NewResolver(routing RoutingSource, dir Directory, park ParkingLookup, log *slog.Logger) *Resolver {
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
	if prefix := table.retrievalPrefix(); strings.HasPrefix(callee, prefix) {
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
// empty slot deliberately does NOT resolve: the supervisor picking it up is what
// lets the caller be told the slot is empty instead of hearing silence.
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
func (r *Resolver) resolveDestination(table *RoutingTable, dest string) (Destination, bool) {
	dest = strings.TrimSpace(dest)

	if name, isGroup := IsGroupTarget(dest); isGroup {
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
	// the supervisor should take, so it can offer voicemail or another person
	// rather than forwarding into a dead end.
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
// something the resolver claims: external reach is the supervisor's and the
// policy's business, not a silent deterministic forward.
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
func lookupDestination(table *RoutingTable, dir Direction, callee string) (string, bool) {
	if dir == DirectionInbound {
		if dest, ok := table.DIDs[callee]; ok {
			return dest, true
		}
		wanted := didDigits(callee)
		if wanted == "" {
			return "", false
		}
		for did, dest := range table.DIDs {
			if didDigits(did) == wanted {
				return dest, true
			}
		}
		return "", false
	}

	dest, ok := table.Extensions[callee]
	return dest, ok
}

// didDigits reduces a DID to bare digits so "+15558001200" and "15558001200"
// compare equal. Carriers are inconsistent about the leading +, and a tenant
// writing its table one way while its carrier signals the other would send every
// inbound call to the model instead of to the person it was dialed for.
//
// This is deliberately NOT policy's normalizeDigits, which preserves a leading +
// because the barred-prefix patterns are written in E.164.
func didDigits(did string) string {
	var b strings.Builder
	for _, r := range did {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// LogDecision records what resolution decided for a call. It is deliberately one
// line per call at info level: "why did the AI answer this?" is the first
// question asked when a deterministic route does not happen, and it should be
// answerable from the log without turning on debug.
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
