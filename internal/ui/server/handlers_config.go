package server

import (
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	types "github.com/sebas/switchboard/api/types/v1"
	"github.com/sebas/switchboard/internal/ui/client"
)

// findClient returns the client for the specified server name, or the first client
func (s *Server) findClient(serverName string) *client.Client {
	if serverName != "" {
		for _, c := range s.clients {
			if c.Name() == serverName {
				return c
			}
		}
	}
	if len(s.clients) > 0 {
		return s.clients[0]
	}
	return nil
}

// handleConfigPage renders the full configuration management page
func (s *Server) handleConfigPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/admin/config" {
		http.NotFound(w, r)
		return
	}

	c := s.findClient(r.URL.Query().Get("server"))
	if c == nil {
		http.Error(w, "No backend configured", http.StatusServiceUnavailable)
		return
	}

	// The tab name becomes a partial's URL, so it comes from an allowlist rather
	// than from the query string. An unknown name is a typo in a bookmark, not a
	// reason to render a page that immediately 404s.
	activeTab := "tenants"
	switch r.URL.Query().Get("tab") {
	case "globals":
		activeTab = "globals"
	case "audio":
		activeTab = "audio"
	case "flowtest":
		activeTab = "flowtest"
	}

	data := ConfigPageData{
		Title:          "Switchboard Configuration",
		ActiveTab:      activeTab,
		TabQuery:       tabQuery(r),
		SelectedServer: c.Name(),
		Backends:       make([]BackendInfo, 0, len(s.clients)),
	}
	for _, cl := range s.clients {
		data.Backends = append(data.Backends, BackendInfo{
			Name:    cl.Name(),
			Address: cl.BaseURL(),
		})
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderConfig(w, data); err != nil {
		slog.Error("[UI] Failed to render config page", "error", err)
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
	}
}

// tabQuery forwards the parameters a tab's partial understands.
//
// An allowlist rather than the raw query string: whatever this returns is
// appended to a URL the page then fetches, so it may only contain keys we
// recognize.
func tabQuery(r *http.Request) string {
	q := r.URL.Query()
	out := url.Values{}
	for _, key := range []string{"tenant", "dialed", "digits", "direction", "name", "file"} {
		if v := q.Get(key); v != "" {
			out.Set(key, v)
		}
	}
	if len(out) == 0 {
		return ""
	}
	return "&" + out.Encode()
}

// fileParam reads which of a tenant's files is being edited, defaulting to the
// routing table.
func fileParam(r *http.Request) string {
	if v := r.URL.Query().Get("file"); v == "flows" {
		return "flows"
	}
	return "routing"
}

// handleConfigTenantsPartial renders the tenant list partial
func (s *Server) handleConfigTenantsPartial(w http.ResponseWriter, r *http.Request) {
	c := s.findClient(r.URL.Query().Get("server"))
	if c == nil {
		http.Error(w, "No backend configured", http.StatusServiceUnavailable)
		return
	}

	tenants, err := c.ListTenants(r.Context())
	data := ConfigTenantsData{Server: c.Name()}
	if err != nil {
		data.Error = fmt.Sprintf("Failed to load tenants: %v", err)
	} else {
		data.Tenants = make([]TenantFileData, 0, len(tenants))
		for _, t := range tenants {
			data.Tenants = append(data.Tenants, TenantFileData{
				Name:     t.Name,
				Size:     t.Size,
				Modified: t.Modified,
				HasFlows: t.HasFlows,
			})
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderConfigTenants(w, data); err != nil {
		slog.Error("[UI] Failed to render config tenants", "error", err)
	}
}

// handleConfigTenantEdit renders the tenant edit partial
func (s *Server) handleConfigTenantEdit(w http.ResponseWriter, r *http.Request) {
	c := s.findClient(r.URL.Query().Get("server"))
	if c == nil {
		http.Error(w, "No backend configured", http.StatusServiceUnavailable)
		return
	}

	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "Tenant name required", http.StatusBadRequest)
		return
	}

	file := fileParam(r)
	content, err := c.GetTenantFile(r.Context(), name, file)
	data := ConfigTenantEditData{Server: c.Name(), Name: name, File: file}
	if err != nil {
		data.Error = fmt.Sprintf("Failed to load %s: %v", file, err)
	} else {
		data.Content = content
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderConfigTenantEdit(w, data); err != nil {
		slog.Error("[UI] Failed to render tenant edit", "error", err)
	}
}

// handleConfigTenantNew renders the new tenant form
func (s *Server) handleConfigTenantNew(w http.ResponseWriter, r *http.Request) {
	c := s.findClient(r.URL.Query().Get("server"))
	if c == nil {
		http.Error(w, "No backend configured", http.StatusServiceUnavailable)
		return
	}

	// Seed a skeleton that actually loads. These files are JSON and the server
	// refuses a write that would not parse, so prose here would guarantee the
	// first save fails.
	file := fileParam(r)
	data := ConfigTenantEditData{
		Server:  c.Name(),
		IsNew:   true,
		File:    file,
		Content: skeletonFor(file),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderConfigTenantEdit(w, data); err != nil {
		slog.Error("[UI] Failed to render tenant new", "error", err)
	}
}

// skeletonFor returns an empty but valid starting point for a tenant file.
func skeletonFor(file string) string {
	if file == "flows" {
		return "{\n  \"flows\": {}\n}\n"
	}
	return `{
  "operator": "user/100",
  "extensions": {},
  "dids": {},
  "groups": {},
  "symbolic_targets": {}
}
`
}

// handleConfigTenantSave saves an existing tenant file
func (s *Server) handleConfigTenantSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	c := s.findClient(r.URL.Query().Get("server"))
	if c == nil {
		http.Error(w, "No backend configured", http.StatusServiceUnavailable)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	name := r.FormValue("name")
	content := normalizeContent(r.FormValue("content"))
	file := fileParam(r)
	if v := r.FormValue("file"); v != "" {
		file = v
	}
	data := ConfigTenantEditData{Server: c.Name(), Name: name, Content: content, File: file}

	warnings, err := c.PutTenantFile(r.Context(), name, file, content)
	if err != nil {
		applyWriteError(&data, err, fmt.Sprintf("Failed to save %s", file))
	} else {
		data.Success = fmt.Sprintf("Saved %s", file)
		data.Warnings = problemData(warnings)
		configSaved(w)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderConfigTenantEdit(w, data); err != nil {
		slog.Error("[UI] Failed to render tenant edit", "error", err)
	}
}

// handleConfigTenantCreate creates a new tenant file
func (s *Server) handleConfigTenantCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	c := s.findClient(r.URL.Query().Get("server"))
	if c == nil {
		http.Error(w, "No backend configured", http.StatusServiceUnavailable)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	name := r.FormValue("name")
	content := normalizeContent(r.FormValue("content"))

	if name == "" {
		data := ConfigTenantEditData{Server: c.Name(), IsNew: true, Content: content, Error: "Tenant name is required"}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = s.templates.RenderConfigTenantEdit(w, data)
		return
	}

	name = strings.TrimSpace(name)

	data := ConfigTenantEditData{Server: c.Name(), Name: name, Content: content, File: "routing"}

	warnings, err := c.CreateTenant(r.Context(), name, content)
	if err != nil {
		data.IsNew = true
		applyWriteError(&data, err, "Failed to create tenant")
	} else {
		data.Success = fmt.Sprintf("Tenant %q created successfully", name)
		data.Warnings = problemData(warnings)
		configSaved(w)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderConfigTenantEdit(w, data); err != nil {
		slog.Error("[UI] Failed to render tenant edit", "error", err)
	}
}

// handleConfigTenantDelete deletes a tenant file
func (s *Server) handleConfigTenantDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	c := s.findClient(r.URL.Query().Get("server"))
	if c == nil {
		http.Error(w, "No backend configured", http.StatusServiceUnavailable)
		return
	}

	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "Tenant name required", http.StatusBadRequest)
		return
	}

	data := ConfigTenantsData{Server: c.Name()}

	if err := c.DeleteTenant(r.Context(), name); err != nil {
		data.Error = fmt.Sprintf("Failed to delete tenant: %v", err)
	} else {
		data.Success = fmt.Sprintf("Tenant %q deleted", name)
	}

	// Reload tenant list
	tenants, err := c.ListTenants(r.Context())
	if err == nil {
		data.Tenants = make([]TenantFileData, 0, len(tenants))
		for _, t := range tenants {
			data.Tenants = append(data.Tenants, TenantFileData{
				Name:     t.Name,
				Size:     t.Size,
				Modified: t.Modified,
				HasFlows: t.HasFlows,
			})
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderConfigTenants(w, data); err != nil {
		slog.Error("[UI] Failed to render config tenants", "error", err)
	}
}

// handleConfigReload triggers a configuration reload
func (s *Server) handleConfigReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	c := s.findClient(r.URL.Query().Get("server"))
	if c == nil {
		http.Error(w, "No backend configured", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	result, err := c.ReloadConfig(r.Context())
	if err != nil {
		slog.Error("[UI] Reload failed", "server", c.Name(), "error", err)
		_, _ = fmt.Fprintf(w, `<div class="flex items-center px-4 py-3 rounded-lg bg-red-500/20 border border-red-500/30 text-red-300 text-sm"><svg class="w-5 h-5 mr-2 flex-shrink-0" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8v4m0 4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"></path></svg>Reload failed: %s</div>`, err.Error())
		return
	}

	// Say what the reload did NOT cover: the deployment-wide files are read once
	// at startup, so answering a plain "reloaded" to an operator who just edited
	// policy.json would be wrong in the way that matters.
	message := result.Message
	if message == "" {
		message = "Configuration reloaded"
	}
	if len(result.NotReloaded) > 0 {
		message += " Waiting on a restart: " + strings.Join(result.NotReloaded, ", ") + "."
	}
	w.Header().Set("HX-Trigger", "config-saved")
	_, _ = fmt.Fprintf(w, `<div class="flex items-start px-4 py-3 rounded-lg bg-emerald-500/20 border border-emerald-500/30 text-emerald-300 text-sm"><svg class="w-5 h-5 mr-2 flex-shrink-0" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7"></path></svg><span>%s</span></div>`, html.EscapeString(message))
}

// applyWriteError renders a failed configuration write into the editor's data.
//
// A refused write carries the individual problems, and showing them against
// their paths is the difference between fixing the flow and guessing at it.
// Every editor goes through here so create, save and the global files all
// report a refusal the same way.
func applyWriteError(data *ConfigTenantEditData, err error, prefix string) {
	var rejected *client.ConfigRejectedError
	if errors.As(err, &rejected) {
		data.Error = "Not saved: the configuration would not load."
		data.Problems = problemData(rejected.Problems())
		return
	}
	data.Error = fmt.Sprintf("%s: %v", prefix, err)
}

// applyGlobalWriteError is applyWriteError for the deployment-wide editor. The
// two data structs are separate because their pages are, but a refusal has to
// read the same way in both.
func applyGlobalWriteError(data *ConfigGlobalEditData, err error, prefix string) {
	var rejected *client.ConfigRejectedError
	if errors.As(err, &rejected) {
		data.Error = "Not saved: the configuration would not load."
		data.Problems = problemData(rejected.Problems())
		return
	}
	data.Error = fmt.Sprintf("%s: %v", prefix, err)
}

// normalizeContent undoes what a browser does to a textarea on submit.
//
// HTML form submission encodes textarea content with CRLF line endings, so
// saving a file through the editor would otherwise rewrite every line of a
// checked-in JSON file the first time anyone opened it. The trailing newline is
// restored for the same reason: a file that ends without one reads as a
// one-line diff on every tool that touches it.
func normalizeContent(content string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content
}

// problemData converts wire problems for the templates.
func problemData(problems []types.ConfigProblem) []ConfigProblemData {
	if len(problems) == 0 {
		return nil
	}
	out := make([]ConfigProblemData, 0, len(problems))
	for _, p := range problems {
		out = append(out, ConfigProblemData{Path: p.Path, Message: p.Message})
	}
	return out
}

// configSaved tells the page a write landed, so the reload banner re-checks
// whether what is on disk is what is serving calls. HTMX fires the named event
// on body, which the banner listens for — no JavaScript involved.
func configSaved(w http.ResponseWriter) {
	w.Header().Set("HX-Trigger", "config-saved")
}
