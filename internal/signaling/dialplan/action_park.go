package dialplan

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sebas/switchboard/internal/signaling/parking"
)

// ParkParams defines parameters for park action.
type ParkParams struct {
	Slot     string   `json:"slot"`      // Specific slot ID (empty for auto-assign)
	MOHFiles []string `json:"moh_files"` // Audio files for music-on-hold
}

// ParkAction parks the current call in a parking slot with MOH.
type ParkAction struct {
	params      ParkParams
	parkService *parking.Service
}

// NewParkActionFactory returns a factory function for park actions.
// The factory captures the ParkService dependency.
func NewParkActionFactory(parkService *parking.Service) ActionFactory {
	return func(raw json.RawMessage) (Action, error) {
		var params ParkParams
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, fmt.Errorf("parse park params: %w", err)
		}
		// Default MOH file if none specified
		if len(params.MOHFiles) == 0 {
			params.MOHFiles = []string{"hold_music.wav"}
		}
		return &ParkAction{params: params, parkService: parkService}, nil
	}
}

// Type returns "park".
func (a *ParkAction) Type() string {
	return "park"
}

// Execute parks the call and starts MOH playback.
// This blocks indefinitely until the call is unparked or caller hangs up.
func (a *ParkAction) Execute(ctx context.Context, session CallSession) error {
	if a.parkService == nil {
		return fmt.Errorf("park service not configured")
	}

	// Build park request
	req := parking.ParkRequest{
		SlotID:    a.params.Slot,
		CallID:    session.CallID(),
		Dialog:    session.GetDialog(),
		SessionID: session.GetSessionID(),
		ParkedBy:  "dialplan",
		MOHFiles:  a.params.MOHFiles,
	}

	// Park the call (starts MOH in background)
	result, err := a.parkService.Park(ctx, req)
	if err != nil {
		return fmt.Errorf("park call: %w", err)
	}

	_ = result // result.SlotID could be logged or used

	// Block until context is canceled (call terminated or unparked).
	// When unparked, the context is NOT canceled - the unpark action
	// will handle bridging. When caller hangs up, context IS canceled.
	<-ctx.Done()

	return nil
}
