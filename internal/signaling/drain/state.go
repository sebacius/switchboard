package drain

import (
	"time"

	"github.com/sebas/switchboard/internal/signaling/mediaclient"
)

// DrainMode configures how aggressively drain behaves
type DrainMode string

const (
	// DrainModeGraceful waits for playback, keeps failed sessions on old node
	DrainModeGraceful DrainMode = "graceful"
	// DrainModeAggressive terminates failed sessions, guarantees drain completion
	DrainModeAggressive DrainMode = "aggressive"
)

// DrainRequest contains the parameters for a drain operation
type DrainRequest struct {
	NodeID  string
	Mode    DrainMode
	Timeout time.Duration // Override default timeout if needed
}

// DefaultDrainTimeout returns the default timeout for a drain mode
func DefaultDrainTimeout(mode DrainMode) time.Duration {
	switch mode {
	case DrainModeAggressive:
		return 30 * time.Second
	default:
		return 120 * time.Second
	}
}

// DrainStatus represents the current state of a drain operation
type DrainStatus struct {
	NodeID          string                 `json:"node_id"`
	State           mediaclient.DrainState `json:"state"`
	Mode            DrainMode              `json:"mode"`
	StartedAt       time.Time              `json:"started_at"`
	TotalSessions   int                    `json:"total_sessions"`
	WaitingPlayback int                    `json:"waiting_playback"`
	MigratedCount   int                    `json:"migrated_count"`
	FailedCount     int                    `json:"failed_count"`
	Errors          []SessionError         `json:"errors,omitempty"`
}

// SessionError records an error during session migration
type SessionError struct {
	SessionID string    `json:"session_id"`
	Error     string    `json:"error"`
	Timestamp time.Time `json:"timestamp"`
}
