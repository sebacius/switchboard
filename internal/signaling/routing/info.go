package routing

import (
	"log/slog"
	"strings"

	"github.com/emiago/sipgo/sip"
	"github.com/sebas/switchboard/internal/signaling/dialog"
)

// SIP INFO is the out-of-band way to send DTMF, and it exists because RFC 4733
// needs the peer to have offered telephone-event and plenty of endpoints —
// older PBXs, some carriers, a few softphones — simply do not. Supporting it is
// about sixty lines and costs nothing at runtime, which is a good trade against
// a menu that silently cannot hear a caller.
//
// Two body formats are in the wild:
//
//	application/dtmf-relay:  "Signal=1\r\nDuration=250"
//	application/dtmf:        "1"
//
// Both are accepted. The digit is delivered to the same buffer the media path
// feeds, so a flow node cannot tell — and should not care — which transport
// carried it.

// DTMFSink receives a digit pressed during a call.
type DTMFSink interface {
	// PushDigit delivers one digit for the given Call-ID.
	PushDigit(callID string, digit rune)
}

// INFOHandler answers SIP INFO requests carrying DTMF.
type INFOHandler struct {
	dialogMgr *dialog.Manager
	sink      DTMFSink
}

// NewINFOHandler builds the handler.
func NewINFOHandler(dialogMgr *dialog.Manager, sink DTMFSink) *INFOHandler {
	return &INFOHandler{dialogMgr: dialogMgr, sink: sink}
}

// HandleINFO processes an in-dialog INFO request.
func (h *INFOHandler) HandleINFO(req *sip.Request, tx sip.ServerTransaction) {
	callID := ""
	if id := req.CallID(); id != nil {
		callID = id.Value()
	}

	digit, ok := parseINFODTMF(req)
	if !ok {
		// An INFO we do not understand is still a well-formed request; 200 is
		// friendlier than an error the far end will retry.
		slog.Debug("[INFO] Ignoring INFO with no recognised DTMF body", "call_id", callID)
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil))
		return
	}

	slog.Debug("[INFO] DTMF digit received out of band", "call_id", callID, "digit", string(digit))
	if h.sink != nil && callID != "" {
		h.sink.PushDigit(callID, digit)
	}

	_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil))
}

// parseINFODTMF extracts a digit from an INFO body in either common format.
func parseINFODTMF(req *sip.Request) (rune, bool) {
	body := strings.TrimSpace(string(req.Body()))
	if body == "" {
		return 0, false
	}

	// application/dtmf-relay: key=value lines, one of which is Signal.
	if strings.Contains(strings.ToLower(body), "signal") {
		for _, line := range strings.Split(body, "\n") {
			key, value, found := strings.Cut(line, "=")
			if !found || !strings.EqualFold(strings.TrimSpace(key), "signal") {
				continue
			}
			return firstDTMFRune(strings.TrimSpace(value))
		}
		return 0, false
	}

	// application/dtmf: the digit alone.
	return firstDTMFRune(body)
}

// firstDTMFRune returns the digit a Signal value names.
//
// The two-character forms are checked FIRST. Some endpoints send 10 and 11 for
// '*' and '#', and reading a leading character would turn both into '1' — a
// caller pressing star would silently dial one instead.
func firstDTMFRune(s string) (rune, bool) {
	switch strings.TrimSpace(s) {
	case "10":
		return '*', true
	case "11":
		return '#', true
	}

	for _, r := range strings.TrimSpace(s) {
		if strings.ContainsRune("0123456789*#ABCD", r) {
			return r, true
		}
		break
	}
	return 0, false
}
