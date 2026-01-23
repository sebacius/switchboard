package dialog

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/sebas/switchboard/internal/signaling/store"
)

// Dialog TTL constants
const (
	// ActiveDialogTTL is the TTL for active dialogs (4 hours)
	ActiveDialogTTL = 4 * time.Hour
	// TerminatedDialogTTL is the TTL for terminated dialogs (for retransmissions, RFC 3261 Timer B)
	TerminatedDialogTTL = 32 * time.Second
	// DialogCleanupInterval is how often the cleanup loop runs
	DialogCleanupInterval = 10 * time.Second
)

// Manager is the central registry for all active dialogs
type Manager struct {
	mu sync.RWMutex

	// Dialog storage by Call-ID using TTLStore for automatic cleanup
	dialogs *store.TTLStore[string, *Dialog]

	// SIP components for sending requests
	sipClient *sipgo.Client
	dialogUA  *sipgo.DialogUA

	// Configuration
	ackTimeout    time.Duration
	cancelTimeout time.Duration

	// Callbacks
	onTerminated func(d *Dialog)
}

// NewManager creates a new dialog manager
func NewManager(client *sipgo.Client, dialogUA *sipgo.DialogUA) *Manager {
	m := &Manager{
		dialogs:       store.NewTTLStore[string, *Dialog](DialogCleanupInterval),
		sipClient:     client,
		dialogUA:      dialogUA,
		ackTimeout:    32 * time.Second, // RFC 3261 Timer B
		cancelTimeout: 5 * time.Second,
	}

	// Set eviction callback to log when dialogs are automatically removed
	m.dialogs.SetOnEvict(func(callID string, d *Dialog) {
		slog.Debug("[Dialog] Evicted from cache", "call_id", callID, "state", d.GetState())
	})

	return m
}

// SetOnTerminated sets the callback called when a dialog terminates
func (m *Manager) SetOnTerminated(fn func(d *Dialog)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onTerminated = fn
}

// CreateFromInvite creates a new dialog from an incoming INVITE request
func (m *Manager) CreateFromInvite(req *sip.Request, tx sip.ServerTransaction) (*Dialog, error) {
	callID := ""
	if req.CallID() != nil {
		// Cast to string directly - .String() adds "Call-ID: " prefix
		callID = string(*req.CallID())
	}
	if callID == "" {
		return nil, fmt.Errorf("INVITE missing Call-ID")
	}

	// Check for duplicate
	if existing, exists := m.dialogs.Get(callID); exists {
		// Could be a retransmission or re-INVITE
		if existing.GetState() != StateTerminated {
			slog.Warn("[Dialog] Duplicate INVITE received", "call_id", callID, "state", existing.GetState())
			return existing, nil
		}
		// Previous dialog terminated, allow new one
	}

	// Create new dialog with active TTL
	dlg := NewDialog(req, tx)
	m.dialogs.Set(callID, dlg, ActiveDialogTTL)

	slog.Info("[Dialog] Created", "call_id", callID)
	return dlg, nil
}

// RegisterOutbound registers an outbound dialog after receiving 200 OK.
// This is called by the Originator when a B-leg call is answered.
// The dialog is already in Confirmed state.
func (m *Manager) RegisterOutbound(invite *sip.Request, resp *sip.Response) (*Dialog, error) {
	callID := ""
	if invite.CallID() != nil {
		// Cast to string directly - .String() adds "Call-ID: " prefix
		callID = string(*invite.CallID())
	}
	if callID == "" {
		return nil, fmt.Errorf("INVITE missing Call-ID")
	}

	// Check for duplicate (shouldn't happen, but be defensive)
	if existing, exists := m.dialogs.Get(callID); exists {
		if existing.GetState() != StateTerminated {
			slog.Warn("[Dialog] Duplicate outbound dialog registration", "call_id", callID)
			return existing, nil
		}
	}

	// Create outbound dialog
	dlg := NewOutboundDialog(invite, resp)
	m.dialogs.Set(callID, dlg, ActiveDialogTTL)

	slog.Info("[Dialog] Registered outbound dialog", "call_id", callID, "direction", dlg.Direction)
	return dlg, nil
}

// SendTrying sends 100 Trying and transitions to Early state
func (m *Manager) SendTrying(d *Dialog) error {
	trying := sip.NewResponseFromRequest(d.InviteRequest, sip.StatusTrying, "Trying", nil)
	if err := d.Transaction.Respond(trying); err != nil {
		return fmt.Errorf("failed to send 100 Trying: %w", err)
	}

	if err := d.TransitionTo(StateEarly); err != nil {
		slog.Warn("[Dialog] State transition failed", "call_id", d.CallID, "error", err)
	}

	slog.Debug("[Dialog] Sent 100 Trying", "call_id", d.CallID)
	return nil
}

// SendProgress sends 183 Session Progress with SDP (early media)
func (m *Manager) SendProgress(d *Dialog, sdpBody []byte) error {
	progress := sip.NewResponseFromRequest(d.InviteRequest, sip.StatusCode(183), "Session Progress", sdpBody)
	ct := sip.ContentTypeHeader("application/sdp")
	progress.AppendHeader(&ct)

	if err := d.Transaction.Respond(progress); err != nil {
		return fmt.Errorf("failed to send 183 Session Progress: %w", err)
	}

	slog.Debug("[Dialog] Sent 183 Session Progress", "call_id", d.CallID)
	return nil
}

// SendOK sends 200 OK with SDP and creates the sipgo dialog session
func (m *Manager) SendOK(d *Dialog, sdpBody []byte) error {
	// Create sipgo dialog session
	session, err := m.dialogUA.ReadInvite(d.InviteRequest, d.Transaction)
	if err != nil {
		return fmt.Errorf("failed to create dialog session: %w", err)
	}
	d.SetSession(session)

	// Send 200 OK with SDP
	if err := session.RespondSDP(sdpBody); err != nil {
		_ = session.Close()
		return fmt.Errorf("failed to send 200 OK: %w", err)
	}

	// Store the response for BYE construction
	d.SetInviteResponse(session.InviteResponse)

	if err := d.TransitionTo(StateWaitingACK); err != nil {
		slog.Warn("[Dialog] State transition failed", "call_id", d.CallID, "error", err)
	}

	slog.Info("[Dialog] Sent 200 OK", "call_id", d.CallID)

	// Start ACK timeout watcher
	go m.watchACKTimeout(d)

	return nil
}

// ConfirmWithACK confirms the dialog when ACK is received
func (m *Manager) ConfirmWithACK(req *sip.Request, tx sip.ServerTransaction) error {
	callID := ""
	if req.CallID() != nil {
		// Cast to string directly - .String() adds "Call-ID: " prefix
		callID = string(*req.CallID())
	}

	d, exists := m.Get(callID)
	if !exists {
		slog.Warn("[Dialog] ACK for unknown dialog", "call_id", callID)
		return fmt.Errorf("dialog not found for ACK: %s", callID)
	}

	// Validate state
	state := d.GetState()
	if state != StateWaitingACK {
		if state == StateConfirmed {
			// ACK retransmission, ignore
			slog.Debug("[Dialog] ACK retransmission ignored", "call_id", callID)
			return nil
		}
		slog.Warn("[Dialog] ACK in unexpected state", "call_id", callID, "state", state)
		return fmt.Errorf("unexpected state for ACK: %s", state)
	}

	// Confirm dialog with sipgo
	if d.Session != nil {
		if err := d.Session.ReadAck(req, tx); err != nil {
			slog.Warn("[Dialog] Failed to read ACK", "call_id", callID, "error", err)
		}
	}

	if err := d.TransitionTo(StateConfirmed); err != nil {
		return fmt.Errorf("failed to transition to Confirmed: %w", err)
	}

	slog.Info("[Dialog] Confirmed (ACK received)", "call_id", callID)
	return nil
}

// HandleIncomingBYE processes a BYE request from the remote party
func (m *Manager) HandleIncomingBYE(req *sip.Request, tx sip.ServerTransaction) error {
	callID := ""
	if req.CallID() != nil {
		// Cast to string directly - .String() adds "Call-ID: " prefix
		callID = string(*req.CallID())
	}

	d, exists := m.Get(callID)
	if !exists {
		// Dialog not found, respond 481 Call/Transaction Does Not Exist
		resp := sip.NewResponseFromRequest(req, 481, "Call/Transaction Does Not Exist", nil)
		_ = tx.Respond(resp)
		return fmt.Errorf("dialog not found for BYE: %s", callID)
	}

	// Read BYE with sipgo session if available
	if d.Session != nil {
		if err := d.Session.ReadBye(req, tx); err != nil {
			slog.Warn("[Dialog] Failed to read BYE", "call_id", callID, "error", err)
		}
	} else {
		// Respond 200 OK manually
		resp := sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil)
		if err := tx.Respond(resp); err != nil {
			slog.Error("[Dialog] Failed to respond to BYE", "call_id", callID, "error", err)
		}
	}

	// Cancel the dialog context to stop media
	d.Cancel()

	// Terminate
	m.terminate(d, ReasonRemoteBYE)

	slog.Info("[Dialog] BYE received, dialog terminated", "call_id", callID)
	return nil
}

// HandleIncomingCANCEL processes a CANCEL request
func (m *Manager) HandleIncomingCANCEL(req *sip.Request, tx sip.ServerTransaction) error {
	callID := ""
	if req.CallID() != nil {
		// Cast to string directly - .String() adds "Call-ID: " prefix
		callID = string(*req.CallID())
	}

	d, exists := m.Get(callID)
	if !exists {
		// CANCEL for unknown dialog
		resp := sip.NewResponseFromRequest(req, 481, "Call/Transaction Does Not Exist", nil)
		_ = tx.Respond(resp)
		return fmt.Errorf("dialog not found for CANCEL: %s", callID)
	}

	state := d.GetState()
	if state != StateEarly && state != StateWaitingACK {
		// CANCEL only valid before dialog confirmed
		slog.Warn("[Dialog] CANCEL in unexpected state", "call_id", callID, "state", state)
		resp := sip.NewResponseFromRequest(req, 481, "Call/Transaction Does Not Exist", nil)
		_ = tx.Respond(resp)
		return nil
	}

	// Respond 200 OK to CANCEL
	resp := sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil)
	if err := tx.Respond(resp); err != nil {
		slog.Error("[Dialog] Failed to respond to CANCEL", "call_id", callID, "error", err)
	}

	// Send 487 Request Terminated for the original INVITE
	if d.Transaction != nil {
		terminated := sip.NewResponseFromRequest(d.InviteRequest, 487, "Request Terminated", nil)
		_ = d.Transaction.Respond(terminated)
	}

	// Cancel context
	d.Cancel()

	// Terminate
	m.terminate(d, ReasonCancel)

	slog.Info("[Dialog] CANCEL received, dialog terminated", "call_id", callID)
	return nil
}

// Terminate terminates a dialog and sends BYE if needed
func (m *Manager) Terminate(callID string, reason TerminateReason) error {
	slog.Debug("[Dialog] Manager.Terminate called",
		"call_id", callID,
		"reason", reason,
	)

	d, exists := m.Get(callID)
	if !exists {
		slog.Warn("[Dialog] Manager.Terminate - dialog not found",
			"call_id", callID,
		)
		return fmt.Errorf("dialog not found: %s", callID)
	}

	state := d.GetState()
	slog.Debug("[Dialog] Manager.Terminate - dialog found",
		"call_id", callID,
		"state", state.String(),
		"direction", d.Direction,
	)

	if state == StateTerminated {
		slog.Debug("[Dialog] Manager.Terminate - already terminated",
			"call_id", callID,
		)
		return nil // Already terminated
	}

	// If confirmed, send BYE
	if state == StateConfirmed && reason == ReasonLocalBYE {
		slog.Info("[Dialog] Manager.Terminate - sending BYE",
			"call_id", callID,
			"direction", d.Direction,
		)
		if err := m.sendBYE(d); err != nil {
			slog.Error("[Dialog] Failed to send BYE", "call_id", callID, "error", err)
		}
	} else {
		slog.Debug("[Dialog] Manager.Terminate - not sending BYE",
			"call_id", callID,
			"state", state.String(),
			"reason", reason,
			"should_send", state == StateConfirmed && reason == ReasonLocalBYE,
		)
	}

	// Cancel context
	d.Cancel()

	// Terminate
	m.terminate(d, reason)

	return nil
}

// sendBYE sends a BYE request to terminate the dialog
func (m *Manager) sendBYE(d *Dialog) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// For inbound dialogs with sipgo session, use the session's Bye method
	if d.Session != nil && d.Direction == DirectionInbound {
		if err := d.Session.Bye(ctx); err != nil {
			return fmt.Errorf("failed to send BYE: %w", err)
		}
		slog.Info("[Dialog] BYE sent via session", "call_id", d.CallID)
		return nil
	}

	// For outbound dialogs or dialogs without session, build and send BYE manually
	localContact := sip.Uri{
		Scheme: "sip",
		User:   "switchboard",
		Host:   "localhost", // Will be overwritten by Via
	}
	// Try to get a better local contact from the INVITE
	if d.InviteRequest != nil {
		if contact := d.InviteRequest.Contact(); contact != nil {
			localContact = contact.Address
		} else if from := d.InviteRequest.From(); from != nil {
			localContact = from.Address
		}
	}

	byeReq, err := d.BuildBYE(localContact)
	if err != nil {
		return fmt.Errorf("failed to build BYE: %w", err)
	}

	tx, err := m.sipClient.TransactionRequest(ctx, byeReq)
	if err != nil {
		return fmt.Errorf("failed to send BYE: %w", err)
	}

	// Wait for response
	select {
	case resp := <-tx.Responses():
		if resp != nil {
			slog.Debug("[Dialog] BYE response received",
				"call_id", d.CallID,
				"status", resp.StatusCode)
		}
	case <-tx.Done():
	case <-ctx.Done():
		slog.Warn("[Dialog] BYE timeout", "call_id", d.CallID)
	}

	slog.Info("[Dialog] BYE sent", "call_id", d.CallID, "direction", d.Direction)
	return nil
}

// terminate marks dialog as terminated and updates TTL for cleanup
func (m *Manager) terminate(d *Dialog, reason TerminateReason) {
	d.mu.Lock()
	d.TerminateReason = reason
	d.mu.Unlock()

	if err := d.TransitionTo(StateTerminated); err != nil {
		slog.Warn("[Dialog] Failed to transition to terminated", "call_id", d.CallID, "error", err)
	}

	// Close sipgo session
	if d.Session != nil {
		_ = d.Session.Close()
	}

	// Notify callback
	m.mu.RLock()
	callback := m.onTerminated
	m.mu.RUnlock()

	if callback != nil {
		go callback(d)
	}

	// Update TTL to short duration for terminated dialogs (handles retransmissions per RFC 3261)
	// TTLStore's cleanup loop will automatically remove it after TerminatedDialogTTL
	m.dialogs.Set(d.CallID, d, TerminatedDialogTTL)
	slog.Debug("[Dialog] Scheduled for cleanup", "call_id", d.CallID, "ttl", TerminatedDialogTTL)
}

// watchACKTimeout watches for ACK timeout
func (m *Manager) watchACKTimeout(d *Dialog) {
	select {
	case <-d.Context().Done():
		return
	case <-time.After(m.ackTimeout):
		state := d.GetState()
		if state == StateWaitingACK {
			slog.Warn("[Dialog] ACK timeout", "call_id", d.CallID)
			d.Cancel()
			m.terminate(d, ReasonTimeout)
		}
	}
}

// Get retrieves a dialog by Call-ID
func (m *Manager) Get(callID string) (*Dialog, bool) {
	return m.dialogs.Get(callID)
}

// List returns all dialogs (including terminated ones pending cleanup)
func (m *Manager) List() []*Dialog {
	all := m.dialogs.All()
	result := make([]*Dialog, 0, len(all))
	for _, d := range all {
		result = append(result, d)
	}
	return result
}

// Count returns the number of dialogs
func (m *Manager) Count() int {
	return m.dialogs.Len()
}

// ForEach iterates over all dialogs, stopping if fn returns false
func (m *Manager) ForEach(fn func(*Dialog) bool) {
	m.dialogs.ForEach(func(_ string, d *Dialog) bool {
		return fn(d)
	})
}

// Close stops the TTLStore cleanup goroutine and releases resources
func (m *Manager) Close() {
	m.dialogs.Close()
}

// FindBySessionID finds a dialog by its RTP session ID
func (m *Manager) FindBySessionID(sessionID string) (*Dialog, bool) {
	var found *Dialog
	m.dialogs.ForEach(func(_ string, d *Dialog) bool {
		if d.GetSessionID() == sessionID {
			found = d
			return false // stop iteration
		}
		return true
	})
	return found, found != nil
}

// ReINVITEResult contains the result of a re-INVITE operation
type ReINVITEResult struct {
	Success    bool
	StatusCode int
	Reason     string
	SDP        []byte // SDP from 200 OK response (if any)
}

// SendReINVITE sends a re-INVITE request and waits for the response
// Returns the result and handles ACK for 200 OK responses
func (m *Manager) SendReINVITE(ctx context.Context, d *Dialog, localContact sip.Uri, opts ReINVITEOptions) (*ReINVITEResult, error) {
	if d.IsTerminated() {
		return nil, fmt.Errorf("cannot send re-INVITE: dialog is terminated")
	}

	state := d.GetState()
	if state != StateConfirmed {
		return nil, fmt.Errorf("cannot send re-INVITE: dialog not in confirmed state (state: %s)", state)
	}

	// Build the re-INVITE request
	reInviteReq, err := d.BuildReINVITE(localContact, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to build re-INVITE: %w", err)
	}

	// Send the request using sipgo client
	tx, err := m.sipClient.TransactionRequest(ctx, reInviteReq)
	if err != nil {
		d.CompleteReINVITE()
		return nil, fmt.Errorf("failed to send re-INVITE: %w", err)
	}
	defer tx.Terminate()

	// Wait for final response
	result := &ReINVITEResult{}
	for {
		select {
		case <-ctx.Done():
			d.CompleteReINVITE()
			return nil, ctx.Err()
		case resp := <-tx.Responses():
			if resp == nil {
				d.CompleteReINVITE()
				return nil, fmt.Errorf("transaction terminated without response")
			}

			statusCode := int(resp.StatusCode)
			result.StatusCode = statusCode
			result.Reason = resp.Reason

			// Handle provisional responses
			if statusCode >= 100 && statusCode < 200 {
				slog.Debug("[Dialog] Re-INVITE provisional response",
					"call_id", d.CallID,
					"status", statusCode,
					"reason", resp.Reason)
				continue
			}

			// Handle final response
			if statusCode >= 200 && statusCode < 300 {
				// Success - extract SDP if present
				if resp.Body() != nil {
					result.SDP = resp.Body()
				}
				result.Success = true

				// Send ACK for 200 OK (required for INVITE transactions)
				ackReq := sip.NewAckRequest(reInviteReq, resp, nil)
				if err := m.sipClient.WriteRequest(ackReq); err != nil {
					slog.Warn("[Dialog] Failed to send ACK for re-INVITE 200 OK",
						"call_id", d.CallID,
						"error", err)
				} else {
					slog.Debug("[Dialog] ACK sent for re-INVITE",
						"call_id", d.CallID)
				}

				slog.Info("[Dialog] Re-INVITE successful",
					"call_id", d.CallID,
					"sdp_length", len(result.SDP))
			} else {
				// Error response (4xx, 5xx, 6xx)
				result.Success = false
				slog.Warn("[Dialog] Re-INVITE failed",
					"call_id", d.CallID,
					"status", statusCode,
					"reason", resp.Reason)

				// Send ACK for error responses (also required per RFC 3261)
				ackReq := sip.NewAckRequest(reInviteReq, resp, nil)
				if err := m.sipClient.WriteRequest(ackReq); err != nil {
					slog.Warn("[Dialog] Failed to send ACK for re-INVITE error response",
						"call_id", d.CallID,
						"error", err)
				}
			}

			d.CompleteReINVITE()
			return result, nil
		}
	}
}
