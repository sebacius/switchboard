package agent

import (
	"context"
	"testing"
	"time"
)

// These drive the REAL CallResolution over the REAL Policy, so what they prove
// is that resolution stays inside the authorization boundary and that a ring
// group's no-answer outcome actually runs.

// groupTable is a routing table with both ring strategies and an operator.
func groupTable() *RoutingTable {
	return &RoutingTable{
		Operator: "user/150",
		Extensions: map[string]string{
			"200": "group/seq",
			"201": "group/rr",
			"202": "group/hangup",
			"203": "group/operator",
			"300": "user/105",
			"400": "+18005551212", // an external destination in the table
		},
		Groups: map[string]RingGroup{
			"seq": {Strategy: StrategySequential, Members: []string{"user/105", "user/106", "user/107"}},
			"rr":  {Strategy: StrategyRoundRobin, Members: []string{"user/105", "user/106", "user/107"}},
			"hangup": {
				Strategy: StrategySequential, Members: []string{"user/105"},
				NoAnswer: NoAnswerHangup,
			},
			"operator": {
				Strategy: StrategySequential, Members: []string{"user/105"},
				NoAnswer: NoAnswerOperator,
			},
		},
	}
}

// newResolution builds the real resolution layer over the given tenant policy.
// Every extension in groupTable is treated as registered so the tests exercise
// authorization and ring behaviour rather than registration.
func newResolution(t *testing.T, tenantPolicy TenantPolicy) *CallResolution {
	t.Helper()
	routing := StaticRouting{"acme": groupTable()}
	return NewCallResolution(CallResolutionConfig{
		Resolver: NewResolver(routing,
			resolverDirectory{"105": true, "106": true, "107": true, "150": true},
			nil, quietLogger()),
		BuildPolicy: func(cc CallContext) *Policy {
			return NewPolicy(cc.Tenant, tenantPolicy, quietLogger())
		},
		Logger: quietLogger(),
	})
}

// An external number in the tenant's own routing table is NOT dialed
// deterministically, even though the table names it. Anyone who can edit a
// routing file would otherwise have a trunk.
//
// Two independent things stop it, and this pins the first: the resolver only
// claims a "user/..." endpoint it can see registered, so an external target is
// never a resolved destination at all — it hands off, and external reach becomes
// the supervisor's request for the policy to adjudicate. The second is the
// policy itself, which is exercised on the ring-group member path where a member
// genuinely can be external (see TestRingGroupMembersAreAuthorizedIndividually).
// Verified live: an internal call to a table entry mapping to +1800… logged
// "resolved=false ... destination +18005551212 is not registered" and no INVITE
// left the box.
func TestResolvedExternalDestinationIsNeverDialedDeterministically(t *testing.T) {
	res := newResolution(t, TenantPolicy{}) // default-deny external
	sess := newFakeSession()

	handled := res.Handle(context.Background(), sess, internalCall("400"))

	if handled {
		t.Fatal("an external table entry must hand off to the supervisor, not be dialed")
	}
	if got := sess.forwards(); len(got) != 0 {
		t.Fatalf("nothing may leave the box for an external table entry, forwarded %v", got)
	}
}

// The permitted case, so the deny above is not passing for the wrong reason.
func TestResolvedInternalDestinationIsForwarded(t *testing.T) {
	res := newResolution(t, TenantPolicy{})
	sess := newFakeSession()

	if !res.Handle(context.Background(), sess, internalCall("300")) {
		t.Fatal("a registered internal destination must be handled deterministically")
	}
	if got := sess.forwards(); len(got) != 1 || got[0] != "user/105" {
		t.Fatalf("expected a forward to user/105, got %v", got)
	}
	if sess.HasAnswered() {
		t.Fatal("a deterministic forward must not answer: the caller hears real ringback")
	}
}

// --- Ring strategies ---

// Sequential rings members in configured order, one per round.
func TestSequentialGroupRingsInConfiguredOrder(t *testing.T) {
	res := newResolution(t, TenantPolicy{})
	sess := newFakeSession()

	if !res.Handle(context.Background(), sess, internalCall("200")) {
		t.Fatal("a ring group must be handled deterministically")
	}

	rounds := sess.rungRounds()
	want := []string{"user/105", "user/106", "user/107"}
	if len(rounds) != len(want) {
		t.Fatalf("expected %d rounds, got %v", len(want), rounds)
	}
	for i, member := range want {
		if len(rounds[i]) != 1 || rounds[i][0] != member {
			t.Fatalf("round %d = %v, want [%s]", i+1, rounds[i], member)
		}
	}
}

// Round-robin starts at a different member on each successive call. Without the
// advancing cursor the first member takes every call and the strategy is a lie.
func TestRoundRobinAdvancesAcrossCalls(t *testing.T) {
	res := newResolution(t, TenantPolicy{})

	var firsts []string
	for range 4 {
		sess := newFakeSession()
		if !res.Handle(context.Background(), sess, internalCall("201")) {
			t.Fatal("a ring group must be handled deterministically")
		}
		rounds := sess.rungRounds()
		if len(rounds) == 0 || len(rounds[0]) == 0 {
			t.Fatalf("no member was rung: %v", rounds)
		}
		firsts = append(firsts, rounds[0][0])
	}

	want := []string{"user/105", "user/106", "user/107", "user/105"}
	for i := range want {
		if firsts[i] != want[i] {
			t.Fatalf("call %d started at %s, want %s (cursor did not advance: %v)",
				i+1, firsts[i], want[i], firsts)
		}
	}
}

// Every member is authorized individually. A group cannot smuggle reach past the
// tenant's Class of Service by listing an external number among internal ones.
func TestRingGroupMembersAreAuthorizedIndividually(t *testing.T) {
	routing := StaticRouting{"acme": {
		Extensions: map[string]string{"210": "group/mixed"},
		Groups: map[string]RingGroup{
			"mixed": {Strategy: StrategySequential, Members: []string{"user/105", "+18005551212"}},
		},
	}}
	res := NewCallResolution(CallResolutionConfig{
		Resolver: NewResolver(routing, resolverDirectory{"105": true}, nil, quietLogger()),
		BuildPolicy: func(cc CallContext) *Policy {
			return NewPolicy(cc.Tenant, TenantPolicy{}, quietLogger())
		},
		Logger: quietLogger(),
	})
	sess := newFakeSession()

	if !res.Handle(context.Background(), sess, internalCall("210")) {
		t.Fatal("the group still has one authorized member, so it must be handled")
	}
	for _, round := range sess.rungRounds() {
		for _, member := range round {
			if member == "+18005551212" {
				t.Fatal("a member the policy denied was rung anyway")
			}
		}
	}
}

// --- No-answer outcomes ---

// The default: nobody answered, the call is still pre-answer and intact, so the
// supervisor picks it up and can offer voicemail or another person.
func TestGroupNoAnswerHandsToSupervisor(t *testing.T) {
	res := newResolution(t, TenantPolicy{})
	sess := newFakeSession()
	sess.groupErr = ErrGroupNoAnswer

	if res.Handle(context.Background(), sess, internalCall("200")) {
		t.Fatal("an unanswered group with no_answer=supervisor must hand off")
	}
	if sess.HasAnswered() {
		t.Fatal("the call must be left pre-answer so the supervisor still has both options")
	}
}

func TestGroupNoAnswerForwardsToOperator(t *testing.T) {
	res := newResolution(t, TenantPolicy{})
	sess := newFakeSession()
	sess.groupErr = ErrGroupNoAnswer

	if !res.Handle(context.Background(), sess, internalCall("203")) {
		t.Fatal("no_answer=operator is handled by resolution, not the supervisor")
	}
	if got := sess.forwards(); len(got) != 1 || got[0] != "user/150" {
		t.Fatalf("expected a forward to the operator, got %v", got)
	}
}

func TestGroupNoAnswerHangsUp(t *testing.T) {
	res := newResolution(t, TenantPolicy{})
	sess := newFakeSession()
	sess.groupErr = ErrGroupNoAnswer

	if !res.Handle(context.Background(), sess, internalCall("202")) {
		t.Fatal("no_answer=hangup ends the call deterministically")
	}
	if sess.hangupCalls.Load() != 1 {
		t.Fatalf("expected exactly one hangup, got %d", sess.hangupCalls.Load())
	}
}

// A tenant that asks for the operator but has none must not drop the caller:
// the floor is "hand it to the supervisor", never "hang up".
func TestGroupNoAnswerWithoutOperatorHandsOff(t *testing.T) {
	routing := StaticRouting{"acme": {
		Extensions: map[string]string{"220": "group/nooperator"},
		Groups: map[string]RingGroup{
			"nooperator": {Strategy: StrategySequential, Members: []string{"user/105"}, NoAnswer: NoAnswerOperator},
		},
	}}
	res := NewCallResolution(CallResolutionConfig{
		Resolver: NewResolver(routing, resolverDirectory{"105": true}, nil, quietLogger()),
		BuildPolicy: func(cc CallContext) *Policy {
			return NewPolicy(cc.Tenant, TenantPolicy{}, quietLogger())
		},
		Logger: quietLogger(),
	})
	sess := newFakeSession()
	sess.groupErr = ErrGroupNoAnswer

	if res.Handle(context.Background(), sess, internalCall("220")) {
		t.Fatal("with no operator configured the call must reach the supervisor")
	}
	if sess.hangupCalls.Load() != 0 {
		t.Fatal("the caller must not be hung up on")
	}
}

// --- Degradation ---

// Resolution makes no LLM request at all, so an outage is invisible to it. This
// is the whole point of the change's failure story: the PBX keeps routing.
func TestResolutionNeedsNoLLM(t *testing.T) {
	res := newResolution(t, TenantPolicy{})
	sess := newFakeSession()

	// There is no chat client anywhere in this test — if resolution needed one,
	// this could not compile, let alone pass.
	if !res.Handle(context.Background(), sess, internalCall("300")) {
		t.Fatal("a resolvable call must complete with no model involved")
	}
	if got := sess.forwards(); len(got) != 1 {
		t.Fatalf("expected the call forwarded, got %v", got)
	}
}

// Resolution refuses to act without a policy rather than forwarding unadjudicated.
func TestResolutionWithoutPolicyHandsOff(t *testing.T) {
	routing := StaticRouting{"acme": groupTable()}
	res := NewCallResolution(CallResolutionConfig{
		Resolver: NewResolver(routing, resolverDirectory{"105": true}, nil, quietLogger()),
		Logger:   quietLogger(),
	})
	sess := newFakeSession()

	if res.Handle(context.Background(), sess, internalCall("300")) {
		t.Fatal("an unadjudicated forward must never happen")
	}
	if len(sess.forwards()) != 0 {
		t.Fatal("nothing may be dialed without a policy")
	}
}

// Sanity: a ring timeout of zero would mean "ring forever" downstream, so the
// group's default must have been applied by the time it reaches the session.
func TestGroupRingTimeoutIsAlwaysPositive(t *testing.T) {
	table := groupTable()
	g, ok := table.Group("seq")
	if !ok {
		t.Fatal("group seq missing")
	}
	if time.Duration(g.MemberTimeoutMs)*time.Millisecond <= 0 {
		t.Fatalf("member timeout must be positive, got %dms", g.MemberTimeoutMs)
	}
}
