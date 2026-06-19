package agent

import (
	"context"
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
	mu      sync.Mutex
	dialed  []string
	hangups int
	dialErr error
	playErr error
	played  []string
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

	tools := BuildRegistry(cc, policy)
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

	tools := BuildRegistry(cc, policy)
	if !hasTool(tools, "dial") {
		t.Fatal("internal registry should contain the dial tool when external is enabled")
	}
	for _, tl := range tools {
		if tl.Name == "dial" && !tl.External {
			t.Fatal("dial tool should be marked External when external dial is enabled")
		}
	}
}

// --- Executor: unknown tool → Terminal. ---

func TestExecutorUnknownToolTerminal(t *testing.T) {
	exec := NewCallExecutor(BuildRegistry(CallContext{Direction: DirectionInternal}, nil), nil)
	_, disp, err := exec.Execute(context.Background(), callWithArgs("teleport", nil), &recordingSession{})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if disp != DispositionTerminal {
		t.Fatalf("expected Terminal for unknown tool, got %s", disp)
	}
}

// --- Executor: missing arg → actionable Continue, no Go error. ---

func TestExecutorMissingArgActionableContinue(t *testing.T) {
	policy := NewPolicy("acme", TenantPolicy{}, quietLogger())
	exec := NewCallExecutor(BuildRegistry(CallContext{Direction: DirectionInternal}, policy), policy)

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
	exec := NewCallExecutor(BuildRegistry(CallContext{Direction: DirectionInternal}, policy), policy)
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
	exec := NewCallExecutor(BuildRegistry(CallContext{Direction: DirectionInternal}, policy), policy)
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

func TestExecutorPermittedDialInvokesHandler(t *testing.T) {
	policy := NewPolicy("acme", TenantPolicy{
		AllowExternalDial:      true,
		ExternalAllowlist:      []string{"+1800"},
		MaxExternalUnitsPerDay: 10,
		SymbolicTargets:        map[string]string{"support": "+18005551212"},
	}, quietLogger())
	exec := NewCallExecutor(BuildRegistry(CallContext{Direction: DirectionInternal}, policy), policy)
	sess := &recordingSession{}

	result, disp, err := exec.Execute(context.Background(),
		callWithArgs("dial", map[string]any{"target": "support"}), sess)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if disp != DispositionContinue {
		t.Fatalf("expected Continue after a permitted dial, got %s", disp)
	}
	dialed := sess.dialedTargets()
	if len(dialed) != 1 || dialed[0] != "+18005551212" {
		t.Fatalf("expected handler dialed the resolved target +18005551212, got %v", dialed)
	}
	if result == "" {
		t.Fatal("expected a non-empty success result")
	}
}

// --- Executor: hangup tool is Terminal and reaches the session. ---

func TestExecutorHangupTerminal(t *testing.T) {
	exec := NewCallExecutor(BuildRegistry(CallContext{Direction: DirectionInternal}, nil), nil)
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
	defs := toToolDefs(BuildRegistry(CallContext{Direction: DirectionInternal}, policy))
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
