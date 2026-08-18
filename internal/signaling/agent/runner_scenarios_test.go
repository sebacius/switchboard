package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sebas/switchboard/internal/signaling/b2bua"
	"github.com/sebas/switchboard/internal/signaling/dialog"
	"github.com/sebas/switchboard/internal/signaling/llm"
	"github.com/sebas/switchboard/internal/signaling/parking"
)

// These are the end-to-end runner scenarios from the change's verification plan.
// They drive the REAL runner, the REAL executor, and the REAL policy against a
// scripted model and a fake session, so what they prove is the interaction of
// those layers — not any one of them in isolation.

// scenarioRunner builds a runner whose executor is the real CallExecutor over a
// real registry and policy, so tool authorization and disposition handling are
// exercised rather than faked.
func scenarioRunner(t *testing.T, chat llm.ChatClient, policy TenantPolicy, deps RegistryDeps) *Runner {
	t.Helper()
	if deps.Logger == nil {
		deps.Logger = quietLogger()
	}
	return NewRunner(RunnerConfig{
		Chat:    chat,
		Logger:  quietLogger(),
		Prompts: StaticPrompts{"acme": "You are the Acme receptionist."},
		BuildExecutor: func(cc CallContext) ToolExecutor {
			p := NewPolicy(cc.Tenant, policy, quietLogger())
			return NewCallExecutor(BuildRegistry(cc, p, deps), p)
		},
	})
}

func dialCall(target string) llm.ToolCall {
	return llm.ToolCall{Function: llm.ToolCallFunction{
		Name:      "dial",
		Arguments: map[string]any{"target": target},
	}}
}

// --- Scenario: silent internal forward (first-turn tool-only). ---
//
// The defining behaviour of the change: a staff member dialing an extension is
// forwarded WITHOUT the supervisor answering, so the caller hears the real
// phone ring instead of an AI greeting. If this test ever passes while
// HasAnswered() is true, the AI has inserted itself into a call that did not
// need it.

func TestSilentInternalForwardNeverAnswers(t *testing.T) {
	chat := llm.NewScriptedClient(&llm.ChatResult{
		// Tool-only: no content at all. This is what "route, don't greet" means.
		Thinking:  "the caller wants extension 105, route it",
		ToolCalls: []llm.ToolCall{dialCall("user/105")},
	})
	runner := scenarioRunner(t, chat, TenantPolicy{}, RegistryDeps{})
	sess := newFakeSession()

	if err := runWithTimeout(t, runner, sess, 2*time.Second); err != nil {
		t.Fatalf("HandleCall: %v", err)
	}

	if got := sess.forwards(); len(got) != 1 || got[0] != "user/105" {
		t.Fatalf("expected a single pre-answer forward to user/105, got %v", got)
	}
	if got := sess.dials(); len(got) != 0 {
		t.Fatalf("an unanswered call must not take the bridge path, got %v", got)
	}
	if sess.HasAnswered() {
		t.Fatal("silent internal route must NOT answer: answering removes the caller's real ringback")
	}
	if got := sess.spoken(); len(got) != 0 {
		t.Fatalf("silent route must speak nothing, got %v", got)
	}
	// The model's reasoning must never reach the caller.
	for _, line := range sess.spoken() {
		if strings.Contains(line, "route it") {
			t.Fatal("model thinking leaked into TTS")
		}
	}
}

// --- Scenario: IVR answer path. ---
//
// The other half of the answer decision: content on the first turn means the
// supervisor is handling this leg's media, so it answers and converses.

func TestIVRAnswerPathAnswersThenConverses(t *testing.T) {
	chat := llm.NewScriptedClient(
		// Turn 1 is the caller's INVITE: content only, so the runner answers.
		&llm.ChatResult{Content: "Thanks for calling Acme, how can I help?"},
		// Turn 2 is driven by the caller's utterance below: speak, then end the
		// call in the same turn so the test finishes deterministically rather
		// than idling until its deadline.
		&llm.ChatResult{
			Content:   "Let me put you through to billing.",
			ToolCalls: []llm.ToolCall{{Function: llm.ToolCallFunction{Name: "hangup"}}},
		},
	)
	runner := scenarioRunner(t, chat, TenantPolicy{}, RegistryDeps{})
	sess := newFakeSession()
	sess.queueTranscript("I have a billing question")

	if err := runInboundWithTimeout(t, runner, sess, 2*time.Second); err != nil {
		t.Fatalf("HandleCall: %v", err)
	}

	if !sess.HasAnswered() {
		t.Fatal("speaking on the first turn must answer the call: the supervisor owns the media")
	}
	spoken := sess.spoken()
	if len(spoken) < 2 {
		t.Fatalf("expected the greeting and the follow-up to be spoken, got %v", spoken)
	}
	if !strings.Contains(spoken[0], "Thanks for calling") {
		t.Fatalf("expected the greeting first, got %q", spoken[0])
	}
	if got := sess.forwards(); len(got) != 0 {
		t.Fatalf("an answered IVR call must not forward, got %v", got)
	}
	if sess.hangupCalls.Load() == 0 {
		t.Fatal("expected teardown to hang the call up")
	}
}

// --- Scenario: forward-then-CANCEL race. ---
//
// The caller abandons while the target is still ringing. The forward must
// observe cancellation and unwind, and teardown must still run exactly once —
// no goroutine left blocked on a leg nobody will answer.

func TestForwardThenCancelUnwindsCleanly(t *testing.T) {
	chat := llm.NewScriptedClient(&llm.ChatResult{
		ToolCalls: []llm.ToolCall{dialCall("user/105")},
	})
	runner := scenarioRunner(t, chat, TenantPolicy{}, RegistryDeps{})
	sess := newFakeSession()
	sess.forwardBlocks = true // the target rings and rings

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.HandleCall(ctx, sess, testCC()) }()

	// Wait until the forward is genuinely in flight, then CANCEL like the caller.
	select {
	case <-sess.forwardStarted:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("forward never started")
	}
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("HandleCall after CANCEL: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HandleCall did not return after CANCEL: the forward is not honoring its context")
	}

	if sess.HasAnswered() {
		t.Fatal("a call cancelled during forwarding must never have been answered")
	}
	if n := sess.hangupCalls.Load(); n != 1 {
		t.Fatalf("expected teardown to hang up exactly once, got %d", n)
	}
}

// --- Scenario: an orphaned B-leg is cancelled by teardown. ---
//
// This drives the REAL sessionImpl cleanup, not a fake: when the caller vanishes
// mid-forward, the outbound leg we created must be hung up, or a phone keeps
// ringing for a call that no longer exists.

func TestOrphanedBLegIsCancelledOnTeardown(t *testing.T) {
	leg := &fakeLeg{}
	sess := &sessionImpl{
		callID: "orphan-call",
		logger: quietLogger(),
	}
	sess.setBLeg(leg)

	sess.hangupBLeg("caller cancelled")

	if n := leg.hangups.Load(); n != 1 {
		t.Fatalf("expected the orphaned B-leg to be hung up once, got %d", n)
	}
	// A second teardown pass must not double-hangup: the leg handle is cleared.
	sess.hangupBLeg("caller cancelled")
	if n := leg.hangups.Load(); n != 1 {
		t.Fatalf("B-leg cleanup must be idempotent, got %d hangups", n)
	}
}

// --- Scenario: Parked disposition holds the call without re-prompting. ---
//
// park must not block the dispatch loop AND must not drive further turns: the
// loop simply holds the call open until unpark or cancellation. Asserting the
// model was not re-prompted is the only way to see the difference between
// "parked" and "continue".

func TestParkedDispositionHoldsCallUntilCancel(t *testing.T) {
	chat := llm.NewScriptedClient(&llm.ChatResult{
		ToolCalls: []llm.ToolCall{{Function: llm.ToolCallFunction{Name: "park"}}},
	})
	park := &fakeParking{}
	runner := scenarioRunner(t, chat, TenantPolicy{}, RegistryDeps{Parking: park})
	sess := newFakeSession()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.HandleCall(ctx, sess, testCC()) }()

	// Give the runner room to park and then (wrongly) re-prompt if it were going
	// to. One scripted result is all it has, so a re-prompt would also error.
	time.Sleep(150 * time.Millisecond)

	if park.parks.Load() != 1 {
		t.Fatalf("expected the call to be parked once, got %d", park.parks.Load())
	}
	if !sess.HasAnswered() {
		t.Fatal("a parked call must own its media: hold music plays down our RTP session")
	}
	if n := chat.Calls(); n != 1 {
		t.Fatalf("Parked must stop the loop re-prompting; model was called %d times", n)
	}
	if sess.hangupCalls.Load() != 0 {
		t.Fatal("a parked call must stay alive, not tear down")
	}

	// Cancellation (caller hangup / unpark elsewhere) is what ends the hold.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("HandleCall after cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a parked call did not release on cancellation")
	}
	if n := sess.hangupCalls.Load(); n != 1 {
		t.Fatalf("expected exactly one teardown after cancel, got %d", n)
	}
}

// --- Scenario: the admission slot is released exactly once by teardown. ---
//
// A leaked slot silently shrinks a tenant's capacity until a restart, so this
// checks the hook fires on a normal end AND that concurrent teardown initiators
// cannot double-release.

func TestAdmissionSlotReleasedOnceByTeardown(t *testing.T) {
	admission := NewAdmission(StaticPrompts{"acme": "prompt"}, 1, nil)
	cc := testCC()

	decision := admission.Admit(cc)
	if !decision.Admitted {
		t.Fatalf("expected admission, got %q", decision.Reason)
	}
	if admission.Active("acme") != 1 {
		t.Fatalf("expected 1 active call, got %d", admission.Active("acme"))
	}

	var releases atomic.Int32
	hook := func(string) {
		releases.Add(1)
		decision.Release()
	}

	chat := llm.NewScriptedClient(&llm.ChatResult{
		ToolCalls: []llm.ToolCall{{Function: llm.ToolCallFunction{Name: "hangup"}}},
	})
	runner := scenarioRunner(t, chat, TenantPolicy{}, RegistryDeps{})
	sess := newFakeSession()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runner.HandleCall(ctx, sess, cc, hook); err != nil {
		t.Fatalf("HandleCall: %v", err)
	}

	if n := releases.Load(); n != 1 {
		t.Fatalf("teardown hook must run exactly once, ran %d times", n)
	}
	if got := admission.Active("acme"); got != 0 {
		t.Fatalf("expected the channel slot to be freed, still %d active", got)
	}

	// The tenant's single channel is available again.
	if next := admission.Admit(cc); !next.Admitted {
		t.Fatalf("expected the freed slot to admit the next call, got %q", next.Reason)
	}
}

// --- Scenario: COS denies an external dial and the caller is kept. ---
//
// The deny must reach the model as an actionable result rather than dropping
// the call, and the session must never see the dial.

func TestCOSDenyOfExternalDialKeepsCallAlive(t *testing.T) {
	chat := llm.NewScriptedClient(
		// The model tries an external destination the tenant never allowed.
		&llm.ChatResult{ToolCalls: []llm.ToolCall{dialCall("+18005551212")}},
		// The deny result comes back as an actionable tool message, so the model
		// gets an autonomous turn to recover. It apologises and ends the call.
		&llm.ChatResult{
			Content:   "I'm not able to place that call, sorry.",
			ToolCalls: []llm.ToolCall{{Function: llm.ToolCallFunction{Name: "hangup"}}},
		},
	)
	// External dialing disabled: the default, safest posture.
	runner := scenarioRunner(t, chat, TenantPolicy{}, RegistryDeps{})
	sess := newFakeSession()

	if err := runWithTimeout(t, runner, sess, 2*time.Second); err != nil {
		t.Fatalf("HandleCall: %v", err)
	}

	if got := sess.forwards(); len(got) != 0 {
		t.Fatalf("a denied dial must never reach the session, got %v", got)
	}
	if got := sess.dials(); len(got) != 0 {
		t.Fatalf("a denied dial must never reach the session, got %v", got)
	}
	spoken := sess.spoken()
	if len(spoken) == 0 || !strings.Contains(spoken[0], "not able to place") {
		t.Fatalf("expected the call to continue and the model to explain, got %v", spoken)
	}
}

// --- Scenario: an inbound call is offered neither external dial nor unpark. ---
//
// Affordance removal is the strongest fraud defense: there is nothing to
// authorize because there is nothing to call. unpark is excluded for a related
// reason — an outside caller guessing a slot number must not pick up a
// colleague's held call.

func TestInboundRegistryOmitsDialAndUnpark(t *testing.T) {
	policy := NewPolicy("acme", TenantPolicy{AllowExternalDial: true}, quietLogger())
	deps := RegistryDeps{Parking: &fakeParking{}, Logger: quietLogger()}

	inbound := BuildRegistry(CallContext{Direction: DirectionInbound, Tenant: "acme"}, policy, deps)
	if hasTool(inbound, "dial") {
		t.Fatal("inbound must not be offered dial at all")
	}
	if hasTool(inbound, "unpark") {
		t.Fatal("inbound must not be able to retrieve a parked call")
	}
	if !hasTool(inbound, "park") {
		t.Fatal("park reaches nothing external and should still be offered inbound")
	}

	internal := BuildRegistry(CallContext{Direction: DirectionInternal, Tenant: "acme"}, policy, deps)
	if !hasTool(internal, "unpark") {
		t.Fatal("internal directory users must be able to retrieve parked calls")
	}

	// The runner advertises exactly what the executor accepts, so an inbound
	// model is never even shown the dial affordance.
	defs := ToolDefs(inbound)
	for _, d := range defs {
		if d.Function.Name == "dial" {
			t.Fatal("dial must not be advertised to an inbound call")
		}
	}
}

// --- Scenario: an unloaded tenant is rejected before any model call. ---

func TestUnloadedTenantRejectedWithoutLLM(t *testing.T) {
	prompts := StaticPrompts{"acme": "prompt"}
	admission := NewAdmission(prompts, 5, nil)

	if d := admission.Admit(CallContext{Tenant: "ghost"}); d.Admitted {
		t.Fatal("an unknown tenant must be rejected: there is no default tenant")
	} else if !strings.Contains(d.Reason, "not loaded") {
		t.Fatalf("expected a tenant-not-loaded reason, got %q", d.Reason)
	}

	// A tenant whose prompt is present but empty is equally not loaded.
	empty := NewAdmission(StaticPrompts{"blank": "   "}, 5, nil)
	if d := empty.Admit(CallContext{Tenant: "blank"}); d.Admitted {
		t.Fatal("an empty prompt must not count as a loaded tenant")
	}
}

// --- Test doubles ---

// fakeLeg is a minimal b2bua.Leg for the orphaned-B-leg cleanup test.
type fakeLeg struct {
	hangups atomic.Int32
}

func (l *fakeLeg) ID() string                                         { return "fake-leg" }
func (l *fakeLeg) CallID() string                                     { return "fake-leg-call" }
func (l *fakeLeg) Direction() b2bua.LegDirection                      { return b2bua.LegDirectionOutbound }
func (l *fakeLeg) GetState() b2bua.LegState                           { return b2bua.LegStateRinging }
func (l *fakeLeg) GetTerminationCause() b2bua.TerminationCause        { return b2bua.TerminationCauseNormal }
func (l *fakeLeg) WaitForState(context.Context, b2bua.LegState) error { return nil }
func (l *fakeLeg) Dialog() *dialog.Dialog                             { return nil }
func (l *fakeLeg) SessionID() string                                  { return "fake-leg-session" }
func (l *fakeLeg) Context() context.Context                           { return context.Background() }
func (l *fakeLeg) Info() *b2bua.LegInfo                               { return nil }
func (l *fakeLeg) Answer(context.Context) error                       { return nil }
func (l *fakeLeg) Destroy()                                           {}

func (l *fakeLeg) OnStateChange(func(old, new b2bua.LegState)) func() { return func() {} }
func (l *fakeLeg) OnTerminated(func(cause b2bua.TerminationCause))    {}

func (l *fakeLeg) Hangup(context.Context, b2bua.TerminationCause) error {
	l.hangups.Add(1)
	return nil
}

// fakeParking records park/unpark calls without touching real media.
type fakeParking struct {
	mu      sync.Mutex
	parks   atomic.Int32
	unparks atomic.Int32
	slots   []string // slot IDs requested, in order
}

func (p *fakeParking) Park(_ context.Context, req parking.ParkRequest) (*parking.ParkResult, error) {
	p.parks.Add(1)
	slot := req.SlotID
	if slot == "" {
		slot = "701" // stand in for the auto-assigned slot
	}
	p.mu.Lock()
	p.slots = append(p.slots, slot)
	p.mu.Unlock()
	return &parking.ParkResult{SlotID: slot}, nil
}

func (p *fakeParking) Unpark(_ context.Context, req parking.UnparkRequest) (*parking.UnparkResult, error) {
	p.unparks.Add(1)
	p.mu.Lock()
	p.slots = append(p.slots, req.SlotID)
	p.mu.Unlock()
	return nil, fmt.Errorf("slot %s is empty", req.SlotID)
}

// The runner must advertise exactly the registry its executor will accept. If
// this regresses, the model is handed an empty tool list and can only reply in
// prose — it would look like a model-quality problem while being a wiring bug,
// so it is worth pinning down explicitly.
func TestRunnerAdvertisesExecutorRegistry(t *testing.T) {
	policy := NewPolicy("acme", TenantPolicy{}, quietLogger())
	exec := NewCallExecutor(
		BuildRegistry(CallContext{Direction: DirectionInternal, Tenant: "acme"}, policy, RegistryDeps{Logger: quietLogger()}),
		policy,
	)

	run := &callRun{executor: exec}
	defs := run.toolDefs()
	if len(defs) == 0 {
		t.Fatal("the runner advertised no tools: the model would have nothing to call")
	}

	names := map[string]bool{}
	for _, d := range defs {
		names[d.Function.Name] = true
		if d.Function.Parameters == nil {
			t.Fatalf("tool %q advertised without a parameters schema", d.Function.Name)
		}
	}
	for _, want := range []string{"dial", "hangup", "play_audio"} {
		if !names[want] {
			t.Fatalf("expected %q to be advertised, got %v", want, names)
		}
	}
}

// A misconfigured runner bails out before the teardown funnel exists, so it must
// release the caller's resources on the way out. This is the worst place to leak:
// a bad config fails every call, so a leaked admission slot would drain a
// tenant's capacity within seconds of a bad deploy.
func TestMisconfiguredRunnerStillReleasesResources(t *testing.T) {
	admission := NewAdmission(StaticPrompts{"acme": "prompt"}, 1, nil)
	cc := testCC()

	decision := admission.Admit(cc)
	if !decision.Admitted {
		t.Fatalf("expected admission, got %q", decision.Reason)
	}

	var releases atomic.Int32
	hook := func(string) {
		releases.Add(1)
		decision.Release()
	}

	// No Chat client configured: HandleCall cannot proceed.
	runner := NewRunner(RunnerConfig{
		Logger:        quietLogger(),
		BuildExecutor: func(CallContext) ToolExecutor { return &fakeExecutor{} },
	})
	sess := newFakeSession()

	if err := runner.HandleCall(context.Background(), sess, cc, hook); err == nil {
		t.Fatal("expected a misconfiguration error")
	}

	if n := releases.Load(); n != 1 {
		t.Fatalf("expected the teardown hook to run once on the bail-out path, ran %d times", n)
	}
	if got := admission.Active("acme"); got != 0 {
		t.Fatalf("a misconfigured runner leaked the channel slot: %d still active", got)
	}
	if sess.hangupCalls.Load() != 1 {
		t.Fatal("a call the runner cannot serve must be hung up, not left ringing")
	}
}

// --- Scenario: design #11 self-correction on an internal first turn. ---
//
// The instruction to route silently lives in settings.md and in
// FirstTurnDirective, but instructions are not guarantees: live testing showed a
// large tenant prompt is enough to make qwen3:8b greet an internal caller
// instead of routing. These cover the one-shot correction that catches it.

// The retry fires, the call routes, and — the part that actually matters — the
// rejected greeting is NEVER spoken. Speaking it would both greet a colleague
// who dialed an extension and send the 200 OK, permanently destroying the
// pre-answer forward path.
func TestInternalFirstTurnProseIsCorrectedToSilentRoute(t *testing.T) {
	chat := llm.NewScriptedClient(
		&llm.ChatResult{Content: "Hi there! How can I help you today?"},  // wrong: prose
		&llm.ChatResult{ToolCalls: []llm.ToolCall{dialCall("user/105")}}, // corrected
	)
	runner := scenarioRunner(t, chat, TenantPolicy{}, RegistryDeps{})
	sess := newFakeSession()

	if err := runWithTimeout(t, runner, sess, 2*time.Second); err != nil {
		t.Fatalf("HandleCall: %v", err)
	}

	if got := sess.spoken(); len(got) != 0 {
		t.Fatalf("the rejected greeting must never reach the caller, spoke %v", got)
	}
	if sess.HasAnswered() {
		t.Fatal("correction must not answer the call: answering destroys the forward path")
	}
	if got := sess.forwards(); len(got) != 1 || got[0] != "user/105" {
		t.Fatalf("expected the corrected turn to forward to user/105, got %v", got)
	}
	if n := chat.Calls(); n != 2 {
		t.Fatalf("expected exactly one corrective re-prompt (2 calls), got %d", n)
	}
}

// One retry is the entire budget. If the model still refuses to route, the
// runner converses rather than leaving the caller in silence — a mediocre
// greeting beats dead air.
func TestInternalFirstTurnGivesUpAfterOneRetry(t *testing.T) {
	chat := llm.NewScriptedClient(
		&llm.ChatResult{Content: "Hi there!"},
		&llm.ChatResult{Content: "Sorry, how can I help?"},
		&llm.ChatResult{ToolCalls: []llm.ToolCall{{Function: llm.ToolCallFunction{Name: "hangup"}}}},
	)
	runner := scenarioRunner(t, chat, TenantPolicy{}, RegistryDeps{})
	sess := newFakeSession()
	sess.queueTranscript("never mind")

	if err := runWithTimeout(t, runner, sess, 2*time.Second); err != nil {
		t.Fatalf("HandleCall: %v", err)
	}

	spoken := sess.spoken()
	if len(spoken) == 0 || spoken[0] != "Sorry, how can I help?" {
		t.Fatalf("expected the retry's reply to be spoken (not the discarded first), got %v", spoken)
	}
	if !sess.HasAnswered() {
		t.Fatal("falling back to conversation must answer the call")
	}
	if n := chat.Calls(); n != 3 {
		t.Fatalf("expected 2 first-turn calls + 1 reactive turn, got %d", n)
	}
}

// A first turn that already routed costs nothing extra — no wasted round-trip
// inside the INVITE transaction.
func TestInternalFirstTurnWithToolCallIsNotRetried(t *testing.T) {
	chat := llm.NewScriptedClient(&llm.ChatResult{
		ToolCalls: []llm.ToolCall{dialCall("user/105")},
	})
	runner := scenarioRunner(t, chat, TenantPolicy{}, RegistryDeps{})
	sess := newFakeSession()

	if err := runWithTimeout(t, runner, sess, 2*time.Second); err != nil {
		t.Fatalf("HandleCall: %v", err)
	}
	if n := chat.Calls(); n != 1 {
		t.Fatalf("a first turn that routed must not be re-prompted, got %d calls", n)
	}
}

// Inbound is exempt: a greeting is the CORRECT answer there, so correcting it
// would both waste a round-trip and produce worse behaviour.
func TestInboundFirstTurnProseIsNotRetried(t *testing.T) {
	chat := llm.NewScriptedClient(
		&llm.ChatResult{Content: "Thanks for calling Acme."},
		&llm.ChatResult{ToolCalls: []llm.ToolCall{{Function: llm.ToolCallFunction{Name: "hangup"}}}},
	)
	runner := scenarioRunner(t, chat, TenantPolicy{}, RegistryDeps{})
	sess := newFakeSession()
	sess.queueTranscript("goodbye")

	if err := runInboundWithTimeout(t, runner, sess, 2*time.Second); err != nil {
		t.Fatalf("HandleCall: %v", err)
	}

	spoken := sess.spoken()
	if len(spoken) != 1 || spoken[0] != "Thanks for calling Acme." {
		t.Fatalf("expected the inbound greeting spoken as-is, got %v", spoken)
	}
	if n := chat.Calls(); n != 2 {
		t.Fatalf("expected 1 greeting + 1 reactive turn (no correction), got %d", n)
	}
}

// The directive is direction-specific and lands LAST in the system message,
// after the tenant prompt — that position is the whole reason it works.
func TestFirstTurnDirectiveIsDirectionSpecificAndLast(t *testing.T) {
	internal := CallContext{Caller: "102", Callee: "105", Direction: DirectionInternal, Tenant: "acme"}
	inbound := CallContext{Caller: "+1555", Callee: "5558001200", Direction: DirectionInbound, Tenant: "acme"}

	if d := internal.FirstTurnDirective(); !strings.Contains(d, "105") || !strings.Contains(d, "NO text") {
		t.Fatalf("internal directive should name the extension and forbid text, got %q", d)
	}
	if d := inbound.FirstTurnDirective(); !strings.Contains(strings.ToLower(d), "greet") {
		t.Fatalf("inbound directive should ask for a greeting, got %q", d)
	}

	// The directive is the LAST thing in the system message, so anything
	// absolute in it overrides the tenant's own configuration. Only the internal
	// case earns that, because silent routing is a hard requirement there. The
	// other two must defer to the tenant file, or a tenant cannot declare what
	// its own extensions mean (e.g. "600 is the assistant, talk to them").
	outbound := CallContext{Caller: "100", Callee: "600", Direction: DirectionOutbound, Tenant: "devtenant"}
	od := outbound.FirstTurnDirective()
	if !strings.Contains(od, "600") {
		t.Fatalf("outbound directive should name the dialed number, got %q", od)
	}
	if !strings.Contains(strings.ToLower(od), "instructions above") {
		t.Fatalf("outbound directive must defer to the tenant instructions, got %q", od)
	}
	if !strings.Contains(strings.ToLower(od), "service you provide yourself") {
		t.Fatalf("outbound directive must allow the assistant to handle the call itself, got %q", od)
	}
	if strings.Contains(od, "Do not explain") {
		t.Fatal("outbound directive must not carry an unconditional gag on explaining itself")
	}
	if !strings.Contains(strings.ToLower(inbound.FirstTurnDirective()), "unless your instructions") {
		t.Fatal("inbound directive should offer a default the tenant can override, not an order")
	}
	if od == internal.FirstTurnDirective() || od == inbound.FirstTurnDirective() {
		t.Fatal("all three directions must produce distinct directives")
	}
	if internal.FirstTurnDirective() == inbound.FirstTurnDirective() {
		t.Fatal("the directive must differ by direction")
	}

	// Compose the system message the way HandleCall does and assert ordering.
	tenantPrompt := strings.Repeat("tenant knowledge base line\n", 200)
	system := internal.FormatForPrompt() + "\n" + tenantPrompt + "\n\n" + internal.FirstTurnDirective()
	if !strings.HasSuffix(system, internal.FirstTurnDirective()) {
		t.Fatal("the directive must be the LAST thing in the system message")
	}
	if strings.Index(system, tenantPrompt) > strings.Index(system, internal.FirstTurnDirective()) {
		t.Fatal("the directive must come after the tenant prompt, not before it")
	}
}
