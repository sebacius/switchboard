package routing

import (
	"log/slog"

	"github.com/emiago/sipgo/sip"
	"github.com/sebas/switchboard/internal/signaling/dialog"
)

// ACKHandler handles incoming ACK requests.
// ACK confirms a 2xx response to INVITE per RFC 3261 Section 13.2.2.4.
type ACKHandler struct {
	dialogMgr *dialog.Manager
}

// NewACKHandler creates a new ACK handler.
func NewACKHandler(dialogMgr *dialog.Manager) *ACKHandler {
	return &ACKHandler{
		dialogMgr: dialogMgr,
	}
}

// HandleACK processes an incoming ACK request.
// Confirms the dialog and transitions it to the confirmed state.
func (h *ACKHandler) HandleACK(req *sip.Request, tx sip.ServerTransaction) {
	if err := h.dialogMgr.ConfirmWithACK(req, tx); err != nil {
		slog.Debug("[ACK] Handling note", "call_id", req.CallID(), "error", err)
	}
}
