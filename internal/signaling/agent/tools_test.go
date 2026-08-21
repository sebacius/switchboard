package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sebas/switchboard/internal/signaling/dialog"
	"github.com/sebas/switchboard/internal/signaling/llm"
	"github.com/sebas/switchboard/internal/signaling/mediaclient"
)

// recordingSession is a CallSession test double that records the targets passed
// to Dial and the hangup count, for executor/handler assertions.
type recordingSession struct {
	mu          sync.Mutex
	dialed      []string
	forwarded   []string
	groupRounds [][]string
	groupErr    error
	hangups     int
	answered    bool
	dialErr     error
	forwardErr  error
	playErr     error
	played      []string
}

func (s *recordingSession) CallID() string                                { return "rec-call" }
func (s *recordingSession) Destination() string                           { return "1000" }
func (s *recordingSession) CallerID() string                              { return "2000" }
func (s *recordingSession) Domain() string                                { return "example.test" }
func (s *recordingSession) Context() context.Context                      { return context.Background() }
func (s *recordingSession) PlayTTS(context.Context, string, string) error { return nil }
func (s *recordingSession) StopAudio() error                              { return nil }
func (s *recordingSession) Listen(context.Context, int, int) (string, error) {
	return "", nil
}

func (s *recordingSession) PlayAudio(_ context.Context, file string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.played = append(s.played, file)
	return s.playErr
}

func (s *recordingSession) Dial(_ context.Context, target string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dialed = append(s.dialed, target)
	return s.dialErr
}

func (s *recordingSession) Answer(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answered = true
	return nil
}

func (s *recordingSession) MarkRinging() {}

func (s *recordingSession) HasAnswered() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.answered
}

func (s *recordingSession) Forward(_ context.Context, target string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forwarded = append(s.forwarded, target)
	return s.forwardErr
}

// forwardedTargets returns what Forward was called with, in order.
func (s *recordingSession) forwardedTargets() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.forwarded...)
}

// ForwardGroup records the rounds a ring group was asked to ring. The tool layer
// never calls it — resolution does — so a recorded round here would mean the
// supervisor grew a ring-group path nobody designed.
func (s *recordingSession) ForwardGroup(_ context.Context, rounds [][]string, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.groupRounds = append(s.groupRounds, rounds...)
	return s.groupErr
}

func (s *recordingSession) Hangup(string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hangups++
	return nil
}

func (s *recordingSession) IsTerminated() bool                   { return false }
func (s *recordingSession) GetDialog() *dialog.Dialog            { return nil }
func (s *recordingSession) GetSessionID() string                 { return "rtp" }
func (s *recordingSession) GetTransport() mediaclient.Transport  { return nil }
func (s *recordingSession) TerminateDialog(string, string) error { return nil }

func (s *recordingSession) dialedTargets() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.dialed...)
}

// callWithArgs builds a tool call with parsed arguments.
func callWithArgs(name string, args map[string]any) llm.ToolCall {
	return llm.ToolCall{Function: llm.ToolCallFunction{Name: name, Arguments: args}}
}

// --- Registry: inbound has no external-dial tool; internal does when enabled. ---

func hasTool(tools []Tool, name string) bool {
	for _, t := range tools {
		if t.Name == name {
			return true
		}
	}
	return false
}

func TestRegistryInboundHasNoExternalDial(t *testing.T) {
	policy := NewPolicy("acme", TenantPolicy{AllowExternalDial: true}, quietLogger())
	cc := CallContext{Direction: DirectionInbound, Tenant: "acme"}

	tools := BuildRegistry(cc, policy, RegistryDeps{Logger: quietLogger()})
	if hasTool(tools, "dial") {
		t.Fatal("inbound registry must not contain the dial tool (affordance removal)")
	}
	// It still gets the non-dialing tools.
	if !hasTool(tools, "hangup") || !hasTool(tools, "play_audio") {
		t.Fatal("inbound registry should still offer hangup and play_audio")
	}
}

func TestRegistryInternalHasDialWhenEnabled(t *testing.T) {
	policy := NewPolicy("acme", TenantPolicy{AllowExternalDial: true}, quietLogger())
	cc := CallContext{Direction: DirectionInternal, Tenant: "acme"}

	tools := BuildRegistry(cc, policy, RegistryDeps{Logger: quietLogger()})
	if !hasTool(tools, "dial") {
		t.Fatal("internal registry should contain the dial tool when external is enabled")
	}
	for _, tl := range tools {
		if tl.Name == "dial" && !tl.External {
			t.Fatal("dial tool should be marked External when external dial is enabled")
		}
	}
}

// --- Executor: unknown tool → operator transfer, never a hangup. ---

// One hallucinated token must not drop a customer's call. With an operator
// configured, an unknown tool hands the caller to that person.
func TestExecutorUnknownToolTransfersToOperator(t *testing.T) {
	policy := NewPolicy("acme", TenantPolicy{}, quietLogger())
	exec := NewCallExecutor(
		BuildRegistry(CallContext{Direction: DirectionInternal}, policy, RegistryDeps{Logger: quietLogger()}),
		policy, "user/150")
	sess := &recordingSession{}

	result, disp, err := exec.Execute(context.Background(), callWithArgs("teleport", nil), sess)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if disp != DispositionTerminal {
		t.Fatalf("a completed operator transfer ends the supervisor's involvement, got %s", disp)
	}
	if got := sess.forwardedTargets(); len(got) != 1 || got[0] != "user/150" {
		t.Fatalf("expected a pre-answer forward to the operator, got %v", got)
	}
	if strings.Contains(result, "ending the call") {
		t.Fatalf("the result must not describe a hangup, got %q", result)
	}
}

// A tenant with no operator must still not hang up: the model is told what the
// real tools are and gets to try again, bounded by the runaway breaker.
func TestExecutorUnknownToolWithoutOperatorKeepsCallAlive(t *testing.T) {
	exec := NewCallExecutor(
		BuildRegistry(CallContext{Direction: DirectionInternal}, nil, RegistryDeps{Logger: quietLogger()}),
		nil, "")
	sess := &recordingSession{}

	result, disp, err := exec.Execute(context.Background(), callWithArgs("teleport", nil), sess)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if disp != DispositionContinue {
		t.Fatalf("expected the call to continue without an operator, got %s", disp)
	}
	if len(sess.forwardedTargets()) != 0 {
		t.Fatal("nothing should have been dialed")
	}
	if !strings.Contains(result, "hangup") || !strings.Contains(result, "play_audio") {
		t.Fatalf("the model must be told which tools actually exist, got %q", result)
	}
}

// --- Executor: missing arg → actionable Continue, no Go error. ---

func TestExecutorMissingArgActionableContinue(t *testing.T) {
	policy := NewPolicy("acme", TenantPolicy{}, quietLogger())
	exec := NewCallExecutor(BuildRegistry(CallContext{Direction: DirectionInternal}, policy, RegistryDeps{Logger: quietLogger()}), policy, "")

	result, disp, err := exec.Execute(context.Background(), callWithArgs("dial", map[string]any{}), &recordingSession{})
	if err != nil {
		t.Fatalf("missing arg must not be a Go error, got: %v", err)
	}
	if disp != DispositionContinue {
		t.Fatalf("expected Continue on missing arg, got %s", disp)
	}
	if result == "" {
		t.Fatal("expected an actionable result string on missing arg")
	}
}

// --- Executor: identical just-failed call refused. ---

func TestExecutorDuplicateFailedCallRefused(t *testing.T) {
	policy := NewPolicy("acme", TenantPolicy{}, quietLogger())
	exec := NewCallExecutor(BuildRegistry(CallContext{Direction: DirectionInternal}, policy, RegistryDeps{Logger: quietLogger()}), policy, "")
	sess := &recordingSession{}

	// First: dial an unknown symbol → policy deny (a failure).
	args := map[string]any{"target": "nowhere"}
	first, disp1, _ := exec.Execute(context.Background(), callWithArgs("dial", args), sess)
	if disp1 != DispositionContinue {
		t.Fatalf("expected Continue on policy deny, got %s", disp1)
	}

	// Second: identical call → refused with a distinct nudge, handler not invoked.
	second, disp2, _ := exec.Execute(context.Background(), callWithArgs("dial", args), sess)
	if disp2 != DispositionContinue {
		t.Fatalf("expected Continue on duplicate refusal, got %s", disp2)
	}
	if second == first {
		t.Fatal("expected a distinct duplicate-refusal message, got the same result as the first failure")
	}
	if len(sess.dialedTargets()) != 0 {
		t.Fatalf("a denied/duplicate dial must never reach the session, dialed: %v", sess.dialedTargets())
	}
}

// --- Executor: policy-denied dial returns the deny reason with Continue. ---

func TestExecutorPolicyDeniedDialContinue(t *testing.T) {
	// External disabled → a symbolic external target is denied.
	policy := NewPolicy("acme", TenantPolicy{
		SymbolicTargets: map[string]string{"support": "+18005551212"},
	}, quietLogger())
	exec := NewCallExecutor(BuildRegistry(CallContext{Direction: DirectionInternal}, policy, RegistryDeps{Logger: quietLogger()}), policy, "")
	sess := &recordingSession{}

	result, disp, err := exec.Execute(context.Background(),
		callWithArgs("dial", map[string]any{"target": "support"}), sess)
	if err != nil {
		t.Fatalf("policy deny must not be a Go error, got: %v", err)
	}
	if disp != DispositionContinue {
		t.Fatalf("expected Continue on policy deny, got %s", disp)
	}
	if result == "" {
		t.Fatal("expected the deny reason as the result")
	}
	if len(sess.dialedTargets()) != 0 {
		t.Fatalf("a denied dial must not invoke the handler, dialed: %v", sess.dialedTargets())
	}
}

// --- Executor: permitted dial invokes the handler with the resolved target. ---
//
// The session starts UNANSWERED, so this exercises the pre-answer Forward path:
// the model dials a symbolic name, the policy resolves it, and the handler
// forwards the INVITE rather than bridging into media we do not own.

func TestExecutorPermittedDialForwardsResolvedTarget(t *testing.T) {
	policy := NewPolicy("acme", TenantPolicy{
		AllowExternalDial:      true,
		ExternalAllowlist:      []string{"+1800"},
		MaxExternalUnitsPerDay: 10,
		SymbolicTargets:        map[string]string{"support": "+18005551212"},
	}, quietLogger())
	exec := NewCallExecutor(BuildRegistry(CallContext{Direction: DirectionInternal}, policy, RegistryDeps{Logger: quietLogger()}), policy, "")
	sess := &recordingSession{}

	result, disp, err := exec.Execute(context.Background(),
		callWithArgs("dial", map[string]any{"target": "support"}), sess)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	// A dial that completed is Terminal: the handler blocks for the life of the
	// bridge, so returning without error means the call is already over.
	if disp != DispositionTerminal {
		t.Fatalf("expected Terminal after a completed dial, got %s", disp)
	}
	forwarded := sess.forwardedTargets()
	if len(forwarded) != 1 || forwarded[0] != "+18005551212" {
		t.Fatalf("expected the handler to FORWARD the resolved target +18005551212, got %v", forwarded)
	}
	if got := sess.dialedTargets(); len(got) != 0 {
		t.Fatalf("pre-answer dial must not take the bridge path, got %v", got)
	}
	if sess.HasAnswered() {
		t.Fatal("forwarding must never answer the caller: that is what preserves real ringback")
	}
	if result == "" {
		t.Fatal("expected a non-empty success result")
	}
}

// After the supervisor has answered it owns the media, so the same dial must
// take the bridge path instead — the other half of the forward-versus-bridge
// requirement.

func TestExecutorDialAfterAnswerBridges(t *testing.T) {
	policy := NewPolicy("acme", TenantPolicy{
		SymbolicTargets: map[string]string{"sales": "user/160"},
	}, quietLogger())
	exec := NewCallExecutor(BuildRegistry(CallContext{Direction: DirectionInternal}, policy, RegistryDeps{Logger: quietLogger()}), policy, "")

	sess := &recordingSession{}
	if err := sess.Answer(context.Background()); err != nil {
		t.Fatalf("answer: %v", err)
	}

	_, disp, err := exec.Execute(context.Background(),
		callWithArgs("dial", map[string]any{"target": "sales"}), sess)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if disp != DispositionTerminal {
		t.Fatalf("expected Terminal after a completed dial, got %s", disp)
	}
	if got := sess.dialedTargets(); len(got) != 1 || got[0] != "user/160" {
		t.Fatalf("expected the post-answer bridge path to dial user/160, got %v", got)
	}
	if got := sess.forwardedTargets(); len(got) != 0 {
		t.Fatalf("post-answer dial must not forward, got %v", got)
	}
}

// A dial that FAILS is downgraded to Continue so the caller is not dropped: the
// model gets an actionable result and can offer an alternative.

func TestExecutorFailedDialIsRecoverable(t *testing.T) {
	policy := NewPolicy("acme", TenantPolicy{
		SymbolicTargets: map[string]string{"sales": "user/160"},
	}, quietLogger())
	exec := NewCallExecutor(BuildRegistry(CallContext{Direction: DirectionInternal}, policy, RegistryDeps{Logger: quietLogger()}), policy, "")
	sess := &recordingSession{forwardErr: errors.New("486 Busy Here")}

	result, disp, err := exec.Execute(context.Background(),
		callWithArgs("dial", map[string]any{"target": "sales"}), sess)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if disp != DispositionContinue {
		t.Fatalf("a failed dial must keep the call alive (Continue), got %s", disp)
	}
	if !strings.Contains(result, "voicemail") {
		t.Fatalf("expected an actionable failure result naming an alternative, got %q", result)
	}
}

// --- Executor: hangup tool is Terminal and reaches the session. ---

func TestExecutorHangupTerminal(t *testing.T) {
	exec := NewCallExecutor(BuildRegistry(CallContext{Direction: DirectionInternal}, nil, RegistryDeps{Logger: quietLogger()}), nil, "")
	sess := &recordingSession{}

	_, disp, err := exec.Execute(context.Background(), callWithArgs("hangup", nil), sess)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if disp != DispositionTerminal {
		t.Fatalf("expected Terminal for hangup, got %s", disp)
	}
	if sess.hangups != 1 {
		t.Fatalf("expected hangup to reach the session once, got %d", sess.hangups)
	}
}

// --- toToolDefs advertises the registry in wire format. ---

func TestToToolDefsShape(t *testing.T) {
	policy := NewPolicy("acme", TenantPolicy{AllowExternalDial: true}, quietLogger())
	defs := ToolDefs(BuildRegistry(CallContext{Direction: DirectionInternal}, policy, RegistryDeps{Logger: quietLogger()}))
	if len(defs) == 0 {
		t.Fatal("expected tool defs")
	}
	for _, d := range defs {
		if d.Type != "function" {
			t.Fatalf("expected type function, got %q", d.Type)
		}
		if d.Function.Name == "" || d.Function.Parameters == nil {
			t.Fatalf("tool def missing name or parameters: %+v", d)
		}
	}
}
