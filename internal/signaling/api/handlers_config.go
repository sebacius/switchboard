package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/sebas/switchboard/internal/signaling/dialplan"
	"github.com/sebas/switchboard/internal/signaling/filemanager"
)

// FileProvider provides file management operations for the API.
type FileProvider interface {
	ListTenants() ([]filemanager.TenantInfo, error)
	GetTenant(name string) (string, error)
	CreateTenant(name, content string) (dialplan.Problems, error)
	PutTenant(name, content string) (dialplan.Problems, error)
	DeleteTenant(name string) error

	// GetTenantFile and PutTenantFile address a tenant's routing table or its
	// flows. A write that would not load is refused; one that would load but is
	// questionable is written, and the warnings come back with it.
	GetTenantFile(name string, kind filemanager.FileKind) (string, error)
	PutTenantFile(name string, kind filemanager.FileKind, content string) (dialplan.Problems, error)

	Reload() error
}

// fileKindFrom reads the ?file= parameter, defaulting to the routing table.
func fileKindFrom(r *http.Request) (filemanager.FileKind, error) {
	switch v := r.URL.Query().Get("file"); v {
	case "", string(filemanager.KindRouting):
		return filemanager.KindRouting, nil
	case string(filemanager.KindFlows):
		return filemanager.KindFlows, nil
	default:
		return "", fmt.Errorf("unknown file %q (want %q or %q)",
			v, filemanager.KindRouting, filemanager.KindFlows)
	}
}

// problemList renders problems for the wire. One shape for refusals and for the
// warnings that ride along with a successful write, so a client parses them the
// same way whichever it got.
func problemList(problems dialplan.Problems) []map[string]string {
	out := make([]map[string]string, 0, len(problems))
	for _, p := range problems {
		out = append(out, map[string]string{
			"path":     p.Path,
			"message":  p.Message,
			"severity": string(p.Severity),
		})
	}
	return out
}

// writeRejection renders a refused write so the caller can see WHICH node is
// wrong rather than only that something is. Both the tenant files and the
// deployment-wide files use it, so an editor renders either the same way.
func (s *Server) writeRejection(w http.ResponseWriter, tenant, file string, problems dialplan.Problems) {
	w.WriteHeader(http.StatusUnprocessableEntity)
	s.writeJSON(w, map[string]any{
		"error":    "configuration is not valid and was not saved",
		"tenant":   tenant,
		"file":     file,
		"problems": problemList(problems),
	})
}

// writeValidationError renders a refused tenant write.
func (s *Server) writeValidationError(w http.ResponseWriter, verr *filemanager.ValidationError, file string) {
	s.writeRejection(w, verr.Tenant, file, verr.ValidationProblems())
}

// writeSaved renders a successful write, carrying any warnings the validator
// raised. A warning does not block the save — refusing one would make the
// editor stricter than the loader — so it has to travel with the success.
func (s *Server) writeSaved(w http.ResponseWriter, fields map[string]any, warnings dialplan.Problems) {
	if len(warnings) > 0 {
		fields["warnings"] = problemList(warnings)
	}
	s.writeJSON(w, fields)
}

// SetFileProvider sets the file manager for config API endpoints.
func (s *Server) SetFileProvider(fp FileProvider) {
	s.fileProvider = fp
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
				"name":      t.Name,
				"size":      t.Size,
				"modified":  t.Modified.Format(time.RFC3339),
				"has_flows": t.HasFlows,
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
		warnings, err := s.fileProvider.CreateTenant(req.Name, req.Content)
		if err != nil {
			slog.Error("[API] Failed to create tenant", "name", req.Name, "error", err)
			var verr *filemanager.ValidationError
			if errors.As(err, &verr) {
				s.writeValidationError(w, verr, string(filemanager.KindRouting))
				return
			}
			if errors.Is(err, filemanager.ErrAlreadyExists) {
				http.Error(w, err.Error(), http.StatusConflict)
			} else {
				http.Error(w, err.Error(), http.StatusBadRequest)
			}
			return
		}
		w.WriteHeader(http.StatusCreated)
		s.writeSaved(w, map[string]any{"status": "created", "name": req.Name}, warnings)

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
		kind, kindErr := fileKindFrom(r)
		if kindErr != nil {
			http.Error(w, kindErr.Error(), http.StatusBadRequest)
			return
		}
		content, err := s.fileProvider.GetTenantFile(name, kind)
		if err != nil {
			if errors.Is(err, filemanager.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
				http.Error(w, "Tenant not found", http.StatusNotFound)
			} else {
				http.Error(w, err.Error(), http.StatusBadRequest)
			}
			return
		}
		s.writeJSON(w, map[string]string{"name": name, "file": string(kind), "content": content})

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
		kind, kindErr := fileKindFrom(r)
		if kindErr != nil {
			http.Error(w, kindErr.Error(), http.StatusBadRequest)
			return
		}
		warnings, err := s.fileProvider.PutTenantFile(name, kind, req.Content)
		if err != nil {
			slog.Error("[API] Failed to update tenant", "name", name, "file", kind, "error", err)

			// A configuration that would not load is refused with the problems
			// attached, so whoever saved it can see which node is wrong rather
			// than only that something is.
			var verr *filemanager.ValidationError
			if errors.As(err, &verr) {
				s.writeValidationError(w, verr, string(kind))
				return
			}
			if errors.Is(err, filemanager.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
				http.Error(w, err.Error(), http.StatusNotFound)
			} else {
				http.Error(w, err.Error(), http.StatusBadRequest)
			}
			return
		}
		s.writeSaved(w, map[string]any{"status": "ok", "file": string(kind)}, warnings)

	case http.MethodDelete:
		if err := s.fileProvider.DeleteTenant(name); err != nil {
			slog.Error("[API] Failed to delete tenant", "name", name, "error", err)
			if errors.Is(err, filemanager.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
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
