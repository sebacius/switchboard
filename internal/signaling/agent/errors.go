package agent

import (
	"errors"
	"fmt"
)

// ErrUserNotFound is returned when a dial target is not a registered user.
var ErrUserNotFound = errors.New("user not registered")

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
