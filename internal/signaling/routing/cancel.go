package routing

import (
	"log/slog"

	"github.com/emiago/sipgo/sip"
	"github.com/sebas/switchboard/internal/signaling/dialog"
)

// CANCELHandler handles incoming CANCEL requests.
// CANCEL terminates a pending INVITE transaction per RFC 3261 Section 9.
type CANCELHandler struct {
	dialogMgr *dialog.Manager
}

// NewCANCELHandler creates a new CANCEL handler.
func NewCANCELHandler(dialogMgr *dialog.Manager) *CANCELHandler {
	return &CANCELHandler{
		dialogMgr: dialogMgr,
	}
}

// HandleCANCEL processes an incoming CANCEL request.
// Cancels the pending INVITE transaction and terminates the dialog.
func (h *CANCELHandler) HandleCANCEL(req *sip.Request, tx sip.ServerTransaction) {
	if err := h.dialogMgr.HandleIncomingCANCEL(req, tx); err != nil {
		slog.Debug("[CANCEL] Handling note", "call_id", req.CallID(), "error", err)
	}
}
