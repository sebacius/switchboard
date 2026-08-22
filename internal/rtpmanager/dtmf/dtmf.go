// Package dtmf decodes RFC 4733 telephone-event payloads and buffers the digits
// a caller presses.
//
// The wire format is four bytes:
//
//	 0                   1                   2                   3
//	 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|     event     |E|R| volume    |          duration             |
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//
// The subtlety is that ONE keypress is many packets. A sender transmits an event
// packet every 20ms for as long as the key is held, then repeats the final
// packet — the one with the End bit set — three times for redundancy. Counting
// packets would turn a single "1" into a dozen. The decoder therefore reports a
// digit exactly once, on the first packet of a new event, and uses the RTP
// timestamp to tell "still holding the same key" from "pressed the same key
// again".
package dtmf

import (
	"github.com/pion/rtp"
)

// eventPayloadLen is the size of an RFC 4733 telephone-event payload.
const eventPayloadLen = 4

// endBit marks the final packet of an event.
const endBit = 0x80

// events maps the wire event code to its character. 0-9, *, # and the A-D
// tones, which some PBXs still use for call control.
var events = [...]rune{
	'0', '1', '2', '3', '4', '5', '6', '7', '8', '9',
	'*', '#',
	'A', 'B', 'C', 'D',
}

// Event is one decoded telephone-event packet.
type Event struct {
	// Digit is the character pressed.
	Digit rune
	// End reports whether this packet ends the event.
	End bool
	// DurationMs is how long the tone has lasted so far.
	DurationMs int
	// Timestamp is the RTP timestamp, which identifies the event: every packet
	// of one keypress carries the same one.
	Timestamp uint32
}

// Decode parses a telephone-event payload. It reports ok=false for anything that
// is not a well-formed event, including the unassigned event codes above 15.
func Decode(payload []byte, timestamp uint32) (Event, bool) {
	if len(payload) < eventPayloadLen {
		return Event{}, false
	}
	code := int(payload[0])
	if code >= len(events) {
		// Codes 16-255 are reserved or for tones this system has no use for.
		return Event{}, false
	}

	// Duration is in timestamp units, which for telephone-event is the 8kHz
	// clock: 8 units per millisecond.
	duration := int(payload[2])<<8 | int(payload[3])

	return Event{
		Digit:      events[code],
		End:        payload[1]&endBit != 0,
		DurationMs: duration / 8,
		Timestamp:  timestamp,
	}, true
}

// Detector turns a stream of telephone-event packets into individual digits.
//
// It is NOT safe for concurrent use; one detector belongs to one session's read
// loop.
type Detector struct {
	// payloadType is the negotiated telephone-event payload type. Zero means the
	// leg negotiated no DTMF transport and every packet is ignored.
	payloadType uint8

	// current identifies the keypress in progress by its RTP timestamp, so the
	// dozen packets of one press yield one digit. Two presses of the same key
	// carry different timestamps and are therefore two digits.
	current    uint32
	haveActive bool
}

// NewDetector builds a detector for a negotiated payload type.
func NewDetector(payloadType int) *Detector {
	if payloadType < 0 || payloadType > 127 {
		payloadType = 0
	}
	return &Detector{payloadType: uint8(payloadType)}
}

// Enabled reports whether this leg negotiated a DTMF transport.
func (d *Detector) Enabled() bool { return d != nil && d.payloadType > 0 }

// Handle feeds one RTP packet to the detector, returning a digit when the packet
// begins a new keypress. Every other packet — a continuation, a redundant end
// packet, audio, or anything on another payload type — returns ok=false.
func (d *Detector) Handle(pkt *rtp.Packet) (rune, bool) {
	if !d.Enabled() || pkt == nil || pkt.PayloadType != d.payloadType {
		return 0, false
	}

	event, ok := Decode(pkt.Payload, pkt.Timestamp)
	if !ok {
		return 0, false
	}

	// The end packet is sent three times for redundancy. Once the event is
	// finished, further packets for it are ignored, but the event stays
	// remembered so those repeats do not look like a fresh press.
	if d.haveActive && d.current == event.Timestamp {
		return 0, false
	}

	d.current = event.Timestamp
	d.haveActive = true
	return event.Digit, true
}
