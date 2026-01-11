package events

import (
	"time"

	"github.com/google/uuid"
)

// Builder provides fluent construction of call events with consistent defaults.
type Builder struct {
	nodeID   string
	tenantID string
}

// NewBuilder creates an event builder with global defaults.
func NewBuilder(nodeID string) *Builder {
	return &Builder{nodeID: nodeID}
}

// WithTenant sets the default tenant ID for all events.
func (b *Builder) WithTenant(tenantID string) *Builder {
	b.tenantID = tenantID
	return b
}

// newBase creates a BaseEvent with common fields populated.
func (b *Builder) newBase(eventType EventType, callUUID, sipCallID string) BaseEvent {
	return BaseEvent{
		EventID:   uuid.New().String(),
		EventType: eventType,
		EventTime: time.Now().UTC(),
		CallUUID:  callUUID,
		SIPCallID: sipCallID,
		TenantID:  b.tenantID,
		NodeID:    b.nodeID,
	}
}

// CallReceivedBuilder constructs CallReceivedEvent.
type CallReceivedBuilder struct {
	event *CallReceivedEvent
}

// CallReceived starts building a CallReceivedEvent.
func (b *Builder) CallReceived(callUUID, sipCallID string) *CallReceivedBuilder {
	return &CallReceivedBuilder{
		event: &CallReceivedEvent{
			BaseEvent: b.newBase(CallReceived, callUUID, sipCallID),
			Direction: DirectionInbound,
		},
	}
}

func (cb *CallReceivedBuilder) Direction(d Direction) *CallReceivedBuilder {
	cb.event.Direction = d
	return cb
}

func (cb *CallReceivedBuilder) From(e Endpoint) *CallReceivedBuilder {
	cb.event.From = e
	return cb
}

func (cb *CallReceivedBuilder) To(e Endpoint) *CallReceivedBuilder {
	cb.event.To = e
	return cb
}

func (cb *CallReceivedBuilder) RequestURI(uri string) *CallReceivedBuilder {
	cb.event.RequestURI = uri
	return cb
}

func (cb *CallReceivedBuilder) Source(ip string, port int) *CallReceivedBuilder {
	cb.event.SourceIP = ip
	cb.event.SourcePort = port
	return cb
}

func (cb *CallReceivedBuilder) Dialplan(name, id string) *CallReceivedBuilder {
	cb.event.DialplanName = name
	cb.event.DialplanID = id
	return cb
}

func (cb *CallReceivedBuilder) UserAgent(ua string) *CallReceivedBuilder {
	cb.event.UserAgent = ua
	return cb
}

func (cb *CallReceivedBuilder) OfferedCodecs(codecs []string) *CallReceivedBuilder {
	cb.event.OfferedCodecs = codecs
	return cb
}

func (cb *CallReceivedBuilder) Leg(leg LegRole) *CallReceivedBuilder {
	cb.event.Leg = leg
	return cb
}

func (cb *CallReceivedBuilder) Build() *CallReceivedEvent {
	return cb.event
}

// CallDialingBuilder constructs CallDialingEvent.
type CallDialingBuilder struct {
	event *CallDialingEvent
}

// CallDialing starts building a CallDialingEvent.
func (b *Builder) CallDialing(callUUID, sipCallID string) *CallDialingBuilder {
	return &CallDialingBuilder{
		event: &CallDialingEvent{
			BaseEvent: b.newBase(CallDialing, callUUID, sipCallID),
		},
	}
}

func (cb *CallDialingBuilder) Destination(e Endpoint) *CallDialingBuilder {
	cb.event.Destination = e
	return cb
}

func (cb *CallDialingBuilder) DialString(s string) *CallDialingBuilder {
	cb.event.DialString = s
	return cb
}

func (cb *CallDialingBuilder) DialTimeout(seconds int) *CallDialingBuilder {
	cb.event.DialTimeout = seconds
	return cb
}

func (cb *CallDialingBuilder) CallerID(e Endpoint) *CallDialingBuilder {
	cb.event.CallerID = e
	return cb
}

func (cb *CallDialingBuilder) Bridge(bridgeID, linkedCallUUID string) *CallDialingBuilder {
	cb.event.BridgeID = bridgeID
	cb.event.LinkedCallUUID = linkedCallUUID
	return cb
}

func (cb *CallDialingBuilder) Leg(leg LegRole) *CallDialingBuilder {
	cb.event.Leg = leg
	return cb
}

func (cb *CallDialingBuilder) Build() *CallDialingEvent {
	return cb.event
}

// CallRingingBuilder constructs CallRingingEvent.
type CallRingingBuilder struct {
	event *CallRingingEvent
}

// CallRinging starts building a CallRingingEvent.
func (b *Builder) CallRinging(callUUID, sipCallID string) *CallRingingBuilder {
	return &CallRingingBuilder{
		event: &CallRingingEvent{
			BaseEvent: b.newBase(CallRinging, callUUID, sipCallID),
		},
	}
}

func (cb *CallRingingBuilder) ResponseCode(code int) *CallRingingBuilder {
	cb.event.ResponseCode = code
	return cb
}

func (cb *CallRingingBuilder) EarlyMedia(hasMedia bool) *CallRingingBuilder {
	cb.event.EarlyMedia = hasMedia
	return cb
}

func (cb *CallRingingBuilder) Media(m *MediaInfo) *CallRingingBuilder {
	cb.event.MediaInfo = m
	return cb
}

func (cb *CallRingingBuilder) Leg(leg LegRole) *CallRingingBuilder {
	cb.event.Leg = leg
	return cb
}

func (cb *CallRingingBuilder) Build() *CallRingingEvent {
	return cb.event
}

// CallAnsweredBuilder constructs CallAnsweredEvent.
type CallAnsweredBuilder struct {
	event *CallAnsweredEvent
}

// CallAnswered starts building a CallAnsweredEvent.
func (b *Builder) CallAnswered(callUUID, sipCallID string) *CallAnsweredBuilder {
	return &CallAnsweredBuilder{
		event: &CallAnsweredEvent{
			BaseEvent:    b.newBase(CallAnswered, callUUID, sipCallID),
			ResponseCode: 200,
		},
	}
}

func (cb *CallAnsweredBuilder) ResponseCode(code int) *CallAnsweredBuilder {
	cb.event.ResponseCode = code
	return cb
}

func (cb *CallAnsweredBuilder) Media(m *MediaInfo) *CallAnsweredBuilder {
	cb.event.MediaInfo = m
	return cb
}

func (cb *CallAnsweredBuilder) SetupDuration(d time.Duration) *CallAnsweredBuilder {
	cb.event.SetupDurationMs = d.Milliseconds()
	return cb
}

func (cb *CallAnsweredBuilder) RingDuration(d time.Duration) *CallAnsweredBuilder {
	cb.event.RingDurationMs = d.Milliseconds()
	return cb
}

func (cb *CallAnsweredBuilder) Leg(leg LegRole) *CallAnsweredBuilder {
	cb.event.Leg = leg
	return cb
}

func (cb *CallAnsweredBuilder) Build() *CallAnsweredEvent {
	return cb.event
}

// CallBridgedBuilder constructs CallBridgedEvent.
type CallBridgedBuilder struct {
	event *CallBridgedEvent
}

// CallBridged starts building a CallBridgedEvent.
func (b *Builder) CallBridged(callUUID, sipCallID, bridgeID string) *CallBridgedBuilder {
	base := b.newBase(CallBridged, callUUID, sipCallID)
	base.BridgeID = bridgeID
	return &CallBridgedBuilder{
		event: &CallBridgedEvent{
			BaseEvent: base,
		},
	}
}

func (cb *CallBridgedBuilder) LegAMedia(m *MediaInfo) *CallBridgedBuilder {
	cb.event.LegAMedia = m
	return cb
}

func (cb *CallBridgedBuilder) LegBMedia(m *MediaInfo) *CallBridgedBuilder {
	cb.event.LegBMedia = m
	return cb
}

func (cb *CallBridgedBuilder) BridgeCodec(codec string) *CallBridgedBuilder {
	cb.event.BridgeCodec = codec
	return cb
}

func (cb *CallBridgedBuilder) Transcoding(active bool) *CallBridgedBuilder {
	cb.event.Transcoding = active
	return cb
}

func (cb *CallBridgedBuilder) Build() *CallBridgedEvent {
	return cb.event
}

// CallEndedBuilder constructs CallEndedEvent.
type CallEndedBuilder struct {
	event *CallEndedEvent
}

// CallEnded starts building a CallEndedEvent.
func (b *Builder) CallEnded(callUUID, sipCallID string) *CallEndedBuilder {
	return &CallEndedBuilder{
		event: &CallEndedEvent{
			BaseEvent: b.newBase(CallEnded, callUUID, sipCallID),
		},
	}
}

func (cb *CallEndedBuilder) Reason(r EndReason, detail string) *CallEndedBuilder {
	cb.event.EndReason = r
	cb.event.EndReasonDetail = detail
	return cb
}

func (cb *CallEndedBuilder) SIPResponse(code int, reason string) *CallEndedBuilder {
	cb.event.SIPResponseCode = code
	cb.event.SIPResponseReason = reason
	return cb
}

func (cb *CallEndedBuilder) HangupSource(source string) *CallEndedBuilder {
	cb.event.HangupSource = source
	return cb
}

func (cb *CallEndedBuilder) Durations(setup, ring, talk, total time.Duration) *CallEndedBuilder {
	cb.event.SetupDurationMs = setup.Milliseconds()
	cb.event.RingDurationMs = ring.Milliseconds()
	cb.event.TalkDurationMs = talk.Milliseconds()
	cb.event.TotalDurationMs = total.Milliseconds()
	return cb
}

func (cb *CallEndedBuilder) BillableDuration(d time.Duration) *CallEndedBuilder {
	cb.event.BillableDurationMs = d.Milliseconds()
	return cb
}

func (cb *CallEndedBuilder) Disposition(code string) *CallEndedBuilder {
	cb.event.DispositionCode = code
	return cb
}

func (cb *CallEndedBuilder) MediaStats(sent, received, lost uint64, jitterMs int) *CallEndedBuilder {
	cb.event.PacketsSent = sent
	cb.event.PacketsReceived = received
	cb.event.PacketsLost = lost
	cb.event.JitterMs = jitterMs
	return cb
}

func (cb *CallEndedBuilder) Leg(leg LegRole) *CallEndedBuilder {
	cb.event.Leg = leg
	return cb
}

func (cb *CallEndedBuilder) Bridge(bridgeID string) *CallEndedBuilder {
	cb.event.BridgeID = bridgeID
	return cb
}

func (cb *CallEndedBuilder) Build() *CallEndedEvent {
	return cb.event
}
