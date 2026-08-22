//go:build manual

package llm

import (
	"context"
	"os"
	"testing"
)

// Drives a real two-turn tool-calling conversation against a live /v1 server.
// Turn 2 replays the assistant's tool_calls plus the tool result, which is the
// exact history a strict server rejects when a tool_call_id is missing or
// unmatched.
// TestLiveTwoTurnToolCallHistory needs a live OpenAI-compatible server. Run it
// with: go test -tags manual -run LiveTwoTurn ./internal/signaling/llm/ -v
//
//	LLM_LIVE_SERVER=http://localhost:11434 LLM_LIVE_MODEL=llama3.1:8b
func TestLiveTwoTurnToolCallHistory(t *testing.T) {
	server, model := os.Getenv("LLM_LIVE_SERVER"), os.Getenv("LLM_LIVE_MODEL")
	if server == "" || model == "" {
		t.Skip("set LLM_LIVE_SERVER and LLM_LIVE_MODEL to run this")
	}
	c, err := New(Config{Provider: ProviderOpenAI, ServerURL: server, Model: model})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	tools := []ToolDef{{Type: "function", Function: ToolFunction{
		Name:        "lookup_extension",
		Description: "Look up the phone extension for a named person",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "person's name"},
			},
			"required": []string{"name"},
		},
	}}}

	conv := []NativeMessage{
		{Role: "system", Content: "You are a receptionist. Use lookup_extension to find extensions."},
		{Role: "user", Content: "What is Dana's extension?"},
	}

	r1, err := c.ChatNative(context.Background(), conv, tools, "")
	if err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	t.Logf("turn 1: content=%q tool_calls=%d", r1.Content, len(r1.ToolCalls))
	if len(r1.ToolCalls) == 0 {
		t.Skip("model emitted no tool call; cannot exercise correlation")
	}
	tc := r1.ToolCalls[0]
	t.Logf("  call: id=%q name=%q args=%v", tc.ID, tc.Function.Name, tc.Function.Arguments)
	if tc.ID == "" {
		t.Fatal("tool call came back with no id")
	}

	// Exactly what the runner builds.
	conv = append(conv, NativeMessage{
		Role: "assistant", Content: r1.Content, Thinking: r1.Thinking, ToolCalls: r1.ToolCalls,
	})
	conv = append(conv, NativeMessage{
		Role: "tool", ToolName: tc.Function.Name, ToolCallID: tc.ID, Content: "extension 105",
	})

	r2, err := c.ChatNative(context.Background(), conv, tools, "")
	if err != nil {
		t.Fatalf("turn 2 (this is the 400 the correlation fix exists to prevent): %v", err)
	}
	t.Logf("turn 2 OK: content=%q", r2.Content)
}
