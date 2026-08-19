package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// ChatClient is the agent-facing LLM interface: one native tool-calling turn.
// The concrete *Client talks to Ollama; ScriptedClient substitutes in tests.
type ChatClient interface {
	ChatNative(ctx context.Context, messages []NativeMessage, tools []ToolDef, model string) (*ChatResult, error)
	Ready() bool
}

// NativeMessage is a message in Ollama's native /api/chat format. Unlike the
// OpenAI-compatible Message it can carry assistant tool calls and tool results.
type NativeMessage struct {
	Role      string     `json:"role"` // system | user | assistant | tool
	Content   string     `json:"content,omitempty"`
	Thinking  string     `json:"thinking,omitempty"`
	ToolName  string     `json:"tool_name,omitempty"`  // for role=tool
	ToolCalls []ToolCall `json:"tool_calls,omitempty"` // for role=assistant
}

// ToolCall is a tool invocation the model emitted.
type ToolCall struct {
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction is the function name and parsed arguments of a tool call.
type ToolCallFunction struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// ToolDef advertises a callable tool to the model (Ollama function-tool shape).
type ToolDef struct {
	Type     string       `json:"type"` // always "function"
	Function ToolFunction `json:"function"`
}

// ToolFunction is the description and JSON-schema parameters of a ToolDef.
type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// ChatResult is the outcome of one native turn. Reasoning, if any, is kept in
// Thinking and never mixed into Content (so it is never spoken).
type ChatResult struct {
	Content   string
	Thinking  string
	ToolCalls []ToolCall
}

// nativeChatRequest is the /api/chat request body.
type nativeChatRequest struct {
	Model    string          `json:"model"`
	Messages []NativeMessage `json:"messages"`
	Tools    []ToolDef       `json:"tools,omitempty"`
	Stream   bool            `json:"stream"`
	Think    bool            `json:"think"`
	// KeepAlive tells Ollama how long to hold the model in memory after this
	// request. Omitted, Ollama unloads after 5 idle minutes — and a PBX is idle
	// most of the night, so the first call every morning pays the full multi-GB
	// load inside the caller's turn budget. Sending it is what makes a warmed
	// model stay warm. Format is Ollama's: "30m", or "-1" for indefinitely.
	KeepAlive string `json:"keep_alive,omitempty"`
}

// nativeChatResponse is the /api/chat response body (non-streaming).
type nativeChatResponse struct {
	Message NativeMessage `json:"message"`
}

// ChatNative performs one tool-calling turn against Ollama's native /api/chat
// endpoint with thinking disabled. thinking/content/tool_calls come back as
// separate fields, so reasoning never leaks into the spoken content.
func (c *Client) ChatNative(ctx context.Context, messages []NativeMessage, tools []ToolDef, model string) (*ChatResult, error) {
	if c.serverURL == "" {
		return nil, fmt.Errorf("LLM server not configured")
	}
	if model == "" {
		model = c.model
	}

	reqBody := nativeChatRequest{
		Model:     model,
		Messages:  messages,
		Tools:     tools,
		Stream:    false,
		Think:     false,
		KeepAlive: c.keepAlive,
	}
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := c.serverURL + "/api/chat"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("LLM request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("LLM server error (status %d): %s", resp.StatusCode, string(body))
	}

	var nr nativeChatResponse
	if err := json.Unmarshal(body, &nr); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	return &ChatResult{
		Content:   nr.Message.Content,
		Thinking:  nr.Message.Thinking,
		ToolCalls: nr.Message.ToolCalls,
	}, nil
}
