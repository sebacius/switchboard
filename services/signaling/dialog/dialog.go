package dialog

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// Dialog represents a SIP dialog with full lifecycle state tracking
type Dialog struct {
	mu sync.RWMutex

	// Identification per RFC 3261 Section 12
	CallID    string
	LocalTag  string
	RemoteTag string

	// State machine
	State          CallState
	CreatedAt      time.Time
	StateChangedAt time.Time

	// SIP layer (from sipgo)
	Session     *sipgo.DialogServerSession
	Transaction sip.ServerTransaction

	// Original request/response for BYE construction
	InviteRequest  *sip.Request
	InviteResponse *sip.Response

	// Media session (from transport layer)
	SessionID  string
	RemoteAddr string
	RemotePort int
	Codec      string

	// Lifecycle control
	ctx    context.Context
	cancel context.CancelFunc

	// Termination info
	TerminateReason TerminateReason
}

// NewDialog creates a new dialog from an incoming INVITE request
func NewDialog(req *sip.Request, tx sip.ServerTransaction) *Dialog {
	ctx, cancel := context.WithCancel(context.Background())

	callID := ""
	if req.CallID() != nil {
		callID = req.CallID().String()
	}

	remoteTag := ""
	if from := req.From(); from != nil {
		if tag, ok := from.Params.Get("tag"); ok {
			remoteTag = tag
		}
	}

	now := time.Now()
	return &Dialog{
		CallID:         callID,
		RemoteTag:      remoteTag,
		State:          StateInitial,
		CreatedAt:      now,
		StateChangedAt: now,
		InviteRequest:  req,
		Transaction:    tx,
		ctx:            ctx,
		cancel:         cancel,
	}
}

// SetSession sets the sipgo DialogServerSession after it's created
func (d *Dialog) SetSession(session *sipgo.DialogServerSession) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Session = session
}

// SetInviteResponse stores the response for later BYE construction
func (d *Dialog) SetInviteResponse(resp *sip.Response) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.InviteResponse = resp

	// Extract our local tag from the To header of the response
	if to := resp.To(); to != nil {
		if tag, ok := to.Params.Get("tag"); ok {
			d.LocalTag = tag
		}
	}
}

// SetMediaEndpoint stores the remote media endpoint info
func (d *Dialog) SetMediaEndpoint(addr string, port int, codec string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.RemoteAddr = addr
	d.RemotePort = port
	d.Codec = codec
}

// SetSessionID stores the transport session ID
func (d *Dialog) SetSessionID(sessionID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.SessionID = sessionID
}

// GetSessionID returns the transport session ID
func (d *Dialog) GetSessionID() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.SessionID
}

// GetState returns the current dialog state
func (d *Dialog) GetState() CallState {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.State
}

// TransitionTo attempts to transition to a new state
func (d *Dialog) TransitionTo(newState CallState) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.State.CanTransitionTo(newState) {
		return fmt.Errorf("invalid state transition: %s -> %s", d.State, newState)
	}

	d.State = newState
	d.StateChangedAt = time.Now()
	return nil
}

// Context returns the dialog's context for lifetime management
func (d *Dialog) Context() context.Context {
	return d.ctx
}

// Cancel cancels the dialog's context
func (d *Dialog) Cancel() {
	d.cancel()
}

// IsTerminated returns true if dialog is in terminal state
func (d *Dialog) IsTerminated() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.State == StateTerminated
}

// GetMediaEndpoint returns the remote media endpoint info
func (d *Dialog) GetMediaEndpoint() (addr string, port int, codec string) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.RemoteAddr, d.RemotePort, d.Codec
}

// BuildBYE constructs a BYE request for this dialog
// Per RFC 3261 Section 12.2.1.1, in-dialog requests must swap From/To
func (d *Dialog) BuildBYE(localContact sip.Uri) (*sip.Request, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if d.InviteRequest == nil || d.InviteResponse == nil {
		return nil, fmt.Errorf("cannot build BYE: missing INVITE request/response")
	}

	// Build BYE request URI from Contact of incoming INVITE
	var recipient sip.Uri
	if contact := d.InviteRequest.Contact(); contact != nil {
		recipient = contact.Address
		recipient.UriParams = sip.NewParams()
	} else {
		recipient = d.InviteRequest.From().Address
	}

	byeReq := sip.NewRequest(sip.BYE, recipient)

	// Copy Route headers if present
	if len(d.InviteRequest.GetHeaders("Route")) > 0 {
		sip.CopyHeaders("Route", d.InviteRequest, byeReq)
	}

	// For BYE as UAS (we received the INVITE), From/To must be swapped:
	// - From: Our identity (To from our 200 OK, with our tag)
	// - To: Their identity (From from INVITE, with their tag)
	if to := d.InviteResponse.To(); to != nil {
		// Create new From header with our identity
		fromHdr := &sip.FromHeader{
			DisplayName: to.DisplayName,
			Address:     to.Address,
			Params:      to.Params.Clone(),
		}
		byeReq.AppendHeader(fromHdr)
	}

	if from := d.InviteRequest.From(); from != nil {
		// Create new To header with their identity
		toHdr := &sip.ToHeader{
			DisplayName: from.DisplayName,
			Address:     from.Address,
			Params:      from.Params.Clone(),
		}
		byeReq.AppendHeader(toHdr)
	}

	// Call-ID must match
	if callIDHdr := d.InviteRequest.CallID(); callIDHdr != nil {
		byeReq.AppendHeader(callIDHdr)
	}

	// CSeq with incremented number
	if cseq := d.InviteRequest.CSeq(); cseq != nil {
		newCSeq := &sip.CSeqHeader{
			SeqNo:      cseq.SeqNo + 1,
			MethodName: sip.BYE,
		}
		byeReq.AppendHeader(newCSeq)
	}

	// Max-Forwards
	maxFwd := sip.MaxForwardsHeader(70)
	byeReq.AppendHeader(&maxFwd)

	// Contact header
	contact := &sip.ContactHeader{
		Address: localContact,
	}
	byeReq.AppendHeader(contact)

	return byeReq, nil
}
