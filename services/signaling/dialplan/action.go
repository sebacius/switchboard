package dialplan

import (
	"context"
	"encoding/json"
	"fmt"
)

// Action represents a single step in a dialplan route.
type Action interface {
	// Type returns the action type identifier (e.g., "play_audio", "dial")
	Type() string

	// Execute runs the action within a call session.
	// Returns error if action fails; context cancellation stops execution.
	Execute(ctx context.Context, session CallSession) error
}

// ActionFactory creates an Action from raw JSON config.
type ActionFactory func(json.RawMessage) (Action, error)

// ActionRegistry manages action type registrations.
type ActionRegistry struct {
	factories map[string]ActionFactory
}

// NewActionRegistry creates an empty registry.
func NewActionRegistry() *ActionRegistry {
	return &ActionRegistry{
		factories: make(map[string]ActionFactory),
	}
}

// Register adds a factory for the given action type.
// Panics if the type is already registered (fail fast at startup).
func (r *ActionRegistry) Register(actionType string, factory ActionFactory) {
	if _, exists := r.factories[actionType]; exists {
		panic(fmt.Sprintf("action type %q already registered", actionType))
	}
	r.factories[actionType] = factory
}

// Create builds an action from a raw config entry.
func (r *ActionRegistry) Create(actionType string, rawConfig json.RawMessage) (Action, error) {
	factory, ok := r.factories[actionType]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrActionNotFound, actionType)
	}
	return factory(rawConfig)
}

// DefaultRegistry returns a registry with all built-in actions.
func DefaultRegistry() *ActionRegistry {
	r := NewActionRegistry()
	r.Register("play_audio", NewPlayAudioAction)
	r.Register("dial", NewDialAction)
	r.Register("hangup", NewHangupAction)
	return r
}
