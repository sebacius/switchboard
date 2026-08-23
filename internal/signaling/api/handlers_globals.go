package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/sebas/switchboard/internal/signaling/filemanager"
)

// The deployment-wide files, as opposed to the per-tenant pair.
//
// Two things make this surface different from the tenant one. A global file is
// addressed by NAME against a closed allowlist, never by path — the API cannot
// be talked into reading /etc/passwd because it never joins a caller's string
// onto a directory. And policy.json is the authorization boundary: it decides
// which tenant may dial out and how much. This port has no authentication, so
// writing it is off unless an operator turns it on.

// handleConfigFileList handles GET /api/v1/config/files.
func (s *Server) handleConfigFileList(w http.ResponseWriter, r *http.Request) {
	if s.fileProvider == nil {
		http.Error(w, "File management not configured", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.writeJSON(w, s.fileProvider.ListGlobalFiles())
}

// handleConfigFile handles GET/PUT /api/v1/config/files/{name}.
func (s *Server) handleConfigFile(w http.ResponseWriter, r *http.Request) {
	if s.fileProvider == nil {
		http.Error(w, "File management not configured", http.StatusServiceUnavailable)
		return
	}

	raw := strings.TrimPrefix(r.URL.Path, "/api/v1/config/files/")
	name, err := url.PathUnescape(raw)
	if err != nil {
		http.Error(w, "Invalid file name encoding", http.StatusBadRequest)
		return
	}
	kind, ok := filemanager.ParseGlobalKind(name)
	if !ok {
		// Not "forbidden path" — this API addresses three files and that is not
		// one of their names.
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodGet:
		content, err := s.fileProvider.GetGlobalFile(kind)
		if err != nil {
			if errors.Is(err, filemanager.ErrNotConfigured) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		info := s.globalInfo(kind)
		s.writeJSON(w, map[string]any{
			"name":       string(kind),
			"path":       info.Path,
			"content":    content,
			"exists":     info.Exists,
			"writable":   s.allowGlobalWrites,
			"activation": string(info.Activation),
		})

	case http.MethodPut:
		if !s.allowGlobalWrites {
			http.Error(w,
				"Writing deployment-wide configuration is disabled. Start the signaling server "+
					"with --allow-global-config-writes to enable it.",
				http.StatusForbidden)
			return
		}
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

		warnings, err := s.fileProvider.PutGlobalFile(kind, req.Content)
		if err != nil {
			slog.Error("[API] Failed to write global config", "file", kind, "error", err)
			var verr *filemanager.ValidationError
			if errors.As(err, &verr) {
				s.writeValidationError(w, verr, string(kind))
				return
			}
			if errors.Is(err, filemanager.ErrNotConfigured) {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.writeSaved(w, map[string]any{
			"status":     "ok",
			"name":       string(kind),
			"activation": string(s.globalInfo(kind).Activation),
		}, warnings)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// globalInfo finds one file's description in the provider's list.
func (s *Server) globalInfo(kind filemanager.GlobalKind) filemanager.GlobalFileInfo {
	for _, info := range s.fileProvider.ListGlobalFiles() {
		if info.Name == kind {
			return info
		}
	}
	return filemanager.GlobalFileInfo{Name: kind, Activation: filemanager.ActivationRestart}
}

// handleConfigStatus handles GET /api/v1/config/status: whether what is on disk
// is what is serving calls.
func (s *Server) handleConfigStatus(w http.ResponseWriter, r *http.Request) {
	if s.fileProvider == nil {
		http.Error(w, "File management not configured", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.writeJSON(w, s.fileProvider.ConfigStatus())
}
