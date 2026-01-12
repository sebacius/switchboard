// Package events provides call lifecycle event definitions and publishing infrastructure.
// Designed for future NATS JetStream integration while remaining transport-agnostic.
package events

import (
	"encoding/json"
	"time"
)

// EventType identifies the type of call event
type EventType string

const (
	// CallReceived fires when INVITE is received and dialplan matched
	CallReceived EventType = "call.received"
	// CallDialing fires when Dial action starts (B2BUA leg B origination)
	CallDialing EventType = "call.dialing"
	// CallRinging fires when 180/183 received from leg B
	CallRinging EventType = "call.ringing"
	// CallAnswered fires when 200 OK received from leg B, bridging starts
	CallAnswered EventType = "call.answered"
	// CallBridged fires when media is flowing between legs
	CallBridged EventType = "call.bridged"
	// CallEnded fires when call terminates (any reason)
	CallEnded EventType = "call.ended"
)

// EndReason explains why a call ended
type EndReason string

const (
	EndReasonNormal      EndReason = "normal"        // Normal hangup (BYE)
	EndReasonBusy        EndReason = "busy"          // 486 Busy Here
	EndReasonNoAnswer    EndReason = "no_answer"     // Timeout waiting for answer
	EndReasonCancelled   EndReason = "cancelled"     // CANCEL from originator
	EndReasonRejected    EndReason = "rejected"      // 4xx/5xx/6xx from destination
	EndReasonUnavailable EndReason = "unavailable"   // Destination unreachable
	EndReasonError       EndReason = "error"         // Internal error
	EndReasonTimeout     EndReason = "timeout"       // ACK timeout, etc.
	EndReasonTransfer    EndReason = "transfer"      // REFER transfer
	EndReasonMediaError  EndReason = "media_error"   // RTP/media failure
)

// LegRole identifies which leg of a B2BUA call
type LegRole string

const (
	LegA LegRole = "A" // Inbound leg (caller)
	LegB LegRole = "B" // Outbound leg (callee)
)

// Direction indicates call direction
type Direction string

const (
	DirectionInbound  Direction = "inbound"
	DirectionOutbound Direction = "outbound"
)

// Event is the base interface for all call events
type Event interface {
	// Type returns the event type for routing/filtering
	Type() EventType
	// Subject returns the NATS subject this event should publish to
	Subject() string
	// Timestamp returns when the event occurred
	Timestamp() time.Time
	// CallID returns the primary correlation ID
	CallID() string
}

// BaseEvent contains fields common to all events
type BaseEvent struct {
	// EventID is a unique identifier for this event instance (for deduplication)
	EventID string `json:"event_id"`
	// EventType identifies the event
	EventType EventType `json:"event_type"`
	// EventTime is when the event occurred (RFC3339Nano)
	EventTime time.Time `json:"event_time"`
	// CallUUID is our internal unique call identifier (stable across retransmits)
	CallUUID string `json:"call_uuid"`
	// SIPCallID is the SIP Call-ID header value
	SIPCallID string `json:"sip_call_id"`
	// BridgeID links leg A and leg B in B2BUA scenarios (empty for single-leg)
	BridgeID string `json:"bridge_id,omitempty"`
	// Leg identifies which leg this event pertains to (A or B)
	Leg LegRole `json:"leg,omitempty"`
	// TenantID for multi-tenant isolation (future use)
	TenantID string `json:"tenant_id,omitempty"`
	// NodeID identifies the switchboard instance (for distributed tracing)
	NodeID string `json:"node_id,omitempty"`
}

func (e *BaseEvent) Type() EventType    { return e.EventType }
func (e *BaseEvent) Timestamp() time.Time { return e.EventTime }
func (e *BaseEvent) CallID() string     { return e.CallUUID }

// Subject returns the NATS subject for routing
// Format: switchboard.calls.<call_uuid>.<event_type_suffix>
func (e *BaseEvent) Subject() string {
	suffix := string(e.EventType)[5:] // strip "call." prefix
	return "switchboard.calls." + e.CallUUID + "." + suffix
}

// Endpoint represents a SIP endpoint (caller or callee)
type Endpoint struct {
	URI         string `json:"uri"`                    // Full SIP URI
	DisplayName string `json:"display_name,omitempty"` // Display name
	User        string `json:"user"`                   // User part of URI
	Host        string `json:"host"`                   // Host part of URI
	Port        int    `json:"port,omitempty"`         // Port if non-default
	Transport   string `json:"transport,omitempty"`    // udp, tcp, tls, ws
}

// MediaInfo captures media negotiation details
type MediaInfo struct {
	LocalAddr     string   `json:"local_addr"`
	LocalPort     int      `json:"local_port"`
	RemoteAddr    string   `json:"remote_addr,omitempty"`
	RemotePort    int      `json:"remote_port,omitempty"`
	Codecs        []string `json:"codecs,omitempty"`        // Offered/negotiated codecs
	SelectedCodec string   `json:"selected_codec,omitempty"` // Final codec
	SSRC          uint32   `json:"ssrc,omitempty"`          // RTP SSRC
	RTPSessionID  string   `json:"rtp_session_id,omitempty"` // RTP Manager session ID
}

// CallReceivedEvent fires when an INVITE is received
type CallReceivedEvent struct {
	BaseEvent
	Direction    Direction `json:"direction"`
	From         Endpoint  `json:"from"`
	To           Endpoint  `json:"to"`
	RequestURI   string    `json:"request_uri"`
	SourceIP     string    `json:"source_ip"`      // Where INVITE came from
	SourcePort   int       `json:"source_port"`
	DialplanName string    `json:"dialplan_name,omitempty"` // Matched dialplan
	DialplanID   string    `json:"dialplan_id,omitempty"`
	UserAgent    string    `json:"user_agent,omitempty"`    // User-Agent header
	// SDP offer info (if present)
	OfferedCodecs []string `json:"offered_codecs,omitempty"`
}

// CallDialingEvent fires when Dial action initiates outbound leg
type CallDialingEvent struct {
	BaseEvent
	// Destination being dialed
	Destination   Endpoint `json:"destination"`
	DialString    string   `json:"dial_string"`    // Original dial string
	DialTimeout   int      `json:"dial_timeout"`   // Timeout in seconds
	CallerID      Endpoint `json:"caller_id"`      // Outbound caller ID
	// Which leg A this is bridging to
	LinkedCallUUID string `json:"linked_call_uuid,omitempty"`
}

// CallRingingEvent fires when 180/183 received
type CallRingingEvent struct {
	BaseEvent
	// SIP response code (180 or 183)
	ResponseCode int `json:"response_code"`
	// Early media present?
	EarlyMedia bool      `json:"early_media"`
	MediaInfo  *MediaInfo `json:"media_info,omitempty"`
}

// CallAnsweredEvent fires when 200 OK received
type CallAnsweredEvent struct {
	BaseEvent
	ResponseCode int        `json:"response_code"` // Usually 200
	MediaInfo    *MediaInfo `json:"media_info,omitempty"`
	// Time from INVITE to 200 OK
	SetupDurationMs int64 `json:"setup_duration_ms"`
	// Time from first ring to answer (Post-Dial Delay)
	RingDurationMs int64 `json:"ring_duration_ms,omitempty"`
}

// CallBridgedEvent fires when media starts flowing
type CallBridgedEvent struct {
	BaseEvent
	// Both legs' media info
	LegAMedia *MediaInfo `json:"leg_a_media,omitempty"`
	LegBMedia *MediaInfo `json:"leg_b_media,omitempty"`
	// Codec used for the bridge
	BridgeCodec string `json:"bridge_codec"`
	// Whether transcoding is active
	Transcoding bool `json:"transcoding"`
}

// CallEndedEvent fires when call terminates
type CallEndedEvent struct {
	BaseEvent
	EndReason       EndReason `json:"end_reason"`
	EndReasonDetail string    `json:"end_reason_detail,omitempty"` // Human-readable
	// SIP response that ended the call (if applicable)
	SIPResponseCode   int    `json:"sip_response_code,omitempty"`
	SIPResponseReason string `json:"sip_response_reason,omitempty"`
	// Who initiated the hangup
	HangupSource string `json:"hangup_source,omitempty"` // "local", "remote", "system"
	// CDR-ready duration fields (in milliseconds)
	SetupDurationMs int64 `json:"setup_duration_ms"`       // INVITE to 200 OK
	RingDurationMs  int64 `json:"ring_duration_ms"`        // First ring to answer
	TalkDurationMs  int64 `json:"talk_duration_ms"`        // Answer to hangup
	TotalDurationMs int64 `json:"total_duration_ms"`       // INVITE to BYE
	// Billing/CDR fields
	BillableDurationMs int64  `json:"billable_duration_ms"` // Talk time for billing
	DispositionCode    string `json:"disposition_code"`     // ANSWERED, NO_ANSWER, BUSY, etc.
	// Final media stats
	PacketsSent     uint64 `json:"packets_sent,omitempty"`
	PacketsReceived uint64 `json:"packets_received,omitempty"`
	PacketsLost     uint64 `json:"packets_lost,omitempty"`
	JitterMs        int    `json:"jitter_ms,omitempty"`
}

// Disposition codes for CDR
const (
	DispositionAnswered = "ANSWERED"
	DispositionNoAnswer = "NO_ANSWER"
	DispositionBusy     = "BUSY"
	DispositionFailed   = "FAILED"
	DispositionCanceled = "CANCELED"
)

// MarshalJSON implements json.Marshaler for Event interface
func MarshalEvent(e Event) ([]byte, error) {
	return json.Marshal(e)
}

// EventMetadata contains optional metadata that can be attached to any event
type EventMetadata struct {
	// Custom key-value pairs (dialplan variables, etc.)
	Custom map[string]string `json:"custom,omitempty"`
	// Trace context for distributed tracing
	TraceID string `json:"trace_id,omitempty"`
	SpanID  string `json:"span_id,omitempty"`
}
