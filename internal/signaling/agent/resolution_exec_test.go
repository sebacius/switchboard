package agent

import (
	"context"
	"testing"
	"time"
)

// ptr makes an addressable CallContext for Handle, which takes a pointer so it
// can report an assistant hand-off back to the caller.
func ptr(cc CallContext) *CallContext { return &cc }

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
			"operator": {
				Strategy: StrategySequential, Members: []string{"user/105"},
			},
		},
	}
}

// newResolution builds the real resolution layer over a default-deny policy.
// Every extension in groupTable is treated as registered so the tests exercise
// authorization and ring behavior rather than registration.
func newResolution(t *testing.T) *CallResolution {
	t.Helper()
	routing := StaticRouting{"acme": groupTable()}
	return NewCallResolution(CallResolutionConfig{
		Resolver: NewResolver(routing,
			resolverDirectory{"105": true, "106": true, "107": true, "150": true},
			nil, quietLogger()),
		BuildPolicy: func(cc CallContext) *Policy {
			return NewPolicy(cc.Tenant, TenantPolicy{}, quietLogger())
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
	res := newResolution(t) // default-deny external
	sess := newFakeSession()

	handled := res.Handle(context.Background(), sess, ptr(internalCall("400")))

	if handled {
		t.Fatal("an external table entry must hand off to the supervisor, not be dialed")
	}
	if got := sess.forwards(); len(got) != 0 {
		t.Fatalf("nothing may leave the box for an external table entry, forwarded %v", got)
	}
}

// The permitted case, so the deny above is not passing for the wrong reason.
func TestResolvedInternalDestinationIsForwarded(t *testing.T) {
	res := newResolution(t)
	sess := newFakeSession()

	if !res.Handle(context.Background(), sess, ptr(internalCall("300"))) {
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
	res := newResolution(t)
	sess := newFakeSession()

	if !res.Handle(context.Background(), sess, ptr(internalCall("200"))) {
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
	res := newResolution(t)

	var firsts []string
	for range 4 {
		sess := newFakeSession()
		if !res.Handle(context.Background(), sess, ptr(internalCall("201"))) {
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

	if !res.Handle(context.Background(), sess, ptr(internalCall("210"))) {
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

// A group no longer carries its own fallback. When nobody answers, resolution
// sends the caller to the tenant operator — one place where that decision is
// written down, rather than two that can disagree.
func TestGroupNoAnswerForwardsToOperator(t *testing.T) {
	res := newResolution(t)
	sess := newFakeSession()
	sess.groupErr = ErrGroupNoAnswer

	if !res.Handle(context.Background(), sess, ptr(internalCall("203"))) {
		t.Fatal("an unanswered group is handled by resolution, not declined")
	}
	if got := sess.forwards(); len(got) != 1 || got[0] != "user/150" {
		t.Fatalf("expected a forward to the operator, got %v", got)
	}
}

// Nobody answering must never become a hangup: the caller gets a destination or
// an honest status, not silence.
func TestGroupNoAnswerNeverHangsUp(t *testing.T) {
	res := newResolution(t)
	sess := newFakeSession()
	sess.groupErr = ErrGroupNoAnswer

	res.Handle(context.Background(), sess, ptr(internalCall("203")))

	if sess.hangupCalls.Load() != 0 {
		t.Fatalf("expected no hangup, got %d", sess.hangupCalls.Load())
	}
}

// A tenant whose group goes unanswered and that has no operator must not drop
// the caller: resolution declines so the caller is told something, never a
// silent hangup.
func TestGroupNoAnswerWithoutOperatorDeclines(t *testing.T) {
	routing := StaticRouting{"acme": {
		Extensions: map[string]string{"220": "group/nooperator"},
		Groups: map[string]RingGroup{
			"nooperator": {Strategy: StrategySequential, Members: []string{"user/105"}},
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

	if res.Handle(context.Background(), sess, ptr(internalCall("220"))) {
		t.Fatal("with no operator configured resolution must decline the call")
	}
	if sess.hangupCalls.Load() != 0 {
		t.Fatal("the caller must not be hung up on")
	}
}

// --- Degradation ---

// Resolution makes no LLM request at all, so an outage is invisible to it. This
// is the whole point of the change's failure story: the PBX keeps routing.
func TestResolutionNeedsNoLLM(t *testing.T) {
	res := newResolution(t)
	sess := newFakeSession()

	// There is no chat client anywhere in this test — if resolution needed one,
	// this could not compile, let alone pass.
	if !res.Handle(context.Background(), sess, ptr(internalCall("300"))) {
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

	if res.Handle(context.Background(), sess, ptr(internalCall("300"))) {
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
