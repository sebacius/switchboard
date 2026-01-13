package dialplan

import (
	"context"
	"encoding/json"
	"fmt"
)

// PlayAudioParams defines parameters for play_audio action.
type PlayAudioParams struct {
	File string `json:"file"`
}

// PlayAudioAction plays an audio file to the caller.
type PlayAudioAction struct {
	params PlayAudioParams
}

// NewPlayAudioAction creates a play_audio action from JSON config.
func NewPlayAudioAction(raw json.RawMessage) (Action, error) {
	var params PlayAudioParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, fmt.Errorf("parse play_audio params: %w", err)
	}
	if params.File == "" {
		return nil, fmt.Errorf("play_audio: file required")
	}
	return &PlayAudioAction{params: params}, nil
}

// Type returns "play_audio".
func (a *PlayAudioAction) Type() string {
	return "play_audio"
}

// Execute plays the audio file and blocks until completion.
func (a *PlayAudioAction) Execute(ctx context.Context, session CallSession) error {
	return session.PlayAudio(ctx, a.params.File)
}
