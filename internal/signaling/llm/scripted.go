package llm

import (
	"context"
	"fmt"
)

// ScriptedClient is a ChatClient test double that returns a pre-programmed
// sequence of results, one per ChatNative call. It lets runner tests cover
// every branch without a live model.
type ScriptedClient struct {
	results []*ChatResult
	idx     int
}

// NewScriptedClient builds a scripted client over the given results, returned in
// order across successive ChatNative calls.
func NewScriptedClient(results ...*ChatResult) *ScriptedClient {
	return &ScriptedClient{results: results}
}

// ChatNative returns the next scripted result, or an error once exhausted.
func (s *ScriptedClient) ChatNative(_ context.Context, _ []NativeMessage, _ []ToolDef, _ string) (*ChatResult, error) {
	if s.idx >= len(s.results) {
		return nil, fmt.Errorf("scripted client: no result for call %d", s.idx+1)
	}
	r := s.results[s.idx]
	s.idx++
	return r, nil
}

// Ready always reports true.
func (s *ScriptedClient) Ready() bool { return true }
