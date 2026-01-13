package media

import (
	"net"
	"sync"
	"time"

	"github.com/pion/rtp"
)

// RTPStreamWriter writes RTP packets with clock-based timing.
// It paces packets according to the codec's sample duration,
// ensuring proper real-time playback without drift.
type RTPStreamWriter struct {
	conn       net.PacketConn
	remoteAddr net.Addr

	// RTP header state
	ssrc      uint32
	pt        uint8
	seq       uint16
	timestamp uint32

	// Codec timing
	codec  Codec
	ticker *time.Ticker

	// Synchronization
	mu     sync.Mutex
	closed bool
}

// NewRTPStreamWriter creates a new clock-paced RTP stream writer.
// The writer will pace packets according to the codec's sample duration.
func NewRTPStreamWriter(conn net.PacketConn, remote net.Addr, codec Codec) *RTPStreamWriter {
	return &RTPStreamWriter{
		conn:       conn,
		remoteAddr: remote,
		ssrc:       GenerateSSRC(),
		pt:         codec.PayloadType,
		seq:        GenerateSequenceStart(),
		timestamp:  GenerateTimestampStart(),
		codec:      codec,
		ticker:     time.NewTicker(codec.SampleDur),
	}
}

// Write writes a payload as an RTP packet with clock pacing.
// It blocks until the next clock tick, ensuring proper timing.
// Implements io.Writer interface.
func (w *RTPStreamWriter) Write(payload []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return 0, net.ErrClosed
	}

	// Wait for clock tick to pace the stream
	<-w.ticker.C

	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    w.pt,
			SequenceNumber: w.seq,
			Timestamp:      w.timestamp,
			SSRC:           w.ssrc,
		},
		Payload: payload,
	}

	data, err := pkt.Marshal()
	if err != nil {
		return 0, err
	}

	_, err = w.conn.WriteTo(data, w.remoteAddr)
	if err != nil {
		return 0, err
	}

	// Advance sequence and timestamp
	w.seq++
	w.timestamp += w.codec.TimestampIncrement()

	return len(payload), nil
}

// WriteRTP writes an RTP packet directly (bypasses clock pacing).
// Use this for DTMF or other non-media packets that need precise timing control.
func (w *RTPStreamWriter) WriteRTP(pkt *rtp.Packet) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return net.ErrClosed
	}

	// Override SSRC to maintain stream consistency
	pkt.SSRC = w.ssrc

	data, err := pkt.Marshal()
	if err != nil {
		return err
	}

	_, err = w.conn.WriteTo(data, w.remoteAddr)
	return err
}

// WritePayload writes a payload with explicit marker bit control.
// Useful for marking the start of a talkspurt or DTMF event.
func (w *RTPStreamWriter) WritePayload(payload []byte, marker bool) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return net.ErrClosed
	}

	// Wait for clock tick
	<-w.ticker.C

	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			Marker:         marker,
			PayloadType:    w.pt,
			SequenceNumber: w.seq,
			Timestamp:      w.timestamp,
			SSRC:           w.ssrc,
		},
		Payload: payload,
	}

	data, err := pkt.Marshal()
	if err != nil {
		return err
	}

	_, err = w.conn.WriteTo(data, w.remoteAddr)
	if err != nil {
		return err
	}

	w.seq++
	w.timestamp += w.codec.TimestampIncrement()

	return nil
}

// SetPayloadType changes the RTP payload type for subsequent packets.
func (w *RTPStreamWriter) SetPayloadType(pt uint8) {
	w.mu.Lock()
	w.pt = pt
	w.mu.Unlock()
}

// SetSSRC changes the SSRC for subsequent packets.
func (w *RTPStreamWriter) SetSSRC(ssrc uint32) {
	w.mu.Lock()
	w.ssrc = ssrc
	w.mu.Unlock()
}

// SSRC returns the current SSRC value.
func (w *RTPStreamWriter) SSRC() uint32 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ssrc
}

// SequenceNumber returns the next sequence number that will be used.
func (w *RTPStreamWriter) SequenceNumber() uint16 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.seq
}

// Timestamp returns the next timestamp that will be used.
func (w *RTPStreamWriter) Timestamp() uint32 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.timestamp
}

// Close stops the ticker and marks the writer as closed.
func (w *RTPStreamWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.closed {
		w.closed = true
		w.ticker.Stop()
	}
	return nil
}

// Ensure RTPStreamWriter implements RTPPacketWriter
var _ RTPPacketWriter = (*RTPStreamWriter)(nil)
