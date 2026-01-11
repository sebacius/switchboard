package routing

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/emiago/sipgo/sip"
	psdp "github.com/pion/sdp/v3"
	"github.com/sebas/switchboard/services/signaling/dialog"
	"github.com/sebas/switchboard/services/signaling/transport"
)

// InviteHandler handles incoming INVITE requests
type InviteHandler struct {
	transport     transport.Transport
	advertiseAddr string
	port          int
	audioFile     string
	dialogMgr     *dialog.Manager
}

// NewInviteHandler creates a new INVITE handler
func NewInviteHandler(
	transport transport.Transport,
	advertiseAddr string,
	port int,
	audioFile string,
	dialogMgr *dialog.Manager,
) *InviteHandler {
	return &InviteHandler{
		transport:     transport,
		advertiseAddr: advertiseAddr,
		port:          port,
		audioFile:     audioFile,
		dialogMgr:     dialogMgr,
	}
}

// HandleINVITE processes incoming INVITE requests
func (h *InviteHandler) HandleINVITE(req *sip.Request, tx sip.ServerTransaction) {
	slog.Info("Received INVITE", "from", req.From(), "to", req.To(), "call_id", req.CallID())

	// Create dialog via manager
	dlg, err := h.dialogMgr.CreateFromInvite(req, tx)
	if err != nil {
		slog.Error("Failed to create dialog", "error", err)
		return
	}

	// Send 100 Trying
	if err := h.dialogMgr.SendTrying(dlg); err != nil {
		slog.Error("Failed to send 100 Trying", "error", err)
		return
	}

	// Extract SDP info from INVITE
	clientAddr, clientPort, offeredCodecs, err := h.extractSDPInfo(req)
	if err != nil {
		slog.Error("Failed to extract SDP info", "error", err)
		notAcceptable := sip.NewResponseFromRequest(req, sip.StatusNotAcceptable, "Not Acceptable - invalid SDP", nil)
		tx.Respond(notAcceptable)
		h.dialogMgr.Terminate(dlg.CallID, dialog.ReasonError)
		return
	}

	// Create media session via transport (this returns SDP)
	sessionResult, err := h.transport.CreateSession(context.Background(), transport.SessionInfo{
		CallID:        dlg.CallID,
		RemoteAddr:    clientAddr,
		RemotePort:    clientPort,
		OfferedCodecs: offeredCodecs,
	})
	if err != nil {
		slog.Error("Failed to create media session", "error", err)
		notAcceptable := sip.NewResponseFromRequest(req, sip.StatusNotAcceptable, "Not Acceptable - "+err.Error(), nil)
		tx.Respond(notAcceptable)
		h.dialogMgr.Terminate(dlg.CallID, dialog.ReasonError)
		return
	}

	// Store session info in dialog
	dlg.SetSessionID(sessionResult.SessionID)
	dlg.SetMediaEndpoint(clientAddr, clientPort, sessionResult.SelectedCodec)

	// Send 183 Session Progress with SDP (early media)
	if err := h.dialogMgr.SendProgress(dlg, sessionResult.SDPBody); err != nil {
		slog.Error("Failed to send 183 Session Progress", "error", err)
	}

	slog.Info("Sent 183 Session Progress", "call_id", dlg.CallID, "session_id", sessionResult.SessionID)

	// Give phone time to process 183
	time.Sleep(500 * time.Millisecond)

	// Send 200 OK (this also creates the sipgo session)
	if err := h.dialogMgr.SendOK(dlg, sessionResult.SDPBody); err != nil {
		slog.Error("Failed to send 200 OK", "error", err)
		h.transport.DestroySession(context.Background(), sessionResult.SessionID, transport.TerminateReasonError)
		h.dialogMgr.Terminate(dlg.CallID, dialog.ReasonError)
		return
	}

	slog.Info("Sent 200 OK", "call_id", dlg.CallID)

	// Start audio streaming
	go h.streamAudio(dlg)
}

// extractSDPInfo parses SDP to get client endpoint and offered codecs
func (h *InviteHandler) extractSDPInfo(req *sip.Request) (clientAddr string, clientPort int, codecs []string, err error) {
	callID := req.CallID()

	if req.Body() == nil {
		return "", 0, nil, fmt.Errorf("no SDP body in INVITE")
	}

	// Parse SDP
	sdpObj := &psdp.SessionDescription{}
	if err := sdpObj.Unmarshal(req.Body()); err != nil {
		return "", 0, nil, fmt.Errorf("failed to parse SDP: %w", err)
	}

	if len(sdpObj.MediaDescriptions) == 0 {
		return "", 0, nil, fmt.Errorf("no media descriptions in SDP")
	}

	// Get first media (audio)
	mediaDesc := sdpObj.MediaDescriptions[0]
	clientPort = mediaDesc.MediaName.Port.Value
	codecs = mediaDesc.MediaName.Formats

	slog.Info("[SDP] Parsed media", "callID", callID, "media", mediaDesc.MediaName.Media, "port", clientPort, "codecs", codecs)

	// Get client address from SDP connection information
	if mediaDesc.ConnectionInformation != nil && mediaDesc.ConnectionInformation.Address != nil {
		clientAddr = mediaDesc.ConnectionInformation.Address.Address
	} else if sdpObj.ConnectionInformation != nil && sdpObj.ConnectionInformation.Address != nil {
		clientAddr = sdpObj.ConnectionInformation.Address.Address
	}

	if clientAddr == "" {
		return "", 0, nil, fmt.Errorf("no client address in SDP")
	}

	return clientAddr, clientPort, codecs, nil
}

// streamAudio starts audio playback and handles completion
func (h *InviteHandler) streamAudio(dlg *dialog.Dialog) {
	sessionID := dlg.GetSessionID()

	playReq := transport.PlayRequest{
		SessionID: sessionID,
		AudioFile: h.audioFile,
		OnComplete: func(sid string) {
			// After playback, terminate dialog (sends BYE)
			slog.Info("[Routing] Playback complete, terminating dialog", "call_id", dlg.CallID, "session_id", sid)
			h.dialogMgr.Terminate(dlg.CallID, dialog.ReasonLocalBYE)
		},
	}

	// Play audio using dialog's context (cancelled on BYE)
	statusCh, err := h.transport.PlayAudio(dlg.Context(), playReq)
	if err != nil {
		slog.Error("[Routing] Failed to start playback", "call_id", dlg.CallID, "error", err)
		return
	}

	// Monitor playback status
	for status := range statusCh {
		switch status.State {
		case transport.PlayStateStarted:
			slog.Debug("[Routing] Playback started", "call_id", dlg.CallID)
		case transport.PlayStateCompleted:
			slog.Info("[Routing] Playback completed", "call_id", dlg.CallID)
		case transport.PlayStateError:
			slog.Error("[Routing] Playback error", "call_id", dlg.CallID, "error", status.Error)
		case transport.PlayStateStopped:
			slog.Info("[Routing] Playback stopped", "call_id", dlg.CallID)
		}
	}
}
