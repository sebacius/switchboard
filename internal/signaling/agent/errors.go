package agent

import (
	"errors"
	"fmt"
)

// ErrUserNotFound is returned when a dial target is not a registered user.
var ErrUserNotFound = errors.New("user not registered")

// ErrGroupNoAnswer is returned by ForwardGroup when every round was exhausted
// without an answer. It is deliberately distinct from a dial failure: the call
// is still pre-answer and intact, so the group's configured no-answer outcome
// can run. Treating it as a plain failure would drop a caller the tenant meant
// to hand to a person.
var ErrGroupNoAnswer = errors.New("no ring group member answered")

// DialError provides details when a dial fails.
type DialError struct {
	Target    string
	SIPCode   int // 0 if not a SIP error
	SIPReason string
	Cause     error
}

func (e *DialError) Error() string {
	if e.SIPCode > 0 {
		return fmt.Sprintf("dial %s: SIP %d %s", e.Target, e.SIPCode, e.SIPReason)
	}
	return fmt.Sprintf("dial %s: %v", e.Target, e.Cause)
}

func (e *DialError) Unwrap() error {
	return e.Cause
}
