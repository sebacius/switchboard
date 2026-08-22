package agent

import (
	"errors"
	"fmt"

	"github.com/sebas/switchboard/internal/signaling/b2bua"
)

// A dial that does not answer has to be told apart from one that does, and told
// apart from the OTHER ways it can fail, or a flow cannot route "busy" somewhere
// different from "nobody home".
//
// The information already existed — b2bua distinguishes 486 from 480 from a
// timeout — it was simply collapsed at the session boundary, where Forward
// relayed a status upstream and returned an opaque error. That relay is the
// reason a typed outcome was needed: once a 486 has been sent to the caller,
// their call is over, and no amount of graph after that point can run. So the
// outcome-returning path deliberately relays NOTHING and lets the flow decide
// what the caller hears.

// DialResult is why a dial ended.
type DialResult int

const (
	// DialAnswered means the target picked up and the legs are bridged. This is
	// terminal: the flow is over.
	DialAnswered DialResult = iota
	// DialNoAnswer means it rang out.
	DialNoAnswer
	// DialBusy means the target was on the phone (486).
	DialBusy
	// DialRejected means the target actively declined (a 4xx/6xx that is not
	// busy or unavailable).
	DialRejected
	// DialUnavailable means the target could not be reached at all: not
	// registered, no contacts, or 480.
	DialUnavailable
	// DialFailed is everything else — a configuration or transport problem
	// rather than a statement about the callee.
	DialFailed
)

// String renders the result as the flow exit name it corresponds to, so logs
// and CDR hops read the same as the configuration an operator wrote.
func (r DialResult) String() string {
	switch r {
	case DialAnswered:
		return "answered"
	case DialNoAnswer:
		return "no_answer"
	case DialBusy:
		return "busy"
	case DialRejected:
		return "rejected"
	case DialUnavailable:
		return "unavailable"
	case DialFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// DialOutcome is the full result of one dial attempt.
type DialOutcome struct {
	Result DialResult
	Target string
	// SIPCode and SIPReason carry the callee's own final response when there was
	// one, so a flow that ends without connecting can relay something honest.
	SIPCode   int
	SIPReason string
	// Err is the underlying failure, for logs. Nil when answered.
	Err error
}

// Answered reports whether the call connected.
func (o DialOutcome) Answered() bool { return o.Result == DialAnswered }

// ExitName is the flow exit this outcome takes.
func (o DialOutcome) ExitName() string { return o.Result.String() }

// GroupOutcome is the result of ringing a ring group: the winning leg's outcome,
// plus what each member did. Per-member detail is what makes "everyone was busy"
// distinguishable from "nobody answered" in a call record.
type GroupOutcome struct {
	DialOutcome
	Members []DialOutcome
}

// classifyDialError maps a dial failure onto a DialResult. Everything it needs
// is already distinguished by b2bua; this is the translation, in one place, so
// every caller agrees on what a 486 means.
func classifyDialError(target string, err error) DialOutcome {
	out := DialOutcome{Target: target, Err: err, Result: DialFailed}
	if err == nil {
		out.Result = DialAnswered
		return out
	}

	// A target that never had a registration is unavailable, not rejected: the
	// distinction matters to a flow that wants to try someone else versus one
	// that wants to take a message.
	switch {
	case errors.Is(err, b2bua.ErrNoContacts),
		errors.Is(err, b2bua.ErrTargetNotFound),
		errors.Is(err, ErrUserNotFound):
		out.Result = DialUnavailable
		return out
	case errors.Is(err, b2bua.ErrDialTimeout), errors.Is(err, ErrGroupNoAnswer):
		out.Result = DialNoAnswer
		return out
	case errors.Is(err, b2bua.ErrDialCanceled):
		// The caller hung up. Not a statement about the callee at all.
		out.Result = DialFailed
		return out
	}

	var dialErr *b2bua.DialError
	if errors.As(err, &dialErr) {
		out.SIPCode, out.SIPReason = dialErr.SIPCode, dialErr.SIPReason
		switch {
		case dialErr.IsBusy():
			out.Result = DialBusy
		case dialErr.IsUnavailable():
			out.Result = DialUnavailable
		case dialErr.IsTimeout():
			out.Result = DialNoAnswer
		case dialErr.SIPCode >= 400 && dialErr.SIPCode <= 699:
			out.Result = DialRejected
		}
		return out
	}

	var agentErr *DialError
	if errors.As(err, &agentErr) {
		out.SIPCode, out.SIPReason = agentErr.SIPCode, agentErr.SIPReason
		switch {
		case agentErr.SIPCode == 486:
			out.Result = DialBusy
		case agentErr.SIPCode == 480:
			out.Result = DialUnavailable
		case agentErr.SIPCode == 408:
			out.Result = DialNoAnswer
		case agentErr.SIPCode >= 400 && agentErr.SIPCode <= 699:
			out.Result = DialRejected
		}
		return out
	}

	return out
}

// Error renders an outcome as an error for callers that still want one.
func (o DialOutcome) Error() error {
	if o.Answered() {
		return nil
	}
	if o.Err != nil {
		return o.Err
	}
	return fmt.Errorf("dial %s: %s", o.Target, o.Result)
}
