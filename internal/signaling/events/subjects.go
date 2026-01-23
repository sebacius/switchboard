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

// BuildCallSubject builds a subject for a specific call event.
// Example: BuildCallSubject("abc-123", "ended") => "switchboard.calls.abc-123.ended"
func BuildCallSubject(callUUID string, eventSuffix string) string {
	return fmt.Sprintf("%s.%s.%s", SubjectCalls, callUUID, eventSuffix)
}
