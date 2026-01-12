package b2bua

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/google/uuid"
	psdp "github.com/pion/sdp/v3"
	"github.com/sebas/switchboard/services/signaling/transport"
)

// OriginatorConfig holds originator configuration.
type OriginatorConfig struct {
	AdvertiseAddr string
	Port          int
	Transport     transport.Transport
	Client        *sipgo.Client
	LocalContact  string
}

// OriginateRequest contains parameters for an outbound call.
type OriginateRequest struct {
	// Target resolution result
	Target *LookupResult

	// A-leg correlation
	ALegCallID string
	ALegID     string

	// Caller ID
	CallerID   string
	CallerName string

	// Options
	Timeout    time.Duration
	EarlyMedia bool
	Codecs     []string // Offered codecs (e.g., ["0", "8"] for PCMU, PCMA)
}

// OriginateResult contains the outcome of an originate attempt.
type OriginateResult struct {
	Success   bool
	Leg       Leg
	SIPCode   int
	SIPReason string
	Error     error
}

// Originator handles outbound call initiation.
type Originator struct {
	cfg   OriginatorConfig
	mu    sync.RWMutex
	legs  map[string]*legImpl // Indexed by B-leg Call-ID
	aToB  map[string]string   // A-leg Call-ID -> B-leg Call-ID mapping
}

// NewOriginator creates a new Originator.
func NewOriginator(cfg OriginatorConfig) *Originator {
	return &Originator{
		cfg:  cfg,
		legs: make(map[string]*legImpl),
		aToB: make(map[string]string),
	}
}

// Originate initiates an outbound call.
// This is the main entry point called from dialplan's Dial action.
func (o *Originator) Originate(ctx context.Context, req OriginateRequest) (*OriginateResult, error) {
	if req.Target == nil || !req.Target.HasContacts() {
		return &OriginateResult{
			Success:   false,
			SIPCode:   404,
			SIPReason: "Not Found",
			Error:     ErrNoContacts,
		}, nil
	}

	// Get primary contact
	contact := req.Target.PrimaryContact()

	// Generate unique Call-ID for B leg
	bLegCallID := generateCallID()
	localTag := generateTag()

	// Create B leg
	leg, err := NewOutboundLeg(bLegCallID, contact.URI)
	if err != nil {
		return nil, fmt.Errorf("create outbound leg: %w", err)
	}
	bleg := leg.(*legImpl)

	// Store B leg - will be cleaned up when the leg terminates
	o.mu.Lock()
	o.legs[bLegCallID] = bleg
	o.aToB[req.ALegCallID] = bLegCallID
	o.mu.Unlock()

	// Register cleanup when leg terminates
	bleg.OnTerminated(func(cause TerminationCause) {
		o.mu.Lock()
		delete(o.legs, bLegCallID)
		delete(o.aToB, req.ALegCallID)
		o.mu.Unlock()
		slog.Debug("[Originator] B-leg cleaned up",
			"call_id", bLegCallID,
			"cause", cause.String(),
		)
	})

	// Step 1: Create media session for B leg (pending remote - we don't know callee's RTP endpoint yet)
	codecs := req.Codecs
	if len(codecs) == 0 {
		codecs = []string{"0"} // Default to PCMU
	}

	sessionResult, err := o.cfg.Transport.CreateSessionPendingRemote(ctx, bLegCallID, codecs)
	if err != nil {
		return &OriginateResult{
			Success:   false,
			SIPCode:   500,
			SIPReason: "Media allocation failed",
			Error:     err,
		}, nil
	}

	bleg.SetSessionID(sessionResult.SessionID)
	bleg.SetMediaEndpoint(sessionResult.LocalAddr, sessionResult.LocalPort, sessionResult.SelectedCodec)

	// Step 2: Build and send INVITE
	inviteReq, err := o.buildINVITE(bleg, contact.URI, localTag, req, sessionResult.SDPBody)
	if err != nil {
		o.destroyMediaSession(ctx, bleg)
		return &OriginateResult{
			Success:   false,
			SIPCode:   500,
			SIPReason: "Failed to build INVITE",
			Error:     err,
		}, nil
	}

	// Step 3: Send INVITE and handle response flow
	result := o.executeINVITE(ctx, bleg, inviteReq, localTag, req.Timeout)

	// Cleanup media on failure
	if !result.Success {
		o.destroyMediaSession(ctx, bleg)
	}

	result.Leg = bleg
	return result, nil
}

// buildINVITE constructs the outbound INVITE request.
func (o *Originator) buildINVITE(bleg *legImpl, targetURI, localTag string, req OriginateRequest, sdpBody []byte) (*sip.Request, error) {
	// Parse target URI
	var requestURI sip.Uri
	if err := sip.ParseUri(targetURI, &requestURI); err != nil {
		return nil, fmt.Errorf("invalid target URI: %w", err)
	}

	invite := sip.NewRequest(sip.INVITE, requestURI)

	// Max-Forwards (RFC 3261 Section 8.1.1.6)
	maxFwd := sip.MaxForwardsHeader(70)
	invite.AppendHeader(&maxFwd)

	// From header - our identity with tag
	fromURI := sip.Uri{
		Scheme: "sip",
		User:   req.CallerID,
		Host:   o.cfg.AdvertiseAddr,
		Port:   o.cfg.Port,
	}
	fromParams := sip.NewParams()
	fromParams.Add("tag", localTag)
	fromHdr := &sip.FromHeader{
		DisplayName: req.CallerName,
		Address:     fromURI,
		Params:      fromParams,
	}
	invite.AppendHeader(fromHdr)

	// To header - their identity (no tag yet)
	var toURI sip.Uri
	sip.ParseUri(targetURI, &toURI)
	toHdr := &sip.ToHeader{
		Address: toURI,
		Params:  sip.NewParams(),
	}
	invite.AppendHeader(toHdr)

	// Call-ID header
	callIDHdr := sip.CallIDHeader(bleg.callID)
	invite.AppendHeader(&callIDHdr)

	// CSeq header
	cseqHdr := &sip.CSeqHeader{
		SeqNo:      1,
		MethodName: sip.INVITE,
	}
	invite.AppendHeader(cseqHdr)

	// Contact header
	contactURI := sip.Uri{
		Scheme: "sip",
		User:   "switchboard",
		Host:   o.cfg.AdvertiseAddr,
		Port:   o.cfg.Port,
	}
	contactHdr := &sip.ContactHeader{
		Address: contactURI,
	}
	invite.AppendHeader(contactHdr)

	// Content-Type for SDP
	contentType := sip.ContentTypeHeader("application/sdp")
	invite.AppendHeader(&contentType)

	// SDP body
	invite.SetBody(sdpBody)

	return invite, nil
}

// executeINVITE sends the INVITE and handles the complete response flow.
func (o *Originator) executeINVITE(ctx context.Context, bleg *legImpl, invite *sip.Request, localTag string, timeout time.Duration) *OriginateResult {
	// Transition to Ringing state (we're about to send INVITE)
	bleg.TransitionTo(LegStateCreated)

	// Create timeout context
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Send INVITE via sipgo client transaction
	tx, err := o.cfg.Client.TransactionRequest(dialCtx, invite)
	if err != nil {
		bleg.TransitionTo(LegStateFailed)
		bleg.SetSIPResponse(503, "Transaction failed")
		return &OriginateResult{
			Success:   false,
			SIPCode:   503,
			SIPReason: "Transaction failed",
			Error:     err,
		}
	}

	slog.Info("[Originate] INVITE sent",
		"bleg_call_id", bleg.callID,
		"target", invite.Recipient.String(),
	)

	// Response handling loop
	for {
		select {
		case <-dialCtx.Done():
			// Timeout or cancellation
			if ctx.Err() != nil {
				// Parent context canceled (A leg hung up)
				o.sendCANCEL(bleg, invite, tx)
				bleg.TransitionTo(LegStateFailed)
				bleg.SetTerminationCause(TerminationCauseCancel)
				return &OriginateResult{
					Success:   false,
					SIPCode:   487,
					SIPReason: "Request Terminated",
					Error:     ctx.Err(),
				}
			}
			// Dial timeout
			o.sendCANCEL(bleg, invite, tx)
			bleg.TransitionTo(LegStateFailed)
			bleg.SetTerminationCause(TerminationCauseTimeout)
			return &OriginateResult{
				Success:   false,
				SIPCode:   408,
				SIPReason: "Request Timeout",
				Error:     context.DeadlineExceeded,
			}

		case resp := <-tx.Responses():
			if resp == nil {
				// Transaction ended without response
				bleg.TransitionTo(LegStateFailed)
				return &OriginateResult{
					Success:   false,
					SIPCode:   408,
					SIPReason: "No Response",
					Error:     fmt.Errorf("no response received"),
				}
			}

			result := o.handleResponse(ctx, bleg, resp, invite, tx)
			if result != nil {
				return result
			}
			// Continue waiting for final response

		case <-tx.Done():
			// Transaction completed
			state := bleg.GetState()
			if state == LegStateAnswered {
				return &OriginateResult{
					Success: true,
					SIPCode: 200,
				}
			}
			// Unexpected termination
			if bleg.sipCode != 0 {
				return &OriginateResult{
					Success:   false,
					SIPCode:   bleg.sipCode,
					SIPReason: bleg.sipReason,
				}
			}
			return &OriginateResult{
				Success:   false,
				SIPCode:   500,
				SIPReason: "Transaction terminated unexpectedly",
			}
		}
	}
}

// handleResponse processes a SIP response.
// Returns nil to continue waiting, or a Result to stop.
func (o *Originator) handleResponse(ctx context.Context, bleg *legImpl, resp *sip.Response, invite *sip.Request, tx sip.ClientTransaction) *OriginateResult {
	statusCode := int(resp.StatusCode)

	slog.Debug("[Originate] Response received",
		"bleg_call_id", bleg.callID,
		"status", statusCode,
		"reason", resp.Reason,
	)

	switch {
	case statusCode == 100:
		// 100 Trying - log only per RFC 3261 Section 17.1.1.2
		slog.Debug("[Originate] 100 Trying", "bleg_call_id", bleg.callID)
		return nil

	case statusCode == 180 || statusCode == 181:
		// 180 Ringing / 181 Call Being Forwarded
		bleg.TransitionTo(LegStateRinging)
		slog.Info("[Originate] Ringing", "bleg_call_id", bleg.callID)
		return nil

	case statusCode == 183:
		// 183 Session Progress - early media
		bleg.TransitionTo(LegStateEarlyMedia)

		// Extract SDP for early media
		if resp.Body() != nil {
			if err := o.extractRemoteMedia(ctx, bleg, resp); err != nil {
				slog.Warn("[Originate] Early media setup failed",
					"bleg_call_id", bleg.callID,
					"error", err,
				)
			}
		}
		slog.Info("[Originate] Early media", "bleg_call_id", bleg.callID)
		return nil

	case statusCode >= 200 && statusCode < 300:
		// 2xx Success - answer
		return o.handle2xx(ctx, bleg, resp, invite, tx)

	case statusCode >= 300 && statusCode < 400:
		// 3xx Redirect - treat as failure for now
		bleg.TransitionTo(LegStateFailed)
		bleg.SetSIPResponse(statusCode, resp.Reason)
		bleg.SetTerminationCause(TerminationCauseRejected)
		return &OriginateResult{
			Success:   false,
			SIPCode:   statusCode,
			SIPReason: resp.Reason,
		}

	case statusCode >= 400:
		// 4xx, 5xx, 6xx - failure
		return o.handleFailure(bleg, resp)
	}

	return nil
}

// handle2xx processes a successful response.
func (o *Originator) handle2xx(ctx context.Context, bleg *legImpl, resp *sip.Response, invite *sip.Request, tx sip.ClientTransaction) *OriginateResult {
	bleg.SetSIPResponse(int(resp.StatusCode), resp.Reason)

	// Extract SDP answer and update RTP manager with remote endpoint
	if resp.Body() != nil {
		if err := o.extractRemoteMedia(ctx, bleg, resp); err != nil {
			slog.Error("[Originate] Failed to extract remote media",
				"bleg_call_id", bleg.callID,
				"error", err,
			)
		}
	}

	// Send ACK per RFC 3261 Section 13.2.2.4
	if err := o.sendACK(bleg, resp, invite, tx); err != nil {
		slog.Error("[Originate] Failed to send ACK",
			"bleg_call_id", bleg.callID,
			"error", err,
		)
		// Still mark as answered - ACK failure doesn't negate the 200 OK
	}

	bleg.TransitionTo(LegStateAnswered)

	slog.Info("[Originate] Call answered",
		"bleg_call_id", bleg.callID,
		"remote_addr", bleg.remoteRTPAddr,
		"remote_port", bleg.remoteRTPPort,
	)

	return &OriginateResult{
		Success: true,
		SIPCode: int(resp.StatusCode),
	}
}

// handleFailure processes a failure response.
func (o *Originator) handleFailure(bleg *legImpl, resp *sip.Response) *OriginateResult {
	bleg.SetSIPResponse(int(resp.StatusCode), resp.Reason)
	bleg.TransitionTo(LegStateFailed)
	bleg.SetTerminationCause(TerminationCauseRejected)

	slog.Info("[Originate] Call rejected",
		"bleg_call_id", bleg.callID,
		"status", resp.StatusCode,
		"reason", resp.Reason,
	)

	return &OriginateResult{
		Success:   false,
		SIPCode:   int(resp.StatusCode),
		SIPReason: resp.Reason,
	}
}

// sendACK sends an ACK for a 2xx response.
// Per RFC 3261 Section 13.2.2.4, ACK for 2xx is a new request (not part of INVITE transaction).
// The Request-URI MUST be set from the Contact header of the 2xx response.
// Per RFC 3261 Section 17.1.1.3, ACK for 2xx is NOT a transaction - it's sent directly via transport.
func (o *Originator) sendACK(bleg *legImpl, resp *sip.Response, invite *sip.Request, tx sip.ClientTransaction) error {
	// Per RFC 3261 Section 13.2.2.4: The Request-URI of the ACK MUST be set to
	// the remote target URI (Contact header from the 2xx response).
	requestURI := invite.Recipient
	if contact := resp.Contact(); contact != nil {
		requestURI = contact.Address
	}

	// Build ACK request with correct Request-URI
	ack := sip.NewRequest(sip.ACK, requestURI)

	// Copy From, Call-ID from INVITE (required for dialog matching)
	sip.CopyHeaders("From", invite, ack)
	sip.CopyHeaders("Call-ID", invite, ack)

	// To header with tag from response (required for dialog identification)
	if to := resp.To(); to != nil {
		toHdr := &sip.ToHeader{
			DisplayName: to.DisplayName,
			Address:     to.Address,
			Params:      to.Params,
		}
		ack.AppendHeader(toHdr)
	}

	// CSeq with same number but ACK method
	if cseq := invite.CSeq(); cseq != nil {
		ackCSeq := &sip.CSeqHeader{
			SeqNo:      cseq.SeqNo,
			MethodName: sip.ACK,
		}
		ack.AppendHeader(ackCSeq)
	}

	maxFwd := sip.MaxForwardsHeader(70)
	ack.AppendHeader(&maxFwd)

	// Determine destination from the response source or Via received
	// This is where the 2xx came from, so we send ACK back there
	destAddr := resp.Source()
	if destAddr == "" {
		// Fallback: extract from Via header of response
		if via := resp.Via(); via != nil {
			if received, ok := via.Params.Get("received"); ok {
				rport := via.Port
				if rportStr, ok := via.Params.Get("rport"); ok {
					fmt.Sscanf(rportStr, "%d", &rport)
				}
				destAddr = fmt.Sprintf("%s:%d", received, rport)
			} else {
				destAddr = fmt.Sprintf("%s:%d", via.Host, via.Port)
			}
		}
	}
	if destAddr == "" {
		port := requestURI.Port
		if port == 0 {
			port = 5060
		}
		destAddr = fmt.Sprintf("%s:%d", requestURI.Host, port)
	}

	// Set destination on request so transport layer knows where to send
	ack.SetDestination(destAddr)

	// Use WriteRequest to send ACK through sipgo's transport layer.
	// This reuses the existing UDP listener connection (port 5060) instead of
	// creating a new ephemeral socket. The transport layer will:
	// 1. Look up the connection pool by remote address (the phone's address)
	// 2. Find the listener connection that received the 200 OK
	// 3. Send the ACK through that same socket
	// 4. Add the Via header with correct local address/port
	if err := o.cfg.Client.WriteRequest(ack); err != nil {
		return fmt.Errorf("write ACK: %w", err)
	}

	slog.Debug("[Originate] ACK sent via sipgo transport",
		"bleg_call_id", bleg.callID,
		"dest", destAddr,
	)
	return nil
}

// sendCANCEL sends a CANCEL for an in-progress INVITE.
func (o *Originator) sendCANCEL(bleg *legImpl, invite *sip.Request, tx sip.ClientTransaction) error {
	bleg.TransitionTo(LegStateFailed)

	// Build CANCEL from original INVITE
	cancelReq := sip.NewRequest(sip.CANCEL, invite.Recipient)

	// Copy headers from INVITE per RFC 3261 Section 9.1
	sip.CopyHeaders("Via", invite, cancelReq)
	sip.CopyHeaders("From", invite, cancelReq)
	sip.CopyHeaders("To", invite, cancelReq)
	sip.CopyHeaders("Call-ID", invite, cancelReq)

	// CSeq with same number but CANCEL method
	if cseq := invite.CSeq(); cseq != nil {
		cancelCSeq := &sip.CSeqHeader{
			SeqNo:      cseq.SeqNo,
			MethodName: sip.CANCEL,
		}
		cancelReq.AppendHeader(cancelCSeq)
	}

	maxFwd := sip.MaxForwardsHeader(70)
	cancelReq.AppendHeader(&maxFwd)

	// Send CANCEL
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cancelTx, err := o.cfg.Client.TransactionRequest(ctx, cancelReq)
	if err != nil {
		return fmt.Errorf("send CANCEL: %w", err)
	}

	// Wait for response to CANCEL
	select {
	case resp := <-cancelTx.Responses():
		if resp != nil {
			slog.Debug("[Originate] CANCEL response",
				"bleg_call_id", bleg.callID,
				"status", resp.StatusCode,
			)
		}
	case <-cancelTx.Done():
	case <-ctx.Done():
	}

	slog.Info("[Originate] CANCEL sent", "bleg_call_id", bleg.callID)
	return nil
}

// SendBYE terminates an answered call.
func (o *Originator) SendBYE(leg Leg) error {
	bleg, ok := leg.(*legImpl)
	if !ok {
		return fmt.Errorf("invalid leg type")
	}

	if bleg.GetState() != LegStateAnswered {
		return ErrLegNotAnswered
	}

	// Build BYE request - this is simplified; full implementation would
	// use dialog state for proper routing
	slog.Info("[Originate] Sending BYE", "bleg_call_id", bleg.callID)

	bleg.Hangup(context.Background(), TerminationCauseNormal)
	return nil
}

// destroyMediaSession releases the media session.
func (o *Originator) destroyMediaSession(ctx context.Context, bleg *legImpl) {
	if bleg.sessionID != "" {
		o.cfg.Transport.DestroySession(ctx, bleg.sessionID, transport.TerminateReasonError)
	}
}

// extractRemoteMedia extracts the remote RTP endpoint from SDP.
func (o *Originator) extractRemoteMedia(ctx context.Context, bleg *legImpl, resp *sip.Response) error {
	if resp.Body() == nil {
		return fmt.Errorf("no SDP in response")
	}

	sdpObj := &psdp.SessionDescription{}
	if err := sdpObj.Unmarshal(resp.Body()); err != nil {
		return fmt.Errorf("parse SDP: %w", err)
	}

	if len(sdpObj.MediaDescriptions) == 0 {
		return fmt.Errorf("no media in SDP")
	}

	media := sdpObj.MediaDescriptions[0]
	remotePort := media.MediaName.Port.Value

	// Get address
	var remoteAddr string
	if media.ConnectionInformation != nil && media.ConnectionInformation.Address != nil {
		remoteAddr = media.ConnectionInformation.Address.Address
	} else if sdpObj.ConnectionInformation != nil && sdpObj.ConnectionInformation.Address != nil {
		remoteAddr = sdpObj.ConnectionInformation.Address.Address
	}

	bleg.SetRemoteMediaEndpoint(remoteAddr, remotePort)

	// Update the RTP manager with the remote endpoint now that we know it
	if bleg.sessionID != "" && remoteAddr != "" && remotePort > 0 {
		if err := o.cfg.Transport.UpdateSessionRemote(ctx, bleg.sessionID, remoteAddr, remotePort); err != nil {
			slog.Warn("[Originate] Failed to update session remote endpoint",
				"bleg_call_id", bleg.callID,
				"session_id", bleg.sessionID,
				"remote", fmt.Sprintf("%s:%d", remoteAddr, remotePort),
				"error", err,
			)
			// Don't fail - the call can still proceed, just logging the issue
		} else {
			slog.Debug("[Originate] Session remote endpoint updated",
				"bleg_call_id", bleg.callID,
				"session_id", bleg.sessionID,
				"remote", fmt.Sprintf("%s:%d", remoteAddr, remotePort),
			)
		}
	}

	return nil
}

// GetLegByALeg returns the B leg associated with an A leg.
func (o *Originator) GetLegByALeg(aLegCallID string) Leg {
	o.mu.RLock()
	defer o.mu.RUnlock()

	bLegCallID, exists := o.aToB[aLegCallID]
	if !exists {
		return nil
	}
	return o.legs[bLegCallID]
}

// GetLegByCallID returns a B-leg by its Call-ID.
// Returns nil if not found.
func (o *Originator) GetLegByCallID(callID string) *legImpl {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.legs[callID]
}

// HandleIncomingBYE handles a BYE request for an outbound (B-leg) call.
// Returns true if the BYE was for a known B-leg, false otherwise.
// If found, responds with 200 OK and terminates the leg.
func (o *Originator) HandleIncomingBYE(req *sip.Request, tx sip.ServerTransaction) bool {
	callID := ""
	if req.CallID() != nil {
		callID = req.CallID().String()
	}
	if callID == "" {
		return false
	}

	bleg := o.GetLegByCallID(callID)
	if bleg == nil {
		return false // Not a B-leg we're tracking
	}

	slog.Info("[Originator] BYE received for B-leg",
		"call_id", callID,
		"leg_id", bleg.id,
	)

	// Respond 200 OK
	resp := sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil)
	if err := tx.Respond(resp); err != nil {
		slog.Error("[Originator] Failed to respond to BYE",
			"call_id", callID,
			"error", err,
		)
	}

	// Terminate the leg - this will trigger the cleanup callback
	bleg.Hangup(context.Background(), TerminationCauseRemoteBYE)

	return true
}

// --- Helper Functions ---

// generateCallID generates a unique Call-ID.
func generateCallID() string {
	return uuid.New().String()
}

// generateTag generates a unique tag for From/To headers.
func generateTag() string {
	return uuid.New().String()[:8]
}
