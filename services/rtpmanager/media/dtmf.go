package media

import (
	"encoding/binary"
	"fmt"
)

// DTMFEvent represents an RFC 4733 telephone-event payload.
// The payload format is 4 bytes:
//
//	 0                   1                   2                   3
//	 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
//	|     event     |E|R| volume    |          duration             |
//	+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
type DTMFEvent struct {
	Event      uint8  // 0-15: 0-9, *, #, A-D
	EndOfEvent bool   // E bit: marks final packet of event
	Volume     uint8  // 0-63: expressed in dBm0 (typically 10)
	Duration   uint16 // Duration in timestamp units
}

// DTMF event codes
const (
	DTMF0     uint8 = 0
	DTMF1     uint8 = 1
	DTMF2     uint8 = 2
	DTMF3     uint8 = 3
	DTMF4     uint8 = 4
	DTMF5     uint8 = 5
	DTMF6     uint8 = 6
	DTMF7     uint8 = 7
	DTMF8     uint8 = 8
	DTMF9     uint8 = 9
	DTMFStar  uint8 = 10
	DTMFPound uint8 = 11
	DTMFA     uint8 = 12
	DTMFB     uint8 = 13
	DTMFC     uint8 = 14
	DTMFD     uint8 = 15
)

// Default DTMF parameters
const (
	DefaultDTMFVolume      uint8  = 10   // -10 dBm0
	DefaultDTMFDuration    uint16 = 1600 // 200ms at 8kHz
	MinDTMFDuration        uint16 = 400  // 50ms minimum
	DTMFPayloadType        uint8  = 101  // Common default for telephone-event
	DTMFSampleRate         uint32 = 8000 // 8kHz
	DTMFSamplesPerDuration        = 8    // samples per ms at 8kHz
)

// RuneToEvent converts a DTMF character to its event code.
// Returns the event code and true if valid, 0 and false otherwise.
func RuneToEvent(r rune) (uint8, bool) {
	switch r {
	case '0':
		return DTMF0, true
	case '1':
		return DTMF1, true
	case '2':
		return DTMF2, true
	case '3':
		return DTMF3, true
	case '4':
		return DTMF4, true
	case '5':
		return DTMF5, true
	case '6':
		return DTMF6, true
	case '7':
		return DTMF7, true
	case '8':
		return DTMF8, true
	case '9':
		return DTMF9, true
	case '*':
		return DTMFStar, true
	case '#':
		return DTMFPound, true
	case 'A', 'a':
		return DTMFA, true
	case 'B', 'b':
		return DTMFB, true
	case 'C', 'c':
		return DTMFC, true
	case 'D', 'd':
		return DTMFD, true
	}
	return 0, false
}

// EventToRune converts a DTMF event code to its character.
// Returns the character and true if valid, 0 and false otherwise.
func EventToRune(event uint8) (rune, bool) {
	switch event {
	case DTMF0:
		return '0', true
	case DTMF1:
		return '1', true
	case DTMF2:
		return '2', true
	case DTMF3:
		return '3', true
	case DTMF4:
		return '4', true
	case DTMF5:
		return '5', true
	case DTMF6:
		return '6', true
	case DTMF7:
		return '7', true
	case DTMF8:
		return '8', true
	case DTMF9:
		return '9', true
	case DTMFStar:
		return '*', true
	case DTMFPound:
		return '#', true
	case DTMFA:
		return 'A', true
	case DTMFB:
		return 'B', true
	case DTMFC:
		return 'C', true
	case DTMFD:
		return 'D', true
	}
	return 0, false
}

// Encode serializes the DTMF event to RFC 4733 4-byte format.
func (e DTMFEvent) Encode() []byte {
	b := make([]byte, 4)
	b[0] = e.Event
	b[1] = e.Volume & 0x3F // Volume is 6 bits
	if e.EndOfEvent {
		b[1] |= 0x80 // Set E bit
	}
	binary.BigEndian.PutUint16(b[2:], e.Duration)
	return b
}

// DecodeDTMFEvent decodes an RFC 4733 4-byte payload into a DTMFEvent.
// Returns an error if the payload is too short.
func DecodeDTMFEvent(payload []byte) (DTMFEvent, error) {
	if len(payload) < 4 {
		return DTMFEvent{}, fmt.Errorf("DTMF payload too short: %d bytes", len(payload))
	}
	return DTMFEvent{
		Event:      payload[0],
		EndOfEvent: (payload[1] & 0x80) != 0,
		Volume:     payload[1] & 0x3F,
		Duration:   binary.BigEndian.Uint16(payload[2:]),
	}, nil
}

// String returns a human-readable representation of the event.
func (e DTMFEvent) String() string {
	char, ok := EventToRune(e.Event)
	if !ok {
		char = '?'
	}
	endStr := ""
	if e.EndOfEvent {
		endStr = " END"
	}
	return fmt.Sprintf("DTMF '%c' vol=%d dur=%d%s", char, e.Volume, e.Duration, endStr)
}
