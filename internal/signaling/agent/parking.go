package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/sebas/switchboard/internal/signaling/parking"
)

// Retrieval is entangled with the answer model: a retrieved call must own its
// media before it can be bridged, so RetrieveParked answers first.
//
// Slot release needs no teardown hook here: app.go wires
// parking.Service.CleanupByCallID into the dialog manager's OnTerminated
// callback, so a parked call whose dialog dies frees its slot through the same
// path a non-agent call always did.

// ParkingService is the seam call retrieval needs from parking.Service.
// Depending on the narrow interface (rather than the concrete type) keeps
// retrieval unit-testable with a fake and documents exactly what parking surface
// the agent uses.
type ParkingService interface {
	Park(ctx context.Context, req parking.ParkRequest) (*parking.ParkResult, error)
	Unpark(ctx context.Context, req parking.UnparkRequest) (*parking.UnparkResult, error)
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

	// Use a background context: the losing side's context is already canceled
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
