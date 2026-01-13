package media

import (
	"crypto/rand"
	"encoding/binary"
)

// GenerateSSRC generates a cryptographically random 32-bit SSRC.
// Per RFC 3550, the SSRC should be chosen randomly to minimize
// collisions in multi-party sessions.
func GenerateSSRC() uint32 {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fallback to a less random value if crypto/rand fails
		// This should never happen on modern systems
		return 0x12345678
	}
	return binary.BigEndian.Uint32(b[:])
}

// GenerateSequenceStart generates a random starting sequence number.
// Per RFC 3550, the initial sequence number should be random to
// make known-plaintext attacks more difficult.
func GenerateSequenceStart() uint16 {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0
	}
	return binary.BigEndian.Uint16(b[:])
}

// GenerateTimestampStart generates a random starting timestamp.
// Per RFC 3550, the initial timestamp should be random.
func GenerateTimestampStart() uint32 {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0
	}
	return binary.BigEndian.Uint32(b[:])
}
