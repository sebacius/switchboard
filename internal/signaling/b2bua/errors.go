package b2bua

import (
	"errors"
	"fmt"
)

// Sentinel errors for use with errors.Is.
var (
	// ErrTargetNotFound indicates the dial target could not be resolved.
	ErrTargetNotFound = errors.New("target not found")

	// ErrNoContacts indicates the target exists but has no active registrations.
	ErrNoContacts = errors.New("no contacts available")

	// ErrLegNotAnswered indicates an operation requiring answered state.
	ErrLegNotAnswered = errors.New("leg not in answered state")

	// ErrLegTerminated indicates the leg has already terminated.
	ErrLegTerminated = errors.New("leg already terminated")

	// ErrBridgeActive indicates the bridge is already active.
	ErrBridgeActive = errors.New("bridge already active")

	// ErrBridgeTerminated indicates the bridge has already terminated.
	ErrBridgeTerminated = errors.New("bridge already terminated")

	// ErrDialTimeout indicates the dial attempt timed out.
	ErrDialTimeout = errors.New("dial timeout")

	// ErrDialCanceled indicates the dial was canceled.
	ErrDialCanceled = errors.New("dial canceled")

	// ErrNotImplemented indicates a feature is not yet implemented.
	ErrNotImplemented = errors.New("not implemented")

	// ErrInvalidState indicates an invalid state for the operation.
	ErrInvalidState = errors.New("invalid state for operation")

	// ErrCodecMismatch indicates incompatible codec negotiation.
	ErrCodecMismatch = errors.New("codec mismatch")
)

// DialError provides detailed information about a dial failure.
type DialError struct {
	// Target is the original dial target.
	Target string

	// ResolvedURI is the SIP URI that was dialed (if resolved).
	ResolvedURI string

	// SIPCode is the SIP response code (0 if not a SIP error).
	SIPCode int

	// SIPReason is the SIP response reason phrase.
	SIPReason string

	// Cause is the underlying error.
	Cause error
}

// Error returns the error message.
func (e *DialError) Error() string {
	if e.SIPCode > 0 {
		return fmt.Sprintf("dial %s: SIP %d %s", e.Target, e.SIPCode, e.SIPReason)
	}
	if e.Cause != nil {
		return fmt.Sprintf("dial %s: %v", e.Target, e.Cause)
	}
	return fmt.Sprintf("dial %s: unknown error", e.Target)
}

// Unwrap returns the underlying error.
func (e *DialError) Unwrap() error {
	return e.Cause
}

// IsTimeout returns true if this is a timeout error.
func (e *DialError) IsTimeout() bool {
	return errors.Is(e.Cause, ErrDialTimeout)
}

// IsRejected returns true if the call was rejected (4xx/6xx).
func (e *DialError) IsRejected() bool {
	return e.SIPCode >= 400 && e.SIPCode < 700
}

// IsBusy returns true if the callee is busy (486).
func (e *DialError) IsBusy() bool {
	return e.SIPCode == 486
}

// IsUnavailable returns true if the callee is unavailable (480).
func (e *DialError) IsUnavailable() bool {
	return e.SIPCode == 480
}

// StateTransitionError indicates an invalid state transition was attempted.
type StateTransitionError struct {
	Entity  string       // "leg" or "bridge"
	ID      string       // Entity identifier
	From    fmt.Stringer // Current state
	To      fmt.Stringer // Attempted state
	Message string       // Additional context
}

// Error returns the error message.
func (e *StateTransitionError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("%s %s: cannot transition from %s to %s: %s",
			e.Entity, e.ID, e.From, e.To, e.Message)
	}
	return fmt.Sprintf("%s %s: cannot transition from %s to %s",
		e.Entity, e.ID, e.From, e.To)
}

// Unwrap returns ErrInvalidState.
func (e *StateTransitionError) Unwrap() error {
	return ErrInvalidState
}

// LookupError indicates a target resolution failure.
type LookupError struct {
	Target  string
	Reason  string
	Cause   error
}

// Error returns the error message.
func (e *LookupError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("lookup %s: %s: %v", e.Target, e.Reason, e.Cause)
	}
	return fmt.Sprintf("lookup %s: %s", e.Target, e.Reason)
}

// Unwrap returns the underlying error.
func (e *LookupError) Unwrap() error {
	return e.Cause
}
