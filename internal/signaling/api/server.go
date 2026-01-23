package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/sebas/switchboard/internal/signaling/dialog"
	"github.com/sebas/switchboard/internal/signaling/drain"
	"github.com/sebas/switchboard/internal/signaling/location"
	"github.com/sebas/switchboard/internal/signaling/mediaclient"
)

// RegistrationProvider provides registration data for the API.
// Implemented by routing.RegisterHandler.
type RegistrationProvider interface {
	GetAllRegistrations() map[string][]*location.Binding
	GetAllBindings(aor string) []*location.Binding
}

// RtpManagerProvider provides RTP manager pool stats for the API.
// Implemented by mediaclient.Pool via StatsProvider interface.
type RtpManagerProvider interface {
	Stats() mediaclient.PoolStats
}

// DrainProvider provides drain operations for the API.
// Implemented by drain.Coordinator.
type DrainProvider interface {
	StartDrain(ctx context.Context, req drain.DrainRequest) (*drain.DrainStatus, error)
	GetDrainStatus(nodeID string) (*drain.DrainStatus, error)
	CancelDrain(nodeID string) error
}

// Server provides HTTP API for the SIP proxy (headless, API only)
type Server struct {
	addr          string
	httpServer    *http.Server
	registrations RegistrationProvider
	dialogMgr     dialog.DialogStore
	rtpManagers   RtpManagerProvider
	drainProvider DrainProvider
	sessionsMu    sync.RWMutex
	sessions      map[string]*SessionRecord
	startTime     time.Time
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
func NewServer(addr string, registrations RegistrationProvider, dialogMgr dialog.DialogStore, rtpManagers RtpManagerProvider) *Server {
	s := &Server{
		addr:          addr,
		registrations: registrations,
		dialogMgr:     dialogMgr,
		rtpManagers:   rtpManagers,
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

	// RTP Managers
	mux.HandleFunc("/api/v1/rtpmanagers", s.handleRtpManagers)
	mux.HandleFunc("/api/v1/rtpmanagers/", s.handleRtpManagerDrain)

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

// --- RTP Managers ---

func (s *Server) handleRtpManagers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.rtpManagers == nil {
		// No RTP manager pool configured
		response := map[string]interface{}{
			"total_members":   0,
			"healthy_members": 0,
			"active_sessions": 0,
			"members":         []interface{}{},
		}
		s.writeJSON(w, response)
		return
	}

	stats := s.rtpManagers.Stats()

	members := make([]map[string]interface{}, 0, len(stats.Members))
	for _, m := range stats.Members {
		members = append(members, map[string]interface{}{
			"node_id":       m.NodeID,
			"address":       m.Address,
			"healthy":       m.Healthy,
			"drain_state":   m.DrainState.String(),
			"session_count": m.SessionCount,
		})
	}

	response := map[string]interface{}{
		"total_members":   stats.TotalMembers,
		"healthy_members": stats.HealthyMembers,
		"active_sessions": stats.ActiveSessions,
		"members":         members,
	}
	s.writeJSON(w, response)
}

// SetDrainProvider sets the drain coordinator for drain API endpoints
func (s *Server) SetDrainProvider(dp DrainProvider) {
	s.drainProvider = dp
}

// handleRtpManagerDrain handles drain operations for specific RTP managers
// POST /api/v1/rtpmanagers/{nodeId}/drain - Start drain
// GET /api/v1/rtpmanagers/{nodeId}/drain - Get drain status
// DELETE /api/v1/rtpmanagers/{nodeId}/drain - Cancel drain
func (s *Server) handleRtpManagerDrain(w http.ResponseWriter, r *http.Request) {
	// Parse node ID and endpoint from path
	// Expected paths:
	// - /api/v1/rtpmanagers/{nodeId}/drain
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/rtpmanagers/")
	parts := strings.Split(path, "/")

	if len(parts) != 2 || parts[1] != "drain" {
		http.Error(w, "Invalid path. Expected /api/v1/rtpmanagers/{nodeId}/drain", http.StatusNotFound)
		return
	}

	nodeID := parts[0]
	if nodeID == "" {
		http.Error(w, "Node ID required", http.StatusBadRequest)
		return
	}

	if s.drainProvider == nil {
		http.Error(w, "Drain not configured", http.StatusServiceUnavailable)
		return
	}

	switch r.Method {
	case http.MethodPost:
		s.handleStartDrain(w, r, nodeID)
	case http.MethodGet:
		s.handleGetDrainStatus(w, nodeID)
	case http.MethodDelete:
		s.handleCancelDrain(w, nodeID)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleStartDrain initiates drain for a node
func (s *Server) handleStartDrain(w http.ResponseWriter, r *http.Request, nodeID string) {
	// Parse mode from query param
	mode := drain.DrainModeGraceful
	if modeParam := r.URL.Query().Get("mode"); modeParam != "" {
		switch modeParam {
		case "graceful":
			mode = drain.DrainModeGraceful
		case "aggressive":
			mode = drain.DrainModeAggressive
		default:
			http.Error(w, "Invalid mode. Use 'graceful' or 'aggressive'", http.StatusBadRequest)
			return
		}
	}

	req := drain.DrainRequest{
		NodeID: nodeID,
		Mode:   mode,
	}

	// Use background context, NOT r.Context()!
	// The HTTP request completes immediately, but drain runs in background.
	// If we use r.Context(), it gets canceled when the response is sent,
	// which would abort the drain operation before any migrations can happen.
	status, err := s.drainProvider.StartDrain(context.Background(), req)
	if err != nil {
		slog.Error("[API] Failed to start drain", "node_id", nodeID, "error", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusAccepted)
	s.writeJSON(w, map[string]interface{}{
		"message":     "Drain started",
		"node_id":     status.NodeID,
		"mode":        status.Mode,
		"total_sessions": status.TotalSessions,
	})
}

// handleGetDrainStatus returns the current drain status
func (s *Server) handleGetDrainStatus(w http.ResponseWriter, nodeID string) {
	status, err := s.drainProvider.GetDrainStatus(nodeID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	response := map[string]interface{}{
		"node_id":          status.NodeID,
		"state":            status.State.String(),
		"mode":             status.Mode,
		"total_sessions":   status.TotalSessions,
		"waiting_playback": status.WaitingPlayback,
		"migrated_count":   status.MigratedCount,
		"failed_count":     status.FailedCount,
	}

	if !status.StartedAt.IsZero() {
		response["started_at"] = status.StartedAt.Format(time.RFC3339)
		response["elapsed_seconds"] = int(time.Since(status.StartedAt).Seconds())
	}

	if len(status.Errors) > 0 {
		errors := make([]map[string]interface{}, 0, len(status.Errors))
		for _, e := range status.Errors {
			errors = append(errors, map[string]interface{}{
				"session_id": e.SessionID,
				"error":      e.Error,
				"timestamp":  e.Timestamp.Format(time.RFC3339),
			})
		}
		response["errors"] = errors
	}

	s.writeJSON(w, response)
}

// handleCancelDrain cancels an in-progress drain
func (s *Server) handleCancelDrain(w http.ResponseWriter, nodeID string) {
	if err := s.drainProvider.CancelDrain(nodeID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.writeJSON(w, map[string]interface{}{
		"message": "Drain cancelled",
		"node_id": nodeID,
	})
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
