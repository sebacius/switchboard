package media

import (
	"github.com/pion/rtp"
)

// RTPReader reads RTP packets from an underlying source.
// Implementations may read from a UDP socket, buffer, or other source.
type RTPReader interface {
	// ReadRTP reads the next RTP packet.
	// Returns the packet or an error if reading fails.
	ReadRTP() (*rtp.Packet, error)
}

// RTPWriter writes RTP packets to an underlying destination.
// Implementations may write to a UDP socket, buffer, or other sink.
type RTPWriter interface {
	// WriteRTP writes an RTP packet.
	// Returns an error if writing fails.
	WriteRTP(p *rtp.Packet) error
}

// RTPPacketReader wraps RTPReader with additional context.
// Useful for extracting the payload from RTP packets while
// maintaining access to the full packet headers.
type RTPPacketReader interface {
	RTPReader

	// LastPacket returns the most recently read packet.
	// Useful for accessing header fields (SSRC, timestamp, etc.)
	// after reading payload data.
	LastPacket() *rtp.Packet
}

// RTPPacketWriter wraps RTPWriter with automatic header management.
// Useful for creating RTP packets from raw payload data with
// automatic sequence numbers and timestamps.
type RTPPacketWriter interface {
	RTPWriter

	// SetPayloadType sets the RTP payload type for subsequent packets.
	SetPayloadType(pt uint8)

	// SetSSRC sets the synchronization source identifier.
	SetSSRC(ssrc uint32)
}

// RTPSession represents a bidirectional RTP media session.
// It combines reading and writing capabilities for full-duplex media.
type RTPSession interface {
	RTPReader
	RTPWriter

	// LocalAddr returns the local RTP address (ip:port).
	LocalAddr() string

	// RemoteAddr returns the remote RTP address (ip:port).
	RemoteAddr() string

	// Close terminates the session and releases resources.
	Close() error
}
