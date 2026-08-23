package routing

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

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
	resolution      CallRouter
	operatorFor     func(agent.CallContext) string
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
	resolution CallRouter,
	operatorFor func(agent.CallContext) string,
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
		resolution:      resolution,
		operatorFor:     operatorFor,
		locStore:        locStore,
		callService:     callService,
		trunk:           sipTrunk,
		didRoutes:       didRoutes,
	}
}

// HandleINVITE processes incoming INVITE requests.
//
// This handler deliberately does NOT send a 200 OK: whoever ends up owning the
// call decides that. It performs the deterministic work — ingress authorization,
// direction and tenant resolution, tenant preflight — sets up media, rings the
// caller, and then hands the call to the flow engine:
//
//	engine claims it   → unpark, a one-node dial, or a walk of the graph
//	engine declines it → the tenant operator, or 480 if none is configured
//
// Every branch is decided from configuration checked at startup. Nothing here
// waits on a network call, and the call blocks this goroutine either way — see
// routeCall for why that is required rather than merely convenient.
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

	// Admission, pre-answer and before any media is allocated: we must know this
	// tenant, and it must be under its channel limit. The slot is taken here
	// rather than later because what it bounds is physical — an RTP port, a media
	// session, and a handler goroutine blocked for the life of the call — so a
	// tenant over its limit must be turned away before it consumes any of them.
	admitted := h.admission.Admit(cc)
	if !admitted.Admitted {
		code, reason := admissionStatus(admitted.Reason)
		slog.Warn("Rejecting INVITE at admission",
			"tenant", cc.Tenant, "direction", cc.Direction, "reason", admitted.Reason, "code", int(code))
		_ = tx.Respond(sip.NewResponseFromRequest(req, code, reason, nil))
		return
	}
	// From here the call owns a channel slot, and every exit path must free it.
	defer admitted.Release()

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
	clientAddr, clientPort, offeredCodecs, codecOffers, err := h.extractSDPInfo(req)
	if err != nil {
		slog.Error("Failed to extract SDP info", "error", err)
		notAcceptable := sip.NewResponseFromRequest(req, sip.StatusNotAcceptable, "Not Acceptable - invalid SDP", nil)
		_ = tx.Respond(notAcceptable)
		_ = h.dialogMgr.Terminate(dlg.CallID, dialog.ReasonError)
		return
	}

	// Create media session via transport (this returns the SDP we will answer
	// with — later, and only if a node takes ownership of the media).
	sessionResult, err := h.transport.CreateSession(context.Background(), mediaclient.SessionInfo{
		CallID:        dlg.CallID,
		RemoteAddr:    clientAddr,
		RemotePort:    clientPort,
		OfferedCodecs: offeredCodecs,
		Offered:       codecOffers,
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
	// agent.CallSession.Answer sends it — what the flow reaches first decides
	// whether this call is forwarded or answered. No 183, no 200 OK.
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

	// Ring the caller BEFORE routing. Until some provisional response arrives the
	// caller's INVITE client transaction is still retransmitting against Timer B,
	// and a slow decision was getting the call CANCELled before it was made. A
	// provisional moves that transaction to Proceeding, which buys routing as
	// much time as it needs, and the caller hears real ringback instead of dead
	// air. Routing is fast now that nothing external is consulted, but the
	// transaction still has to be held: a dial can ring for its full timeout.
	//
	// 180 is deliberately not 200: it holds the transaction without answering, so
	// a flow that forwards can still relay the target's own 200, and one that
	// speaks still sends our 200 at that point. Ordinary SIP either way — the
	// phone rings, then somebody picks up.
	if err := h.dialogMgr.SendRinging(dlg); err != nil {
		// Not fatal: the call can still complete, the caller just waits in silence.
		slog.Warn("[Routing] Failed to send 180 Ringing", "call_id", dlg.CallID, "error", err)
	} else {
		session.MarkRinging()
	}

	// Run the call on THIS goroutine, not a new one. sipgo's server calls
	// tx.Terminate() the moment this handler returns (server.go handleRequest:
	// "Must be called to prevent any transaction leaks"), and a terminated
	// transaction silently swallows every later response — ServerTx.Respond spins
	// a finished FSM and returns nil without writing anything. Handing the call to
	// a goroutine and returning therefore made it impossible to ever deliver a
	// 200 OK, a relayed final response, or a late 486: the caller sat on 180 until
	// it gave up. Blocking here is the intended usage — sipgo's transaction layer
	// already dispatches every request on its own goroutine
	// (transaction_layer.go: "go txl.handleRequest(msg)").
	h.routeCall(dlg, session, cc)
}

// CallRouter is whatever decides where a call goes. It returns true when it has
// taken the call and the call is finished.
//
// The contract is the important half: an implementation BLOCKS for the life of
// the call, because sipgo terminates the transaction when this handler returns
// and a terminated transaction silently swallows every later response.
type CallRouter interface {
	Handle(ctx context.Context, sess agent.CallSession, cc *agent.CallContext) bool
}

// operatorDialTimeout bounds the fallback forward to the tenant operator. A
// zero timeout would let Forward apply its own default; naming it here keeps the
// fallback's budget visible at the place the decision is made.
const operatorDialTimeout = 45 * time.Second

// routeCall gives the call to deterministic resolution, and falls back to the
// tenant operator when nothing claims it. It runs on the SIP transaction
// goroutine because every path blocks for the life of the call.
func (h *InviteHandler) routeCall(dlg *dialog.Dialog, session agent.CallSession, cc agent.CallContext) {
	if h.resolution != nil && h.resolution.Handle(dlg.Context(), session, &cc) {
		// Resolution took the call and it is over.
		if !dlg.IsTerminated() {
			slog.Info("[Routing] Resolved call complete, terminating dialog", "call_id", dlg.CallID)
			_ = h.dialogMgr.Terminate(dlg.CallID, dialog.ReasonLocalBYE)
		}
		return
	}

	h.fallbackToOperator(dlg, session, cc)
}

// fallbackToOperator is what happens when the flow engine does not claim a call:
// send the caller to a human if the tenant named one, and otherwise decline
// honestly rather than leaving them on 180 forever.
func (h *InviteHandler) fallbackToOperator(dlg *dialog.Dialog, session agent.CallSession, cc agent.CallContext) {
	operator := ""
	if h.operatorFor != nil {
		operator = h.operatorFor(cc)
	}

	if operator == "" {
		slog.Info("[Routing] Nothing resolved and no operator configured",
			"call_id", dlg.CallID, "tenant", cc.Tenant, "callee", cc.Callee)
		if err := h.dialogMgr.RespondStatus(dlg, sip.StatusTemporarilyUnavailable,
			"Temporarily Unavailable - no destination for this call"); err != nil {
			slog.Warn("[Routing] Failed to decline unresolved call", "call_id", dlg.CallID, "error", err)
		}
		_ = h.dialogMgr.Terminate(dlg.CallID, dialog.ReasonError)
		return
	}

	slog.Info("[Routing] Nothing resolved, forwarding to operator",
		"call_id", dlg.CallID, "tenant", cc.Tenant, "callee", cc.Callee, "operator", operator)

	// Forward relays the operator's own status upstream on failure, which is the
	// honest thing to send a caller we could not place.
	if err := session.Forward(dlg.Context(), operator, operatorDialTimeout); err != nil {
		slog.Info("[Routing] Operator forward ended", "call_id", dlg.CallID, "error", err)
	}

	if !dlg.IsTerminated() {
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

// extractSDPInfo parses SDP to get the client endpoint and the full codec offer.
//
// The rtpmap attributes matter as much as the m-line formats. A bare payload
// type identifies a static codec, but telephone-event is DYNAMIC: real endpoints
// offer it anywhere in 96-127, so discarding a=rtpmap makes the peer's DTMF
// payload type unknowable and RFC 4733 impossible. Assuming 101 works in a lab
// and fails in the field.
func (h *InviteHandler) extractSDPInfo(req *sip.Request) (clientAddr string, clientPort int, codecs []string, offers []mediaclient.CodecOffer, err error) {
	callID := req.CallID()

	if req.Body() == nil {
		return "", 0, nil, nil, fmt.Errorf("no SDP body in INVITE")
	}

	// Parse SDP
	sdpObj := &psdp.SessionDescription{}
	if err := sdpObj.Unmarshal(req.Body()); err != nil {
		return "", 0, nil, nil, fmt.Errorf("failed to parse SDP: %w", err)
	}

	if len(sdpObj.MediaDescriptions) == 0 {
		return "", 0, nil, nil, fmt.Errorf("no media descriptions in SDP")
	}

	// Get first media (audio)
	mediaDesc := sdpObj.MediaDescriptions[0]
	clientPort = mediaDesc.MediaName.Port.Value
	codecs = mediaDesc.MediaName.Formats
	offers = codecOffersFrom(mediaDesc)

	slog.Info("[SDP] Parsed media", "callID", callID, "media", mediaDesc.MediaName.Media,
		"port", clientPort, "codecs", codecs, "offers", len(offers))

	// Get client address from SDP connection information
	if mediaDesc.ConnectionInformation != nil && mediaDesc.ConnectionInformation.Address != nil {
		clientAddr = mediaDesc.ConnectionInformation.Address.Address
	} else if sdpObj.ConnectionInformation != nil && sdpObj.ConnectionInformation.Address != nil {
		clientAddr = sdpObj.ConnectionInformation.Address.Address
	}

	if clientAddr == "" {
		return "", 0, nil, nil, fmt.Errorf("no client address in SDP")
	}

	return clientAddr, clientPort, codecs, offers, nil
}

// codecOffersFrom joins an m-line's formats with the a=rtpmap and a=fmtp
// attributes that describe them. A format with no rtpmap keeps an empty encoding
// name: for a static payload type the number itself defines the codec, so the
// far side needs nothing more.
func codecOffersFrom(media *psdp.MediaDescription) []mediaclient.CodecOffer {
	rtpmaps := map[int]mediaclient.CodecOffer{}
	fmtps := map[int]string{}

	for _, attr := range media.Attributes {
		switch attr.Key {
		case "rtpmap":
			// "101 telephone-event/8000"
			pt, rest, ok := splitAttrPayload(attr.Value)
			if !ok {
				continue
			}
			name, rate := rest, 0
			if slash := strings.Index(rest, "/"); slash >= 0 {
				name = rest[:slash]
				// The clock rate may be followed by "/channels".
				rateStr := rest[slash+1:]
				if s := strings.Index(rateStr, "/"); s >= 0 {
					rateStr = rateStr[:s]
				}
				rate, _ = strconv.Atoi(rateStr)
			}
			rtpmaps[pt] = mediaclient.CodecOffer{PayloadType: pt, EncodingName: name, ClockRate: rate}

		case "fmtp":
			// "101 0-15"
			pt, rest, ok := splitAttrPayload(attr.Value)
			if !ok {
				continue
			}
			fmtps[pt] = rest
		}
	}

	offers := make([]mediaclient.CodecOffer, 0, len(media.MediaName.Formats))
	for _, format := range media.MediaName.Formats {
		pt, err := strconv.Atoi(format)
		if err != nil {
			continue
		}
		offer := rtpmaps[pt]
		offer.PayloadType = pt
		offer.FMTP = fmtps[pt]
		offers = append(offers, offer)
	}
	return offers
}

// splitAttrPayload splits "101 telephone-event/8000" into its payload type and
// the remainder.
func splitAttrPayload(value string) (int, string, bool) {
	space := strings.Index(value, " ")
	if space < 0 {
		return 0, "", false
	}
	pt, err := strconv.Atoi(strings.TrimSpace(value[:space]))
	if err != nil {
		return 0, "", false
	}
	return pt, strings.TrimSpace(value[space+1:]), true
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
