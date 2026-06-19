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
	"github.com/sebas/switchboard/internal/signaling/dialog"
	"github.com/sebas/switchboard/internal/signaling/dialplan"
	"github.com/sebas/switchboard/internal/signaling/location"
	"github.com/sebas/switchboard/internal/signaling/mediaclient"
	"github.com/sebas/switchboard/internal/signaling/trunk"
)

// SessionRecorder records session info for the API
type SessionRecorder interface {
	RecordSession(callID, clientAddr string, clientPort int, serverAddr string, serverPort int)
}

// InviteHandler handles incoming INVITE requests
type InviteHandler struct {
	transport       mediaclient.Transport
	advertiseAddr   string
	port            int
	dialogMgr       *dialog.Manager
	sessionRecorder SessionRecorder
	executor        *dialplan.Executor
	locStore        location.LocationStore
	callService     b2bua.CallService
	trunk           trunk.Trunk
	didRoutes       *trunk.DIDRoutes
}

// NewInviteHandler creates a new INVITE handler
func NewInviteHandler(
	transport mediaclient.Transport,
	advertiseAddr string,
	port int,
	dialogMgr *dialog.Manager,
	sessionRecorder SessionRecorder,
	executor *dialplan.Executor,
	locStore location.LocationStore,
	callService b2bua.CallService,
	sipTrunk trunk.Trunk,
	didRoutes *trunk.DIDRoutes,
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
		trunk:           sipTrunk,
		didRoutes:       didRoutes,
	}
}

// HandleINVITE processes incoming INVITE requests
func (h *InviteHandler) HandleINVITE(req *sip.Request, tx sip.ServerTransaction) {
	slog.Info("Received INVITE", "from", req.From(), "to", req.To(), "call_id", req.CallID())

	// Classify the source before doing any work: an INVITE must come from a
	// registered directory user or a configured trunk peer. Unknown sources are
	// rejected (toll-fraud ingress protection); inbound trunk calls for an
	// unmapped DID are declined.
	srcIP, _ := parseSourceAddr(req.Source())
	if !h.classifyAndAuthorize(req, tx, srcIP) {
		return
	}

	// Create dialog via manager
	dlg, err := h.dialogMgr.CreateFromInvite(req, tx)
	if err != nil {
		slog.Error("Failed to create dialog", "error", err)
		return
	}

	// Set the SIP source as initial remote endpoint for display purposes.
	// This ensures the dialog has remote info even if media setup fails.
	// Will be updated with SDP info after media session is created.
	sourceIP, sourcePort := parseSourceAddr(req.Source())
	if sourceIP != "" {
		dlg.SetRemoteEndpoint(sourceIP, sourcePort)
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
		_ = tx.Respond(notAcceptable)
		_ = h.dialogMgr.Terminate(dlg.CallID, dialog.ReasonError)
		return
	}

	// Create media session via transport (this returns SDP)
	sessionResult, err := h.transport.CreateSession(context.Background(), mediaclient.SessionInfo{
		CallID:        dlg.CallID,
		RemoteAddr:    clientAddr,
		RemotePort:    clientPort,
		OfferedCodecs: offeredCodecs,
	})
	if err != nil {
		slog.Error("Failed to create media session", "error", err)
		notAcceptable := sip.NewResponseFromRequest(req, sip.StatusNotAcceptable, "Not Acceptable - "+err.Error(), nil)
		_ = tx.Respond(notAcceptable)
		_ = h.dialogMgr.Terminate(dlg.CallID, dialog.ReasonError)
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
		_ = h.transport.DestroySession(context.Background(), sessionResult.SessionID, mediaclient.TerminateReasonError)
		_ = h.dialogMgr.Terminate(dlg.CallID, dialog.ReasonError)
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

// classifyAndAuthorize gates an incoming INVITE by source. It returns true when
// the call may proceed. Registered directory users and inbound trunk peers are
// allowed; any other source is rejected with 403. For trunk-origin calls the
// dialed DID must map to a tenant, otherwise the call is declined with 603.
func (h *InviteHandler) classifyAndAuthorize(req *sip.Request, tx sip.ServerTransaction, srcIP string) bool {
	// Registered directory user?
	isUser := false
	if from := req.From(); from != nil {
		aor := from.Address.String()
		if h.locStore.Has(aor) || len(h.locStore.LookupByUser(from.Address.User)) > 0 {
			isUser = true
		}
	}

	// Configured inbound trunk peer?
	var peer *trunk.Peer
	isTrunk := false
	if h.trunk != nil {
		peer, isTrunk = h.trunk.MatchInbound(srcIP)
	}

	if !isUser && !isTrunk {
		slog.Warn("Rejecting INVITE from unknown source", "source", srcIP, "from", req.From())
		resp := sip.NewResponseFromRequest(req, sip.StatusForbidden, "Forbidden - unknown source", nil)
		_ = tx.Respond(resp)
		return false
	}

	// Inbound trunk call: resolve DID -> tenant; reject unmapped DIDs (no default).
	if isTrunk && !isUser {
		did := h.extractDestination(req)
		var tenant string
		var ok bool
		if h.didRoutes != nil {
			tenant, ok = h.didRoutes.TenantForDID(did)
		}
		if !ok {
			slog.Warn("Declining inbound trunk call for unmapped DID", "did", did, "peer", peer.Name)
			resp := sip.NewResponseFromRequest(req, sip.StatusGlobalDecline, "Declined - unmapped DID", nil)
			_ = tx.Respond(resp)
			return false
		}
		slog.Info("Inbound trunk call", "peer", peer.Name, "did", did, "tenant", tenant)
	}

	return true
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

// extractDomain extracts the SIP domain from the To header.
func (h *InviteHandler) extractDomain(req *sip.Request) string {
	to := req.To()
	if to == nil {
		return ""
	}
	return to.Address.Host
}

// executeDialplan runs the dialplan for the call.
func (h *InviteHandler) executeDialplan(dlg *dialog.Dialog, destination string) {
	callerID := ""
	callerName := ""
	domain := ""
	if dlg.InviteRequest != nil {
		callerID = h.extractCallerID(dlg.InviteRequest)
		callerName = h.extractCallerName(dlg.InviteRequest)
		domain = h.extractDomain(dlg.InviteRequest)
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
		Domain:      domain,
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
		_ = h.dialogMgr.Terminate(dlg.CallID, dialog.ReasonLocalBYE)
	}
}
