package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/sebas/switchboard/internal/signaling/dialog"
	"github.com/sebas/switchboard/internal/signaling/location"
)

// RegistrationProvider provides registration data for the API.
// Implemented by routing.RegisterHandler.
type RegistrationProvider interface {
	GetAllRegistrations() map[string][]*location.Binding
	GetAllBindings(aor string) []*location.Binding
}

// Server provides HTTP API for the SIP proxy (headless, API only)
type Server struct {
	addr         string
	httpServer   *http.Server
	registrations RegistrationProvider
	dialogMgr    dialog.DialogStore
	sessionsMu   sync.RWMutex
	sessions     map[string]*SessionRecord
	startTime    time.Time
}

// SessionRecord tracks an active RTP session
type SessionRecord struct {
	CallID     string
	ClientAddr string
	ClientPort int
	ServerAddr string
	ServerPort int
	StartTime  time.Time
}

// NewServer creates a new API server (headless, API only - no UI)
func NewServer(addr string, registrations RegistrationProvider, dialogMgr dialog.DialogStore) *Server {
	s := &Server{
		addr:          addr,
		registrations: registrations,
		dialogMgr:     dialogMgr,
		sessions:      make(map[string]*SessionRecord),
		startTime:     time.Now(),
	}

	mux := http.NewServeMux()

	// Health and stats
	mux.HandleFunc("/api/v1/health", s.handleHealth)
	mux.HandleFunc("/api/v1/stats", s.handleStats)

	// Registrations (locations)
	mux.HandleFunc("/api/v1/registrations", s.handleRegistrations)
	mux.HandleFunc("/api/v1/registrations/", s.handleRegistrationByAOR)

	// Dialogs
	mux.HandleFunc("/api/v1/dialogs", s.handleDialogs)
	mux.HandleFunc("/api/v1/dialogs/", s.handleDialogByID)

	// Sessions (RTP)
	mux.HandleFunc("/api/v1/sessions", s.handleSessions)

	// Admin
	mux.HandleFunc("/api/v1/shutdown", s.handleShutdown)

	s.httpServer = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	return s
}

// RecordSession records an active RTP session
func (s *Server) RecordSession(callID string, clientAddr string, clientPort int, serverAddr string, serverPort int) {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()

	s.sessions[callID] = &SessionRecord{
		CallID:     callID,
		ClientAddr: clientAddr,
		ClientPort: clientPort,
		ServerAddr: serverAddr,
		ServerPort: serverPort,
		StartTime:  time.Now(),
	}
}

// RemoveSession removes a session record
func (s *Server) RemoveSession(callID string) {
	s.sessionsMu.Lock()
	defer s.sessionsMu.Unlock()

	delete(s.sessions, callID)
}

// Start begins listening for HTTP requests
func (s *Server) Start() error {
	slog.Info("[API] Starting HTTP API server", "addr", s.addr)
	go func() {
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("[API] Server error", "error", err)
			panic(err)
		}
	}()
	return nil
}

// Stop gracefully shuts down the server
func (s *Server) Stop() error {
	if s.httpServer != nil {
		return s.httpServer.Close()
	}
	return nil
}

// --- Health & Stats ---

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	uptime := time.Since(s.startTime).Seconds()
	response := map[string]interface{}{
		"status": "ok",
		"uptime": int64(uptime),
	}
	s.writeJSON(w, response)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	s.sessionsMu.RLock()
	activeSessions := len(s.sessions)
	s.sessionsMu.RUnlock()

	registrations := s.registrations.GetAllRegistrations()
	totalBindings := 0
	for _, bindings := range registrations {
		totalBindings += len(bindings)
	}

	dialogCount := 0
	if s.dialogMgr != nil {
		dialogCount = s.dialogMgr.Count()
	}

	response := map[string]interface{}{
		"total_sessions":      activeSessions,
		"active_sessions":     activeSessions,
		"total_registrations": len(registrations),
		"total_bindings":      totalBindings,
		"active_dialogs":      dialogCount,
	}
	s.writeJSON(w, response)
}

// --- Registrations ---

func (s *Server) handleRegistrations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	registrations := s.registrations.GetAllRegistrations()

	// Convert to API format
	type bindingResponse struct {
		AOR          string   `json:"aor"`
		ContactURI   string   `json:"contact_uri"`
		BindingID    string   `json:"binding_id"`
		ReceivedIP   string   `json:"received_ip,omitempty"`
		ReceivedPort int      `json:"received_port,omitempty"`
		Transport    string   `json:"transport"`
		Expires      int      `json:"expires"`
		ExpiresAt    string   `json:"expires_at"`
		RegisteredAt string   `json:"registered_at"`
		QValue       float32  `json:"q,omitempty"`
		UserAgent    string   `json:"user_agent,omitempty"`
		InstanceID   string   `json:"instance_id,omitempty"`
		Path         []string `json:"path,omitempty"`
	}

	response := make([]bindingResponse, 0)
	for _, bindings := range registrations {
		for _, b := range bindings {
			response = append(response, bindingResponse{
				AOR:          b.AOR,
				ContactURI:   b.ContactURI,
				BindingID:    b.BindingID,
				ReceivedIP:   b.ReceivedIP,
				ReceivedPort: b.ReceivedPort,
				Transport:    b.Transport,
				Expires:      b.Expires,
				ExpiresAt:    b.ExpiresAt.Format(time.RFC3339),
				RegisteredAt: b.RegisteredAt.Format(time.RFC3339),
				QValue:       b.QValue,
				UserAgent:    b.UserAgent,
				InstanceID:   b.InstanceID,
				Path:         b.Path,
			})
		}
	}

	s.writeJSON(w, response)
}

func (s *Server) handleRegistrationByAOR(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract AOR from path: /api/v1/registrations/{aor}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/registrations/")
	if path == "" {
		http.Error(w, "AOR required", http.StatusBadRequest)
		return
	}

	// URL decode the AOR (may contain special chars like @, :, etc.)
	aor, err := url.PathUnescape(path)
	if err != nil {
		http.Error(w, "Invalid AOR encoding", http.StatusBadRequest)
		return
	}

	bindings := s.registrations.GetAllBindings(aor)
	if len(bindings) == 0 {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	s.writeJSON(w, bindings)
}

// --- Dialogs ---

func (s *Server) handleDialogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.dialogMgr == nil {
		s.writeJSON(w, []interface{}{})
		return
	}

	dialogs := s.dialogMgr.List()
	infos := dialog.ListInfos(dialogs)
	s.writeJSON(w, infos)
}

func (s *Server) handleDialogByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.dialogMgr == nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	// Extract dialog ID from path: /api/v1/dialogs/{id}
	// ID can be Call-ID or full dialog ID (Call-ID;LocalTag;RemoteTag)
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/dialogs/")
	if path == "" {
		http.Error(w, "Dialog ID required", http.StatusBadRequest)
		return
	}

	dialogID, err := url.PathUnescape(path)
	if err != nil {
		http.Error(w, "Invalid dialog ID encoding", http.StatusBadRequest)
		return
	}

	// Try to find by Call-ID (the primary key we use)
	callID := dialogID
	if idx := strings.Index(dialogID, ";"); idx > 0 {
		callID = dialogID[:idx]
	}

	dlg, exists := s.dialogMgr.Get(callID)
	if !exists {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	s.writeJSON(w, dlg.ToInfo())
}

// --- Sessions ---

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.sessionsMu.RLock()
	defer s.sessionsMu.RUnlock()

	sessions := make([]map[string]interface{}, 0)
	for _, session := range s.sessions {
		duration := time.Since(session.StartTime).Seconds()
		sessions = append(sessions, map[string]interface{}{
			"call_id":     session.CallID,
			"client_addr": session.ClientAddr,
			"client_port": session.ClientPort,
			"server_addr": session.ServerAddr,
			"server_port": session.ServerPort,
			"duration":    int(duration),
			"status":      "active",
		})
	}

	s.writeJSON(w, sessions)
}

// --- Admin ---

func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	response := map[string]interface{}{
		"message": "Shutdown initiated",
	}
	s.writeJSON(w, response)
}

// --- Helpers ---

func (s *Server) writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("[API] Failed to encode JSON", "error", err)
	}
}

