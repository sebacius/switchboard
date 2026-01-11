package api

import (
	"embed"
	"html/template"
	"io"
	"time"
)

//go:embed templates/*.html
var templatesFS embed.FS

// Templates holds all parsed templates
type Templates struct {
	dashboard     *template.Template
	statsPartial  *template.Template
	regsPartial   *template.Template
	dialogPartial *template.Template
	sessPartial   *template.Template
}

// TemplateData holds data for rendering templates
type TemplateData struct {
	Title         string
	Health        HealthData
	Stats         StatsData
	Registrations []RegistrationData
	Dialogs       []DialogData
	Sessions      []SessionData
}

// HealthData holds health information
type HealthData struct {
	Status string
	Uptime string
}

// StatsData holds statistics
type StatsData struct {
	ActiveSessions     int
	TotalRegistrations int
	TotalBindings      int
	ActiveDialogs      int
}

// RegistrationData holds registration info for display
type RegistrationData struct {
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
	CallID     string
	ClientAddr string
	ClientPort int
	ServerAddr string
	ServerPort int
	Duration   string
	Status     string
}

// templateFuncs provides helper functions for templates
var templateFuncs = template.FuncMap{
	"formatDuration": func(seconds int) string {
		d := time.Duration(seconds) * time.Second
		if d < time.Minute {
			return d.String()
		}
		if d < time.Hour {
			return d.Round(time.Second).String()
		}
		hours := int(d.Hours())
		mins := int(d.Minutes()) % 60
		secs := int(d.Seconds()) % 60
		result := time.Duration(hours)*time.Hour + time.Duration(mins)*time.Minute + time.Duration(secs)*time.Second
		return result.String()
	},
}

// NewTemplates parses and returns all templates
func NewTemplates() (*Templates, error) {
	t := &Templates{}

	var err error

	// Parse dashboard template
	t.dashboard, err = template.New("dashboard.html").Funcs(templateFuncs).ParseFS(templatesFS, "templates/dashboard.html")
	if err != nil {
		return nil, err
	}

	// Parse partials
	t.statsPartial, err = template.New("stats.html").Funcs(templateFuncs).ParseFS(templatesFS, "templates/stats.html")
	if err != nil {
		return nil, err
	}

	t.regsPartial, err = template.New("registrations.html").Funcs(templateFuncs).ParseFS(templatesFS, "templates/registrations.html")
	if err != nil {
		return nil, err
	}

	t.dialogPartial, err = template.New("dialogs.html").Funcs(templateFuncs).ParseFS(templatesFS, "templates/dialogs.html")
	if err != nil {
		return nil, err
	}

	t.sessPartial, err = template.New("sessions.html").Funcs(templateFuncs).ParseFS(templatesFS, "templates/sessions.html")
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
