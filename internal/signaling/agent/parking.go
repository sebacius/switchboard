package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/sebas/switchboard/internal/signaling/parking"
)

// Park and unpark are the two tools entangled with the answer model: a parked
// call must already own its media (you cannot play hold music down a leg you
// never answered), so both handlers answer first.
//
// The dispatch-loop contract (spec: agent-tools "Park does not block the loop")
// shapes the design: `park` starts music-on-hold and returns DispositionParked
// immediately. The runner's loop — not the handler — is what holds the call
// open, so the supervisor stays responsive to a caller hangup while parked.
//
// Slot release needs no teardown hook here: app.go wires
// parking.Service.CleanupByCallID into the dialog manager's OnTerminated
// callback, so a parked call whose dialog dies frees its slot through the same
// path a non-agent call always did.

// defaultMOHFile is the hold music used when the tenant names none.
const defaultMOHFile = "hold_music.wav"

// ParkingService is the seam the park/unpark tools need from parking.Service.
// Depending on the narrow interface (rather than the concrete type) keeps the
// tools unit-testable with a fake and documents exactly what parking surface
// the agent uses.
type ParkingService interface {
	Park(ctx context.Context, req parking.ParkRequest) (*parking.ParkResult, error)
	Unpark(ctx context.Context, req parking.UnparkRequest) (*parking.UnparkResult, error)
}

// parkTool places the caller in a parking slot with music-on-hold. Its
// disposition is Parked: the handler returns at once and the runner's loop holds
// the call alive until the caller hangs up or someone retrieves the slot.
func parkTool(svc ParkingService, logger *slog.Logger) Tool {
	return Tool{
		Name: "park",
		Description: "Put the caller on hold in a numbered parking slot with hold music, so a colleague " +
			"can pick the call up from another phone. Tell the caller the slot number afterwards.",
		Params: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slot": map[string]any{
					"type":        "string",
					"description": "Optional specific slot number (e.g. '701'). Omit to auto-assign the next free slot.",
				},
			},
		},
		Disposition: DispositionParked,
		Handler: func(ctx context.Context, args map[string]any, sess CallSession) (string, error) {
			if svc == nil {
				return "", fmt.Errorf("parking is not available on this system")
			}

			// A parked call must own its media — hold music plays down our RTP
			// session — so answer before parking if the first turn went straight
			// to park without speaking.
			if err := sess.Answer(ctx); err != nil {
				return "", fmt.Errorf("answer before park: %w", err)
			}

			slot, _ := stringArg(args, "slot")
			result, err := svc.Park(ctx, parking.ParkRequest{
				SlotID:    normalizeSlotID(slot),
				CallID:    sess.CallID(),
				Dialog:    sess.GetDialog(),
				SessionID: sess.GetSessionID(),
				ParkedBy:  "supervisor",
				MOHFiles:  []string{defaultMOHFile},
			})
			if err != nil {
				return "", fmt.Errorf("park failed: %w", err)
			}

			logger.Info("[Agent] Call parked", "call_id", sess.CallID(), "slot", result.SlotID)
			return fmt.Sprintf("call parked in slot %s; the caller is on hold with music", result.SlotID), nil
		},
	}
}

// unparkTool retrieves a parked call and bridges it to this caller. It is
// registered ONLY for internal calls: retrieving a parked call is a directory-
// user action, and an inbound caller who could guess a slot number would
// otherwise be able to pick up someone else's held call.
func unparkTool(svc ParkingService, logger *slog.Logger) Tool {
	return Tool{
		Name: "unpark",
		Description: "Retrieve a call that is parked in a numbered slot and connect it to this caller. " +
			"Use when the caller asks to pick up a parked or held call by slot number.",
		Params: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slot": map[string]any{
					"type":        "string",
					"description": "The parking slot number to retrieve (e.g. '701').",
				},
			},
			"required": []any{"slot"},
		},
		Disposition: DispositionParked,
		Handler: func(ctx context.Context, args map[string]any, sess CallSession) (string, error) {
			if svc == nil {
				return "", fmt.Errorf("parking is not available on this system")
			}

			slot, ok := stringArg(args, "slot")
			slot = normalizeSlotID(slot)
			if !ok || slot == "" {
				return "", fmt.Errorf("unpark requires a 'slot' number; ask the caller which slot to retrieve")
			}
			return RetrieveParked(ctx, svc, slot, sess, logger)
		},
	}
}

// RetrieveParked answers the retriever, takes the parked call out of its slot,
// and bridges the two. It is shared by the unpark tool and by the deterministic
// resolver's *7XX path: a colleague dialing *701 and a colleague asking the
// assistant to pick up slot 701 must do exactly the same thing, and having one
// implementation is what guarantees it.
//
// It does not block: the bridge outlives the call into this function, so the
// teardown watch runs in its own goroutine. Callers that need to stay alive for
// the bridge's lifetime wait on the session's context.
func RetrieveParked(ctx context.Context, svc ParkingService, slot string, sess CallSession, logger *slog.Logger) (string, error) {
	if svc == nil {
		return "", fmt.Errorf("parking is not available on this system")
	}
	slot = normalizeSlotID(slot)
	if slot == "" {
		return "", fmt.Errorf("no parking slot given")
	}

	// The retriever must own media before it can be bridged to the parked party.
	if err := sess.Answer(ctx); err != nil {
		return "", fmt.Errorf("answer before unpark: %w", err)
	}

	result, err := svc.Unpark(ctx, parking.UnparkRequest{SlotID: slot})
	if err != nil {
		return "", fmt.Errorf("no call is parked in slot %s: %w", slot, err)
	}
	parked := result.Slot

	transport := sess.GetTransport()
	if transport == nil {
		return "", fmt.Errorf("media transport unavailable")
	}

	bridgeID, err := transport.BridgeMedia(ctx, parked.SessionID, sess.GetSessionID())
	if err != nil {
		return "", fmt.Errorf("bridge parked call: %w", err)
	}

	logger.Info("[Agent] Unparked and bridged",
		"slot", slot,
		"bridge_id", bridgeID,
		"parked_call_id", parked.CallID,
		"retriever_call_id", sess.CallID(),
	)

	go watchUnparkedBridge(sess, parked, bridgeID, transport, logger)

	return fmt.Sprintf("connected the caller to the call parked in slot %s", slot), nil
}

// watchUnparkedBridge waits for either party to hang up, unbridges the media,
// and terminates the surviving leg. It mirrors the semantics the dialplan's
// unpark action had, moved off the dispatch loop into its own goroutine.
func watchUnparkedBridge(sess CallSession, parked *parking.ParkSlot, bridgeID string, transport interface {
	UnbridgeMedia(ctx context.Context, bridgeID string) error
}, logger *slog.Logger) {
	retrieverCtx := sess.Context()
	parkedCtx := parked.Dialog.Context()

	var parkerHungUp bool
	select {
	case <-retrieverCtx.Done():
	case <-parkedCtx.Done():
		parkerHungUp = true
	}

	// Use a background context: the losing side's context is already cancelled
	// but the unbridge still has to reach the RTP manager.
	if err := transport.UnbridgeMedia(context.Background(), bridgeID); err != nil {
		logger.Warn("[Agent] Failed to unbridge unparked call", "bridge_id", bridgeID, "error", err)
	}

	if parkerHungUp {
		if err := sess.Hangup("parked party hung up"); err != nil {
			logger.Warn("[Agent] Failed to hang up retriever", "call_id", sess.CallID(), "error", err)
		}
		return
	}
	if err := sess.TerminateDialog(parked.CallID, "retriever hung up"); err != nil {
		logger.Warn("[Agent] Failed to hang up parked call", "call_id", parked.CallID, "error", err)
	}
}

// normalizeSlotID strips the dial-prefix asterisks a caller or tenant prompt may
// carry ("*701", "**701" → "701") so the model can echo the dialed string
// verbatim without the slot lookup failing on punctuation.
func normalizeSlotID(slot string) string {
	return strings.TrimLeft(strings.TrimSpace(slot), "*")
}
