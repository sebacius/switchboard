package routing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/emiago/sipgo/sip"
	psdp "github.com/pion/sdp/v3"
	"github.com/sebas/switchboard/internal/signaling/agent"
	"github.com/sebas/switchboard/internal/signaling/b2bua"
	"github.com/sebas/switchboard/internal/signaling/dialog"
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
	router          *agent.Router
	admission       *agent.Admission
	runner          *agent.Runner
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
	callRouter *agent.Router,
	admission *agent.Admission,
	runner *agent.Runner,
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
		router:          callRouter,
		admission:       admission,
		runner:          runner,
		locStore:        locStore,
		callService:     callService,
		trunk:           sipTrunk,
		didRoutes:       didRoutes,
	}
}

// HandleINVITE processes incoming INVITE requests.
//
// The supervisor owns the answer decision (design #7), so this handler
// deliberately does NOT send a 200 OK. It performs only the deterministic work
// that must happen before any LLM round-trip — ingress authorization, direction
// and tenant resolution, admission — sets up media, and then hands the call to
// the agent runner, which decides between forwarding the INVITE (relaying real
// ringback and the target's own final response) and answering to own the media.
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

	// Resolve direction + tenant deterministically. There is no default tenant:
	// a call we cannot attribute is rejected rather than supervised by a guess.
	cc, ok := h.router.Route(agent.RouteInput{
		Caller:   h.extractCallerID(req),
		FromAOR:  fromAOR(req),
		FromHost: fromHost(req),
		Callee:   h.extractDestination(req),
		SourceIP: srcIP,
	})
	if !ok {
		slog.Warn("Rejecting INVITE: unresolved direction or tenant", "from", req.From(), "to", req.To())
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusNotFound, "Not Found - no tenant for this call", nil))
		return
	}

	// Admission runs pre-answer and without any LLM call: an unloaded tenant is a
	// hard reject, and a tenant at its channel limit gets 486 Busy. The limit is
	// what keeps the first-turn LLM call from queueing past SIP Timer B under
	// load — calls fail fast instead of dying in a transaction timeout.
	decision := h.admission.Admit(cc)
	if !decision.Admitted {
		code, reason := admissionStatus(decision.Reason)
		slog.Warn("Rejecting INVITE at admission",
			"tenant", cc.Tenant, "direction", cc.Direction, "reason", decision.Reason, "code", int(code))
		_ = tx.Respond(sip.NewResponseFromRequest(req, code, reason, nil))
		return
	}

	// From here the channel slot is held. Every failure path before the runner
	// takes ownership must release it, or the tenant leaks capacity.
	admitted := false
	defer func() {
		if !admitted {
			decision.Release()
		}
	}()

	// Create dialog via manager
	dlg, err := h.dialogMgr.CreateFromInvite(req, tx)
	if err != nil {
		slog.Error("Failed to create dialog", "error", err)
		return
	}

	// Set the SIP source as initial remote endpoint for display purposes.
	// This ensures the dialog has remote info even if media setup fails.
	// Will be updated with SDP info after media session is created.
	if srcIP != "" {
		_, sourcePort := parseSourceAddr(req.Source())
		dlg.SetRemoteEndpoint(srcIP, sourcePort)
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

	// Create media session via transport (this returns the SDP we will answer
	// with — later, and only if the supervisor decides to own the media).
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

	// The answer SDP is handed to the session and held until
	// agent.CallSession.Answer sends it — the supervisor's first turn still
	// decides whether this call is forwarded or answered. No 183, no 200 OK.
	session := agent.NewSession(agent.SessionConfig{
		Dialog:      dlg,
		Transport:   h.transport,
		DialogMgr:   h.dialogMgr,
		LocStore:    h.locStore,
		CallService: h.callService,
		Logger:      slog.Default(),
		Destination: cc.Callee,
		CallerID:    cc.Caller,
		CallerName:  h.extractCallerName(req),
		Domain:      h.extractDomain(req),
		SDPBody:     sessionResult.SDPBody,
	})

	// Ring the caller BEFORE the first LLM turn. The turn can take tens of
	// seconds on modest hardware, and until some provisional response arrives the
	// caller's INVITE client transaction is still retransmitting against Timer B
	// — so the call was being CANCELled before the supervisor ever decided
	// anything. A provisional moves that transaction to Proceeding, which buys
	// the decision as much time as it needs, and the caller hears real ringback
	// instead of dead air.
	//
	// 180 is deliberately not 200: it holds the transaction without answering, so
	// a first turn that chooses to forward can still relay the target's own 200,
	// and one that chooses to speak still sends our 200 at that point. Ordinary
	// SIP either way — the phone rings, then somebody picks up.
	if err := h.dialogMgr.SendRinging(dlg); err != nil {
		// Not fatal: the call can still complete, the caller just waits in silence.
		slog.Warn("[Routing] Failed to send 180 Ringing", "call_id", dlg.CallID, "error", err)
	} else {
		session.MarkRinging()
	}

	admitted = true
	go h.superviseCall(dlg, session, cc, decision.Release)
}

// superviseCall runs the agent runner for one call and guarantees the dialog is
// torn down afterwards. The admission slot is released through the runner's
// teardown funnel, so it is freed exactly once no matter which initiator ends
// the call (caller BYE, the hangup tool, or a timeout).
func (h *InviteHandler) superviseCall(dlg *dialog.Dialog, session agent.CallSession, cc agent.CallContext, release func()) {
	releaseHook := func(reason string) {
		slog.Debug("[Routing] Releasing admission slot",
			"call_id", dlg.CallID, "tenant", cc.Tenant, "reason", reason)
		release()
	}

	if err := h.runner.HandleCall(dlg.Context(), session, cc, releaseHook); err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.Error("[Routing] Supervisor failed",
				"call_id", dlg.CallID,
				"tenant", cc.Tenant,
				"direction", cc.Direction,
				"error", err,
			)
		}
	}

	// Belt and braces: the runner's teardown funnel already hung the session up,
	// but a supervisor that returned before answering (or an early error) can
	// leave the dialog alive.
	if !dlg.IsTerminated() {
		slog.Info("[Routing] Supervisor complete, terminating dialog", "call_id", dlg.CallID)
		_ = h.dialogMgr.Terminate(dlg.CallID, dialog.ReasonLocalBYE)
	}
}

// admissionStatus maps an admission rejection reason onto a SIP status. A
// tenant at its channel limit is a capacity condition (486 Busy Here, which
// carriers and phones retry sensibly); anything else — an unloaded tenant above
// all — is 404, because we genuinely have no service for that call.
func admissionStatus(reason string) (sip.StatusCode, string) {
	if strings.Contains(reason, "channel limit") {
		return sip.StatusBusyHere, "Busy Here - tenant at channel limit"
	}
	return sip.StatusNotFound, "Not Found - " + reason
}

// fromAOR renders the From header's address as an AOR for an exact registration
// lookup ("sip:102@acme.switchboard.com").
func fromAOR(req *sip.Request) string {
	from := req.From()
	if from == nil {
		return ""
	}
	return from.Address.String()
}

// fromHost returns the From URI host, whose leftmost label selects the tenant
// for internal and outbound calls.
func fromHost(req *sip.Request) string {
	from := req.From()
	if from == nil {
		return ""
	}
	return from.Address.Host
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
