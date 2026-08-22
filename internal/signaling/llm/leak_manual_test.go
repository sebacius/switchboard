//go:build manual

package llm

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveReasoningNeverReachesContent drives a model that has been measured
// folding its chain of thought into content, on whichever provider is named, and
// asserts nothing reasoning-shaped survives into the spoken half.
//
//	LLM_LIVE_SERVER=http://localhost:11434 LLM_LIVE_MODEL=qwen3:4b \
//	  LLM_LIVE_PROVIDER=ollama|openai go test -tags manual -run LiveReasoning ./internal/signaling/llm/ -v
func TestLiveReasoningNeverReachesContent(t *testing.T) {
	server, model := os.Getenv("LLM_LIVE_SERVER"), os.Getenv("LLM_LIVE_MODEL")
	provider := Provider(os.Getenv("LLM_LIVE_PROVIDER"))
	if server == "" || model == "" || provider == "" {
		t.Skip("set LLM_LIVE_SERVER, LLM_LIVE_MODEL and LLM_LIVE_PROVIDER to run this")
	}

	c, err := New(Config{Provider: provider, ServerURL: server, Model: model, Timeout: 5 * time.Minute})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	prompts := []string{
		"A caller says: 'I need to talk to whoever handles overdue invoices, but only if they are in today.' Reply with what you would SAY to the caller.",
		"Think carefully, then greet a caller to Acme Corp in one sentence.",
		"Work out step by step whether 17 times 23 is more than 400, then tell the caller the answer in one short sentence.",
	}

	for i, p := range prompts {
		res, err := c.ChatNative(context.Background(), []NativeMessage{
			{Role: "system", Content: "You are a phone receptionist. Everything you write in content is spoken aloud to the caller."},
			{Role: "user", Content: p},
		}, nil, "")
		if err != nil {
			t.Fatalf("prompt %d: %v", i, err)
		}
		t.Logf("prompt %d\n  SPOKEN:   %q\n  THINKING: %q", i, truncate(res.Content), truncate(res.Thinking))

		for _, marker := range []string{"<think>", "</think>"} {
			if strings.Contains(res.Content, marker) {
				t.Errorf("prompt %d: spoken content carries %s — a caller would hear the scratchpad: %q",
					i, marker, res.Content)
			}
		}
	}
}

func truncate(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 160 {
		return s[:160] + "..."
	}
	return s
}
