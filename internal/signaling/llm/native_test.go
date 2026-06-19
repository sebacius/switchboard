package llm

import (
	"context"
	"encoding/json"
	"testing"
)

func TestClientImplementsChatClient(t *testing.T) {
	var _ ChatClient = (*Client)(nil)
	var _ ChatClient = (*ScriptedClient)(nil)
}

func TestNativeRequestMarshalsThinkFalseAndTools(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register(&AgentTool{
		Name:        "dial",
		Description: "Dial a target",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"target": map[string]any{"type": "string"}},
			"required":   []any{"target"},
		},
	})

	data, err := json.Marshal(nativeChatRequest{
		Model:    "qwen3:8b",
		Messages: []NativeMessage{{Role: "user", Content: "hi"}},
		Tools:    reg.AsOllamaTools(),
		Stream:   false,
		Think:    false,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if think, ok := m["think"].(bool); !ok || think {
		t.Fatalf("think should be present and false, got %v", m["think"])
	}
	if stream, ok := m["stream"].(bool); !ok || stream {
		t.Fatalf("stream should be present and false, got %v", m["stream"])
	}
	tools, ok := m["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %v", m["tools"])
	}
}

func TestAsOllamaToolsShape(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register(&AgentTool{Name: "hangup", Description: "End the call"})
	defs := reg.AsOllamaTools()
	if len(defs) != 1 {
		t.Fatalf("expected 1 def, got %d", len(defs))
	}
	if defs[0].Type != "function" || defs[0].Function.Name != "hangup" {
		t.Fatalf("unexpected def: %+v", defs[0])
	}
	// nil Parameters should default to an object schema.
	if defs[0].Function.Parameters["type"] != "object" {
		t.Fatalf("expected default object schema, got %v", defs[0].Function.Parameters)
	}
}

func TestToolRegistryOrderAndLookup(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register(&AgentTool{Name: "dial"})
	reg.Register(&AgentTool{Name: "hangup"})
	reg.Register(&AgentTool{Name: "dial", Description: "replaced"}) // replace keeps order

	names := reg.Names()
	if len(names) != 2 || names[0] != "dial" || names[1] != "hangup" {
		t.Fatalf("unexpected order: %v", names)
	}
	if d, ok := reg.Get("dial"); !ok || d.Description != "replaced" {
		t.Fatalf("expected replaced dial, got %+v ok=%v", d, ok)
	}
	if _, ok := reg.Get("missing"); ok {
		t.Fatal("unexpected lookup hit for missing tool")
	}
}

func TestScriptedClientSequence(t *testing.T) {
	sc := NewScriptedClient(
		&ChatResult{ToolCalls: []ToolCall{{Function: ToolCallFunction{Name: "dial", Arguments: map[string]any{"target": "user/105"}}}}},
		&ChatResult{Content: "Goodbye"},
	)

	r1, err := sc.ChatNative(context.Background(), nil, nil, "")
	if err != nil || len(r1.ToolCalls) != 1 || r1.ToolCalls[0].Function.Name != "dial" {
		t.Fatalf("turn1 unexpected: %+v err=%v", r1, err)
	}
	if got := r1.ToolCalls[0].Function.Arguments["target"]; got != "user/105" {
		t.Fatalf("turn1 arg target = %v, want user/105", got)
	}
	r2, err := sc.ChatNative(context.Background(), nil, nil, "")
	if err != nil || r2.Content != "Goodbye" {
		t.Fatalf("turn2 unexpected: %+v err=%v", r2, err)
	}
	if _, err := sc.ChatNative(context.Background(), nil, nil, ""); err == nil {
		t.Fatal("expected error after exhausting scripted results")
	}
}
