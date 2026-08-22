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

	// Answer-model state (design #7). answered records the 200 OK; forwarded
	// and dialed record which SIP path the dial tool took.
	answeredVal atomic.Bool
	answerCalls atomic.Int32
	rangEarly   atomic.Bool
	forwarded   []string
	dialed      []string

	// forwardErr / dialErr make the outbound leg fail, for the relay path.
	forwardErr error
	dialErr    error

	// forwardBlocks makes Forward hang until the call context is canceled,
	// modeling a target that rings and rings while the caller may CANCEL.
	forwardBlocks bool
	// forwardStarted closes once Forward has been entered, so a test can
	// deterministically CANCEL mid-forward.
	forwardStarted chan struct{}

	// Ring-group state. groupRounds records what ForwardGroup was asked to ring,
	// in order, which is how the strategy tests assert ring order. groupErr is
	// what it returns — set it to ErrGroupNoAnswer to exercise a group nobody
	// picks up.
	groupRounds [][]string
	groupErr    error
}

func newFakeSession() *fakeSession {
	return &fakeSession{
		callID:         "test-call",
		listenQ:        make(chan string, 8),
		forwardStarted: make(chan struct{}),
	}
}

func (f *fakeSession) queueTranscript(t string) { f.listenQ <- t }

func (f *fakeSession) spoken() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ttsSpoken...)
}

func (f *fakeSession) CallID() string           { return f.callID }
func (f *fakeSession) Destination() string      { return "1000" }
func (f *fakeSession) CallerID() string         { return "2000" }
func (f *fakeSession) Domain() string           { return "example.test" }
func (f *fakeSession) Context() context.Context { return context.Background() }

func (f *fakeSession) PlayAudio(ctx context.Context, file string) error {
	return f.Answer(ctx)
}

// Answer records the 200 OK. It is idempotent, like the real session, so the
// runner calling it before every utterance costs nothing after the first.
func (f *fakeSession) Answer(context.Context) error {
	f.answerCalls.Add(1)
	f.answeredVal.Store(true)
	return nil
}

func (f *fakeSession) HasAnswered() bool { return f.answeredVal.Load() }

// MarkRinging records the early 180 the INVITE handler sends before the first
// turn, so a later Forward knows not to send a second one.
func (f *fakeSession) MarkRinging() { f.rangEarly.Store(true) }

// Forward is the pre-answer routing path. It never answers: that is the whole
// invariant under test for a silent internal route.
func (f *fakeSession) Forward(ctx context.Context, target string, _ time.Duration) error {
	f.mu.Lock()
	f.forwarded = append(f.forwarded, target)
	blocks := f.forwardBlocks
	err := f.forwardErr
	f.mu.Unlock()

	if f.forwardStarted != nil {
		select {
		case <-f.forwardStarted:
		default:
			close(f.forwardStarted)
		}
	}

	if err != nil {
		return err
	}
	if blocks {
		// The target is ringing. Real Forward blocks here until the B-leg
		// answers or the call goes away.
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

// ForwardGroup is the pre-answer ring-group path. It records the rounds it was
// given and answers (or not) according to groupErr, so a test can drive both the
// "someone picked up" and "nobody picked up" branches without a media stack.
func (f *fakeSession) ForwardGroup(_ context.Context, rounds [][]string, _ time.Duration) error {
	f.mu.Lock()
	f.groupRounds = append(f.groupRounds, rounds...)
	err := f.groupErr
	f.mu.Unlock()

	if err != nil {
		return err
	}
	// A member answered: a real ForwardGroup relays the 200 upstream.
	f.answeredVal.Store(true)
	f.answerCalls.Add(1)
	return nil
}

// rungRounds returns the rounds ForwardGroup was asked to ring, in order.
func (f *fakeSession) rungRounds() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]string, 0, len(f.groupRounds))
	for _, r := range f.groupRounds {
		out = append(out, append([]string(nil), r...))
	}
	return out
}

func (f *fakeSession) forwards() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.forwarded...)
}

func (f *fakeSession) dials() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.dialed...)
}

func (f *fakeSession) PlayTTS(ctx context.Context, text, _ string) error {
	if err := f.Answer(ctx); err != nil {
		return err
	}
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

func (f *fakeSession) Dial(_ context.Context, target string, _ time.Duration) error {
	f.mu.Lock()
	f.dialed = append(f.dialed, target)
	err := f.dialErr
	f.mu.Unlock()
	return err
}

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

	// delay makes a tool outlive its turn budget, which is how a test reaches
	// the loop's cancellation path with the batch only partly dispatched.
	delay time.Duration
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
	if e.delay > 0 {
		time.Sleep(e.delay)
	}
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

// inboundCC is the context for tests that model a CONVERSATION. Direction
// matters to the runner now: an internal first turn that returns prose is
// re-prompted once for a silent route (design #11), because a colleague dialing
// an extension must not be greeted. A test that wants a greeting is, by
// definition, describing an inbound call.
func inboundCC() CallContext {
	return CallContext{Caller: "+15551234567", Callee: "5558001200", Direction: DirectionInbound, Tenant: "acme"}
}

// runInboundWithTimeout is runWithTimeout for conversational scenarios.
func runInboundWithTimeout(t *testing.T, r *Runner, sess CallSession, d time.Duration) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return r.HandleCall(ctx, sess, inboundCC())
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
	if err := runInboundWithTimeout(t, r, sess, 2*time.Second); err != nil {
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

	// A teardown hook stands in for the admission channel slot: if the funnel
	// ran twice it would be released twice and the tenant would over-count
	// capacity for the rest of the process's life.
	var hookRuns atomic.Int32
	run := &callRun{
		cfg:        r.cfg,
		log:        r.log,
		session:    sess,
		callCtx:    ctx,
		callCancel: cancel,
		events:     make(chan Event, 1),
		hooks:      []TeardownHook{func(string) { hookRuns.Add(1) }},
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
	if n := hookRuns.Load(); n != 1 {
		t.Fatalf("expected the teardown hook to run once, ran %d times", n)
	}
	if ctx.Err() == nil {
		t.Fatal("expected callCtx canceled after teardown")
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
	go func() { done <- r.HandleCall(ctx, sess, inboundCC()) }()

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
