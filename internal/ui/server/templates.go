package server

import (
	"embed"
	"html/template"
	"io"
)

//go:embed templates/*.html
var templatesFS embed.FS

// Templates holds all parsed templates
type Templates struct {
	dashboard          *template.Template
	statsPartial       *template.Template
	backendsPartial    *template.Template
	rtpmanagersPartial *template.Template
	regsPartial        *template.Template
	dialogPartial      *template.Template
	sessPartial        *template.Template
	drainModalPartial  *template.Template
}

// TemplateData holds data for rendering templates
type TemplateData struct {
	Title         string
	Health        HealthData
	Stats         StatsData
	Backends      []BackendData
	RtpManagers   []RtpManagerData
	Registrations []RegistrationData
	Dialogs       []DialogData
	Sessions      []SessionData
	MultiBackend  bool // true if multiple backends configured
}

// HealthData holds health information
type HealthData struct {
	Status string
	Uptime string
}

// StatsData holds aggregated statistics
type StatsData struct {
	ActiveSessions     int
	TotalRegistrations int
	TotalBindings      int
	ActiveDialogs      int
}

// BackendData holds backend server information
type BackendData struct {
	Name    string
	Address string
	Status  string
	Uptime  string
}

// RegistrationData holds registration info for display
type RegistrationData struct {
	Server       string // Backend server name
	AOR          string
	ContactURI   string
	Transport    string
	ReceivedIP   string
	ReceivedPort int
	Expires      int
	TTL          string
	UserAgent    string
	RegisteredAt string
}

// DialogData holds dialog info for display
type DialogData struct {
	Server          string // Backend server name
	CallID          string
	State           string
	LocalURI        string
	RemoteURI       string
	RemoteAddr      string
	RemotePort      int
	Duration        string
	CreatedAt       string
	TerminateReason string
}

// SessionData holds RTP session info for display
type SessionData struct {
	Server     string // Backend server name
	CallID     string
	ClientAddr string
	ClientPort int
	ServerAddr string
	ServerPort int
	Duration   string
	Status     string
}

// RtpManagerData holds RTP manager info for display
type RtpManagerData struct {
	Server            string // Backend server name (signaling server)
	NodeID            string // RTP manager node ID (e.g., "rtpmanager-0")
	Address           string // RTP manager address (e.g., "localhost:9090")
	Healthy           bool
	Status            string // "Healthy" or "Unhealthy"
	DrainState        string // "active", "draining", or "disabled"
	SessionCount      int    // Number of active sessions on this node
	InitialSessions   int    // Initial session count when drain started (for progress)
	RemainingSessions int    // Remaining sessions during drain
}

// DrainModalData holds data for the drain confirmation modal
type DrainModalData struct {
	Server       string
	NodeID       string
	Address      string
	SessionCount int
}

// DrainResultData holds the result of a drain operation for HTMX response
type DrainResultData struct {
	Success bool
	Message string
	NodeID  string
	Server  string
}

// NewTemplates parses and returns all templates
func NewTemplates() (*Templates, error) {
	t := &Templates{}

	var err error

	// Parse dashboard template
	t.dashboard, err = template.New("dashboard.html").ParseFS(templatesFS, "templates/dashboard.html")
	if err != nil {
		return nil, err
	}

	// Parse partials
	t.statsPartial, err = template.New("stats.html").ParseFS(templatesFS, "templates/stats.html")
	if err != nil {
		return nil, err
	}

	t.backendsPartial, err = template.New("backends.html").ParseFS(templatesFS, "templates/backends.html")
	if err != nil {
		return nil, err
	}

	t.rtpmanagersPartial, err = template.New("rtpmanagers.html").ParseFS(templatesFS, "templates/rtpmanagers.html")
	if err != nil {
		return nil, err
	}

	t.regsPartial, err = template.New("registrations.html").ParseFS(templatesFS, "templates/registrations.html")
	if err != nil {
		return nil, err
	}

	t.dialogPartial, err = template.New("dialogs.html").ParseFS(templatesFS, "templates/dialogs.html")
	if err != nil {
		return nil, err
	}

	t.sessPartial, err = template.New("sessions.html").ParseFS(templatesFS, "templates/sessions.html")
	if err != nil {
		return nil, err
	}

	t.drainModalPartial, err = template.New("drain_modal.html").ParseFS(templatesFS, "templates/drain_modal.html")
	if err != nil {
		return nil, err
	}

	return t, nil
}

// RenderDashboard renders the main dashboard
func (t *Templates) RenderDashboard(w io.Writer, data TemplateData) error {
	return t.dashboard.Execute(w, data)
}

// RenderStats renders the stats partial
func (t *Templates) RenderStats(w io.Writer, data TemplateData) error {
	return t.statsPartial.Execute(w, data)
}

// RenderBackends renders the backends partial
func (t *Templates) RenderBackends(w io.Writer, data TemplateData) error {
	return t.backendsPartial.Execute(w, data)
}

// RenderRtpManagers renders the RTP managers partial
func (t *Templates) RenderRtpManagers(w io.Writer, data TemplateData) error {
	return t.rtpmanagersPartial.Execute(w, data)
}

// RenderRegistrations renders the registrations partial
func (t *Templates) RenderRegistrations(w io.Writer, data TemplateData) error {
	return t.regsPartial.Execute(w, data)
}

// RenderDialogs renders the dialogs partial
func (t *Templates) RenderDialogs(w io.Writer, data TemplateData) error {
	return t.dialogPartial.Execute(w, data)
}

// RenderSessions renders the sessions partial
func (t *Templates) RenderSessions(w io.Writer, data TemplateData) error {
	return t.sessPartial.Execute(w, data)
}

// RenderDrainModal renders the drain confirmation modal
func (t *Templates) RenderDrainModal(w io.Writer, data DrainModalData) error {
	return t.drainModalPartial.Execute(w, data)
}
