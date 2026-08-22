package routing

import (
	"testing"

	"github.com/emiago/sipgo/sip"
)

// infoRequest builds a SIP INFO carrying the given body.
func infoRequest(body string) *sip.Request {
	req := sip.NewRequest(sip.INFO, sip.Uri{User: "100", Host: "example.test"})
	req.SetBody([]byte(body))
	return req
}

// Both wire formats appear in the field, and an endpoint that can only speak
// one of them is exactly the endpoint that has no RFC 4733 either.
func TestParseINFODTMFFormats(t *testing.T) {
	cases := []struct {
		name string
		body string
		want rune
		ok   bool
	}{
		{"dtmf-relay", "Signal=1\r\nDuration=250", '1', true},
		{"dtmf-relay lowercase", "signal=7\r\nduration=100", '7', true},
		{"dtmf-relay star", "Signal=*\r\nDuration=250", '*', true},
		{"dtmf-relay numeric star", "Signal=10\r\nDuration=250", '*', true},
		{"dtmf-relay numeric hash", "Signal=11\r\nDuration=250", '#', true},
		{"bare digit", "5", '5', true},
		{"bare hash", "#", '#', true},
		{"empty", "", 0, false},
		{"unrecognized", "Hello", 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseINFODTMF(infoRequest(tc.body))
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Errorf("digit = %q, want %q", got, tc.want)
			}
		})
	}
}
