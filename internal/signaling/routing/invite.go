package routing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/emiago/sipgo/sip"
	psdp "github.com/pion/sdp/v3"
	"github.com/sebas/switchboard/internal/signaling/b2bua"
	"github.com/sebas/switchboard/internal/signaling/dialplan"
	"github.com/sebas/switchboard/internal/signaling/dialog"
	"github.com/sebas/switchboard/internal/signaling/location"
	"github.com/sebas/switchboard/internal/signaling/transport"
)

// SessionRecorder records session info for the API
type SessionRecorder interface {
	RecordSession(callID, clientAddr string, clientPort int, serverAddr string, serverPort int)
}

// InviteHandler handles incoming INVITE requests
type InviteHandler struct {
	transport       transport.Transport
	advertiseAddr   string
	port            int
	dialogMgr       *dialog.Manager
	sessionRecorder SessionRecorder
	executor        *dialplan.Executor
	locStore        location.LocationStore
	callService     b2bua.CallService
}

// NewInviteHandler creates a new INVITE handler
func NewInviteHandler(
	transport transport.Transport,
	advertiseAddr string,
	port int,
	dialogMgr *dialog.Manager,
	sessionRecorder SessionRecorder,
	executor *dialplan.Executor,
	locStore location.LocationStore,
	callService b2bua.CallService,
) *InviteHandler {
	return &InviteHandler{
		transport:       transport,
		advertiseAddr:   advertiseAddr,
		port:            port,
		dialogMgr:       dialogMgr,
		sessionRecorder: sessionRecorder,
		executor:        executor,
		locStore:        locStore,
		callService:     callService,
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

	// Record session for API visibility
	if h.sessionRecorder != nil {
		h.sessionRecorder.RecordSession(dlg.CallID, clientAddr, clientPort, sessionResult.LocalAddr, sessionResult.LocalPort)
	}

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

	// Extract destination for dialplan matching
	destination := h.extractDestination(req)

	// Execute dialplan
	go h.executeDialplan(dlg, destination)
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

// extractDestination extracts the destination from the To header.
func (h *InviteHandler) extractDestination(req *sip.Request) string {
	to := req.To()
	if to == nil {
		return ""
	}
	// Extract user part from To URI
	user := to.Address.User
	if user == "" {
		// Fallback to host if no user
		return to.Address.Host
	}
	return user
}

// extractCallerID extracts the caller ID (user part) from the From header.
// This is the phone number or extension, e.g., "1001" from "sip:1001@example.com".
func (h *InviteHandler) extractCallerID(req *sip.Request) string {
	from := req.From()
	if from == nil {
		return ""
	}
	return from.Address.User
}

// extractCallerName extracts the caller display name from the From header.
// This is the human-readable name, e.g., "John Doe" from "John Doe" <sip:1001@example.com>.
func (h *InviteHandler) extractCallerName(req *sip.Request) string {
	from := req.From()
	if from == nil {
		return ""
	}
	if from.DisplayName != "" {
		return strings.Trim(from.DisplayName, "\"")
	}
	return ""
}

// executeDialplan runs the dialplan for the call.
func (h *InviteHandler) executeDialplan(dlg *dialog.Dialog, destination string) {
	callerID := ""
	callerName := ""
	if dlg.InviteRequest != nil {
		callerID = h.extractCallerID(dlg.InviteRequest)
		callerName = h.extractCallerName(dlg.InviteRequest)
	}

	// Create call session for dialplan execution
	session := dialplan.NewSession(dialplan.SessionConfig{
		Dialog:      dlg,
		Transport:   h.transport,
		DialogMgr:   h.dialogMgr,
		LocStore:    h.locStore,
		CallService: h.callService,
		Logger:      slog.Default(),
		Destination: destination,
		CallerID:    callerID,
		CallerName:  callerName,
	})

	// Execute dialplan
	err := h.executor.Execute(dlg.Context(), session)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.Error("[Routing] Dialplan execution failed",
				"call_id", dlg.CallID,
				"destination", destination,
				"error", err,
			)
		}
	}

	// Terminate dialog after dialplan completes (if not already terminated)
	if !dlg.IsTerminated() {
		slog.Info("[Routing] Dialplan complete, terminating dialog", "call_id", dlg.CallID)
		h.dialogMgr.Terminate(dlg.CallID, dialog.ReasonLocalBYE)
	}
}
