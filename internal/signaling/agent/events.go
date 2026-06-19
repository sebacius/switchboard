// Package agent implements the LLM call supervisor: an event-loop runner that
// owns a call from INVITE to teardown, dispatching native tool calls to call-
// control handlers. It replaces the JSON dialplan + text-protocol AI agent.
package agent

// EventKind identifies the source of a runner Event. Speech is the only producer
// today; the rest are forward-compatible slots so future producers (DTMF,
// signaling, media) can write to the same events channel without a refactor.
type EventKind int

const (
	// EventSpeech is a transcribed caller utterance (ASR).
	EventSpeech EventKind = iota
	// EventDTMF is one or more DTMF digits. Future producer.
	EventDTMF
	// EventSignaling is a SIP signaling change (e.g. a leg answered/ended). Future producer.
	EventSignaling
	// EventMedia is a media-layer notification (e.g. recording started/failed). Future producer.
	EventMedia
)

// String renders an EventKind for logs.
func (k EventKind) String() string {
	switch k {
	case EventSpeech:
		return "speech"
	case EventDTMF:
		return "dtmf"
	case EventSignaling:
		return "signaling"
	case EventMedia:
		return "media"
	default:
		return "unknown"
	}
}

// Event is a single item on the runner's events channel. The payload is an
// intentionally minimal string so the channel topology stays simple; richer
// event data, if ever needed, is a bounded change to this type.
type Event struct {
	Kind    EventKind
	Payload string
}
