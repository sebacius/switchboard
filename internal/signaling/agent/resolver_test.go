package agent

import (
	"testing"

	"github.com/sebas/switchboard/internal/signaling/dialplan"
	"github.com/sebas/switchboard/internal/signaling/parking"
)

// These cover the CLOSED set of resolvable shapes from the call-resolution spec.
// The negative cases matter at least as much as the positive ones: every call
// the resolver declines is a call the supervisor gets to explain to a human, and
// every call it wrongly claims is a caller forwarded into silence.

// resolverDirectory answers registration lookups from a set of user parts.
type resolverDirectory map[string]bool

func (d resolverDirectory) IsRegistered(_, user string) bool { return d[user] }

// resolverParking reports which slots hold a call.
type resolverParking map[string]bool

func (p resolverParking) Get(slotID string) (*parking.ParkSlot, bool) {
	if !p[slotID] {
		return nil, false
	}
	return &parking.ParkSlot{ID: slotID}, true
}

// testTable is the routing table these tests resolve against.
func testTable() *dialplan.RoutingTable {
	return &dialplan.RoutingTable{
		Operator:        "user/150",
		RetrievalPrefix: "*",
		Extensions: map[string]string{
			"105": "user/105",
			"106": "user/106", // present in the table, never registered
			"100": "assistant",
			"130": "group/claims",
		},
		SymbolicTargets: map[string]string{"claims": "group/claims"},
		DIDs: map[string]string{
			"+15558001200": "assistant",
			"+15558001250": "group/claims",
			"+15558001210": "user/105",
		},
		Groups: map[string]dialplan.RingGroup{
			"claims": {Strategy: dialplan.StrategySequential, Members: []string{"user/105"}},
		},
	}
}

func testResolver(park resolverParking) *Resolver {
	return NewResolver(
		dialplan.StaticRouting{"acme": testTable()},
		resolverDirectory{"105": true, "150": true},
		park,
		quietLogger(),
	)
}

func internalCall(callee string) CallContext {
	return CallContext{Caller: "102", Callee: callee, Direction: DirectionInternal, Tenant: "acme"}
}

func inboundCall(callee string) CallContext {
	return CallContext{Caller: "+15551234567", Callee: callee, Direction: DirectionInbound, Tenant: "acme"}
}

// The headline case: a colleague dials a registered extension and the model is
// never consulted.
func TestResolveRegisteredExtension(t *testing.T) {
	dest, ok := testResolver(nil).Resolve(internalCall("105"))
	if !ok {
		t.Fatalf("a registered extension must resolve, got %q", dest.Reason)
	}
	if dest.Kind != DestinationEndpoint || dest.Target != "user/105" {
		t.Fatalf("expected endpoint user/105, got %+v", dest)
	}
}

// An extension that exists on paper but has no phone online must NOT resolve.
// Forwarding it would send the caller into a dead end; the supervisor can offer
// voicemail or another person instead.
func TestResolveUnregisteredExtensionHandsOff(t *testing.T) {
	dest, ok := testResolver(nil).Resolve(internalCall("106"))
	if ok {
		t.Fatalf("an unregistered extension must not resolve, got %+v", dest)
	}
	if dest.Kind != DestinationHandOff {
		t.Fatalf("expected a hand-off, got %+v", dest)
	}
}

// A target the table does not mention at all is the supervisor's problem.
func TestResolveUnknownTargetHandsOff(t *testing.T) {
	if _, ok := testResolver(nil).Resolve(internalCall("999")); ok {
		t.Fatal("a target absent from the routing table must not resolve")
	}
}

// A tenant with no routing table resolves nothing — the correct degradation, and
// the reason a fresh deployment still works before anyone writes one.
func TestResolveTenantWithoutTableHandsOff(t *testing.T) {
	r := NewResolver(dialplan.StaticRouting{}, resolverDirectory{"105": true}, nil, quietLogger())
	if _, ok := r.Resolve(internalCall("105")); ok {
		t.Fatal("a tenant with no routing table must resolve nothing")
	}
}

// An extension mapped to the assistant is a resolved answer whose answer happens
// to be "the supervisor takes this".
func TestResolveAssistantMappingHandsOff(t *testing.T) {
	dest, ok := testResolver(nil).Resolve(internalCall("100"))
	if ok {
		t.Fatal("an assistant mapping must hand off, not dial")
	}
	if dest.Reason == "" {
		t.Fatal("the hand-off must record why, or nobody can explain why the AI answered")
	}
}

func TestResolveExtensionToRingGroup(t *testing.T) {
	dest, ok := testResolver(nil).Resolve(internalCall("130"))
	if !ok {
		t.Fatalf("an extension mapped to a group must resolve, got %q", dest.Reason)
	}
	if dest.Kind != DestinationGroup || dest.GroupName != "claims" {
		t.Fatalf("expected the claims group, got %+v", dest)
	}
	// Defaults are applied at resolution, not left for the ring path to guess.
	if dest.Group.MemberTimeoutMs != dialplan.DefaultMemberTimeoutMs {
		t.Fatalf("group defaults were not applied: %+v", dest.Group)
	}
}

// --- Call retrieval (*7XX) ---

func TestResolveRetrievalOfOccupiedSlot(t *testing.T) {
	dest, ok := testResolver(resolverParking{"701": true}).Resolve(internalCall("*701"))
	if !ok {
		t.Fatalf("an occupied slot must resolve, got %q", dest.Reason)
	}
	if dest.Kind != DestinationRetrieve || dest.Slot != "701" {
		t.Fatalf("expected retrieval of slot 701, got %+v", dest)
	}
}

// An empty slot deliberately does not resolve: the supervisor picking it up is
// what lets the caller be TOLD the slot is empty instead of hearing silence.
func TestResolveRetrievalOfEmptySlotHandsOff(t *testing.T) {
	dest, ok := testResolver(resolverParking{}).Resolve(internalCall("*701"))
	if ok {
		t.Fatalf("an empty slot must not resolve, got %+v", dest)
	}
}

// Retrieval is internal-only. An outside caller who guessed a slot number must
// not be able to pick up someone else's held call.
func TestResolveRetrievalFromInboundIsRefused(t *testing.T) {
	dest, ok := testResolver(resolverParking{"701": true}).Resolve(inboundCall("*701"))
	if ok {
		t.Fatalf("an inbound caller must never retrieve a parked call, got %+v", dest)
	}
}

// --- Inbound DIDs ---

func TestResolveInboundDIDToGroup(t *testing.T) {
	dest, ok := testResolver(nil).Resolve(inboundCall("+15558001250"))
	if !ok {
		t.Fatalf("a mapped DID must resolve, got %q", dest.Reason)
	}
	if dest.Kind != DestinationGroup || dest.GroupName != "claims" {
		t.Fatalf("expected the claims group, got %+v", dest)
	}
}

// A DID mapped to the assistant is the AI receptionist's own number.
func TestResolveInboundDIDToAssistantHandsOff(t *testing.T) {
	if _, ok := testResolver(nil).Resolve(inboundCall("+15558001200")); ok {
		t.Fatal("a DID that routes to the assistant must hand off to the supervisor")
	}
}

// Carriers are inconsistent about the leading +, so a DID matches on its digits
// as well as literally. Getting this wrong sends every inbound call to the model.
func TestResolveInboundDIDMatchesOnDigits(t *testing.T) {
	dest, ok := testResolver(nil).Resolve(inboundCall("15558001210"))
	if !ok {
		t.Fatalf("a DID without the leading + must still match, got %q", dest.Reason)
	}
	if dest.Target != "user/105" {
		t.Fatalf("expected user/105, got %+v", dest)
	}
}

// The extension table is not consulted for inbound calls and the DID table is
// not consulted for internal ones — otherwise a colleague dialing a string that
// happens to look like a DID gets silently re-routed.
func TestResolveTablesAreDirectionScoped(t *testing.T) {
	r := testResolver(nil)
	if _, ok := r.Resolve(inboundCall("105")); ok {
		t.Fatal("an inbound call must not resolve through the extension table")
	}
	if _, ok := r.Resolve(internalCall("+15558001210")); ok {
		t.Fatal("an internal call must not resolve through the DID table")
	}
}
