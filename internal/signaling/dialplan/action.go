package dialplan

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sebas/switchboard/internal/signaling/llm"
	"github.com/sebas/switchboard/internal/signaling/parking"
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
	r.Register("tts", NewTTSAction)
	r.Register("dial", NewDialAction)
	r.Register("hangup", NewHangupAction)
	return r
}

// RegisterParkingActions registers the park and unpark actions with a ParkService.
// Call this after DefaultRegistry() to add parking support.
func RegisterParkingActions(r *ActionRegistry, parkService interface{}) {
	// Type assertion to avoid import cycle
	if ps, ok := parkService.(*parking.Service); ok {
		r.Register("park", NewParkActionFactory(ps))
		r.Register("unpark", NewUnparkActionFactory(ps))
	}
}

// RegisterAIAgentAction registers the ai_agent action with an LLM client and optional park service.
// settingsPath points to the directory containing settings.md (loaded once and cached).
// Call this after DefaultRegistry() to add AI voice assistant support.
// Returns the factory so it can be used for settings reload at runtime.
func RegisterAIAgentAction(r *ActionRegistry, llmClient *llm.Client, parkService *parking.Service, settingsPath string) *AIAgentFactory {
	factory := NewAIAgentActionFactory(llmClient, parkService, settingsPath)
	r.Register("ai_agent", factory.Create)
	return factory
}
