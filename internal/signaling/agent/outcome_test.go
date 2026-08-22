package agent

import (
	"errors"
	"testing"

	"github.com/sebas/switchboard/internal/signaling/b2bua"
)

// Every way a dial can end must map to exactly one flow exit, or a graph cannot
// route "busy" somewhere different from "nobody home".
func TestClassifyDialError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want DialResult
	}{
		{"answered", nil, DialAnswered},
		{"busy", &b2bua.DialError{Target: "user/110", SIPCode: 486, SIPReason: "Busy Here"}, DialBusy},
		{"unavailable", &b2bua.DialError{Target: "user/110", SIPCode: 480}, DialUnavailable},
		{"rejected", &b2bua.DialError{Target: "user/110", SIPCode: 603}, DialRejected},
		{"decline", &b2bua.DialError{Target: "user/110", SIPCode: 403}, DialRejected},
		{"timeout as no answer", b2bua.ErrDialTimeout, DialNoAnswer},
		{"no contacts is unavailable", b2bua.ErrNoContacts, DialUnavailable},
		{"unknown target is unavailable", b2bua.ErrTargetNotFound, DialUnavailable},
		{"group no answer", ErrGroupNoAnswer, DialNoAnswer},
		{"caller cancelled", b2bua.ErrDialCanceled, DialFailed},
		{"anything else", errors.New("transport exploded"), DialFailed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyDialError("user/110", tc.err)
			if got.Result != tc.want {
				t.Errorf("classify(%v) = %s, want %s", tc.err, got.Result, tc.want)
			}
		})
	}
}

// The exit names must match what an operator writes in a flow file, or the
// configuration and the code disagree about the same word.
func TestResultNamesMatchFlowExits(t *testing.T) {
	want := map[DialResult]string{
		DialAnswered:    "answered",
		DialNoAnswer:    "no_answer",
		DialBusy:        "busy",
		DialRejected:    "rejected",
		DialUnavailable: "unavailable",
		DialFailed:      "failed",
	}
	for result, name := range want {
		if got := result.String(); got != name {
			t.Errorf("DialResult(%d) = %q, want %q", int(result), got, name)
		}
	}
}

// The SIP code survives classification so a flow that ends without connecting
// can still relay something honest.
func TestOutcomeCarriesTheCalleeStatus(t *testing.T) {
	out := classifyDialError("user/110", &b2bua.DialError{
		Target: "user/110", SIPCode: 486, SIPReason: "Busy Here",
	})

	if out.SIPCode != 486 || out.SIPReason != "Busy Here" {
		t.Fatalf("outcome lost the callee's response: %+v", out)
	}
	if out.ExitName() != "busy" {
		t.Errorf("ExitName = %q, want busy", out.ExitName())
	}
}

// A group where every member was busy is materially different from one where
// nobody picked up, and the summary must preserve that.
func TestGroupSummaryDistinguishesBusyFromNoAnswer(t *testing.T) {
	cases := []struct {
		name    string
		members []DialOutcome
		want    DialResult
	}{
		{"all busy", []DialOutcome{{Result: DialBusy}, {Result: DialBusy}}, DialBusy},
		{"all unregistered", []DialOutcome{{Result: DialUnavailable}}, DialUnavailable},
		{"mixed", []DialOutcome{{Result: DialBusy}, {Result: DialNoAnswer}}, DialNoAnswer},
		{"nobody at all", nil, DialUnavailable},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := summariseGroup(tc.members); got != tc.want {
				t.Errorf("summariseGroup = %s, want %s", got, tc.want)
			}
		})
	}
}
