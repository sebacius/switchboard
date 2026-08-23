package server

import (
	"embed"
	"html/template"
	"io"

	types "github.com/sebas/switchboard/api/types/v1"
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
	parkedCallsPartial *template.Template
	// Configuration templates
	configPage       *template.Template
	configTenants    *template.Template
	configTenantEdit *template.Template
	configGlobals    *template.Template
	configGlobalEdit *template.Template
	reloadBanner     *template.Template
	configAudio      *template.Template
	flowTest         *template.Template
	flowTestResult   *template.Template
	configFlows      *template.Template
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
	ParkedCalls   []ParkedCallData
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
	Direction       string
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

// ParkedCallData holds parked call info for display
type ParkedCallData struct {
	Server   string // Backend server name
	SlotID   string // Parking slot ID
	CallID   string // SIP Call-ID
	Duration string // Formatted duration
	ParkedBy string // Who parked the call (e.g., "dialplan", user extension)
	ParkedAt string // Formatted timestamp
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

	t.parkedCallsPartial, err = template.New("parkedcalls.html").ParseFS(templatesFS, "templates/parkedcalls.html")
	if err != nil {
		return nil, err
	}

	// Configuration templates
	t.configPage, err = template.New("config.html").ParseFS(templatesFS, "templates/config.html")
	if err != nil {
		return nil, err
	}

	t.configTenants, err = template.New("config_tenants.html").ParseFS(templatesFS, "templates/config_tenants.html")
	if err != nil {
		return nil, err
	}

	t.configTenantEdit, err = template.New("config_tenant_edit.html").ParseFS(templatesFS, "templates/config_tenant_edit.html")
	if err != nil {
		return nil, err
	}

	t.configGlobals, err = template.New("config_globals.html").ParseFS(templatesFS, "templates/config_globals.html")
	if err != nil {
		return nil, err
	}

	t.configGlobalEdit, err = template.New("config_global_edit.html").ParseFS(templatesFS, "templates/config_global_edit.html")
	if err != nil {
		return nil, err
	}

	t.reloadBanner, err = template.New("config_reload_banner.html").ParseFS(templatesFS, "templates/config_reload_banner.html")
	if err != nil {
		return nil, err
	}

	t.configAudio, err = template.New("config_audio.html").ParseFS(templatesFS, "templates/config_audio.html")
	if err != nil {
		return nil, err
	}

	t.flowTest, err = template.New("config_flowtest.html").ParseFS(templatesFS, "templates/config_flowtest.html")
	if err != nil {
		return nil, err
	}

	// add is for 1-based hop numbering, which is how printTrace numbers them in
	// the CLI; the two outputs should read the same way.
	t.flowTestResult, err = template.New("config_flowtest_result.html").
		Funcs(template.FuncMap{"add": func(a, b int) int { return a + b }}).
		ParseFS(templatesFS, "templates/config_flowtest_result.html")
	if err != nil {
		return nil, err
	}

	t.configFlows, err = template.New("config_flows.html").ParseFS(templatesFS, "templates/config_flows.html")
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

// RenderParkedCalls renders the parked calls partial
func (t *Templates) RenderParkedCalls(w io.Writer, data TemplateData) error {
	return t.parkedCallsPartial.Execute(w, data)
}

// --- Configuration Data Types ---

// BackendInfo holds basic backend info for the config page
type BackendInfo struct {
	Name    string
	Address string
}

// ConfigPageData holds data for the config page layout
type ConfigPageData struct {
	Title          string
	ActiveTab      string
	SelectedServer string
	Backends       []BackendInfo
	// TabQuery carries extra parameters through to the tab's partial, so a link
	// like ?tab=flowtest&dialed=700&digits=2 arrives as a filled-in form rather
	// than an empty one. Built from an allowlist, never from the raw query.
	TabQuery string
}

// TenantFileData holds tenant file info for display
type TenantFileData struct {
	Name     string
	Size     int64
	Modified string
	// HasFlows reports whether the tenant has a flow graph. Flows are optional,
	// so the list says which tenants have one rather than implying all do.
	HasFlows bool
}

// ConfigTenantsData holds data for the tenant list
type ConfigTenantsData struct {
	Server  string
	Tenants []TenantFileData
	Success string
	Error   string
}

// ConfigProblemData is one validation finding shown against its path.
type ConfigProblemData struct {
	Path    string
	Message string
}

// ConfigTenantEditData holds data for the tenant editor.
type ConfigTenantEditData struct {
	Server  string
	Name    string
	Content string
	// File is "routing" or "flows".
	File    string
	IsNew   bool
	Success string
	Error   string
	// Problems are errors that BLOCKED the write; Warnings were saved anyway
	// and are worth a second look. Rendering them the same way would tell the
	// operator to fix something that is already live.
	Problems []ConfigProblemData
	Warnings []ConfigProblemData
}

// IsFlows reports whether the flow graph is being edited, for the template.
func (d ConfigTenantEditData) IsFlows() bool { return d.File == "flows" }

// GlobalFileData describes one deployment-wide file for the templates.
type GlobalFileData struct {
	Name        string
	Path        string
	Size        int64
	Modified    string
	Exists      bool
	Configured  bool
	Writable    bool
	Activation  string
	Title       string
	Description string
}

// NeedsRestart reports whether saving this file requires a restart to take
// effect, for the template.
func (d GlobalFileData) NeedsRestart() bool { return d.Activation == "restart" }

// ConfigGlobalsData holds the deployment-wide file list.
type ConfigGlobalsData struct {
	Server string
	Files  []GlobalFileData
	Error  string
}

// ConfigGlobalEditData holds the editor for one deployment-wide file.
type ConfigGlobalEditData struct {
	Server   string
	Name     string
	Content  string
	File     GlobalFileData
	Success  string
	Error    string
	Problems []ConfigProblemData
	Warnings []ConfigProblemData
}

// ReloadBannerData says whether what is on disk is what is serving calls.
type ReloadBannerData struct {
	Server string
	// LoadedAt is when the routing cache in force was built.
	LoadedAt string
	// StaleTenants a reload activates; StaleGlobals only a restart does. They
	// are separate because offering a Reload button for the second kind would
	// be a lie.
	StaleTenants []string
	StaleGlobals []string
}

// FlowTestData holds the simulator form.
type FlowTestData struct {
	Server    string
	Tenants   []types.LoadedTenant
	Tenant    string
	Dialed    string
	Digits    string
	Direction string
	// AutoRun runs the simulation as the panel loads, so a deep link comes back
	// as a trace rather than as a form to fill in again.
	AutoRun bool
	Error   string
}

// FlowTestResultData holds one simulation's outcome.
type FlowTestResultData struct {
	Server  string
	Request types.SimulateRequest
	Result  types.SimulateResult
	Error   string
}

// IsFlow, IsDirect and IsNone distinguish the three ways a call can be routed.
// A direct dial produces no trace, and rendering that as an empty traversal
// would read as "the flow did nothing" rather than "no flow was involved".
func (d FlowTestResultData) IsFlow() bool   { return d.Result.Routed == "flow" }
func (d FlowTestResultData) IsDirect() bool { return d.Result.Routed == "direct" }
func (d FlowTestResultData) IsNone() bool   { return d.Result.Routed == "none" }

// ConfigFlowsData holds the graphical flow editor.
type ConfigFlowsData struct {
	Server string
	// Tenants are only those that HAVE flows: the canvas edits a graph, and
	// offering a tenant with no graph would promise something the tab cannot do.
	Tenants []TenantFileData
	Tenant  string
	// Flows are the selected tenant's flow names, in document order.
	Flows []string
	Flow  string
	// Graph is the selected flow as JSON for the canvas, and Schema is the node
	// catalogue exported from the dialplan package. Both are serialized here so
	// the page never restates the exit contract in JavaScript.
	Graph  template.JS
	Schema template.JS
	// Content is the file exactly as read. It travels with the form so a save
	// splices into the document the operator was actually looking at.
	Content string
	Success string
	Error   string
	// Problems BLOCKED the write; Warnings were saved anyway. Same distinction,
	// and same rendering, as the raw JSON editor.
	Problems []ConfigProblemData
	Warnings []ConfigProblemData
}

// HasFlow reports whether there is a graph to draw, for the template.
func (d ConfigFlowsData) HasFlow() bool { return d.Flow != "" && len(d.Flows) > 0 }

// --- Configuration Render Methods ---

// RenderConfig renders the config page
func (t *Templates) RenderConfig(w io.Writer, data ConfigPageData) error {
	return t.configPage.Execute(w, data)
}

// RenderConfigTenants renders the tenant list partial
func (t *Templates) RenderConfigTenants(w io.Writer, data ConfigTenantsData) error {
	return t.configTenants.Execute(w, data)
}

// RenderConfigTenantEdit renders the tenant editor partial
func (t *Templates) RenderConfigTenantEdit(w io.Writer, data ConfigTenantEditData) error {
	return t.configTenantEdit.Execute(w, data)
}

// RenderConfigGlobals renders the deployment-wide file list.
func (t *Templates) RenderConfigGlobals(w io.Writer, data ConfigGlobalsData) error {
	return t.configGlobals.Execute(w, data)
}

// RenderConfigGlobalEdit renders the deployment-wide file editor.
func (t *Templates) RenderConfigGlobalEdit(w io.Writer, data ConfigGlobalEditData) error {
	return t.configGlobalEdit.Execute(w, data)
}

// RenderConfigAudio renders the audio inventory.
func (t *Templates) RenderConfigAudio(w io.Writer, data ConfigAudioData) error {
	return t.configAudio.Execute(w, data)
}

// RenderFlowTest renders the simulator form.
func (t *Templates) RenderFlowTest(w io.Writer, data FlowTestData) error {
	return t.flowTest.Execute(w, data)
}

// RenderFlowTestResult renders one simulation's outcome.
func (t *Templates) RenderFlowTestResult(w io.Writer, data FlowTestResultData) error {
	return t.flowTestResult.Execute(w, data)
}

// RenderConfigFlows renders the graphical flow editor.
func (t *Templates) RenderConfigFlows(w io.Writer, data ConfigFlowsData) error {
	return t.configFlows.Execute(w, data)
}

// RenderReloadBanner renders the configuration drift banner.
func (t *Templates) RenderReloadBanner(w io.Writer, data ReloadBannerData) error {
	return t.reloadBanner.Execute(w, data)
}
