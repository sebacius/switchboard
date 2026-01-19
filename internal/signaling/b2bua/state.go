// Package b2bua provides B2BUA (Back-to-Back User Agent) primitives
// for call origination and bridging.
package b2bua

import "fmt"

// LegState represents the current state of a call leg.
type LegState int

const (
	// LegStateCreated indicates the leg has been initialized but no signaling sent.
	LegStateCreated LegState = iota
	// LegStateRinging indicates provisional response (180/183) received.
	LegStateRinging
	// LegStateEarlyMedia indicates 183 with SDP received (early media available).
	LegStateEarlyMedia
	// LegStateAnswered indicates 200 OK received, ACK sent, media flowing.
	LegStateAnswered
	// LegStateFailed indicates the leg failed to establish.
	LegStateFailed
	// LegStateDestroyed indicates the leg has been terminated.
	LegStateDestroyed
)

// String returns the string representation of LegState.
func (s LegState) String() string {
	switch s {
	case LegStateCreated:
		return "Created"
	case LegStateRinging:
		return "Ringing"
	case LegStateEarlyMedia:
		return "EarlyMedia"
	case LegStateAnswered:
		return "Answered"
	case LegStateFailed:
		return "Failed"
	case LegStateDestroyed:
		return "Destroyed"
	default:
		return fmt.Sprintf("Unknown(%d)", s)
	}
}

// IsTerminal returns true if the leg is in a terminal state.
func (s LegState) IsTerminal() bool {
	return s == LegStateFailed || s == LegStateDestroyed
}

// LegDirection indicates whether the leg is inbound or outbound.
type LegDirection int

const (
	// LegDirectionInbound represents an incoming call (A leg).
	LegDirectionInbound LegDirection = iota
	// LegDirectionOutbound represents an outgoing call (B leg).
	LegDirectionOutbound
)

// String returns the string representation of LegDirection.
func (d LegDirection) String() string {
	switch d {
	case LegDirectionInbound:
		return "Inbound"
	case LegDirectionOutbound:
		return "Outbound"
	default:
		return fmt.Sprintf("Unknown(%d)", d)
	}
}

// BridgeState represents the current state of a bridge.
type BridgeState int

const (
	// BridgeStateCreated indicates the bridge has been created but not active.
	BridgeStateCreated BridgeState = iota
	// BridgeStatePartial indicates one leg is ready, waiting for the other.
	BridgeStatePartial
	// BridgeStateActive indicates both legs are connected and media is flowing.
	BridgeStateActive
	// BridgeStateHeld indicates the bridge is on hold.
	BridgeStateHeld
	// BridgeStateTerminating indicates the bridge is being torn down.
	BridgeStateTerminating
	// BridgeStateTerminated indicates the bridge has been destroyed.
	BridgeStateTerminated
)

// String returns the string representation of BridgeState.
func (s BridgeState) String() string {
	switch s {
	case BridgeStateCreated:
		return "Created"
	case BridgeStatePartial:
		return "Partial"
	case BridgeStateActive:
		return "Active"
	case BridgeStateHeld:
		return "Held"
	case BridgeStateTerminating:
		return "Terminating"
	case BridgeStateTerminated:
		return "Terminated"
	default:
		return fmt.Sprintf("Unknown(%d)", s)
	}
}

// IsTerminal returns true if the bridge is in a terminal state.
func (s BridgeState) IsTerminal() bool {
	return s == BridgeStateTerminated
}

// TerminationCause indicates why a leg or bridge was terminated.
type TerminationCause int

const (
	// TerminationCauseNone indicates no termination has occurred.
	TerminationCauseNone TerminationCause = iota
	// TerminationCauseNormal indicates a normal hangup (BYE sent/received).
	TerminationCauseNormal
	// TerminationCauseCancel indicates the call was canceled before answer.
	TerminationCauseCancel
	// TerminationCauseRejected indicates the call was rejected (4xx/6xx).
	TerminationCauseRejected
	// TerminationCauseTimeout indicates a timeout occurred.
	TerminationCauseTimeout
	// TerminationCauseError indicates an internal error occurred.
	TerminationCauseError
	// TerminationCauseBridgePeer indicates the peer leg in the bridge hung up.
	TerminationCauseBridgePeer
	// TerminationCauseTransfer indicates a call transfer.
	TerminationCauseTransfer
	// TerminationCauseRemoteBYE indicates the remote party sent BYE.
	TerminationCauseRemoteBYE
)

// String returns the string representation of TerminationCause.
func (c TerminationCause) String() string {
	switch c {
	case TerminationCauseNone:
		return "None"
	case TerminationCauseNormal:
		return "Normal"
	case TerminationCauseCancel:
		return "Cancel"
	case TerminationCauseRejected:
		return "Rejected"
	case TerminationCauseTimeout:
		return "Timeout"
	case TerminationCauseError:
		return "Error"
	case TerminationCauseBridgePeer:
		return "BridgePeer"
	case TerminationCauseTransfer:
		return "Transfer"
	case TerminationCauseRemoteBYE:
		return "RemoteBYE"
	default:
		return fmt.Sprintf("Unknown(%d)", c)
	}
}
