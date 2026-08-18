package llm

import (
	"context"
	"fmt"
	"sync"
)

// ScriptedClient is a ChatClient test double that returns a pre-programmed
// sequence of results, one per ChatNative call. It lets runner tests cover
// every branch without a live model.
type ScriptedClient struct {
	// mu guards idx so a test can read Calls() from its own goroutine while the
	// runner drives turns from the dispatch loop.
	mu      sync.Mutex
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.idx >= len(s.results) {
		return nil, fmt.Errorf("scripted client: no result for call %d", s.idx+1)
	}
	r := s.results[s.idx]
	s.idx++
	return r, nil
}

// Calls reports how many turns the runner has driven so far. Tests use it to
// prove a NEGATIVE — that a Parked disposition stopped the loop re-prompting,
// for instance — which is otherwise invisible.
func (s *ScriptedClient) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.idx
}

// Ready always reports true.
func (s *ScriptedClient) Ready() bool { return true }
