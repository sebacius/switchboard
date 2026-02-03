// Package parking provides call parking functionality for the signaling server.
// Parking allows calls to be placed in a holding state with music-on-hold,
// waiting to be retrieved by another party.
package parking

import (
	"time"

	"github.com/sebas/switchboard/internal/signaling/dialog"
)

// ParkSlot represents a parked call waiting for pickup.
type ParkSlot struct {
	// ID is the numeric slot identifier (e.g., "701")
	ID string

	// CallID is the original A-leg SIP Call-ID
	CallID string

	// Dialog is a reference to the parked dialog
	Dialog *dialog.Dialog

	// SessionID is the RTP session ID for the parked call
	SessionID string

	// ParkedAt is when the call was parked
	ParkedAt time.Time

	// ParkedBy identifies who parked the call (extension or "dialplan")
	ParkedBy string

	// MOHFiles are the audio files being played as music-on-hold
	MOHFiles []string
}

// Duration returns how long the call has been parked.
func (s *ParkSlot) Duration() time.Duration {
	return time.Since(s.ParkedAt)
}

// SlotInfo is a serializable representation of ParkSlot for API responses.
type SlotInfo struct {
	ID        string   `json:"id"`
	CallID    string   `json:"call_id"`
	SessionID string   `json:"session_id"`
	ParkedAt  string   `json:"parked_at"`
	ParkedBy  string   `json:"parked_by"`
	Duration  int      `json:"duration_seconds"`
	MOHFiles  []string `json:"moh_files,omitempty"`
}

// ToInfo converts a ParkSlot to its API representation.
func (s *ParkSlot) ToInfo() SlotInfo {
	return SlotInfo{
		ID:        s.ID,
		CallID:    s.CallID,
		SessionID: s.SessionID,
		ParkedAt:  s.ParkedAt.Format(time.RFC3339),
		ParkedBy:  s.ParkedBy,
		Duration:  int(s.Duration().Seconds()),
		MOHFiles:  s.MOHFiles,
	}
}
