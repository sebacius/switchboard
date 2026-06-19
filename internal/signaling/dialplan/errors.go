// Package dialplan provides call routing based on JSON configuration.
package dialplan

import (
	"errors"
	"fmt"
)

// Sentinel errors for error checking with errors.Is
var (
	ErrNoRouteMatch    = errors.New("no matching route")
	ErrSessionCanceled = errors.New("session canceled")
	ErrActionNotFound  = errors.New("unknown action type")
	ErrDialTimeout     = errors.New("dial timeout")
	ErrDialRejected    = errors.New("dial rejected")
)

// ExecutionError captures partial execution state.
// Use errors.As to extract this from wrapped errors.
type ExecutionError struct {
	RouteID        string
	CompletedSteps int    // How many actions succeeded
	TotalSteps     int    // Total actions in route
	FailedAction   string // Type of action that failed
	Cause          error  // Underlying error
}

func (e *ExecutionError) Error() string {
	return fmt.Sprintf("route %s: action %d/%d (%s) failed: %v",
		e.RouteID, e.CompletedSteps+1, e.TotalSteps, e.FailedAction, e.Cause)
}

func (e *ExecutionError) Unwrap() error {
	return e.Cause
}
