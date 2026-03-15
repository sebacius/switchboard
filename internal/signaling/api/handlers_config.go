package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sebas/switchboard/internal/signaling/filemanager"
)

// FileProvider provides file management operations for the API.
type FileProvider interface {
	GetSettings() (string, error)
	PutSettings(content string) error
	ListTenants() ([]filemanager.TenantInfo, error)
	GetTenant(name string) (string, error)
	CreateTenant(name, content string) error
	PutTenant(name, content string) error
	DeleteTenant(name string) error
	GetDialplan() (string, error)
	PutDialplan(content string) error
	Reload() error
}

// SetFileProvider sets the file manager for config API endpoints.
func (s *Server) SetFileProvider(fp FileProvider) {
	s.fileProvider = fp
}

// handleConfigSettings handles GET/PUT /api/v1/config/settings
func (s *Server) handleConfigSettings(w http.ResponseWriter, r *http.Request) {
	if s.fileProvider == nil {
		http.Error(w, "File management not configured", http.StatusServiceUnavailable)
		return
	}

	switch r.Method {
	case http.MethodGet:
		content, err := s.fileProvider.GetSettings()
		if err != nil {
			slog.Error("[API] Failed to read settings", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.writeJSON(w, map[string]string{"content": content})

	case http.MethodPut:
		body, err := io.ReadAll(io.LimitReader(r.Body, 512*1024))
		if err != nil {
			http.Error(w, "Failed to read body", http.StatusBadRequest)
			return
		}
		var req struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "Invalid JSON body", http.StatusBadRequest)
			return
		}
		if err := s.fileProvider.PutSettings(req.Content); err != nil {
			slog.Error("[API] Failed to write settings", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.writeJSON(w, map[string]string{"status": "ok"})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleConfigTenantList handles GET /api/v1/config/tenants and POST (create)
func (s *Server) handleConfigTenantList(w http.ResponseWriter, r *http.Request) {
	if s.fileProvider == nil {
		http.Error(w, "File management not configured", http.StatusServiceUnavailable)
		return
	}

	switch r.Method {
	case http.MethodGet:
		tenants, err := s.fileProvider.ListTenants()
		if err != nil {
			slog.Error("[API] Failed to list tenants", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Convert to API format
		result := make([]map[string]interface{}, 0, len(tenants))
		for _, t := range tenants {
			result = append(result, map[string]interface{}{
				"name":     t.Name,
				"size":     t.Size,
				"modified": t.Modified.Format(time.RFC3339),
			})
		}
		s.writeJSON(w, result)

	case http.MethodPost:
		body, err := io.ReadAll(io.LimitReader(r.Body, 2*1024*1024))
		if err != nil {
			http.Error(w, "Failed to read body", http.StatusBadRequest)
			return
		}
		var req struct {
			Name    string `json:"name"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "Invalid JSON body", http.StatusBadRequest)
			return
		}
		if req.Name == "" {
			http.Error(w, "Name is required", http.StatusBadRequest)
			return
		}
		if err := s.fileProvider.CreateTenant(req.Name, req.Content); err != nil {
			slog.Error("[API] Failed to create tenant", "name", req.Name, "error", err)
			if strings.Contains(err.Error(), "already exists") {
				http.Error(w, err.Error(), http.StatusConflict)
			} else {
				http.Error(w, err.Error(), http.StatusBadRequest)
			}
			return
		}
		w.WriteHeader(http.StatusCreated)
		s.writeJSON(w, map[string]string{"status": "created", "name": req.Name})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleConfigTenant handles GET/PUT/DELETE /api/v1/config/tenants/{name}
func (s *Server) handleConfigTenant(w http.ResponseWriter, r *http.Request) {
	if s.fileProvider == nil {
		http.Error(w, "File management not configured", http.StatusServiceUnavailable)
		return
	}

	// Extract tenant name from path
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/config/tenants/")
	if path == "" {
		http.Error(w, "Tenant name required", http.StatusBadRequest)
		return
	}
	name, err := url.PathUnescape(path)
	if err != nil {
		http.Error(w, "Invalid tenant name encoding", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		content, err := s.fileProvider.GetTenant(name)
		if err != nil {
			if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "no such file") {
				http.Error(w, "Tenant not found", http.StatusNotFound)
			} else {
				http.Error(w, err.Error(), http.StatusBadRequest)
			}
			return
		}
		s.writeJSON(w, map[string]string{"name": name, "content": content})

	case http.MethodPut:
		body, err := io.ReadAll(io.LimitReader(r.Body, 2*1024*1024))
		if err != nil {
			http.Error(w, "Failed to read body", http.StatusBadRequest)
			return
		}
		var req struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "Invalid JSON body", http.StatusBadRequest)
			return
		}
		if err := s.fileProvider.PutTenant(name, req.Content); err != nil {
			slog.Error("[API] Failed to update tenant", "name", name, "error", err)
			if strings.Contains(err.Error(), "not found") {
				http.Error(w, err.Error(), http.StatusNotFound)
			} else {
				http.Error(w, err.Error(), http.StatusBadRequest)
			}
			return
		}
		s.writeJSON(w, map[string]string{"status": "ok"})

	case http.MethodDelete:
		if err := s.fileProvider.DeleteTenant(name); err != nil {
			slog.Error("[API] Failed to delete tenant", "name", name, "error", err)
			if strings.Contains(err.Error(), "not found") {
				http.Error(w, err.Error(), http.StatusNotFound)
			} else {
				http.Error(w, err.Error(), http.StatusBadRequest)
			}
			return
		}
		s.writeJSON(w, map[string]string{"status": "deleted", "name": name})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleConfigDialplan handles GET/PUT /api/v1/config/dialplan
func (s *Server) handleConfigDialplan(w http.ResponseWriter, r *http.Request) {
	if s.fileProvider == nil {
		http.Error(w, "File management not configured", http.StatusServiceUnavailable)
		return
	}

	switch r.Method {
	case http.MethodGet:
		content, err := s.fileProvider.GetDialplan()
		if err != nil {
			slog.Error("[API] Failed to read dialplan", "error", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.writeJSON(w, map[string]string{"content": content})

	case http.MethodPut:
		body, err := io.ReadAll(io.LimitReader(r.Body, 512*1024))
		if err != nil {
			http.Error(w, "Failed to read body", http.StatusBadRequest)
			return
		}
		var req struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "Invalid JSON body", http.StatusBadRequest)
			return
		}
		if err := s.fileProvider.PutDialplan(req.Content); err != nil {
			slog.Error("[API] Failed to write dialplan", "error", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.writeJSON(w, map[string]string{"status": "ok"})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleConfigReload handles POST /api/v1/config/reload
func (s *Server) handleConfigReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.fileProvider == nil {
		http.Error(w, "File management not configured", http.StatusServiceUnavailable)
		return
	}

	if err := s.fileProvider.Reload(); err != nil {
		slog.Error("[API] Reload failed", "error", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	slog.Info("[API] Configuration reloaded successfully")
	s.writeJSON(w, map[string]string{"status": "ok", "message": "Configuration reloaded"})
}
