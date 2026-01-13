package media

import (
	"context"
	"fmt"

	"github.com/pion/rtp"
)

// DTMFReader detects DTMF digits from an RTP stream.
// It implements a state machine per RFC 4733 to properly handle
// event start, continuation, and end packets.
type DTMFReader struct {
	reader     RTPReader
	dtmfPT     uint8  // Payload type for telephone-event
	sampleRate uint32 // Sample rate (typically 8000)

	// State machine
	lastEvent   uint8  // Last event code seen
	lastDur     uint16 // Last duration seen
	pending     bool   // Whether we're in an event
	minDuration uint16 // Minimum duration to accept (filters noise)
}

// NewDTMFReader creates a new DTMF detector.
func NewDTMFReader(reader RTPReader, payloadType uint8) *DTMFReader {
	return &DTMFReader{
		reader:      reader,
		dtmfPT:      payloadType,
		sampleRate:  DTMFSampleRate,
		minDuration: MinDTMFDuration, // 50ms minimum
	}
}

// SetMinDuration sets the minimum duration (in timestamp units) to accept.
// This helps filter out noise or very brief accidental presses.
func (d *DTMFReader) SetMinDuration(samples uint16) {
	d.minDuration = samples
}

// ReadDigit blocks until a complete DTMF digit is detected.
// A digit is complete when an end-of-event packet is received
// with duration >= minDuration.
// Returns the digit rune or an error if the context is cancelled or reading fails.
func (d *DTMFReader) ReadDigit(ctx context.Context) (rune, error) {
	for {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		default:
		}

		pkt, err := d.reader.ReadRTP()
		if err != nil {
			return 0, fmt.Errorf("read RTP: %w", err)
		}

		digit, ok := d.processPacket(pkt)
		if ok {
			return digit, nil
		}
	}
}

// processPacket processes an RTP packet and returns a digit if one is complete.
func (d *DTMFReader) processPacket(pkt *rtp.Packet) (rune, bool) {
	// Ignore non-DTMF packets
	if pkt.PayloadType != d.dtmfPT {
		return 0, false
	}

	// Decode the DTMF event
	if len(pkt.Payload) < 4 {
		return 0, false
	}

	evt, err := DecodeDTMFEvent(pkt.Payload)
	if err != nil {
		return 0, false
	}

	// State machine logic
	if evt.EndOfEvent {
		// End of event - check if it's for the pending event
		if d.pending && evt.Event == d.lastEvent {
			// Validate duration meets minimum
			if evt.Duration >= d.minDuration {
				d.pending = false
				char, ok := EventToRune(evt.Event)
				if ok {
					return char, true
				}
			}
		}
		// Even if we don't return a digit, clear pending state
		d.pending = false
	} else {
		// Start or continuation of event
		if !d.pending || evt.Event != d.lastEvent {
			// New event starting
			d.lastEvent = evt.Event
			d.lastDur = evt.Duration
			d.pending = true
		} else {
			// Continuation - update duration
			d.lastDur = evt.Duration
		}
	}

	return 0, false
}

// ReadDigits continuously reads digits and sends them to the provided channel.
// Stops when context is cancelled or an error occurs.
// Closes the channel when done.
func (d *DTMFReader) ReadDigits(ctx context.Context, digits chan<- rune) error {
	defer close(digits)

	for {
		digit, err := d.ReadDigit(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil // Normal cancellation
			}
			return err
		}

		select {
		case digits <- digit:
		case <-ctx.Done():
			return nil
		}
	}
}

// DetectDTMF is a convenience function that returns whether a packet contains DTMF.
// Useful for routing packets to different handlers.
func (d *DTMFReader) DetectDTMF(pkt *rtp.Packet) bool {
	return pkt.PayloadType == d.dtmfPT && len(pkt.Payload) >= 4
}

// Reset clears the state machine.
func (d *DTMFReader) Reset() {
	d.pending = false
	d.lastEvent = 0
	d.lastDur = 0
}
