package agent

import (
	"strings"
	"testing"
)

func TestCallContextFormatForPrompt(t *testing.T) {
	cc := CallContext{
		Caller:    "102",
		Callee:    "105",
		Direction: DirectionInternal,
		Tenant:    "acme",
	}
	out := cc.FormatForPrompt()

	if !strings.HasPrefix(out, "# Call Context") {
		t.Fatalf("expected Call Context header, got:\n%s", out)
	}
	for _, want := range []string{"Caller: 102", "Callee: 105", "Direction: internal", "Tenant: acme"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestEventKindString(t *testing.T) {
	cases := map[EventKind]string{
		EventSpeech:    "speech",
		EventDTMF:      "dtmf",
		EventSignaling: "signaling",
		EventMedia:     "media",
		EventKind(99):  "unknown",
	}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Fatalf("EventKind(%d).String() = %q, want %q", int(k), got, want)
		}
	}
}
