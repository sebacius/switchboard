package dialplan

import (
	"context"
	"encoding/json"
	"fmt"
)

// HangupParams defines parameters for hangup action.
type HangupParams struct {
	Reason string `json:"reason"` // Optional reason for logging
}

// HangupAction terminates the call.
type HangupAction struct {
	params HangupParams
}

// NewHangupAction creates a hangup action from JSON config.
func NewHangupAction(raw json.RawMessage) (Action, error) {
	var params HangupParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, fmt.Errorf("parse hangup params: %w", err)
		}
	}
	if params.Reason == "" {
		params.Reason = "dialplan_hangup"
	}
	return &HangupAction{params: params}, nil
}

// Type returns "hangup".
func (a *HangupAction) Type() string {
	return "hangup"
}

// Execute terminates the call.
func (a *HangupAction) Execute(ctx context.Context, session CallSession) error {
	return session.Hangup(a.params.Reason)
}
