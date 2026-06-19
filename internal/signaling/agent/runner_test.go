package agent

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sebas/switchboard/internal/signaling/dialog"
	"github.com/sebas/switchboard/internal/signaling/llm"
	"github.com/sebas/switchboard/internal/signaling/mediaclient"
)

// fakeSession is a test CallSession. It records PlayTTS text and serves Listen
// from a queue of transcripts; once the queue is drained, Listen blocks on ctx
// so the dispatch loop parks on the events channel like the real producer.
type fakeSession struct {
	mu sync.Mutex

	callID string

	ttsSpoken []string
	listenQ   chan string // transcripts to feed; closed when no more

	hangupCalls   atomic.Int32
	terminatedVal atomic.Bool
}

func newFakeSession() *fakeSession {
	return &fakeSession{
		callID:  "test-call",
		listenQ: make(chan string, 8),
	}
}

func (f *fakeSession) queueTranscript(t string) { f.listenQ <- t }

func (f *fakeSession) spoken() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ttsSpoken...)
}

func (f *fakeSession) CallID() string                          { return f.callID }
func (f *fakeSession) Destination() string                     { return "1000" }
func (f *fakeSession) CallerID() string                        { return "2000" }
func (f *fakeSession) Domain() string                          { return "example.test" }
func (f *fakeSession) Context() context.Context                { return context.Background() }
func (f *fakeSession) PlayAudio(context.Context, string) error { return nil }

func (f *fakeSession) PlayTTS(ctx context.Context, text, _ string) error {
	f.mu.Lock()
	f.ttsSpoken = append(f.ttsSpoken, text)
	f.mu.Unlock()
	return nil
}

func (f *fakeSession) StopAudio() error { return nil }

func (f *fakeSession) Listen(ctx context.Context, _, _ int) (string, error) {
	select {
	case t, ok := <-f.listenQ:
		if !ok {
			// Queue drained: behave like a silent listen that blocks on ctx.
			<-ctx.Done()
			return "", ctx.Err()
		}
		return t, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (f *fakeSession) Dial(context.Context, string, time.Duration) error { return nil }

func (f *fakeSession) Hangup(string) error {
	f.hangupCalls.Add(1)
	f.terminatedVal.Store(true)
	return nil
}

func (f *fakeSession) IsTerminated() bool                   { return f.terminatedVal.Load() }
func (f *fakeSession) GetDialog() *dialog.Dialog            { return nil }
func (f *fakeSession) GetSessionID() string                 { return "rtp-sess" }
func (f *fakeSession) GetTransport() mediaclient.Transport  { return nil }
func (f *fakeSession) TerminateDialog(string, string) error { return nil }

// fakeExecutor is a programmable ToolExecutor. Each call returns the next queued
// disposition/result, recording the tool names it saw.
type fakeExecutor struct {
	mu    sync.Mutex
	resp  []execResp
	idx   int
	calls []string
}

type execResp struct {
	result string
	disp   Disposition
	err    error
}

func (e *fakeExecutor) Execute(_ context.Context, call llm.ToolCall, _ CallSession) (string, Disposition, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, call.Function.Name)
	if e.idx >= len(e.resp) {
		// Default: continue with a generic result.
		return "ok", DispositionContinue, nil
	}
	r := e.resp[e.idx]
	e.idx++
	return r.result, r.disp, r.err
}

func (e *fakeExecutor) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.calls)
}

// buildExec adapts a fixed executor into the per-call BuildExecutor seam the
// runner now uses; every call gets the same fake.
func buildExec(e ToolExecutor) func(CallContext) ToolExecutor {
	return func(CallContext) ToolExecutor { return e }
}

func toolCall(name string) llm.ToolCall {
	return llm.ToolCall{Function: llm.ToolCallFunction{Name: name}}
}

func testCC() CallContext {
	return CallContext{Caller: "2000", Callee: "1000", Direction: DirectionInternal, Tenant: "acme"}
}

// runWithTimeout runs HandleCall under a deadline so a hung test fails fast
// instead of blocking the suite. Returns the HandleCall error.
func runWithTimeout(t *testing.T, r *Runner, sess CallSession, d time.Duration) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return r.HandleCall(ctx, sess, testCC())
}

// --- Silent route: tool-only first turn → executor invoked, no TTS spoken. ---

func TestFirstTurnSilentRoute(t *testing.T) {
	chat := llm.NewScriptedClient(&llm.ChatResult{
		ToolCalls: []llm.ToolCall{toolCall("dial")},
	})
	exec := &fakeExecutor{resp: []execResp{{result: "forwarded", disp: DispositionTerminal}}}
	sess := newFakeSession()
	r := NewRunner(RunnerConfig{Chat: chat, BuildExecutor: buildExec(exec)})

	if err := runWithTimeout(t, r, sess, time.Second); err != nil {
		t.Fatalf("HandleCall: %v", err)
	}
	if exec.callCount() != 1 {
		t.Fatalf("expected 1 tool execution, got %d", exec.callCount())
	}
	if got := sess.spoken(); len(got) != 0 {
		t.Fatalf("expected no TTS on silent route, spoke %v", got)
	}
	if n := sess.hangupCalls.Load(); n != 1 {
		t.Fatalf("expected hangup once (terminal tool), got %d", n)
	}
}

// --- Conversational: text first turn → TTS spoken, then a speech event drives a
// second turn. ---

func TestConversationalTurns(t *testing.T) {
	chat := llm.NewScriptedClient(
		&llm.ChatResult{Content: "Hi, how can I help?"},                // first turn
		&llm.ChatResult{ToolCalls: []llm.ToolCall{toolCall("hangup")}}, // reply to speech
	)
	exec := &fakeExecutor{resp: []execResp{{result: "bye", disp: DispositionTerminal}}}
	sess := newFakeSession()
	sess.queueTranscript("I want billing") // drives the second turn

	r := NewRunner(RunnerConfig{Chat: chat, BuildExecutor: buildExec(exec)})
	if err := runWithTimeout(t, r, sess, 2*time.Second); err != nil {
		t.Fatalf("HandleCall: %v", err)
	}

	spoken := sess.spoken()
	if len(spoken) != 1 || spoken[0] != "Hi, how can I help?" {
		t.Fatalf("expected greeting spoken once, got %v", spoken)
	}
	if exec.callCount() != 1 {
		t.Fatalf("expected the speech-turn tool to run once, got %d", exec.callCount())
	}
}

// --- Terminal tool ends the loop and tears down exactly once. ---

func TestTerminalToolTeardownOnce(t *testing.T) {
	chat := llm.NewScriptedClient(&llm.ChatResult{
		Content:   "Goodbye",
		ToolCalls: []llm.ToolCall{toolCall("hangup")},
	})
	exec := &fakeExecutor{resp: []execResp{{result: "done", disp: DispositionTerminal}}}
	sess := newFakeSession()
	r := NewRunner(RunnerConfig{Chat: chat, BuildExecutor: buildExec(exec)})

	if err := runWithTimeout(t, r, sess, time.Second); err != nil {
		t.Fatalf("HandleCall: %v", err)
	}
	if n := sess.hangupCalls.Load(); n != 1 {
		t.Fatalf("expected exactly one hangup, got %d", n)
	}
}

// --- Teardown idempotency: calling teardown from two places runs the guarded
// body once. ---

func TestTeardownIdempotent(t *testing.T) {
	sess := newFakeSession()
	r := NewRunner(RunnerConfig{Chat: llm.NewScriptedClient(), BuildExecutor: buildExec(&fakeExecutor{})})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	run := &callRun{
		cfg:        r.cfg,
		log:        r.log,
		session:    sess,
		callCtx:    ctx,
		callCancel: cancel,
		events:     make(chan Event, 1),
	}

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run.teardown("concurrent")
		}()
	}
	wg.Wait()

	if n := sess.hangupCalls.Load(); n != 1 {
		t.Fatalf("expected teardown body to run once, hangup called %d times", n)
	}
	if ctx.Err() == nil {
		t.Fatal("expected callCtx cancelled after teardown")
	}
}

// --- Runaway breaker: an executor that keeps returning Continue with
// re-prompting stops at the soft cap and tears down by the hard cap. ---

func TestRunawayBreaker(t *testing.T) {
	// Build enough scripted results that the runner would spin forever if the
	// breaker did not stop it. Every turn re-emits a tool; the executor always
	// says Continue, so each is an autonomous turn.
	results := make([]*llm.ChatResult, 0, 50)
	for i := 0; i < 50; i++ {
		results = append(results, &llm.ChatResult{ToolCalls: []llm.ToolCall{toolCall("lookup")}})
	}
	chat := llm.NewScriptedClient(results...)
	exec := &fakeExecutor{} // default: Continue, "ok"
	sess := newFakeSession()

	// Hard cap is checked before the soft cap in autonomousReprompt, so a hard
	// cap at or below the soft cap exercises the teardown branch. Here hard=4
	// fires before soft=10 ever would.
	r := NewRunner(RunnerConfig{
		Chat:          chat,
		BuildExecutor: buildExec(exec),
		SoftCap:       10,
		HardCap:       4,
	})

	if err := runWithTimeout(t, r, sess, 2*time.Second); err != nil {
		t.Fatalf("HandleCall: %v", err)
	}

	// The first turn is reactive (turn 1), then autonomous re-prompts increment.
	// Hard cap 4 → teardown by the 4th autonomous turn; well under 50.
	if n := exec.callCount(); n == 0 || n > 5 {
		t.Fatalf("breaker did not bound autonomous turns: %d tool calls", n)
	}
	if n := sess.hangupCalls.Load(); n != 1 {
		t.Fatalf("expected hard cap to tear down once, hangup=%d", n)
	}
	spoken := sess.spoken()
	if len(spoken) != 1 || spoken[0] != defaultRunawayHardSpeak {
		t.Fatalf("expected deterministic runaway message spoken, got %v", spoken)
	}
}

// --- Runaway soft cap alone: with a high hard cap, the runner stops re-prompting
// at the soft cap and waits for a caller event (which resets the counter). ---

func TestRunawaySoftCapWaitsForCaller(t *testing.T) {
	results := make([]*llm.ChatResult, 0, 20)
	// Three autonomous tool turns, then the model would keep going, but the soft
	// cap should stop it before exhausting the script.
	for i := 0; i < 20; i++ {
		results = append(results, &llm.ChatResult{ToolCalls: []llm.ToolCall{toolCall("lookup")}})
	}
	chat := llm.NewScriptedClient(results...)
	exec := &fakeExecutor{}
	sess := newFakeSession()

	r := NewRunner(RunnerConfig{
		Chat:          chat,
		BuildExecutor: buildExec(exec),
		SoftCap:       2,
		HardCap:       100, // far away, so the soft cap is what stops the spin
	})

	// No caller transcripts: after the soft cap the runner parks on events and
	// never tears down on its own, so cancel after it has settled.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_ = r.HandleCall(ctx, sess, testCC())

	if n := exec.callCount(); n > 3 {
		t.Fatalf("soft cap did not stop autonomous re-prompting: %d tool calls", n)
	}
}

// --- callCtx cancellation exits the loop and stops the speech producer without
// a goroutine leak. ---

func TestCancellationStopsLoop(t *testing.T) {
	chat := llm.NewScriptedClient(&llm.ChatResult{Content: "Hello"}) // engages media, no tools
	exec := &fakeExecutor{}
	sess := newFakeSession() // no transcripts: Listen blocks, loop parks on events

	r := NewRunner(RunnerConfig{Chat: chat, BuildExecutor: buildExec(exec)})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.HandleCall(ctx, sess, testCC()) }()

	// Let the first turn run and the loop settle.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("HandleCall returned error on cancel: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("HandleCall did not return after callCtx cancellation (goroutine leak)")
	}
}
