package session

import (
	"strings"
	"testing"
)

// The payload type is echoed, never assumed. Hard-coding 101 works against a
// softphone that happens to use 101 and silently fails against everything else,
// which is the failure this test exists to prevent.
func TestTelephoneEventPayloadTypeIsEchoed(t *testing.T) {
	for _, pt := range []int{101, 96, 100, 127} {
		offers := []CodecOffer{
			{PayloadType: 0, EncodingName: "PCMU", ClockRate: 8000},
			{PayloadType: pt, EncodingName: "telephone-event", ClockRate: 8000, FMTP: "0-15"},
		}

		spec, err := Negotiate(offers, nil)
		if err != nil {
			t.Fatalf("pt %d: Negotiate: %v", pt, err)
		}
		if spec.TelephoneEventPT != pt {
			t.Errorf("negotiated telephone-event pt = %d, want the offerer's %d", spec.TelephoneEventPT, pt)
		}
		if spec.AudioPT != 0 {
			t.Errorf("audio pt = %d, want 0 (PCMU)", spec.AudioPT)
		}
	}
}

// An answer may only contain formats from the offer. Inventing telephone-event
// would claim a DTMF transport the peer never agreed to, which then fails at the
// only moment it matters.
func TestOfferWithoutTelephoneEventIsAnsweredWithout(t *testing.T) {
	spec, err := Negotiate([]CodecOffer{{PayloadType: 0, EncodingName: "PCMU", ClockRate: 8000}}, nil)
	if err != nil {
		t.Fatalf("Negotiate: %v", err)
	}

	if spec.HasDTMF() {
		t.Fatalf("answered with telephone-event pt %d that was never offered", spec.TelephoneEventPT)
	}
	if got := spec.Formats(); len(got) != 1 || got[0] != "0" {
		t.Errorf("formats = %v, want just the audio codec", got)
	}
}

// Some endpoints omit the rtpmap for static payload types, where the number
// alone defines the codec.
func TestStaticPCMUNeedsNoRTPMap(t *testing.T) {
	spec, err := Negotiate([]CodecOffer{{PayloadType: 0}}, nil)
	if err != nil {
		t.Fatalf("PCMU without an rtpmap must still negotiate: %v", err)
	}
	if spec.AudioPT != 0 {
		t.Errorf("audio pt = %d, want 0", spec.AudioPT)
	}
}

// An older signaling server sends only the bare payload-type list, and audio
// must still negotiate.
func TestLegacyCodecListStillNegotiatesAudio(t *testing.T) {
	spec, err := Negotiate(nil, []string{"0", "8"})
	if err != nil {
		t.Fatalf("Negotiate: %v", err)
	}
	if spec.AudioPT != 0 {
		t.Errorf("audio pt = %d, want 0", spec.AudioPT)
	}
	if spec.HasDTMF() {
		t.Error("a bare codec list cannot identify telephone-event, so none should be negotiated")
	}
}

func TestOfferWithoutPCMUIsRejected(t *testing.T) {
	_, err := Negotiate([]CodecOffer{
		{PayloadType: 8, EncodingName: "PCMA", ClockRate: 8000},
		{PayloadType: 101, EncodingName: "telephone-event", ClockRate: 8000},
	}, nil)

	if err == nil {
		t.Fatal("an offer with no PCMU must be rejected")
	}
	if !strings.Contains(err.Error(), "PCMU") {
		t.Errorf("error should say what is required: %v", err)
	}
}

// Audio comes first in the answer's m= line.
func TestFormatsPutAudioFirst(t *testing.T) {
	spec := AnswerSpec{AudioPT: 0, TelephoneEventPT: 96}
	got := spec.Formats()
	if len(got) != 2 || got[0] != "0" || got[1] != "96" {
		t.Errorf("formats = %v, want [0 96]", got)
	}
}
