package dialog

import (
	"time"
)

// Info is a JSON-serializable view of a Dialog for the API.
// Contains all fields needed to understand dialog state without internal pointers.
type Info struct {
	// Identification per RFC 3261 Section 12
	CallID    string `json:"call_id"`
	LocalTag  string `json:"local_tag"`
	RemoteTag string `json:"remote_tag"`
	DialogID  string `json:"dialog_id"` // Composite: CallID + LocalTag + RemoteTag

	// URIs
	LocalURI  string `json:"local_uri"`  // Our URI (To header in our response)
	RemoteURI string `json:"remote_uri"` // Their URI (From header in INVITE)

	// Contact
	LocalContact  string `json:"local_contact,omitempty"`  // Our Contact
	RemoteContact string `json:"remote_contact,omitempty"` // Their Contact

	// State
	State          string `json:"state"`
	StateChangedAt string `json:"state_changed_at"`

	// CSeq tracking
	LocalCSeq  uint32 `json:"local_cseq"`
	RemoteCSeq uint32 `json:"remote_cseq"`

	// Route set (for in-dialog requests)
	RouteSet []string `json:"route_set,omitempty"`

	// Media session (if active)
	SessionID  string `json:"session_id,omitempty"`
	RemoteAddr string `json:"remote_addr,omitempty"`
	RemotePort int    `json:"remote_port,omitempty"`
	Codec      string `json:"codec,omitempty"`

	// Timing
	CreatedAt string `json:"created_at"`
	Duration  int    `json:"duration_seconds"` // Seconds since created

	// Termination (if applicable)
	TerminateReason string `json:"terminate_reason,omitempty"`
}

// ToInfo converts a Dialog to a JSON-serializable Info struct
func (d *Dialog) ToInfo() *Info {
	d.mu.RLock()
	defer d.mu.RUnlock()

	info := &Info{
		CallID:          d.CallID,
		LocalTag:        d.LocalTag,
		RemoteTag:       d.RemoteTag,
		State:           d.State.String(),
		StateChangedAt:  d.StateChangedAt.Format(time.RFC3339),
		CreatedAt:       d.CreatedAt.Format(time.RFC3339),
		Duration:        int(time.Since(d.CreatedAt).Seconds()),
		SessionID:       d.SessionID,
		RemoteAddr:      d.RemoteAddr,
		RemotePort:      d.RemotePort,
		Codec:           d.Codec,
		TerminateReason: d.TerminateReason.String(),
	}

	// Construct dialog ID
	info.DialogID = d.CallID
	if d.LocalTag != "" {
		info.DialogID += ";" + d.LocalTag
	}
	if d.RemoteTag != "" {
		info.DialogID += ";" + d.RemoteTag
	}

	// Extract URIs from request/response if available
	if d.InviteRequest != nil {
		if from := d.InviteRequest.From(); from != nil {
			info.RemoteURI = from.Address.String()
		}
		if contact := d.InviteRequest.Contact(); contact != nil {
			info.RemoteContact = contact.Address.String()
		}
	}

	if d.InviteResponse != nil {
		if to := d.InviteResponse.To(); to != nil {
			info.LocalURI = to.Address.String()
		}
		if contact := d.InviteResponse.Contact(); contact != nil {
			info.LocalContact = contact.Address.String()
		}

		// Extract CSeq
		if cseq := d.InviteResponse.CSeq(); cseq != nil {
			info.LocalCSeq = cseq.SeqNo
		}
	}

	if d.InviteRequest != nil {
		if cseq := d.InviteRequest.CSeq(); cseq != nil {
			info.RemoteCSeq = cseq.SeqNo
		}

		// Extract Route set
		routeHdrs := d.InviteRequest.GetHeaders("Record-Route")
		if len(routeHdrs) > 0 {
			info.RouteSet = make([]string, 0, len(routeHdrs))
			for _, hdr := range routeHdrs {
				info.RouteSet = append(info.RouteSet, hdr.Value())
			}
		}
	}

	// Clear terminate reason if not terminated
	if d.State != StateTerminated {
		info.TerminateReason = ""
	}

	return info
}

// ListInfos converts a slice of Dialogs to Info structs
func ListInfos(dialogs []*Dialog) []*Info {
	infos := make([]*Info, len(dialogs))
	for i, d := range dialogs {
		infos[i] = d.ToInfo()
	}
	return infos
}
