package llm

import (
	"context"
	"time"
)

// ChatClient is the agent-facing LLM interface: one tool-calling turn. It is the
// ONLY thing the agent runner knows about the LLM, which is what lets a provider
// be added without the runner learning which one is in use. ScriptedClient
// substitutes in tests.
type ChatClient interface {
	ChatNative(ctx context.Context, messages []NativeMessage, tools []ToolDef, model string) (*ChatResult, error)
	Ready() bool
}

// Prober is the startup-probe surface, kept separate from ChatClient because the
// runner has no business knowing how a provider is health-checked.
type Prober interface {
	ServerURL() string
	ListModels(ctx context.Context) ([]string, error)
	Warm(ctx context.Context, model string) (time.Duration, error)
	ProbeProfile() ProbeProfile
}

// Client is a fully wired provider: a chat turn, a probe surface, and its own
// identity. llm.New returns one of these.
type Client interface {
	ChatClient
	Prober
	Provider() Provider
}

// ProbeProfile tells ProbeAndWarm how to interpret what it finds, so the probe
// stays one function rather than a switch over providers — and so it never logs
// advice that makes no sense for the provider in use.
type ProbeProfile struct {
	// ModelListAuthoritative reports whether absence from the model listing
	// means every call WILL fail. True for Ollama, which cannot run a model it
	// has not pulled. False for OpenAI-compatible servers, where listings are
	// partial and gateway-dependent, so absence proves nothing.
	ModelListAuthoritative bool

	// Warmable reports whether a throwaway request meaningfully preloads the
	// model. True for Ollama; false for a hosted API, where it buys nothing and
	// may be billed.
	Warmable bool

	// MissingModelHint is the operator instruction logged when the configured
	// model is not in the listing.
	MissingModelHint string

	// ProbeTimeout bounds the listing call. A loopback call and a WAN TLS
	// handshake do not deserve the same budget.
	ProbeTimeout time.Duration
}

// NativeMessage is one message in the supervisor's conversation. It is the
// provider-agnostic representation: the Ollama client serializes it directly
// (the json tags are Ollama's own /api/chat shape), and the OpenAI client
// translates it, because /v1 rejects properties it does not recognize.
//
// Fields only one provider uses are omitempty, so a provider never sees another
// provider's vocabulary. ToolName and ToolCallID are the same fact — which call
// a result answers — expressed the two different ways the two APIs demand.
type NativeMessage struct {
	Role       string     `json:"role"` // system | user | assistant | tool
	Content    string     `json:"content,omitempty"`
	Thinking   string     `json:"thinking,omitempty"`
	ToolName   string     `json:"tool_name,omitempty"`    // for role=tool (Ollama)
	ToolCallID string     `json:"tool_call_id,omitempty"` // for role=tool (OpenAI)
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`   // for role=assistant
}

// ToolCall is a tool invocation the model emitted.
type ToolCall struct {
	// ID correlates a result back to this call. OpenAI requires it and returns
	// one; Ollama has no such concept and returns none, so on that path it stays
	// empty and is omitted from the wire.
	ID       string           `json:"id,omitempty"`
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
