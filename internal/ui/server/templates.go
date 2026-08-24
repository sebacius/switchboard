package server

import (
	"embed"
	"fmt"
	"html/template"
	"io"

	types "github.com/sebas/switchboard/api/types/v1"
)

//go:embed templates/*.html
var templatesFS embed.FS

// Templates is the parsed template set: one entry per page, one per fragment.
//
// A PAGE is parsed together with layout.html and every partial it embeds for
// first paint, and rendered by executing "layout.html". A PARTIAL is parsed
// alone and rendered by executing its own {{define}} name. The same file can be
// both — the seven live monitors are — which is what collapses the copies the
// dashboard used to carry: one definition, two call sites.
//
// One parse set per page rather than one set for everything. A single set makes
// every {{define}} name global, so a collision is silent last-wins, and one bad
// template breaks all the pages at startup instead of the one that is broken.
type Templates struct {
	pages    map[string]*template.Template
	partials map[string]*template.Template
}

// livePartials are the fragments a page also renders for first paint, so an
// operator does not watch an empty table for three seconds. Each of these files
// is a single {{define}} named here.
var livePartials = []string{
	"stats", "backends", "rtpmanagers",
	"registrations", "dialogs", "sessions", "parkedcalls",
}

// pageSpec is a page and the partial files its first paint needs.
type pageSpec struct {
	file     string
	partials []string
}

var pageSpecs = map[string]pageSpec{
	"dashboard": {file: "dashboard.html", partials: livePartials},
	"config":    {file: "config.html"},
}

// partialSpec is a fragment: its file, and any file carrying a shared
// definition it uses.
type partialSpec struct {
	file string
	// includes are extra files parsed alongside, for shared {{define}}s.
	includes []string
	// defineName, when set, is the template to execute — the file's whole body
	// is a single {{define}} and Execute would render nothing.
	defineName string
}

// sharedFlash carries the flash region every configuration editor renders.
const sharedFlash = "config_flash.html"

// partialSpecs is every fragment the server can render on its own.
var partialSpecs = map[string]partialSpec{
	// The live monitors: the same files the dashboard embeds for first paint.
	"stats":         {file: "stats.html", defineName: "stats"},
	"backends":      {file: "backends.html", defineName: "backends"},
	"rtpmanagers":   {file: "rtpmanagers.html", defineName: "rtpmanagers"},
	"registrations": {file: "registrations.html", defineName: "registrations"},
	"dialogs":       {file: "dialogs.html", defineName: "dialogs"},
	"sessions":      {file: "sessions.html", defineName: "sessions"},
	"parkedcalls":   {file: "parkedcalls.html", defineName: "parkedcalls"},

	// Config fragments, each swapped into #config-content.
	"drain_modal":            {file: "drain_modal.html"},
	"config_tenants":         {file: "config_tenants.html", includes: []string{sharedFlash}},
	"config_tenant_edit":     {file: "config_tenant_edit.html", includes: []string{sharedFlash}},
	"config_globals":         {file: "config_globals.html"},
	"config_global_edit":     {file: "config_global_edit.html", includes: []string{sharedFlash}},
	"config_reload_banner":   {file: "config_reload_banner.html"},
	"config_audio":           {file: "config_audio.html"},
	"config_flowtest":        {file: "config_flowtest.html"},
	"config_flowtest_result": {file: "config_flowtest_result.html"},
	"config_flows":           {file: "config_flows.html", includes: []string{sharedFlash}},
}

// templateFuncs are the helpers templates may call. Kept small on purpose: a
// func map is a place logic hides from tests.
var templateFuncs = template.FuncMap{
	// add exists for 1-based hop numbering in a flow trace.
	"add": func(a, b int) int { return a + b },
}

// TemplateData holds data for rendering templates
type TemplateData struct {
	// Layout is what the shell needs. A named field, not embedded: several view
	// models already carry a Server field of their own.
	Layout        LayoutData
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

// NewTemplates parses every page and every fragment.
//
// Parsing at boot rather than per request means a broken template is a startup
// failure, which is the right time to find out.
func NewTemplates() (*Templates, error) {
	t := &Templates{
		pages:    map[string]*template.Template{},
		partials: map[string]*template.Template{},
	}

	for name, spec := range pageSpecs {
		files := []string{"templates/layout.html", "templates/" + spec.file}
		for _, p := range spec.partials {
			files = append(files, "templates/"+partialSpecs[p].file)
		}
		parsed, err := template.New("layout.html").Funcs(templateFuncs).ParseFS(templatesFS, files...)
		if err != nil {
			return nil, fmt.Errorf("parse page %s: %w", name, err)
		}
		t.pages[name] = parsed
	}

	for name, spec := range partialSpecs {
		files := []string{"templates/" + spec.file}
		for _, inc := range spec.includes {
			files = append(files, "templates/"+inc)
		}
		parsed, err := template.New(spec.file).Funcs(templateFuncs).ParseFS(templatesFS, files...)
		if err != nil {
			return nil, fmt.Errorf("parse partial %s: %w", name, err)
		}
		t.partials[name] = parsed
	}

	return t, nil
}

// renderPage renders a page: the layout, with that page's content inside it.
func (t *Templates) renderPage(w io.Writer, name string, data any) error {
	page, ok := t.pages[name]
	if !ok {
		return fmt.Errorf("no such page: %s", name)
	}
	return page.ExecuteTemplate(w, "layout.html", data)
}

// renderPartial renders a fragment on its own.
//
// A file whose whole body is a {{define}} renders EMPTY under Execute, because
// Execute runs the file-named template and that template is now only a
// definition. So the entry point is named explicitly. That is also what lets a
// page's first paint and a poll share one definition instead of the two drifting
// copies this replaced.
func (t *Templates) renderPartial(w io.Writer, name string, data any) error {
	partial, ok := t.partials[name]
	if !ok {
		return fmt.Errorf("no such partial: %s", name)
	}
	if def := partialSpecs[name].defineName; def != "" {
		return partial.ExecuteTemplate(w, def, data)
	}
	return partial.Execute(w, data)
}

// RenderDashboard renders the main dashboard
func (t *Templates) RenderDashboard(w io.Writer, data TemplateData) error {
	return t.renderPage(w, "dashboard", data)
}

// RenderStats renders the stats partial
func (t *Templates) RenderStats(w io.Writer, data TemplateData) error {
	return t.renderPartial(w, "stats", data)
}

// RenderBackends renders the backends partial
func (t *Templates) RenderBackends(w io.Writer, data TemplateData) error {
	return t.renderPartial(w, "backends", data)
}

// RenderRtpManagers renders the RTP managers partial
func (t *Templates) RenderRtpManagers(w io.Writer, data TemplateData) error {
	return t.renderPartial(w, "rtpmanagers", data)
}

// RenderRegistrations renders the registrations partial
func (t *Templates) RenderRegistrations(w io.Writer, data TemplateData) error {
	return t.renderPartial(w, "registrations", data)
}

// RenderDialogs renders the dialogs partial
func (t *Templates) RenderDialogs(w io.Writer, data TemplateData) error {
	return t.renderPartial(w, "dialogs", data)
}

// RenderSessions renders the sessions partial
func (t *Templates) RenderSessions(w io.Writer, data TemplateData) error {
	return t.renderPartial(w, "sessions", data)
}

// RenderDrainModal renders the drain confirmation modal
func (t *Templates) RenderDrainModal(w io.Writer, data DrainModalData) error {
	return t.renderPartial(w, "drain_modal", data)
}

// RenderParkedCalls renders the parked calls partial
func (t *Templates) RenderParkedCalls(w io.Writer, data TemplateData) error {
	return t.renderPartial(w, "parkedcalls", data)
}

// --- Configuration Data Types ---

// BackendInfo holds basic backend info for the config page
type BackendInfo struct {
	Name    string
	Address string
}

// ConfigPageData holds data for the config page layout
type ConfigPageData struct {
	Layout         LayoutData
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
	// Problems and Warnings are carried so this page renders the same flash
	// region as every other configuration editor. A tenant delete can be
	// refused with reasons like any other write.
	Problems []ConfigProblemData
	Warnings []ConfigProblemData
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
	// catalog exported from the dialplan package. Both are serialized here so
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
	return t.renderPage(w, "config", data)
}

// RenderConfigTenants renders the tenant list partial
func (t *Templates) RenderConfigTenants(w io.Writer, data ConfigTenantsData) error {
	return t.renderPartial(w, "config_tenants", data)
}

// RenderConfigTenantEdit renders the tenant editor partial
func (t *Templates) RenderConfigTenantEdit(w io.Writer, data ConfigTenantEditData) error {
	return t.renderPartial(w, "config_tenant_edit", data)
}

// RenderConfigGlobals renders the deployment-wide file list.
func (t *Templates) RenderConfigGlobals(w io.Writer, data ConfigGlobalsData) error {
	return t.renderPartial(w, "config_globals", data)
}

// RenderConfigGlobalEdit renders the deployment-wide file editor.
func (t *Templates) RenderConfigGlobalEdit(w io.Writer, data ConfigGlobalEditData) error {
	return t.renderPartial(w, "config_global_edit", data)
}

// RenderConfigAudio renders the audio inventory.
func (t *Templates) RenderConfigAudio(w io.Writer, data ConfigAudioData) error {
	return t.renderPartial(w, "config_audio", data)
}

// RenderFlowTest renders the simulator form.
func (t *Templates) RenderFlowTest(w io.Writer, data FlowTestData) error {
	return t.renderPartial(w, "config_flowtest", data)
}

// RenderFlowTestResult renders one simulation's outcome.
func (t *Templates) RenderFlowTestResult(w io.Writer, data FlowTestResultData) error {
	return t.renderPartial(w, "config_flowtest_result", data)
}

// RenderConfigFlows renders the graphical flow editor.
func (t *Templates) RenderConfigFlows(w io.Writer, data ConfigFlowsData) error {
	return t.renderPartial(w, "config_flows", data)
}

// RenderReloadBanner renders the configuration drift banner.
func (t *Templates) RenderReloadBanner(w io.Writer, data ReloadBannerData) error {
	return t.renderPartial(w, "config_reload_banner", data)
}
