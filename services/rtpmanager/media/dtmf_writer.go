package media

import (
	"fmt"
	"time"

	"github.com/pion/rtp"
)

// DTMFWriter generates RFC 4733 DTMF events.
// It sends DTMF digits as RTP telephone-event packets with proper
// redundancy for reliable detection.
type DTMFWriter struct {
	writer      RTPWriter
	payloadType uint8
	sampleRate  uint32
}

// NewDTMFWriter creates a new DTMF writer that sends events via the provided RTP writer.
func NewDTMFWriter(writer RTPWriter, payloadType uint8) *DTMFWriter {
	return &DTMFWriter{
		writer:      writer,
		payloadType: payloadType,
		sampleRate:  DTMFSampleRate, // 8kHz standard
	}
}

// SendDigit sends a DTMF digit with proper RFC 4733 encoding.
// The digit should be one of: 0-9, *, #, A-D (case insensitive).
// Duration specifies how long the digit should be (minimum 50ms recommended).
//
// Per RFC 4733:
//   - Multiple packets are sent during the event (redundancy)
//   - End-of-event packets are sent 3 times (redundancy for reliability)
//   - Timestamp remains constant throughout the event
//   - Duration field increases with each packet
func (d *DTMFWriter) SendDigit(digit rune, duration time.Duration) error {
	event, ok := RuneToEvent(digit)
	if !ok {
		return fmt.Errorf("invalid DTMF digit: %c", digit)
	}

	// Calculate duration in timestamp units (samples)
	samples := uint16(duration.Seconds() * float64(d.sampleRate))
	if samples < MinDTMFDuration {
		samples = MinDTMFDuration // Minimum 50ms
	}

	// Use 20ms intervals for intermediate packets
	intervalDuration := 20 * time.Millisecond
	intervalSamples := uint16(160) // 20ms at 8kHz

	// Get the current sequence number and timestamp from the writer
	// For this implementation, we create packets with proper headers
	ssrc := GenerateSSRC()
	seqStart := GenerateSequenceStart()
	tsStart := GenerateTimestampStart()

	seq := seqStart
	currentDuration := intervalSamples

	// Send intermediate packets with increasing duration
	for currentDuration < samples {
		evt := DTMFEvent{
			Event:    event,
			Volume:   DefaultDTMFVolume,
			Duration: currentDuration,
		}

		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				Marker:         seq == seqStart, // Marker on first packet
				PayloadType:    d.payloadType,
				SequenceNumber: seq,
				Timestamp:      tsStart, // Timestamp stays constant during event
				SSRC:           ssrc,
			},
			Payload: evt.Encode(),
		}

		if err := d.writer.WriteRTP(pkt); err != nil {
			return fmt.Errorf("send DTMF packet: %w", err)
		}

		seq++
		currentDuration += intervalSamples
		time.Sleep(intervalDuration)
	}

	// Send 3 end-of-event packets (RFC 4733 recommends 3x for redundancy)
	for i := 0; i < 3; i++ {
		evt := DTMFEvent{
			Event:      event,
			EndOfEvent: true,
			Volume:     DefaultDTMFVolume,
			Duration:   samples,
		}

		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    d.payloadType,
				SequenceNumber: seq,
				Timestamp:      tsStart, // Same timestamp as entire event
				SSRC:           ssrc,
			},
			Payload: evt.Encode(),
		}

		if err := d.writer.WriteRTP(pkt); err != nil {
			return fmt.Errorf("send DTMF end packet: %w", err)
		}

		seq++

		// Small delay between redundant end packets
		if i < 2 {
			time.Sleep(5 * time.Millisecond)
		}
	}

	return nil
}

// SendDigitString sends a string of DTMF digits with specified inter-digit delay.
func (d *DTMFWriter) SendDigitString(digits string, digitDuration, interDigitDelay time.Duration) error {
	for i, digit := range digits {
		if err := d.SendDigit(digit, digitDuration); err != nil {
			return fmt.Errorf("digit %d (%c): %w", i, digit, err)
		}
		if i < len(digits)-1 {
			time.Sleep(interDigitDelay)
		}
	}
	return nil
}
