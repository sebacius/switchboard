package llm

import "context"

// ToolHandler executes a tool call. sess is the call session, typed as any to
// avoid an import cycle (the agent layer type-asserts it to its CallSession).
// The returned string is fed back into the conversation as the tool result.
type ToolHandler func(ctx context.Context, args map[string]any, sess any) (string, error)

// AgentTool is a callable tool: its advertised schema plus its handler.
type AgentTool struct {
	Name        string
	Description string
	Parameters  map[string]any // JSON schema for the arguments object
	Handler     ToolHandler
}

// ToolRegistry is an ordered set of tools offered to the model for one call.
type ToolRegistry struct {
	tools map[string]*AgentTool
	order []string
}

// NewToolRegistry creates an empty registry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{tools: make(map[string]*AgentTool)}
}

// Register adds a tool, or replaces one already registered under the same name.
func (r *ToolRegistry) Register(t *AgentTool) {
	if _, exists := r.tools[t.Name]; !exists {
		r.order = append(r.order, t.Name)
	}
	r.tools[t.Name] = t
}

// Get looks up a tool by name.
func (r *ToolRegistry) Get(name string) (*AgentTool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Names returns the registered tool names in registration order.
func (r *ToolRegistry) Names() []string {
	return append([]string(nil), r.order...)
}

// AsToolDefs converts the registry to tool definitions, preserving registration
// order. A tool with no Parameters gets an empty object schema. The shape is the
// one both providers accept — Ollama and OpenAI agree on how a tool is DEFINED,
// and differ only in how a call and its result are expressed.
func (r *ToolRegistry) AsToolDefs() []ToolDef {
	defs := make([]ToolDef, 0, len(r.order))
	for _, name := range r.order {
		t := r.tools[name]
		params := t.Parameters
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		defs = append(defs, ToolDef{
			Type: "function",
			Function: ToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			},
		})
	}
	return defs
}
