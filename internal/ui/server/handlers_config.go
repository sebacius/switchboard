package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

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

	activeTab := r.URL.Query().Get("tab")
	if activeTab == "" {
		activeTab = "tenants"
	}

	data := ConfigPageData{
		Title:          "Switchboard Configuration",
		ActiveTab:      activeTab,
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

	content, err := c.GetTenant(r.Context(), name)
	data := ConfigTenantEditData{Server: c.Name(), Name: name}
	if err != nil {
		data.Error = fmt.Sprintf("Failed to load tenant: %v", err)
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

	data := ConfigTenantEditData{
		Server:  c.Name(),
		IsNew:   true,
		Content: "# Tenant Name\n\nDescribe the tenant knowledge base here.\n",
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderConfigTenantEdit(w, data); err != nil {
		slog.Error("[UI] Failed to render tenant new", "error", err)
	}
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
	content := r.FormValue("content")
	data := ConfigTenantEditData{Server: c.Name(), Name: name, Content: content}

	if err := c.PutTenant(r.Context(), name, content); err != nil {
		data.Error = fmt.Sprintf("Failed to save tenant: %v", err)
	} else {
		data.Success = "Tenant saved successfully"
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
	content := r.FormValue("content")

	if name == "" {
		data := ConfigTenantEditData{Server: c.Name(), IsNew: true, Content: content, Error: "Tenant name is required"}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = s.templates.RenderConfigTenantEdit(w, data)
		return
	}

	// Sanitize name
	name = strings.TrimSuffix(name, ".md")
	name = strings.TrimSpace(name)

	data := ConfigTenantEditData{Server: c.Name(), Name: name, Content: content}

	if err := c.CreateTenant(r.Context(), name, content); err != nil {
		data.IsNew = true
		data.Error = fmt.Sprintf("Failed to create tenant: %v", err)
	} else {
		data.Success = fmt.Sprintf("Tenant %q created successfully", name)
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
			})
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderConfigTenants(w, data); err != nil {
		slog.Error("[UI] Failed to render config tenants", "error", err)
	}
}

// handleConfigDialplanPartial renders the dialplan editor partial
func (s *Server) handleConfigDialplanPartial(w http.ResponseWriter, r *http.Request) {
	c := s.findClient(r.URL.Query().Get("server"))
	if c == nil {
		http.Error(w, "No backend configured", http.StatusServiceUnavailable)
		return
	}

	content, err := c.GetDialplan(r.Context())
	data := ConfigDialplanData{Server: c.Name()}
	if err != nil {
		data.Error = fmt.Sprintf("Failed to load dialplan: %v", err)
	} else {
		data.Content = content
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderConfigDialplan(w, data); err != nil {
		slog.Error("[UI] Failed to render config dialplan", "error", err)
	}
}

// handleConfigDialplanSave saves the dialplan.json content
func (s *Server) handleConfigDialplanSave(w http.ResponseWriter, r *http.Request) {
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

	content := r.FormValue("content")
	data := ConfigDialplanData{Server: c.Name(), Content: content}

	if err := c.PutDialplan(r.Context(), content); err != nil {
		data.Error = fmt.Sprintf("Failed to save dialplan: %v", err)
	} else {
		data.Success = "Dialplan saved successfully"
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderConfigDialplan(w, data); err != nil {
		slog.Error("[UI] Failed to render config dialplan", "error", err)
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

	if err := c.ReloadConfig(r.Context()); err != nil {
		slog.Error("[UI] Reload failed", "server", c.Name(), "error", err)
		_, _ = fmt.Fprintf(w, `<div class="flex items-center px-4 py-3 rounded-lg bg-red-500/20 border border-red-500/30 text-red-300 text-sm"><svg class="w-5 h-5 mr-2 flex-shrink-0" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 8v4m0 4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"></path></svg>Reload failed: %s</div>`, err.Error())
		return
	}

	_, _ = fmt.Fprintf(w, `<div class="flex items-center px-4 py-3 rounded-lg bg-emerald-500/20 border border-emerald-500/30 text-emerald-300 text-sm"><svg class="w-5 h-5 mr-2 flex-shrink-0" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7"></path></svg>Configuration reloaded successfully</div>`)
}
