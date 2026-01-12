package dialog

import (
	"github.com/emiago/sipgo/sip"
)

// DialogStore defines the interface for SIP dialog management.
// This allows for different implementations and enables proper
// dependency injection for testing.
type DialogStore interface {
	// CreateFromInvite creates a new dialog from an incoming INVITE request.
	// Returns the dialog and any error.
	CreateFromInvite(req *sip.Request, tx sip.ServerTransaction) (*Dialog, error)

	// SendTrying sends 100 Trying and transitions to Early state.
	SendTrying(d *Dialog) error

	// SendProgress sends 183 Session Progress with SDP (early media).
	SendProgress(d *Dialog, sdpBody []byte) error

	// SendOK sends 200 OK with SDP and creates the sipgo dialog session.
	SendOK(d *Dialog, sdpBody []byte) error

	// ConfirmWithACK confirms the dialog when ACK is received.
	ConfirmWithACK(req *sip.Request, tx sip.ServerTransaction) error

	// HandleIncomingBYE processes a BYE request from the remote party.
	HandleIncomingBYE(req *sip.Request, tx sip.ServerTransaction) error

	// HandleIncomingCANCEL processes a CANCEL request.
	HandleIncomingCANCEL(req *sip.Request, tx sip.ServerTransaction) error

	// Terminate terminates a dialog and sends BYE if needed.
	Terminate(callID string, reason TerminateReason) error

	// Get retrieves a dialog by Call-ID.
	Get(callID string) (*Dialog, bool)

	// List returns all dialogs (including terminated ones pending cleanup).
	List() []*Dialog

	// Count returns the number of dialogs.
	Count() int

	// ForEach iterates over all dialogs, stopping if fn returns false.
	ForEach(fn func(*Dialog) bool)

	// SetOnTerminated sets the callback called when a dialog terminates.
	SetOnTerminated(fn func(d *Dialog))

	// Close stops background cleanup goroutines and releases resources.
	Close()
}

// Ensure Manager implements DialogStore
var _ DialogStore = (*Manager)(nil)
