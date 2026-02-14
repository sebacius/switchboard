package dialplan

import (
	"context"
	"encoding/json"
	"fmt"
)

// TTSParams defines parameters for tts action.
type TTSParams struct {
	Text  string `json:"text"`
	Voice string `json:"voice"`
}

// TTSAction plays synthesized speech to the caller.
type TTSAction struct {
	params TTSParams
}

// NewTTSAction creates a tts action from JSON config.
func NewTTSAction(raw json.RawMessage) (Action, error) {
	var params TTSParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("parse tts params: %w", err)
	}
	if params.Text == "" {
		return nil, fmt.Errorf("tts: text required")
	}
	// Default voice if not specified
	if params.Voice == "" {
		params.Voice = "alloy"
	}
	return &TTSAction{params: params}, nil
}

// Type returns "tts".
func (a *TTSAction) Type() string {
	return "tts"
}

// Execute synthesizes and plays the text.
func (a *TTSAction) Execute(ctx context.Context, session CallSession) error {
	return session.PlayTTS(ctx, a.params.Text, a.params.Voice)
}
