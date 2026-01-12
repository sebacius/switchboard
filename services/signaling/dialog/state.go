package dialog

import "fmt"

// CallState represents the lifecycle state of a SIP dialog
type CallState int

const (
	// StateInitial is the initial state when dialog is created
	StateInitial CallState = iota
	// StateEarly is after 1xx provisional response sent (100 Trying, 183 Session Progress)
	StateEarly
	// StateWaitingACK is after 200 OK sent, awaiting ACK
	StateWaitingACK
	// StateConfirmed is after ACK received, dialog is fully established
	StateConfirmed
	// StateTerminating is when BYE has been sent, awaiting response
	StateTerminating
	// StateTerminated is the final state after dialog ends
	StateTerminated
)

// String returns the string representation of the state
func (s CallState) String() string {
	switch s {
	case StateInitial:
		return "Initial"
	case StateEarly:
		return "Early"
	case StateWaitingACK:
		return "WaitingACK"
	case StateConfirmed:
		return "Confirmed"
	case StateTerminating:
		return "Terminating"
	case StateTerminated:
		return "Terminated"
	default:
		return fmt.Sprintf("Unknown(%d)", s)
	}
}

// validTransitions defines which state transitions are allowed
var validTransitions = map[CallState][]CallState{
	StateInitial:     {StateEarly, StateTerminated},
	StateEarly:       {StateWaitingACK, StateTerminated},
	StateWaitingACK:  {StateConfirmed, StateTerminated},
	StateConfirmed:   {StateTerminating, StateTerminated},
	StateTerminating: {StateTerminated},
	StateTerminated:  {}, // Terminal state, no transitions allowed
}

// CanTransitionTo checks if a transition from current state to next state is valid
func (s CallState) CanTransitionTo(next CallState) bool {
	allowed, ok := validTransitions[s]
	if !ok {
		return false
	}
	for _, state := range allowed {
		if state == next {
			return true
		}
	}
	return false
}

// IsTerminal returns true if this is a terminal state
func (s CallState) IsTerminal() bool {
	return s == StateTerminated
}

// TerminateReason explains why a dialog was terminated
type TerminateReason int

const (
	// ReasonLocalBYE means we initiated the BYE (e.g., playback complete)
	ReasonLocalBYE TerminateReason = iota
	// ReasonRemoteBYE means the remote party sent BYE
	ReasonRemoteBYE
	// ReasonCancel means CANCEL was received during early dialog
	ReasonCancel
	// ReasonTimeout means ACK or response timeout occurred
	ReasonTimeout
	// ReasonError means an error occurred
	ReasonError
)

// String returns the string representation of the termination reason
func (r TerminateReason) String() string {
	switch r {
	case ReasonLocalBYE:
		return "LocalBYE"
	case ReasonRemoteBYE:
		return "RemoteBYE"
	case ReasonCancel:
		return "Cancel"
	case ReasonTimeout:
		return "Timeout"
	case ReasonError:
		return "Error"
	default:
		return fmt.Sprintf("Unknown(%d)", r)
	}
}
