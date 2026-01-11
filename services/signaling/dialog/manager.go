package dialog

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// Manager is the central registry for all active dialogs
type Manager struct {
	mu sync.RWMutex

	// Dialog storage by Call-ID (primary key for our use case)
	dialogs map[string]*Dialog

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
	return &Manager{
		dialogs:       make(map[string]*Dialog),
		sipClient:     client,
		dialogUA:      dialogUA,
		ackTimeout:    32 * time.Second, // RFC 3261 Timer B
		cancelTimeout: 5 * time.Second,
	}
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
		callID = req.CallID().String()
	}
	if callID == "" {
		return nil, fmt.Errorf("INVITE missing Call-ID")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Check for duplicate
	if existing, exists := m.dialogs[callID]; exists {
		// Could be a retransmission or re-INVITE
		if existing.GetState() != StateTerminated {
			slog.Warn("[Dialog] Duplicate INVITE received", "call_id", callID, "state", existing.GetState())
			return existing, nil
		}
		// Previous dialog terminated, allow new one
	}

	// Create new dialog
	dlg := NewDialog(req, tx)
	m.dialogs[callID] = dlg

	slog.Info("[Dialog] Created", "call_id", callID)
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
		session.Close()
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
		callID = req.CallID().String()
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
		callID = req.CallID().String()
	}

	d, exists := m.Get(callID)
	if !exists {
		// Dialog not found, respond 481 Call/Transaction Does Not Exist
		resp := sip.NewResponseFromRequest(req, 481, "Call/Transaction Does Not Exist", nil)
		tx.Respond(resp)
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
		callID = req.CallID().String()
	}

	d, exists := m.Get(callID)
	if !exists {
		// CANCEL for unknown dialog
		resp := sip.NewResponseFromRequest(req, 481, "Call/Transaction Does Not Exist", nil)
		tx.Respond(resp)
		return fmt.Errorf("dialog not found for CANCEL: %s", callID)
	}

	state := d.GetState()
	if state != StateEarly && state != StateWaitingACK {
		// CANCEL only valid before dialog confirmed
		slog.Warn("[Dialog] CANCEL in unexpected state", "call_id", callID, "state", state)
		resp := sip.NewResponseFromRequest(req, 481, "Call/Transaction Does Not Exist", nil)
		tx.Respond(resp)
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
		d.Transaction.Respond(terminated)
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
	d, exists := m.Get(callID)
	if !exists {
		return fmt.Errorf("dialog not found: %s", callID)
	}

	state := d.GetState()
	if state == StateTerminated {
		return nil // Already terminated
	}

	// If confirmed, send BYE
	if state == StateConfirmed && reason == ReasonLocalBYE {
		if err := m.sendBYE(d); err != nil {
			slog.Error("[Dialog] Failed to send BYE", "call_id", callID, "error", err)
		}
	}

	// Cancel context
	d.Cancel()

	// Terminate
	m.terminate(d, reason)

	return nil
}

// sendBYE sends a BYE request to terminate the dialog
func (m *Manager) sendBYE(d *Dialog) error {
	if d.Session == nil {
		return fmt.Errorf("no session for BYE")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := d.Session.Bye(ctx); err != nil {
		return fmt.Errorf("failed to send BYE: %w", err)
	}

	slog.Info("[Dialog] BYE sent", "call_id", d.CallID)
	return nil
}

// terminate marks dialog as terminated and schedules cleanup
func (m *Manager) terminate(d *Dialog, reason TerminateReason) {
	d.mu.Lock()
	d.TerminateReason = reason
	d.mu.Unlock()

	if err := d.TransitionTo(StateTerminated); err != nil {
		slog.Warn("[Dialog] Failed to transition to terminated", "call_id", d.CallID, "error", err)
	}

	// Close sipgo session
	if d.Session != nil {
		d.Session.Close()
	}

	// Notify callback
	m.mu.RLock()
	callback := m.onTerminated
	m.mu.RUnlock()

	if callback != nil {
		go callback(d)
	}

	// Schedule removal from map (keep briefly for retransmissions)
	go func() {
		time.Sleep(32 * time.Second)
		m.mu.Lock()
		delete(m.dialogs, d.CallID)
		m.mu.Unlock()
		slog.Debug("[Dialog] Removed from cache", "call_id", d.CallID)
	}()
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
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.dialogs[callID]
	return d, ok
}

// List returns all active dialogs
func (m *Manager) List() []*Dialog {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*Dialog, 0, len(m.dialogs))
	for _, d := range m.dialogs {
		result = append(result, d)
	}
	return result
}

// Count returns the number of active dialogs
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.dialogs)
}

// ForEach iterates over all dialogs, stopping if fn returns false
func (m *Manager) ForEach(fn func(*Dialog) bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, d := range m.dialogs {
		if !fn(d) {
			break
		}
	}
}
