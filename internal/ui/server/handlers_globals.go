package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	types "github.com/sebas/switchboard/api/types/v1"
)

// handleConfigGlobalsPartial lists the deployment-wide configuration files.
func (s *Server) handleConfigGlobalsPartial(w http.ResponseWriter, r *http.Request) {
	c := s.findClient(r.URL.Query().Get("server"))
	if c == nil {
		http.Error(w, "No backend configured", http.StatusServiceUnavailable)
		return
	}

	data := ConfigGlobalsData{Server: c.Name()}
	files, err := c.ListGlobalFiles(r.Context())
	if err != nil {
		data.Error = fmt.Sprintf("Failed to load configuration files: %v", err)
	} else {
		for _, f := range files {
			data.Files = append(data.Files, globalFileData(f))
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderConfigGlobals(w, data); err != nil {
		slog.Error("[UI] Failed to render global config list", "error", err)
	}
}

// handleConfigGlobalEdit renders the editor for one deployment-wide file.
func (s *Server) handleConfigGlobalEdit(w http.ResponseWriter, r *http.Request) {
	c := s.findClient(r.URL.Query().Get("server"))
	if c == nil {
		http.Error(w, "No backend configured", http.StatusServiceUnavailable)
		return
	}

	name := r.URL.Query().Get("name")
	data := ConfigGlobalEditData{Server: c.Name(), Name: name}

	file, err := c.GetGlobalFile(r.Context(), name)
	if err != nil {
		data.Error = fmt.Sprintf("Failed to load %s: %v", name, err)
	} else {
		data.File = globalFileData(file)
		data.Content = file.Content
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderConfigGlobalEdit(w, data); err != nil {
		slog.Error("[UI] Failed to render global config editor", "error", err)
	}
}

// handleConfigGlobalSave writes one deployment-wide file.
//
// A save here is only half of an activation: all three files are read once at
// startup, so the editor has to keep saying "restart" rather than letting the
// Reload button imply otherwise.
func (s *Server) handleConfigGlobalSave(w http.ResponseWriter, r *http.Request) {
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
	data := ConfigGlobalEditData{Server: c.Name(), Name: name, Content: content}

	// Re-fetch the description so the page keeps its title and its restart
	// notice whichever way the save went.
	if file, err := c.GetGlobalFile(r.Context(), name); err == nil {
		data.File = globalFileData(file)
	}

	warnings, err := c.PutGlobalFile(r.Context(), name, content)
	if err != nil {
		applyGlobalWriteError(&data, err, fmt.Sprintf("Failed to save %s", name))
	} else {
		data.Success = fmt.Sprintf("Saved %s. It takes effect when the signaling server restarts.", name)
		data.Warnings = problemData(warnings)
		configSaved(w)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderConfigGlobalEdit(w, data); err != nil {
		slog.Error("[UI] Failed to render global config editor", "error", err)
	}
}

// handleReloadBanner reports whether what is on disk is what is serving calls.
//
// It asks the signaling server rather than remembering what this UI saved,
// because the question outlives a browser tab: another operator, a UI restart,
// or a signaling restart that already picked the change up.
func (s *Server) handleReloadBanner(w http.ResponseWriter, r *http.Request) {
	c := s.findClient(r.URL.Query().Get("server"))
	if c == nil {
		return
	}

	status, err := c.ConfigStatus(r.Context())
	if err != nil {
		// A banner is not worth an error box of its own; the page's real
		// content will have reported the backend being unreachable.
		return
	}

	data := ReloadBannerData{
		Server:       c.Name(),
		StaleTenants: status.StaleTenants,
		StaleGlobals: status.StaleGlobals,
	}
	if t, err := time.Parse(time.RFC3339, status.LoadedAt); err == nil {
		data.LoadedAt = t.Format("15:04:05")
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderReloadBanner(w, data); err != nil {
		slog.Error("[UI] Failed to render reload banner", "error", err)
	}
}

// globalFileData converts a wire description for the templates.
func globalFileData(f types.GlobalFile) GlobalFileData {
	d := GlobalFileData{
		Name:        f.Name,
		Path:        f.Path,
		Size:        f.Size,
		Exists:      f.Exists,
		Configured:  f.Configured,
		Writable:    f.Writable,
		Activation:  f.Activation,
		Title:       f.Title,
		Description: f.Description,
	}
	if t, err := time.Parse(time.RFC3339, f.Modified); err == nil && !t.IsZero() {
		d.Modified = t.Format("2006-01-02 15:04")
	}
	return d
}
