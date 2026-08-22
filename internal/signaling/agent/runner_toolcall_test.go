package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sebas/switchboard/internal/signaling/llm"
)

// convRecorder is a ChatClient that snapshots the conversation it was handed on
// every turn. The conversation is otherwise unobservable — it lives inside
// callRun — and it is the thing under test here, because a malformed history is
// rejected by the provider on the turn AFTER the one that malformed it.
type convRecorder struct {
	mu      sync.Mutex
	seen    [][]llm.NativeMessage
	results []*llm.ChatResult
}

func (r *convRecorder) ChatNative(_ context.Context, msgs []llm.NativeMessage, _ []llm.ToolDef, _ string) (*llm.ChatResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, append([]llm.NativeMessage(nil), msgs...))

	i := len(r.seen) - 1
	if i < len(r.results) {
		return r.results[i], nil
	}
	return &llm.ChatResult{Content: "..."}, nil
}

func (r *convRecorder) Ready() bool { return true }

// turn returns the conversation as it was on turn n (1-based), or nil.
func (r *convRecorder) turn(n int) []llm.NativeMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n < 1 || n > len(r.seen) {
		return nil
	}
	return r.seen[n-1]
}

func (r *convRecorder) turns() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.seen)
}

// toolResults picks the role=tool messages out of a conversation.
func toolResults(msgs []llm.NativeMessage) []llm.NativeMessage {
	var out []llm.NativeMessage
	for _, m := range msgs {
		if m.Role == "tool" {
			out = append(out, m)
		}
	}
	return out
}

func callWithID(name, id string) llm.ToolCall {
	return llm.ToolCall{ID: id, Function: llm.ToolCallFunction{Name: name}}
}

// A tool result has to carry BOTH correlation forms: Ollama matches a result to
// its call by tool name, OpenAI by the call id it issued. Dropping either one
// breaks that provider and only that provider, which is exactly the kind of bug
// that ships.
func TestToolResultCarriesNameAndCallID(t *testing.T) {
	chat := &convRecorder{results: []*llm.ChatResult{
		{ToolCalls: []llm.ToolCall{callWithID("dial", "call_abc")}},
		// End the call so the test does not idle to its deadline.
		{ToolCalls: []llm.ToolCall{callWithID("hangup", "call_end")}},
	}}
	exec := &fakeExecutor{resp: []execResp{
		{result: "ok", disp: DispositionContinue},
		{result: "bye", disp: DispositionTerminal},
	}}
	sess := newFakeSession()
	r := NewRunner(RunnerConfig{Chat: chat, BuildExecutor: buildExec(exec)})

	if err := runWithTimeout(t, r, sess, 2*time.Second); err != nil {
		t.Fatalf("HandleCall: %v", err)
	}

	// Turn 2 is the autonomous re-prompt: the first turn's tool result is in it.
	results := toolResults(chat.turn(2))
	if len(results) != 1 {
		t.Fatalf("expected 1 tool result in the replayed conversation, got %d", len(results))
	}
	if results[0].ToolName != "dial" {
		t.Errorf("ToolName = %q, want %q (Ollama correlates by name)", results[0].ToolName, "dial")
	}
	if results[0].ToolCallID != "call_abc" {
		t.Errorf("ToolCallID = %q, want %q (OpenAI correlates by id)", results[0].ToolCallID, "call_abc")
	}
}

// Parking returns early from the dispatch of a tool batch, so any call after the
// one that parked was advertised to the model and never answered. Ollama accepts
// that history; an OpenAI-compatible server rejects it outright — and because
// the whole conversation is replayed every turn, the rejection lands on the
// unpark, not on the turn that created the gap.
func TestParkedTurnAnswersTheCallsItDidNotRun(t *testing.T) {
	chat := &convRecorder{results: []*llm.ChatResult{
		{ToolCalls: []llm.ToolCall{
			callWithID("park", "call_1"),
			callWithID("dial", "call_2"), // never runs: park returns first
		}},
	}}
	exec := &fakeExecutor{resp: []execResp{{result: "parked", disp: DispositionParked}}}
	sess := newFakeSession()
	r := NewRunner(RunnerConfig{Chat: chat, BuildExecutor: buildExec(exec)})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.HandleCall(ctx, sess, inboundCC()) }()

	// Caller speech after the park is what an unpark looks like to the loop: it
	// drives the next turn, which replays the history the park left behind.
	waitFor(t, func() bool { return chat.turns() >= 1 && exec.callCount() == 1 })
	sess.queueTranscript("are you still there?")
	waitFor(t, func() bool { return chat.turns() >= 2 })

	if exec.callCount() != 1 {
		t.Fatalf("dial must not have run after the park; executor saw %d calls", exec.callCount())
	}

	results := toolResults(chat.turn(2))
	if len(results) != 2 {
		t.Fatalf("every advertised tool call needs a result; got %d for 2 calls", len(results))
	}
	byID := map[string]llm.NativeMessage{}
	for _, m := range results {
		byID[m.ToolCallID] = m
	}
	if _, ok := byID["call_1"]; !ok {
		t.Error("the executed park has no result in the replayed conversation")
	}
	unrun, ok := byID["call_2"]
	if !ok {
		t.Fatal("the un-run dial has no result: this history is a 400 on an OpenAI provider")
	}
	if !strings.Contains(unrun.Content, "not executed") {
		t.Errorf("un-run tool result = %q, want it to say it did not run", unrun.Content)
	}
	if unrun.ToolName != "dial" {
		t.Errorf("un-run result ToolName = %q, want %q", unrun.ToolName, "dial")
	}

	cancel()
	<-done
}

// The same gap opens when a turn runs out of budget part-way through its tool
// batch. Unlike parking, this one leaves the call alive and conversing, so the
// malformed history is replayed on every subsequent turn.
func TestCancelledTurnAnswersTheCallsItDidNotRun(t *testing.T) {
	chat := &convRecorder{results: []*llm.ChatResult{
		{Content: "Thanks for calling Acme."}, // turn 1: answer and converse
		{ToolCalls: []llm.ToolCall{
			callWithID("lookup", "call_1"),
			callWithID("dial", "call_2"), // budget expires before this one
		}},
	}}
	// The first tool outlives the turn budget, so the loop sees a canceled
	// context before it reaches the second call.
	exec := &fakeExecutor{resp: []execResp{{result: "slow", disp: DispositionContinue}}}
	exec.delay = 250 * time.Millisecond
	sess := newFakeSession()
	r := NewRunner(RunnerConfig{
		Chat:          chat,
		BuildExecutor: buildExec(exec),
		TurnTimeout:   120 * time.Millisecond,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.HandleCall(ctx, sess, inboundCC()) }()

	waitFor(t, func() bool { return chat.turns() >= 1 })
	sess.queueTranscript("put me through to sales")
	waitFor(t, func() bool { return chat.turns() >= 2 && exec.callCount() == 1 })
	sess.queueTranscript("hello?")
	waitFor(t, func() bool { return chat.turns() >= 3 })

	results := toolResults(chat.turn(3))
	if len(results) != 2 {
		t.Fatalf("a canceled turn still owes a result per advertised call; got %d of 2", len(results))
	}
	var unrun *llm.NativeMessage
	for i := range results {
		if results[i].ToolCallID == "call_2" {
			unrun = &results[i]
		}
	}
	if unrun == nil {
		t.Fatal("the un-run dial has no result after a canceled turn")
	}
	if !strings.Contains(unrun.Content, "not executed") {
		t.Errorf("un-run tool result = %q, want it to say it did not run", unrun.Content)
	}

	cancel()
	<-done
}

// waitFor polls until cond holds, failing the test rather than hanging the suite.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the runner to reach the expected state")
}
