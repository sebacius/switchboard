package events

import "fmt"

// Subject naming conventions for NATS.
//
// Hierarchy:
//   switchboard.calls.<call_uuid>.<event_suffix>  - Per-call events
//   switchboard.cdr.raw                           - Raw CDR stream
//   switchboard.cdr.rated                         - Post-rating CDR stream
//   switchboard.registrations.<endpoint>          - SIP registration events
//   switchboard.sessions.<session_id>.media       - RTP session events
//
// Wildcard subscriptions:
//   switchboard.calls.>                           - All call events
//   switchboard.calls.*.ended                     - All call.ended events
//   switchboard.calls.<call_uuid>.*               - All events for one call

const (
	// SubjectPrefix is the root of all switchboard subjects
	SubjectPrefix = "switchboard"

	// Call event subjects
	SubjectCalls        = SubjectPrefix + ".calls"
	SubjectCallReceived = "received"
	SubjectCallDialing  = "dialing"
	SubjectCallRinging  = "ringing"
	SubjectCallAnswered = "answered"
	SubjectCallBridged  = "bridged"
	SubjectCallEnded    = "ended"

	// CDR subjects
	SubjectCDRRaw   = SubjectPrefix + ".cdr.raw"
	SubjectCDRRated = SubjectPrefix + ".cdr.rated"

	// Registration subjects
	SubjectRegistrations = SubjectPrefix + ".registrations"

	// Media session subjects
	SubjectSessions = SubjectPrefix + ".sessions"
)

// CallSubject builds a subject for a specific call event.
// Example: CallSubject("abc-123", "ended") => "switchboard.calls.abc-123.ended"
func CallSubject(callUUID string, eventSuffix string) string {
	return fmt.Sprintf("%s.%s.%s", SubjectCalls, callUUID, eventSuffix)
}

// RegistrationSubject builds a subject for registration events.
// Example: RegistrationSubject("alice@example.com") => "switchboard.registrations.alice@example.com"
func RegistrationSubject(endpoint string) string {
	return fmt.Sprintf("%s.%s", SubjectRegistrations, endpoint)
}

// SessionSubject builds a subject for RTP session events.
// Example: SessionSubject("sess-123", "started") => "switchboard.sessions.sess-123.started"
func SessionSubject(sessionID string, event string) string {
	return fmt.Sprintf("%s.%s.%s", SubjectSessions, sessionID, event)
}

// Subject patterns for common consumer configurations
var (
	// PatternAllCalls matches all call events
	PatternAllCalls = SubjectCalls + ".>"

	// PatternCallEnded matches all call.ended events (for CDR)
	PatternCallEnded = SubjectCalls + ".*.ended"

	// PatternCallAnswered matches all call.answered events (for billing)
	PatternCallAnswered = SubjectCalls + ".*.answered"

	// PatternAllRegistrations matches all registration events
	PatternAllRegistrations = SubjectRegistrations + ".>"

	// PatternAllSessions matches all RTP session events
	PatternAllSessions = SubjectSessions + ".>"
)

// SubjectForEventType returns the suffix used for a given event type.
func SubjectForEventType(t EventType) string {
	switch t {
	case CallReceived:
		return SubjectCallReceived
	case CallDialing:
		return SubjectCallDialing
	case CallRinging:
		return SubjectCallRinging
	case CallAnswered:
		return SubjectCallAnswered
	case CallBridged:
		return SubjectCallBridged
	case CallEnded:
		return SubjectCallEnded
	default:
		return "unknown"
	}
}
