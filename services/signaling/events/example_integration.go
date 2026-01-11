package events

import (
	"context"
	"log/slog"
	"time"
)

/*
INTEGRATION EXAMPLE

This file shows how to integrate event publishing into the existing
Switchboard signaling service. The events complement (not replace)
the existing gRPC communication with RTP Manager.

Key integration points:
1. InviteHandler.HandleINVITE - publish CallReceived
2. Dialog state transitions - publish CallRinging, CallAnswered
3. Dialog termination callback - publish CallEnded
4. Future B2BUA Dial action - publish CallDialing, CallBridged

The integration follows these principles:
- Events are fire-and-forget (async publishing)
- Event failures never block call processing
- NoopPublisher used until NATS is deployed
*/

// Example: Adding event publishing to InviteHandler
//
// Modify services/signaling/routing/invite.go:
//
// type InviteHandler struct {
//     transport       transport.Transport
//     advertiseAddr   string
//     port            int
//     audioFile       string
//     dialogMgr       *dialog.Manager
//     sessionRecorder SessionRecorder
//     events          events.Publisher    // <-- Add this
//     eventBuilder    *events.Builder     // <-- Add this
// }
//
// func NewInviteHandler(..., eventPub events.Publisher, nodeID string) *InviteHandler {
//     return &InviteHandler{
//         ...
//         events:       eventPub,
//         eventBuilder: events.NewBuilder(nodeID),
//     }
// }

// ExampleHandleINVITE shows event publishing in INVITE handler
func ExampleHandleINVITE() {
	// This would be inside InviteHandler.HandleINVITE

	pub := NewLoggingPublisher(slog.Default())
	builder := NewBuilder("switchboard-node-1")

	// Simulated values from actual INVITE
	callUUID := "550e8400-e29b-41d4-a716-446655440000"
	sipCallID := "abc123@192.168.1.100"

	// After creating dialog, publish CallReceived
	event := builder.CallReceived(callUUID, sipCallID).
		Direction(DirectionInbound).
		From(Endpoint{
			URI:         "sip:alice@example.com",
			DisplayName: "Alice",
			User:        "alice",
			Host:        "example.com",
		}).
		To(Endpoint{
			URI:  "sip:bob@switchboard.local",
			User: "bob",
			Host: "switchboard.local",
		}).
		Source("192.168.1.100", 5060).
		UserAgent("Obi200/1.0.0").
		OfferedCodecs([]string{"0", "8", "101"}). // PCMU, PCMA, telephone-event
		Leg(LegA).
		Build()

	// Async publish - never blocks call processing
	pub.PublishAsync(event)
}

// ExampleDialogTermination shows event publishing on call end
func ExampleDialogTermination() {
	// This would be in the dialog termination callback in app.go

	pub := NewLoggingPublisher(slog.Default())
	builder := NewBuilder("switchboard-node-1")

	callUUID := "550e8400-e29b-41d4-a716-446655440000"
	sipCallID := "abc123@192.168.1.100"

	// Timing calculations from dialog
	inviteTime := time.Now().Add(-30 * time.Second)
	answerTime := time.Now().Add(-25 * time.Second)
	endTime := time.Now()

	setupDuration := answerTime.Sub(inviteTime)
	talkDuration := endTime.Sub(answerTime)
	totalDuration := endTime.Sub(inviteTime)

	event := builder.CallEnded(callUUID, sipCallID).
		Reason(EndReasonNormal, "Normal call clearing").
		SIPResponse(200, "OK").
		HangupSource("local").
		Durations(setupDuration, 0, talkDuration, totalDuration).
		BillableDuration(talkDuration).
		Disposition(DispositionAnswered).
		MediaStats(15000, 14500, 50, 15). // packets sent/recv/lost, jitter
		Leg(LegA).
		Build()

	// Synchronous publish for call.ended (CDR-critical)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = pub.Publish(ctx, event)
}

// ExampleB2BUABridge shows events for B2BUA dial scenario
func ExampleB2BUABridge() {
	pub := NewLoggingPublisher(slog.Default())
	builder := NewBuilder("switchboard-node-1")

	// Leg A (inbound) already published CallReceived
	legACallUUID := "550e8400-e29b-41d4-a716-446655440000"
	legASIPCallID := "abc123@192.168.1.100"

	// Dialplan executes Dial action, creating leg B
	legBCallUUID := "550e8400-e29b-41d4-a716-446655440001"
	legBSIPCallID := "def456@switchboard.local"
	bridgeID := "br-550e8400" // Links both legs

	// 1. Publish CallDialing for leg B
	dialingEvent := builder.CallDialing(legBCallUUID, legBSIPCallID).
		Destination(Endpoint{
			URI:  "sip:+15551234567@carrier.com",
			User: "+15551234567",
			Host: "carrier.com",
		}).
		DialString("sofia/gateway/carrier/+15551234567").
		DialTimeout(30).
		CallerID(Endpoint{
			URI:  "sip:+15559876543@switchboard.local",
			User: "+15559876543",
		}).
		Bridge(bridgeID, legACallUUID).
		Leg(LegB).
		Build()

	pub.PublishAsync(dialingEvent)

	// 2. When 180 Ringing received from carrier
	ringingEvent := builder.CallRinging(legBCallUUID, legBSIPCallID).
		ResponseCode(180).
		EarlyMedia(false).
		Leg(LegB).
		Build()
	ringingEvent.BridgeID = bridgeID

	pub.PublishAsync(ringingEvent)

	// 3. When 200 OK received from carrier
	answeredEvent := builder.CallAnswered(legBCallUUID, legBSIPCallID).
		Media(&MediaInfo{
			LocalAddr:     "10.0.0.50",
			LocalPort:     16000,
			RemoteAddr:    "203.0.113.50",
			RemotePort:    20000,
			SelectedCodec: "PCMU",
			RTPSessionID:  "sess-leg-b",
		}).
		SetupDuration(5 * time.Second).
		RingDuration(3 * time.Second).
		Leg(LegB).
		Build()
	answeredEvent.BridgeID = bridgeID

	pub.PublishAsync(answeredEvent)

	// 4. When media bridge established
	bridgedEvent := builder.CallBridged(legACallUUID, legASIPCallID, bridgeID).
		LegAMedia(&MediaInfo{
			LocalAddr:     "10.0.0.50",
			LocalPort:     12000,
			RemoteAddr:    "192.168.1.100",
			RemotePort:    20000,
			SelectedCodec: "PCMU",
			RTPSessionID:  "sess-leg-a",
		}).
		LegBMedia(&MediaInfo{
			LocalAddr:     "10.0.0.50",
			LocalPort:     16000,
			RemoteAddr:    "203.0.113.50",
			RemotePort:    20000,
			SelectedCodec: "PCMU",
			RTPSessionID:  "sess-leg-b",
		}).
		BridgeCodec("PCMU").
		Transcoding(false).
		Build()

	pub.PublishAsync(bridgedEvent)
}

// ExampleAppStartup shows how to wire up the publisher
func ExampleAppStartup() {
	// In cmd/signaling/main.go or app.NewServer:

	// For development/testing without NATS:
	var eventPub Publisher = NewNoopPublisher()

	// For local debugging:
	// eventPub = NewLoggingPublisher(slog.Default())

	// For testing with assertions:
	// eventPub = NewChannelPublisher(1000)

	// For production with NATS:
	// natsCfg := DefaultNATSConfig()
	// natsCfg.URL = os.Getenv("NATS_URL")
	// eventPub, err = NewNATSPublisher(natsCfg, logger)
	// if err != nil {
	//     log.Fatal("Failed to connect to NATS:", err)
	// }

	// Multiple destinations:
	// eventPub = NewMultiPublisher(
	//     natsPublisher,
	//     NewChannelPublisher(1000), // Local CDR processor
	// )

	_ = eventPub // Pass to InviteHandler, etc.
}
