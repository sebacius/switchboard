package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/sebas/switchboard/internal/signaling/llm"
)

// budgetRecorder is a ChatClient that records the deadline it was handed on each
// turn, so a test can prove which budget applied without waiting for one.
type budgetRecorder struct {
	mu        sync.Mutex
	deadlines []time.Duration
	results   []*llm.ChatResult
}

func (b *budgetRecorder) ChatNative(ctx context.Context, _ []llm.NativeMessage, _ []llm.ToolDef, _ string) (*llm.ChatResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if dl, ok := ctx.Deadline(); ok {
		// Round to the nearest 100ms: what matters is which budget was chosen, not
		// the microseconds of scheduling between the deadline being set and read.
		b.deadlines = append(b.deadlines, time.Until(dl).Round(100*time.Millisecond))
	} else {
		b.deadlines = append(b.deadlines, 0)
	}

	i := len(b.deadlines) - 1
	if i < len(b.results) {
		return b.results[i], nil
	}
	return &llm.ChatResult{Content: "..."}, nil
}

func (b *budgetRecorder) Ready() bool { return true }

func (b *budgetRecorder) seen() []time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]time.Duration(nil), b.deadlines...)
}

// The first turn and a mid-call turn are bounded by different things: the first
// runs while the caller hears ringback and may have to load a multi-gigabyte
// model, while a mid-call turn is a silence with an open mic. A single shared
// deadline forces one of those two to be wrong — which is what produced a live
// call failing at exactly 30s on a model that was merely cold.
func TestFirstTurnGetsALargerBudgetThanMidCallTurns(t *testing.T) {
	chat := &budgetRecorder{results: []*llm.ChatResult{
		{Content: "Thanks for calling Acme."}, // first turn: answers and converses
		// The reactive turn hangs up, so the call ends as soon as both budgets
		// have been observed rather than idling until the harness deadline.
		{ToolCalls: []llm.ToolCall{{Function: llm.ToolCallFunction{Name: "hangup"}}}},
	}}

	runner := NewRunner(RunnerConfig{
		Chat:    chat,
		Logger:  quietLogger(),
		Prompts: StaticPrompts{"acme": "You are the Acme receptionist."},
		// Scaled down from the real 30s/90s so they sit well inside the harness
		// deadline below; the turn ctx is a child of it, and the shorter of the two
		// is what a turn actually sees.
		TurnTimeout:      400 * time.Millisecond,
		FirstTurnTimeout: 1200 * time.Millisecond,
		BuildExecutor: func(cc CallContext) ToolExecutor {
			p := NewPolicy(cc.Tenant, TenantPolicy{}, quietLogger())
			return NewCallExecutor(BuildRegistry(cc, p, RegistryDeps{Logger: quietLogger()}), p, "")
		},
	})

	sess := newFakeSession()
	sess.queueTranscript("I need billing")

	if err := runInboundWithTimeout(t, runner, sess, 10*time.Second); err != nil {
		t.Fatalf("HandleCall: %v", err)
	}

	seen := chat.seen()
	if len(seen) < 2 {
		t.Fatalf("expected at least a first and a reactive turn, got %d", len(seen))
	}
	if seen[0] != 1200*time.Millisecond {
		t.Fatalf("first turn budget = %s, want 1.2s", seen[0])
	}
	if seen[1] != 400*time.Millisecond {
		t.Fatalf("mid-call turn budget = %s, want 400ms", seen[1])
	}
}

// Both budgets have defaults, and the first-turn one is the larger. A zero value
// must not silently mean "no deadline".
func TestTurnBudgetDefaults(t *testing.T) {
	chat := &budgetRecorder{}
	runner := NewRunner(RunnerConfig{
		Chat:    chat,
		Logger:  quietLogger(),
		Prompts: StaticPrompts{"acme": "prompt"},
		BuildExecutor: func(cc CallContext) ToolExecutor {
			p := NewPolicy(cc.Tenant, TenantPolicy{}, quietLogger())
			return NewCallExecutor(BuildRegistry(cc, p, RegistryDeps{Logger: quietLogger()}), p, "")
		},
	})

	if runner.cfg.TurnTimeout != defaultTurnTimeout {
		t.Fatalf("TurnTimeout default = %s, want %s", runner.cfg.TurnTimeout, defaultTurnTimeout)
	}
	if runner.cfg.FirstTurnTimeout != defaultFirstTurnTimeout {
		t.Fatalf("FirstTurnTimeout default = %s, want %s", runner.cfg.FirstTurnTimeout, defaultFirstTurnTimeout)
	}
	if runner.cfg.FirstTurnTimeout <= runner.cfg.TurnTimeout {
		t.Fatal("the first turn must get at least as much room as a mid-call turn")
	}
}
