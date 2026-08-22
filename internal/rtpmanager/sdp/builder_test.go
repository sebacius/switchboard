package sdp

import (
	"strings"
	"testing"
)

// The rtpmap and fmtp lines for telephone-event were written here long ago and
// were unreachable, because the formats list was hard-coded to one element.
// This is the test that they now reach the wire.
func TestAnswerCarriesTelephoneEvent(t *testing.T) {
	body := string(BuildAnswerSDP("192.0.2.1", 10000, 0, 101, "0-15"))

	for _, want := range []string{
		"m=audio 10000 RTP/AVP 0 101",
		"a=rtpmap:0 PCMU/8000",
		"a=rtpmap:101 telephone-event/8000",
		"a=fmtp:101 0-15",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("answer missing %q:\n%s", want, body)
		}
	}
}

// The fmtp must name whatever payload type was negotiated, not a fixed 101.
func TestAnswerEchoesNonStandardPayloadType(t *testing.T) {
	body := string(BuildAnswerSDP("192.0.2.1", 10000, 0, 96, "0-15"))

	for _, want := range []string{
		"m=audio 10000 RTP/AVP 0 96",
		"a=rtpmap:96 telephone-event/8000",
		"a=fmtp:96 0-15",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("answer missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "101") {
		t.Errorf("answer should not mention 101 when 96 was negotiated:\n%s", body)
	}
}

// An offer without telephone-event must be answered without it.
func TestAnswerWithoutTelephoneEvent(t *testing.T) {
	body := string(BuildAnswerSDP("192.0.2.1", 10000, 0, 0, ""))

	if !strings.Contains(body, "m=audio 10000 RTP/AVP 0") {
		t.Errorf("answer missing the audio m-line:\n%s", body)
	}
	if strings.Contains(body, "telephone-event") {
		t.Errorf("answer invented a DTMF transport that was never offered:\n%s", body)
	}
}

// The offerer's own fmtp is echoed when they supplied one.
func TestAnswerEchoesOfferedFMTP(t *testing.T) {
	body := string(BuildAnswerSDP("192.0.2.1", 10000, 0, 101, "0-16"))

	if !strings.Contains(body, "a=fmtp:101 0-16") {
		t.Errorf("answer should echo the offered fmtp:\n%s", body)
	}
}

// The single-codec form still works for callers that know nothing about DTMF.
func TestLegacySingleCodecAnswer(t *testing.T) {
	body := string(BuildResponseSDP("192.0.2.1", 10000, "0"))

	if !strings.Contains(body, "m=audio 10000 RTP/AVP 0") {
		t.Errorf("legacy answer missing the audio m-line:\n%s", body)
	}
	if strings.Contains(body, "telephone-event") {
		t.Errorf("legacy answer should carry audio only:\n%s", body)
	}
}
