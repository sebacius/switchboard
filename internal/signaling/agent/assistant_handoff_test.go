package agent

import (
	"context"
	"strings"
	"testing"
)

// Regression: a caller dialing the number that IS the assistant.
//
// Live symptom — devtenant extension 600 maps to `assistant`. Resolution handed
// the call off correctly, and then the supervisor emitted `dial(600)`: routing
// the caller to the number they had already reached. The policy denied it
// (600 is neither a "user/..." target nor a symbolic name), the model gave up
// and hung up, and the caller was dropped without ever being answered.
//
// The model was not being stupid. The tenant's routing table is deliberately
// NOT in the prompt, so "600 means you" was information it did not have and
// could not derive. The resolver knew; nobody told the runner.

func assistantRouting() StaticRouting {
	return StaticRouting{"devtenant": {
		Extensions: map[string]string{
			"600": "assistant", // the assistant's own number
			"105": "user/105",
		},
	}}
}

func assistantResolution(t *testing.T) *CallResolution {
	t.Helper()
	return NewCallResolution(CallResolutionConfig{
		Resolver: NewResolver(assistantRouting(), resolverDirectory{"105": true}, nil, quietLogger()),
		BuildPolicy: func(cc CallContext) *Policy {
			return NewPolicy(cc.Tenant, TenantPolicy{}, quietLogger())
		},
		Logger: quietLogger(),
	})
}

// Resolution must report WHY it handed off, so the runner can tell the model the
// caller asked for it rather than leaving it to guess from a callee number.
func TestAssistantHandOffIsReportedToTheRunner(t *testing.T) {
	res := assistantResolution(t)
	cc := CallContext{Caller: "230", Callee: "600", Direction: DirectionOutbound, Tenant: "devtenant"}

	if res.Handle(context.Background(), newFakeSession(), &cc) {
		t.Fatal("a call to the assistant must be handed to the supervisor, not dialed")
	}
	if !cc.ForAssistant {
		t.Fatal("the supervisor was not told the caller asked for it; it will try to dial the callee")
	}
}

// A hand-off for any OTHER reason must not claim the caller asked for the
// assistant — those two need opposite first moves.
func TestOrdinaryHandOffIsNotMarkedAsForTheAssistant(t *testing.T) {
	res := assistantResolution(t)
	cc := CallContext{Caller: "230", Callee: "999", Direction: DirectionOutbound, Tenant: "devtenant"}

	if res.Handle(context.Background(), newFakeSession(), &cc) {
		t.Fatal("an unmapped target must hand off")
	}
	if cc.ForAssistant {
		t.Fatal("an unresolved target is not the same as the caller asking for the assistant")
	}
}

// The directive must tell the model to talk, and explicitly not to dial the
// number the caller is already on — that dial is the exact bug.
func TestAssistantDirectiveForbidsDialingTheCallee(t *testing.T) {
	cc := CallContext{Caller: "230", Callee: "600", Direction: DirectionOutbound, Tenant: "devtenant", ForAssistant: true}
	d := cc.FirstTurnDirective()

	if !strings.Contains(d, "600") {
		t.Fatalf("the directive should name the dialed number, got %q", d)
	}
	if !strings.Contains(strings.ToLower(d), "that is you") {
		t.Fatalf("the directive must tell the model the number is itself, got %q", d)
	}
	if !strings.Contains(strings.ToLower(d), "do not try to dial") {
		t.Fatalf("the directive must forbid dialing the callee, got %q", d)
	}
}

// Being asked for by name outranks direction. Without this the outbound
// directive still says "your instructions above say what that number is" —
// and since the routing data left the prompt, they no longer do.
func TestAssistantDirectiveOverridesDirection(t *testing.T) {
	for _, dir := range []Direction{DirectionInbound, DirectionOutbound, DirectionInternal} {
		cc := CallContext{Caller: "230", Callee: "600", Direction: dir, Tenant: "devtenant", ForAssistant: true}
		if !strings.Contains(strings.ToLower(cc.FirstTurnDirective()), "that is you") {
			t.Fatalf("direction %s lost the assistant directive", dir)
		}
	}
}
