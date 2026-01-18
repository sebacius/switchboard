// Package store provides storage abstractions for the signaling server.
//
// Storage is organized into two categories:
//
// 1. Ephemeral (cache/NoSQL): Short-lived data with TTL support
//   - Dialogs: Active SIP dialogs (via dialog.DialogStore)
//   - Registrations: SIP location bindings (via location.LocationStore)
//   - Sessions: Active RTP sessions
//
// 2. Persistent (SQL): Long-term data requiring durability
//   - CDRs: Call Detail Records for billing/analytics
//   - Profiles: SIP user profiles and configuration
//
// Interfaces are defined here to allow swapping implementations:
//   - In-memory (default, for development)
//   - Redis (for distributed ephemeral storage)
//   - PostgreSQL/MySQL (for persistent storage)
package store

import (
	"context"
	"time"
)

// --- CDR (Call Detail Records) Repository ---

// CDR represents a call detail record for billing and analytics.
type CDR struct {
	ID            string    `json:"id"`
	CallID        string    `json:"call_id"`
	CallerNumber  string    `json:"caller_number"`
	CallerName    string    `json:"caller_name,omitempty"`
	CalledNumber  string    `json:"called_number"`
	Direction     string    `json:"direction"` // "inbound", "outbound", "internal"
	StartTime     time.Time `json:"start_time"`
	AnswerTime    time.Time `json:"answer_time,omitempty"`
	EndTime       time.Time `json:"end_time"`
	Duration      int       `json:"duration"`      // Total duration in seconds
	BillDuration  int       `json:"bill_duration"` // Billable duration (post-answer)
	Disposition   string    `json:"disposition"`   // "answered", "no_answer", "busy", "failed", "canceled"
	HangupCause   string    `json:"hangup_cause,omitempty"`
	SIPCode       int       `json:"sip_code,omitempty"`
	SourceIP      string    `json:"source_ip,omitempty"`
	DestinationIP string    `json:"destination_ip,omitempty"`
	UserAgent     string    `json:"user_agent,omitempty"`
	Codec         string    `json:"codec,omitempty"`
	Bridged       bool      `json:"bridged"`
	BridgeID      string    `json:"bridge_id,omitempty"`
	RecordingPath string    `json:"recording_path,omitempty"`
	Metadata      string    `json:"metadata,omitempty"` // JSON blob for custom fields
}

// CDRFilter specifies query criteria for CDR lookups.
type CDRFilter struct {
	CallerNumber string
	CalledNumber string
	Direction    string
	Disposition  string
	StartAfter   time.Time
	StartBefore  time.Time
	Limit        int
	Offset       int
}

// CDRRepository provides persistent storage for call detail records.
// Implementation: PostgreSQL/MySQL for production, in-memory for testing.
type CDRRepository interface {
	// Create stores a new CDR.
	Create(ctx context.Context, cdr *CDR) error

	// Get retrieves a CDR by ID.
	Get(ctx context.Context, id string) (*CDR, error)

	// GetByCallID retrieves a CDR by Call-ID.
	GetByCallID(ctx context.Context, callID string) (*CDR, error)

	// Query returns CDRs matching the filter criteria.
	Query(ctx context.Context, filter CDRFilter) ([]*CDR, error)

	// Count returns the number of CDRs matching the filter.
	Count(ctx context.Context, filter CDRFilter) (int64, error)

	// Update modifies an existing CDR (e.g., to add end time).
	Update(ctx context.Context, cdr *CDR) error

	// Delete removes a CDR by ID.
	Delete(ctx context.Context, id string) error
}

// --- Profile Repository ---

// Profile represents a SIP user profile and configuration.
type Profile struct {
	ID          string    `json:"id"`
	Username    string    `json:"username"`     // SIP username (e.g., "1001")
	Domain      string    `json:"domain"`       // SIP domain (e.g., "example.com")
	DisplayName string    `json:"display_name"` // Caller ID name
	Password    string    `json:"-"`            // SIP auth password (not in JSON)
	PasswordHA1 string    `json:"-"`            // HA1 hash for digest auth
	Enabled     bool      `json:"enabled"`
	CallerID    string    `json:"caller_id,omitempty"`   // Override caller ID number
	Mailbox     string    `json:"mailbox,omitempty"`     // Voicemail box
	Context     string    `json:"context,omitempty"`     // Dialplan context
	MaxCalls    int       `json:"max_calls,omitempty"`   // Concurrent call limit
	Codecs      []string  `json:"codecs,omitempty"`      // Allowed codecs
	NAT         bool      `json:"nat,omitempty"`         // NAT handling enabled
	Metadata    string    `json:"metadata,omitempty"`    // JSON blob for custom fields
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ProfileRepository provides persistent storage for SIP user profiles.
// Implementation: PostgreSQL/MySQL for production, in-memory for testing.
type ProfileRepository interface {
	// Create stores a new profile.
	Create(ctx context.Context, profile *Profile) error

	// Get retrieves a profile by ID.
	Get(ctx context.Context, id string) (*Profile, error)

	// GetByUsername retrieves a profile by username and domain.
	GetByUsername(ctx context.Context, username, domain string) (*Profile, error)

	// List returns all profiles (with optional pagination).
	List(ctx context.Context, limit, offset int) ([]*Profile, error)

	// Update modifies an existing profile.
	Update(ctx context.Context, profile *Profile) error

	// Delete removes a profile by ID.
	Delete(ctx context.Context, id string) error

	// Count returns the total number of profiles.
	Count(ctx context.Context) (int64, error)

	// Authenticate validates credentials and returns the profile if valid.
	Authenticate(ctx context.Context, username, domain, password string) (*Profile, error)
}

// --- Session Repository (Ephemeral) ---

// Session represents an active RTP media session.
type Session struct {
	ID         string    `json:"id"`
	CallID     string    `json:"call_id"`
	LocalAddr  string    `json:"local_addr"`
	LocalPort  int       `json:"local_port"`
	RemoteAddr string    `json:"remote_addr"`
	RemotePort int       `json:"remote_port"`
	Codec      string    `json:"codec"`
	Direction  string    `json:"direction"` // "sendrecv", "sendonly", "recvonly"
	State      string    `json:"state"`     // "created", "active", "bridged", "terminated"
	BridgeID   string    `json:"bridge_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// SessionRepository provides ephemeral storage for active RTP sessions.
// Implementation: Redis for distributed, in-memory for single-node.
type SessionRepository interface {
	// Create stores a new session.
	Create(ctx context.Context, session *Session) error

	// Get retrieves a session by ID.
	Get(ctx context.Context, id string) (*Session, error)

	// GetByCallID retrieves sessions for a Call-ID.
	GetByCallID(ctx context.Context, callID string) ([]*Session, error)

	// List returns all active sessions.
	List(ctx context.Context) ([]*Session, error)

	// Update modifies an existing session.
	Update(ctx context.Context, session *Session) error

	// Delete removes a session by ID.
	Delete(ctx context.Context, id string) error

	// Count returns the number of active sessions.
	Count(ctx context.Context) (int64, error)
}
