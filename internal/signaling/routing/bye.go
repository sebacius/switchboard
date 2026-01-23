package routing

import (
	"log/slog"

	"github.com/emiago/sipgo/sip"
	"github.com/sebas/switchboard/internal/signaling/b2bua"
	"github.com/sebas/switchboard/internal/signaling/dialog"
)

// BYEHandler handles incoming BYE requests.
// BYE terminates an established dialog per RFC 3261 Section 15.
type BYEHandler struct {
	dialogMgr   *dialog.Manager
	callService b2bua.CallService
}

// NewBYEHandler creates a new BYE handler.
func NewBYEHandler(dialogMgr *dialog.Manager, callService b2bua.CallService) *BYEHandler {
	return &BYEHandler{
		dialogMgr:   dialogMgr,
		callService: callService,
	}
}

// HandleBYE processes an incoming BYE request.
// First checks if it's for an outbound (B-leg) call via CallService,
// then falls back to inbound (A-leg) dialog handling.
func (h *BYEHandler) HandleBYE(req *sip.Request, tx sip.ServerTransaction) {
	slog.Debug("[BYE] Received BYE request",
		"call_id", req.CallID(),
		"from", req.From(),
		"to", req.To(),
	)

	// First, check if this is a BYE for an outbound (B-leg) call.
	// B-legs are tracked by the CallService/Originator, not the dialog manager.
	if h.callService != nil && h.callService.HandleIncomingBYE(req, tx) {
		slog.Debug("[BYE] Handled as B-leg BYE", "call_id", req.CallID())
		return // Handled by CallService
	}

	slog.Debug("[BYE] Not a B-leg, trying A-leg dialog", "call_id", req.CallID())

	// Otherwise, handle as an inbound (A-leg) dialog
	if err := h.dialogMgr.HandleIncomingBYE(req, tx); err != nil {
		slog.Debug("[BYE] Handling note", "call_id", req.CallID(), "error", err)
	}
}
