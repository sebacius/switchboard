// Package types defines shared API types for signaling servers and UI.
package types

// HealthResponse is the response from /api/v1/health
type HealthResponse struct {
	Status string `json:"status"`
	Uptime int64  `json:"uptime"`
}

// StatsResponse is the response from /api/v1/stats
type StatsResponse struct {
	TotalSessions      int `json:"total_sessions"`
	ActiveSessions     int `json:"active_sessions"`
	TotalRegistrations int `json:"total_registrations"`
	TotalBindings      int `json:"total_bindings"`
	ActiveDialogs      int `json:"active_dialogs"`
}

// Registration represents a SIP registration binding
type Registration struct {
	AOR          string   `json:"aor"`
	ContactURI   string   `json:"contact_uri"`
	BindingID    string   `json:"binding_id"`
	ReceivedIP   string   `json:"received_ip,omitempty"`
	ReceivedPort int      `json:"received_port,omitempty"`
	Transport    string   `json:"transport"`
	Expires      int      `json:"expires"`
	ExpiresAt    string   `json:"expires_at"`
	RegisteredAt string   `json:"registered_at"`
	QValue       float32  `json:"q,omitempty"`
	UserAgent    string   `json:"user_agent,omitempty"`
	InstanceID   string   `json:"instance_id,omitempty"`
	Path         []string `json:"path,omitempty"`
}

// Dialog represents a SIP dialog (call)
type Dialog struct {
	CallID          string `json:"call_id"`
	Direction       string `json:"direction"`
	State           string `json:"state"`
	LocalURI        string `json:"local_uri"`
	RemoteURI       string `json:"remote_uri"`
	RemoteAddr      string `json:"remote_addr"`
	RemotePort      int    `json:"remote_port"`
	Duration        int    `json:"duration"`
	CreatedAt       string `json:"created_at"`
	TerminateReason string `json:"terminate_reason,omitempty"`
}

// Session represents an RTP session
type Session struct {
	CallID     string `json:"call_id"`
	ClientAddr string `json:"client_addr"`
	ClientPort int    `json:"client_port"`
	ServerAddr string `json:"server_addr"`
	ServerPort int    `json:"server_port"`
	Duration   int    `json:"duration"`
	Status     string `json:"status"`
}

// RtpManager represents an RTP manager instance
type RtpManager struct {
	NodeID       string `json:"node_id"`
	Address      string `json:"address"`
	Healthy      bool   `json:"healthy"`
	DrainState   string `json:"drain_state"`
	SessionCount int    `json:"session_count"`
}

// RtpManagersResponse is the response from /api/v1/rtpmanagers
type RtpManagersResponse struct {
	TotalMembers   int          `json:"total_members"`
	HealthyMembers int          `json:"healthy_members"`
	ActiveSessions int          `json:"active_sessions"`
	Members        []RtpManager `json:"members"`
}

// ParkedCall represents a parked call slot
type ParkedCall struct {
	ID              string   `json:"id"`
	CallID          string   `json:"call_id"`
	SessionID       string   `json:"session_id"`
	ParkedAt        string   `json:"parked_at"`
	ParkedBy        string   `json:"parked_by"`
	DurationSeconds int      `json:"duration_seconds"`
	MohFiles        []string `json:"moh_files,omitempty"`
}

// --- Configuration Management Types ---

// FileContent is a wrapper for file content in config API responses.
type FileContent struct {
	Content string `json:"content"`
}

// TenantFile represents a tenant's configuration.
type TenantFile struct {
	Name     string `json:"name"`
	Content  string `json:"content,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Modified string `json:"modified,omitempty"`
	// HasFlows reports whether the tenant has a flow graph as well as a routing
	// table. Flows are optional, so the list has to say.
	HasFlows bool `json:"has_flows,omitempty"`
}

// ConfigProblem is one reason a configuration write was refused. Path locates
// it — "flows.main.nodes.greeting.exits.timeout" — so an editor can point at
// the node rather than saying only that something is wrong.
type ConfigProblem struct {
	Path     string `json:"path"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
}

// ConfigRejection is the body returned when a write fails validation.
type ConfigRejection struct {
	Error    string          `json:"error"`
	Tenant   string          `json:"tenant"`
	Problems []ConfigProblem `json:"problems"`
}

// CreateTenantRequest is the body for creating a new tenant file.
type CreateTenantRequest struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// ReloadResponse is the response from POST /api/v1/config/reload.
type ReloadResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}
