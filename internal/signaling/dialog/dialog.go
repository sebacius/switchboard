package dialog

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// DialogDirection indicates whether we initiated or received the dialog
type DialogDirection int

const (
	// DirectionInbound - we received the INVITE (UAS role)
	DirectionInbound DialogDirection = iota
	// DirectionOutbound - we sent the INVITE (UAC role)
	DirectionOutbound
)

// String returns the string representation of the direction
func (d DialogDirection) String() string {
	switch d {
	case DirectionInbound:
		return "inbound"
	case DirectionOutbound:
		return "outbound"
	default:
		return "unknown"
	}
}

// HoldType represents the type of call hold for re-INVITE
type HoldType int

const (
	// HoldTypeNone - no hold attribute modification
	HoldTypeNone HoldType = iota
	// HoldTypeSendOnly - a=sendonly (local hold)
	HoldTypeSendOnly
	// HoldTypeRecvOnly - a=recvonly (remote hold)
	HoldTypeRecvOnly
	// HoldTypeInactive - a=inactive (both directions held)
	HoldTypeInactive
)

// ReINVITEOptions configures a re-INVITE request
type ReINVITEOptions struct {
	// SDP body (nil to keep existing SDP - for hold scenarios)
	SDP []byte

	// Headers to add or replace (key is header name)
	Headers map[string]string

	// HoldType for call hold scenarios (modifies SDP direction attribute)
	HoldType HoldType
}

// Dialog represents a SIP dialog with full lifecycle state tracking
type Dialog struct {
	mu sync.RWMutex

	// Identification per RFC 3261 Section 12
	CallID    string
	LocalTag  string
	RemoteTag string

	// Direction indicates if we initiated (outbound) or received (inbound) the dialog
	Direction DialogDirection

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

	// Outbound dialog info (populated from 200 OK for UAC dialogs)
	// RemoteContactURI is used as Request-URI for BYE/re-INVITE
	RemoteContactURI string

	// CSeq tracking for outgoing requests
	// LocalCSeq is our CSeq for requests we initiate (BYE, re-INVITE)
	// Starts from the initial INVITE CSeq + 1
	localCSeq atomic.Uint32

	// Re-INVITE state (prevent concurrent re-INVITEs)
	reInviteInProgress atomic.Bool

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

	// Initialize local CSeq from the incoming INVITE's CSeq
	// Our next request will be CSeq + 1
	var initialCSeq uint32
	if cseq := req.CSeq(); cseq != nil {
		initialCSeq = cseq.SeqNo
	}

	now := time.Now()
	d := &Dialog{
		CallID:         callID,
		RemoteTag:      remoteTag,
		Direction:      DirectionInbound,
		State:          StateInitial,
		CreatedAt:      now,
		StateChangedAt: now,
		InviteRequest:  req,
		Transaction:    tx,
		ctx:            ctx,
		cancel:         cancel,
	}
	d.localCSeq.Store(initialCSeq)
	return d
}

// NewOutboundDialog creates a new dialog for an outbound call (UAC role).
// Called when we send an INVITE and receive a 200 OK response.
// The invite is the INVITE request we sent, resp is the 200 OK we received.
func NewOutboundDialog(invite *sip.Request, resp *sip.Response) *Dialog {
	ctx, cancel := context.WithCancel(context.Background())

	callID := ""
	if invite.CallID() != nil {
		callID = invite.CallID().String()
	}

	// Our local tag is from the From header of our INVITE
	localTag := ""
	if from := invite.From(); from != nil {
		if tag, ok := from.Params.Get("tag"); ok {
			localTag = tag
		}
	}

	// Remote tag is from the To header of the 200 OK
	remoteTag := ""
	if to := resp.To(); to != nil {
		if tag, ok := to.Params.Get("tag"); ok {
			remoteTag = tag
		}
	}

	// Remote Contact URI from 200 OK - used as Request-URI for BYE/re-INVITE
	remoteContactURI := ""
	if contact := resp.Contact(); contact != nil {
		remoteContactURI = contact.Address.String()
	}

	// Start CSeq from 1 (the INVITE we sent)
	// Our next request will be CSeq 2
	var initialCSeq uint32 = 1
	if cseq := invite.CSeq(); cseq != nil {
		initialCSeq = cseq.SeqNo
	}

	now := time.Now()
	d := &Dialog{
		CallID:           callID,
		LocalTag:         localTag,
		RemoteTag:        remoteTag,
		Direction:        DirectionOutbound,
		State:            StateConfirmed, // Outbound dialog is confirmed after 200 OK + ACK
		CreatedAt:        now,
		StateChangedAt:   now,
		InviteRequest:    invite,
		InviteResponse:   resp,
		RemoteContactURI: remoteContactURI,
		ctx:              ctx,
		cancel:           cancel,
	}
	d.localCSeq.Store(initialCSeq)
	return d
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

// SetRemoteEndpoint stores the remote SIP endpoint address.
// This is typically set from the SIP request source for display purposes.
func (d *Dialog) SetRemoteEndpoint(addr string, port int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.RemoteAddr = addr
	d.RemotePort = port
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
// Per RFC 3261 Section 12.2.1.1, in-dialog requests use the dialog's identifiers
func (d *Dialog) BuildBYE(localContact sip.Uri) (*sip.Request, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if d.InviteRequest == nil {
		return nil, fmt.Errorf("cannot build BYE: missing INVITE request")
	}

	// Determine Request-URI based on direction
	var recipient sip.Uri
	if d.Direction == DirectionOutbound {
		// For outbound (UAC): use Remote Contact from 200 OK
		if d.RemoteContactURI != "" {
			if err := sip.ParseUri(d.RemoteContactURI, &recipient); err != nil {
				return nil, fmt.Errorf("cannot parse remote contact URI: %w", err)
			}
		} else if d.InviteResponse != nil && d.InviteResponse.Contact() != nil {
			recipient = d.InviteResponse.Contact().Address
		} else {
			// Fallback to To header from our INVITE
			if to := d.InviteRequest.To(); to != nil {
				recipient = to.Address
			}
		}
	} else {
		// For inbound (UAS): use Contact from incoming INVITE
		if contact := d.InviteRequest.Contact(); contact != nil {
			recipient = contact.Address
			recipient.UriParams = sip.NewParams()
		} else {
			recipient = d.InviteRequest.From().Address
		}
	}

	byeReq := sip.NewRequest(sip.BYE, recipient)

	// Copy Route headers if present
	if len(d.InviteRequest.GetHeaders("Route")) > 0 {
		sip.CopyHeaders("Route", d.InviteRequest, byeReq)
	}

	// Build From/To headers based on direction
	if d.Direction == DirectionOutbound {
		// For outbound (UAC): From/To same as our original INVITE
		// From = our identity (with our tag)
		// To = their identity (with their tag from 200 OK)
		if from := d.InviteRequest.From(); from != nil {
			fromHdr := &sip.FromHeader{
				DisplayName: from.DisplayName,
				Address:     from.Address,
				Params:      from.Params.Clone(),
			}
			byeReq.AppendHeader(fromHdr)
		}

		// To header with remote tag from 200 OK
		if to := d.InviteRequest.To(); to != nil {
			toHdr := &sip.ToHeader{
				DisplayName: to.DisplayName,
				Address:     to.Address,
				Params:      sip.NewParams(),
			}
			if d.RemoteTag != "" {
				toHdr.Params.Add("tag", d.RemoteTag)
			}
			byeReq.AppendHeader(toHdr)
		}
	} else {
		// For inbound (UAS): From/To must be swapped
		// From = our identity (To from our 200 OK, with our tag)
		// To = their identity (From from INVITE, with their tag)
		if d.InviteResponse != nil {
			if to := d.InviteResponse.To(); to != nil {
				fromHdr := &sip.FromHeader{
					DisplayName: to.DisplayName,
					Address:     to.Address,
					Params:      to.Params.Clone(),
				}
				byeReq.AppendHeader(fromHdr)
			}
		}

		if from := d.InviteRequest.From(); from != nil {
			toHdr := &sip.ToHeader{
				DisplayName: from.DisplayName,
				Address:     from.Address,
				Params:      from.Params.Clone(),
			}
			byeReq.AppendHeader(toHdr)
		}
	}

	// Call-ID must match
	if callIDHdr := d.InviteRequest.CallID(); callIDHdr != nil {
		byeReq.AppendHeader(callIDHdr)
	}

	// CSeq with incremented number (atomically increment our local CSeq)
	newSeqNo := d.localCSeq.Add(1)
	byeReq.AppendHeader(&sip.CSeqHeader{
		SeqNo:      newSeqNo,
		MethodName: sip.BYE,
	})

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

// BuildReINVITE constructs a re-INVITE request for this dialog
// Used for session updates like SDP renegotiation, hold, or media migration
func (d *Dialog) BuildReINVITE(localContact sip.Uri, opts ReINVITEOptions) (*sip.Request, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if d.InviteRequest == nil {
		return nil, fmt.Errorf("cannot build re-INVITE: missing INVITE request")
	}

	// Check if a re-INVITE is already in progress
	if !d.reInviteInProgress.CompareAndSwap(false, true) {
		return nil, fmt.Errorf("re-INVITE already in progress for dialog %s", d.CallID)
	}

	// Determine Request-URI based on direction
	var recipient sip.Uri
	if d.Direction == DirectionOutbound {
		// For outbound (UAC): use Remote Contact from 200 OK
		if d.RemoteContactURI != "" {
			if err := sip.ParseUri(d.RemoteContactURI, &recipient); err != nil {
				d.reInviteInProgress.Store(false)
				return nil, fmt.Errorf("cannot parse remote contact URI: %w", err)
			}
		} else if d.InviteResponse != nil && d.InviteResponse.Contact() != nil {
			recipient = d.InviteResponse.Contact().Address
		} else {
			// Fallback to To header from our INVITE
			if to := d.InviteRequest.To(); to != nil {
				recipient = to.Address
			}
		}
	} else {
		// For inbound (UAS): use Contact from incoming INVITE
		if contact := d.InviteRequest.Contact(); contact != nil {
			recipient = contact.Address
			recipient.UriParams = sip.NewParams()
		} else {
			recipient = d.InviteRequest.From().Address
		}
	}

	reInviteReq := sip.NewRequest(sip.INVITE, recipient)

	// Copy Route headers if present
	if len(d.InviteRequest.GetHeaders("Route")) > 0 {
		sip.CopyHeaders("Route", d.InviteRequest, reInviteReq)
	}

	// Build From/To headers based on direction
	if d.Direction == DirectionOutbound {
		// For outbound (UAC): From/To same as our original INVITE
		if from := d.InviteRequest.From(); from != nil {
			fromHdr := &sip.FromHeader{
				DisplayName: from.DisplayName,
				Address:     from.Address,
				Params:      from.Params.Clone(),
			}
			reInviteReq.AppendHeader(fromHdr)
		}

		// To header with remote tag from 200 OK
		if to := d.InviteRequest.To(); to != nil {
			toHdr := &sip.ToHeader{
				DisplayName: to.DisplayName,
				Address:     to.Address,
				Params:      sip.NewParams(),
			}
			if d.RemoteTag != "" {
				toHdr.Params.Add("tag", d.RemoteTag)
			}
			reInviteReq.AppendHeader(toHdr)
		}
	} else {
		// For inbound (UAS): From/To are swapped
		if d.InviteResponse != nil {
			if to := d.InviteResponse.To(); to != nil {
				fromHdr := &sip.FromHeader{
					DisplayName: to.DisplayName,
					Address:     to.Address,
					Params:      to.Params.Clone(),
				}
				reInviteReq.AppendHeader(fromHdr)
			}
		}

		if from := d.InviteRequest.From(); from != nil {
			toHdr := &sip.ToHeader{
				DisplayName: from.DisplayName,
				Address:     from.Address,
				Params:      from.Params.Clone(),
			}
			reInviteReq.AppendHeader(toHdr)
		}
	}

	// Call-ID must match
	if callIDHdr := d.InviteRequest.CallID(); callIDHdr != nil {
		reInviteReq.AppendHeader(callIDHdr)
	}

	// CSeq with incremented number
	newSeqNo := d.localCSeq.Add(1)
	reInviteReq.AppendHeader(&sip.CSeqHeader{
		SeqNo:      newSeqNo,
		MethodName: sip.INVITE,
	})

	// Max-Forwards
	maxFwd := sip.MaxForwardsHeader(70)
	reInviteReq.AppendHeader(&maxFwd)

	// Contact header
	contactHdr := &sip.ContactHeader{
		Address: localContact,
	}
	reInviteReq.AppendHeader(contactHdr)

	// Add custom headers if provided
	for name, value := range opts.Headers {
		reInviteReq.AppendHeader(sip.NewHeader(name, value))
	}

	// Set SDP body if provided
	if len(opts.SDP) > 0 {
		reInviteReq.SetBody(opts.SDP)
		reInviteReq.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	}

	return reInviteReq, nil
}

// CompleteReINVITE marks the re-INVITE as completed (success or failure)
// Must be called after re-INVITE response is handled
func (d *Dialog) CompleteReINVITE() {
	d.reInviteInProgress.Store(false)
}

// IsReINVITEInProgress returns true if a re-INVITE is currently pending
func (d *Dialog) IsReINVITEInProgress() bool {
	return d.reInviteInProgress.Load()
}
