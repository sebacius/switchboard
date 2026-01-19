package events

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestEventSubjectNaming(t *testing.T) {
	builder := NewBuilder("test-node")

	event := builder.CallReceived("call-123", "sip-call-id").Build()

	expected := "switchboard.calls.call-123.received"
	if got := event.Subject(); got != expected {
		t.Errorf("Subject() = %q, want %q", got, expected)
	}
}

func TestCallReceivedEventJSON(t *testing.T) {
	builder := NewBuilder("test-node").WithTenant("tenant-abc")

	event := builder.CallReceived("call-123", "abc@192.168.1.1").
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
		Dialplan("default", "dp-001").
		UserAgent("Test/1.0").
		OfferedCodecs([]string{"0", "8"}).
		Leg(LegA).
		Build()

	data, err := json.MarshalIndent(event, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	t.Logf("CallReceivedEvent JSON:\n%s", string(data))

	// Verify key fields are present
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	checks := map[string]string{
		"event_type": "call.received",
		"call_uuid":  "call-123",
		"tenant_id":  "tenant-abc",
		"node_id":    "test-node",
		"direction":  "inbound",
		"leg":        "A",
	}

	for k, want := range checks {
		if got, ok := m[k].(string); !ok || got != want {
			t.Errorf("m[%q] = %v, want %q", k, m[k], want)
		}
	}
}

func TestCallEndedEventCDRFields(t *testing.T) {
	builder := NewBuilder("test-node")

	event := builder.CallEnded("call-123", "abc@192.168.1.1").
		Reason(EndReasonNormal, "Normal hangup").
		SIPResponse(200, "OK").
		HangupSource("remote").
		Durations(
			5*time.Second,   // setup
			2*time.Second,   // ring
			120*time.Second, // talk
			127*time.Second, // total
		).
		BillableDuration(120*time.Second).
		Disposition(DispositionAnswered).
		MediaStats(6000, 5900, 10, 20).
		Build()

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Failed to unmarshal: %v", err)
	}

	// Verify CDR-critical fields
	if got := m["setup_duration_ms"].(float64); got != 5000 {
		t.Errorf("setup_duration_ms = %v, want 5000", got)
	}
	if got := m["talk_duration_ms"].(float64); got != 120000 {
		t.Errorf("talk_duration_ms = %v, want 120000", got)
	}
	if got := m["billable_duration_ms"].(float64); got != 120000 {
		t.Errorf("billable_duration_ms = %v, want 120000", got)
	}
	if got := m["disposition_code"].(string); got != "ANSWERED" {
		t.Errorf("disposition_code = %v, want ANSWERED", got)
	}
}

func TestNoopPublisher(t *testing.T) {
	pub := NewNoopPublisher()
	builder := NewBuilder("test")

	event := builder.CallReceived("call-1", "sip-1").Build()

	// Should not error
	if err := pub.Publish(context.Background(), event); err != nil {
		t.Errorf("NoopPublisher.Publish() error = %v", err)
	}

	pub.PublishAsync(event)

	if err := pub.Flush(context.Background()); err != nil {
		t.Errorf("NoopPublisher.Flush() error = %v", err)
	}

	if err := pub.Close(); err != nil {
		t.Errorf("NoopPublisher.Close() error = %v", err)
	}
}

func TestChannelPublisher(t *testing.T) {
	pub := NewChannelPublisher(10)
	builder := NewBuilder("test")

	ctx := context.Background()

	// Publish events
	for i := 0; i < 5; i++ {
		event := builder.CallReceived("call-"+string(rune('0'+i)), "sip").Build()
		if err := pub.Publish(ctx, event); err != nil {
			t.Errorf("Publish() error = %v", err)
		}
	}

	// Receive events
	ch := pub.Events()
	for i := 0; i < 5; i++ {
		select {
		case e := <-ch:
			if e.Type() != CallReceived {
				t.Errorf("got type %v, want CallReceived", e.Type())
			}
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for event")
		}
	}

	pub.Close()
}

func TestChannelPublisherDropsOnFull(t *testing.T) {
	pub := NewChannelPublisher(2)
	builder := NewBuilder("test")

	ctx := context.Background()

	// Fill buffer
	pub.Publish(ctx, builder.CallReceived("call-1", "sip").Build())
	pub.Publish(ctx, builder.CallReceived("call-2", "sip").Build())

	// This should be dropped
	pub.Publish(ctx, builder.CallReceived("call-3", "sip").Build())

	if got := pub.DroppedCount(); got != 1 {
		t.Errorf("DroppedCount() = %d, want 1", got)
	}

	pub.Close()
}

func TestMultiPublisher(t *testing.T) {
	ch1 := NewChannelPublisher(10)
	ch2 := NewChannelPublisher(10)

	multi := NewMultiPublisher(ch1, ch2)
	builder := NewBuilder("test")

	event := builder.CallReceived("call-1", "sip").Build()
	if err := multi.Publish(context.Background(), event); err != nil {
		t.Errorf("MultiPublisher.Publish() error = %v", err)
	}

	// Both should receive the event
	select {
	case <-ch1.Events():
	case <-time.After(time.Second):
		t.Error("ch1 did not receive event")
	}

	select {
	case <-ch2.Events():
	case <-time.After(time.Second):
		t.Error("ch2 did not receive event")
	}

	multi.Close()
}

func TestB2BUACorrelation(t *testing.T) {
	builder := NewBuilder("test-node")
	bridgeID := "bridge-123"

	// Leg A: inbound call
	legAUUID := "call-leg-a"
	legAReceived := builder.CallReceived(legAUUID, "sip-leg-a").
		Leg(LegA).
		Build()

	// Leg B: outbound call (dial action)
	legBUUID := "call-leg-b"
	legBDialing := builder.CallDialing(legBUUID, "sip-leg-b").
		Bridge(bridgeID, legAUUID).
		Leg(LegB).
		Build()

	// Verify correlation
	if legBDialing.BridgeID != bridgeID {
		t.Errorf("BridgeID = %q, want %q", legBDialing.BridgeID, bridgeID)
	}
	if legBDialing.LinkedCallUUID != legAUUID {
		t.Errorf("LinkedCallUUID = %q, want %q", legBDialing.LinkedCallUUID, legAUUID)
	}

	// When bridge established
	bridgedEvent := builder.CallBridged(legAUUID, "sip-leg-a", bridgeID).Build()

	if bridgedEvent.BridgeID != bridgeID {
		t.Errorf("bridged.BridgeID = %q, want %q", bridgedEvent.BridgeID, bridgeID)
	}

	// Subjects for each leg
	subjectLegA := legAReceived.Subject()
	subjectLegB := legBDialing.Subject()

	t.Logf("Leg A subject: %s", subjectLegA)
	t.Logf("Leg B subject: %s", subjectLegB)

	// Verify they're on different subjects (per-call)
	if subjectLegA == subjectLegB {
		t.Error("Leg A and B should have different subjects")
	}
}

func TestSubjectPatterns(t *testing.T) {
	tests := []struct {
		name     string
		callUUID string
		evtType  EventType
		want     string
	}{
		{"received", "abc-123", CallReceived, "switchboard.calls.abc-123.received"},
		{"dialing", "abc-123", CallDialing, "switchboard.calls.abc-123.dialing"},
		{"ringing", "abc-123", CallRinging, "switchboard.calls.abc-123.ringing"},
		{"answered", "abc-123", CallAnswered, "switchboard.calls.abc-123.answered"},
		{"bridged", "abc-123", CallBridged, "switchboard.calls.abc-123.bridged"},
		{"ended", "abc-123", CallEnded, "switchboard.calls.abc-123.ended"},
	}

	builder := NewBuilder("test")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var event Event
			switch tt.evtType {
			case CallReceived:
				event = builder.CallReceived(tt.callUUID, "sip").Build()
			case CallDialing:
				event = builder.CallDialing(tt.callUUID, "sip").Build()
			case CallRinging:
				event = builder.CallRinging(tt.callUUID, "sip").Build()
			case CallAnswered:
				event = builder.CallAnswered(tt.callUUID, "sip").Build()
			case CallBridged:
				event = builder.CallBridged(tt.callUUID, "sip", "").Build()
			case CallEnded:
				event = builder.CallEnded(tt.callUUID, "sip").Build()
			}

			if got := event.Subject(); got != tt.want {
				t.Errorf("Subject() = %q, want %q", got, tt.want)
			}
		})
	}
}
