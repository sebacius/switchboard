package dialplan

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/sebas/switchboard/internal/signaling/parking"
)

// UnparkParams defines parameters for unpark action.
type UnparkParams struct {
	Slot string `json:"slot"` // Slot ID to retrieve from (can include prefix like **701)
}

// UnparkAction retrieves a parked call and bridges it with the current caller.
type UnparkAction struct {
	params      UnparkParams
	parkService *parking.Service
}

// NewUnparkActionFactory returns a factory function for unpark actions.
// The factory captures the ParkService dependency.
func NewUnparkActionFactory(parkService *parking.Service) ActionFactory {
	return func(raw json.RawMessage) (Action, error) {
		var params UnparkParams
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, fmt.Errorf("parse unpark params: %w", err)
		}
		if params.Slot == "" {
			return nil, fmt.Errorf("unpark: slot required")
		}
		return &UnparkAction{params: params, parkService: parkService}, nil
	}
}

// extractSlotID extracts the numeric slot ID from a destination string.
// Handles formats like "**701" -> "701", "*701" -> "701", "701" -> "701"
func extractSlotID(dest string) string {
	// Strip leading asterisks
	slot := strings.TrimLeft(dest, "*")
	return slot
}

// Type returns "unpark".
func (a *UnparkAction) Type() string {
	return "unpark"
}

// Execute retrieves the parked call and bridges it with the current caller.
// This blocks until one party hangs up.
func (a *UnparkAction) Execute(ctx context.Context, session CallSession) error {
	if a.parkService == nil {
		return fmt.Errorf("park service not configured")
	}

	// Extract slot ID (handles prefixes like ** from destination)
	slotID := extractSlotID(a.params.Slot)

	// Unpark the call (stops MOH and removes from registry)
	result, err := a.parkService.Unpark(ctx, parking.UnparkRequest{
		SlotID: slotID,
	})
	if err != nil {
		return fmt.Errorf("unpark call: %w", err)
	}

	parkedSlot := result.Slot

	slog.Info("[Unpark] Retrieved parked call",
		"slot", slotID,
		"parked_call_id", parkedSlot.CallID,
		"parked_session_id", parkedSlot.SessionID,
		"retriever_call_id", session.CallID(),
		"retriever_session_id", session.GetSessionID(),
	)

	// Get transport for bridging
	transport := session.GetTransport()
	if transport == nil {
		return fmt.Errorf("transport not available")
	}

	// Bridge the parked session with the retriever's session
	bridgeID, err := transport.BridgeMedia(ctx, parkedSlot.SessionID, session.GetSessionID())
	if err != nil {
		return fmt.Errorf("bridge parked call: %w", err)
	}

	slog.Info("[Unpark] Calls bridged",
		"bridge_id", bridgeID,
		"parked_session", parkedSlot.SessionID,
		"retriever_session", session.GetSessionID(),
	)

	// Get both dialog contexts to watch for either party hanging up
	retrieverCtx := ctx                      // Retriever's context (passed in)
	parkedCtx := parkedSlot.Dialog.Context() // Parked call's dialog context

	// Block until either party hangs up
	var parkerHungUp bool
	select {
	case <-retrieverCtx.Done():
		// Retriever hung up - need to terminate the parked call
		slog.Info("[Unpark] Retriever hung up, terminating parked call",
			"bridge_id", bridgeID,
			"retriever_call_id", session.CallID(),
			"parked_call_id", parkedSlot.CallID,
		)
	case <-parkedCtx.Done():
		// Parker hung up - need to terminate the retriever
		parkerHungUp = true
		slog.Info("[Unpark] Parker hung up, terminating retriever",
			"bridge_id", bridgeID,
			"parked_call_id", parkedSlot.CallID,
			"retriever_call_id", session.CallID(),
		)
	}

	// Cleanup: unbridge on termination (use background context since original may be canceled)
	if err := transport.UnbridgeMedia(context.Background(), bridgeID); err != nil {
		slog.Warn("[Unpark] Failed to unbridge", "bridge_id", bridgeID, "error", err)
	}

	// Terminate the other party
	if parkerHungUp {
		// Parker hung up, terminate the retriever
		if err := session.Hangup("parked call hung up"); err != nil {
			slog.Warn("[Unpark] Failed to hangup retriever",
				"retriever_call_id", session.CallID(),
				"error", err,
			)
		}
	} else {
		// Retriever hung up, terminate the parked call
		if err := session.TerminateDialog(parkedSlot.CallID, "retriever hung up"); err != nil {
			slog.Warn("[Unpark] Failed to hangup parked call",
				"parked_call_id", parkedSlot.CallID,
				"error", err,
			)
		}
	}

	return nil
}
