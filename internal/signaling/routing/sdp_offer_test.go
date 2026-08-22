package routing

import (
	"testing"

	psdp "github.com/pion/sdp/v3"
)

// parseOffer extracts the first media description from an SDP body.
func parseOffer(t *testing.T, body string) *psdp.MediaDescription {
	t.Helper()
	var desc psdp.SessionDescription
	if err := desc.Unmarshal([]byte(body)); err != nil {
		t.Fatalf("fixture is not valid SDP: %v", err)
	}
	if len(desc.MediaDescriptions) == 0 {
		t.Fatal("fixture has no media descriptions")
	}
	return desc.MediaDescriptions[0]
}

// Discarding a=rtpmap is what made the peer's DTMF payload type unknowable.
// Telephone-event is dynamic, so the number alone means nothing.
func TestCodecOffersCarryRTPMap(t *testing.T) {
	media := parseOffer(t, "v=0\r\n"+
		"o=- 1 1 IN IP4 192.0.2.9\r\n"+
		"s=-\r\n"+
		"c=IN IP4 192.0.2.9\r\n"+
		"t=0 0\r\n"+
		"m=audio 40000 RTP/AVP 0 101\r\n"+
		"a=rtpmap:0 PCMU/8000\r\n"+
		"a=rtpmap:101 telephone-event/8000\r\n"+
		"a=fmtp:101 0-15\r\n")

	offers := codecOffersFrom(media)
	if len(offers) != 2 {
		t.Fatalf("expected 2 offers, got %d: %+v", len(offers), offers)
	}

	if offers[0].PayloadType != 0 || offers[0].EncodingName != "PCMU" || offers[0].ClockRate != 8000 {
		t.Errorf("audio offer = %+v, want PCMU/8000 on pt 0", offers[0])
	}
	if offers[1].PayloadType != 101 || offers[1].EncodingName != "telephone-event" {
		t.Errorf("dtmf offer = %+v, want telephone-event on pt 101", offers[1])
	}
	if offers[1].FMTP != "0-15" {
		t.Errorf("fmtp = %q, want 0-15", offers[1].FMTP)
	}
}

// Real endpoints put telephone-event anywhere in 96-127. Assuming 101 works in
// a lab and fails in the field.
func TestCodecOffersHandleNonStandardPayloadType(t *testing.T) {
	media := parseOffer(t, "v=0\r\n"+
		"o=- 1 1 IN IP4 192.0.2.9\r\n"+
		"s=-\r\n"+
		"c=IN IP4 192.0.2.9\r\n"+
		"t=0 0\r\n"+
		"m=audio 40000 RTP/AVP 0 96\r\n"+
		"a=rtpmap:0 PCMU/8000\r\n"+
		"a=rtpmap:96 telephone-event/8000\r\n")

	offers := codecOffersFrom(media)
	if len(offers) != 2 {
		t.Fatalf("expected 2 offers, got %d", len(offers))
	}
	if offers[1].PayloadType != 96 || offers[1].EncodingName != "telephone-event" {
		t.Errorf("offer = %+v, want telephone-event on pt 96", offers[1])
	}
}

// A static payload type carries no rtpmap in many offers; the number defines it.
func TestCodecOffersTolerateMissingRTPMap(t *testing.T) {
	media := parseOffer(t, "v=0\r\n"+
		"o=- 1 1 IN IP4 192.0.2.9\r\n"+
		"s=-\r\n"+
		"c=IN IP4 192.0.2.9\r\n"+
		"t=0 0\r\n"+
		"m=audio 40000 RTP/AVP 0\r\n")

	offers := codecOffersFrom(media)
	if len(offers) != 1 {
		t.Fatalf("expected 1 offer, got %d", len(offers))
	}
	if offers[0].PayloadType != 0 {
		t.Errorf("payload type = %d, want 0", offers[0].PayloadType)
	}
	if offers[0].EncodingName != "" {
		t.Errorf("encoding = %q, want empty for a static type with no rtpmap", offers[0].EncodingName)
	}
}

// A stereo rtpmap carries a channel count after the clock rate.
func TestCodecOffersParseChannelCount(t *testing.T) {
	media := parseOffer(t, "v=0\r\n"+
		"o=- 1 1 IN IP4 192.0.2.9\r\n"+
		"s=-\r\n"+
		"c=IN IP4 192.0.2.9\r\n"+
		"t=0 0\r\n"+
		"m=audio 40000 RTP/AVP 0 111\r\n"+
		"a=rtpmap:0 PCMU/8000\r\n"+
		"a=rtpmap:111 opus/48000/2\r\n")

	offers := codecOffersFrom(media)
	if len(offers) != 2 {
		t.Fatalf("expected 2 offers, got %d", len(offers))
	}
	if offers[1].EncodingName != "opus" || offers[1].ClockRate != 48000 {
		t.Errorf("offer = %+v, want opus at 48000", offers[1])
	}
}
