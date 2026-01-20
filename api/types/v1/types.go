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
	Address string `json:"address"`
	Healthy bool   `json:"healthy"`
}

// RtpManagersResponse is the response from /api/v1/rtpmanagers
type RtpManagersResponse struct {
	TotalMembers   int          `json:"total_members"`
	HealthyMembers int          `json:"healthy_members"`
	ActiveSessions int          `json:"active_sessions"`
	Members        []RtpManager `json:"members"`
}
