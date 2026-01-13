package media

// SequenceTracker tracks RTP sequence numbers with rollover handling.
// RTP sequence numbers are 16-bit and wrap around at 65535.
// This tracker maintains an extended 32-bit counter for accurate
// packet loss calculation across rollovers.
type SequenceTracker struct {
	initialized bool
	lastSeq     uint16
	cycles      uint32 // Rollover count (upper 16 bits of extended seq)
	lost        uint64 // Total packets detected as lost
	received    uint64 // Total packets received
}

// NewSequenceTracker creates a new sequence tracker.
func NewSequenceTracker() *SequenceTracker {
	return &SequenceTracker{}
}

// Update records a received sequence number and returns statistics.
// Returns the extended sequence number (32-bit) and packets lost since last.
// The extended sequence includes rollover count in upper bits.
func (s *SequenceTracker) Update(seq uint16) (extended uint32, lost int) {
	s.received++

	if !s.initialized {
		s.initialized = true
		s.lastSeq = seq
		return uint32(seq), 0
	}

	// Calculate difference handling wrap-around per RFC 3550.
	// We use uint16 arithmetic first to get the forward distance,
	// then interpret it as signed for direction.
	udiff := seq - s.lastSeq
	diff := int16(udiff)

	if diff > 0 {
		// Forward jump - may have lost packets
		if diff > 1 {
			lost = int(diff) - 1
			s.lost += uint64(lost)
		}
	} else if diff < 0 && udiff > 0x8000 {
		// Large forward jump in uint16 space but negative in int16 space
		// means sequence wrapped around (rollover from 65535 to 0)
		// e.g., lastSeq=65530, seq=5: udiff=11, diff=11 (positive, handled above)
		// e.g., lastSeq=5, seq=65530: udiff=65525, diff=-11 (this branch)
		// This is actually a late/reordered packet from before rollover
		// Don't increment cycles for reordered packets
	} else if diff < 0 {
		// Small negative diff means out-of-order packet - no action needed
	}

	// Check for rollover: if lastSeq was high and new seq is low
	if s.lastSeq > 0xF000 && seq < 0x1000 {
		s.cycles++
	}

	s.lastSeq = seq
	return (s.cycles << 16) | uint32(seq), lost
}

// Stats returns cumulative statistics.
func (s *SequenceTracker) Stats() (received, lost uint64) {
	return s.received, s.lost
}

// LossRate returns the packet loss rate as a fraction (0.0 to 1.0).
func (s *SequenceTracker) LossRate() float64 {
	if s.received == 0 && s.lost == 0 {
		return 0.0
	}
	total := s.received + s.lost
	return float64(s.lost) / float64(total)
}

// Reset clears all tracking state.
func (s *SequenceTracker) Reset() {
	s.initialized = false
	s.lastSeq = 0
	s.cycles = 0
	s.lost = 0
	s.received = 0
}
