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

// ConfigProblem is one finding about a configuration file. Path locates it —
// "flows.main.nodes.greeting.exits.timeout" — so an editor can point at the
// node rather than saying only that something is wrong. Severity "error" means
// the write was refused; "warning" means it was saved and is worth a look.
type ConfigProblem struct {
	Path     string `json:"path"`
	Message  string `json:"message"`
	Severity string `json:"severity"`
}

// ConfigRejection is the body returned when a write fails validation.
type ConfigRejection struct {
	Error  string `json:"error"`
	Tenant string `json:"tenant"`
	// File names which file was refused — a tenant's "routing"/"flows", or a
	// deployment-wide "policy"/"routes"/"trunk_peers".
	File     string          `json:"file,omitempty"`
	Problems []ConfigProblem `json:"problems"`
}

// ConfigSaved is the body of a successful configuration write. Warnings are
// findings the loader would accept, so they ride along with the success rather
// than blocking it.
type ConfigSaved struct {
	Status   string          `json:"status"`
	File     string          `json:"file,omitempty"`
	Name     string          `json:"name,omitempty"`
	Warnings []ConfigProblem `json:"warnings,omitempty"`
}

// GlobalFile is one deployment-wide configuration file: policy.json,
// routes.json or trunk_peers.json.
type GlobalFile struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
	Exists   bool   `json:"exists"`
	// Configured reports whether the server was given a path for this file.
	Configured bool `json:"configured"`
	// Writable reports whether this server accepts writes to it at all.
	Writable bool `json:"writable"`
	// Activation is "reload" or "restart" — what it takes for a saved edit to
	// actually take effect.
	Activation  string `json:"activation"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Content     string `json:"content,omitempty"`
}

// ConfigStatus answers whether what is on disk is what is serving calls.
type ConfigStatus struct {
	LoadedAt  string `json:"loaded_at"`
	StartedAt string `json:"started_at"`
	// StaleTenants were written since the routing cache was built; a reload
	// activates them.
	StaleTenants []string `json:"stale_tenants"`
	// StaleGlobals were written since the process started; only a restart
	// activates them, which is why they are counted separately.
	StaleGlobals []string `json:"stale_globals"`
}

// ReloadResult says what a reload actually activated. Half the files the config
// API writes cannot be reloaded at all, so a bare "ok" would let an operator
// believe a policy edit is live.
type ReloadResult struct {
	Status      string   `json:"status"`
	Message     string   `json:"message"`
	Reloaded    []string `json:"reloaded"`
	NotReloaded []string `json:"not_reloaded"`
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
