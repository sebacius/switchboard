package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/sebas/switchboard/services/signaling/registration"
)

// Server provides HTTP API for the SIP proxy
type Server struct {
	addr            string
	httpServer      *http.Server
	registrationMgr *registration.Handler
	sessionsMu      sync.RWMutex
	sessions        map[string]*SessionRecord
	startTime       time.Time
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

// NewServer creates a new API server
func NewServer(addr string, registrationMgr *registration.Handler) *Server {
	s := &Server{
		addr:            addr,
		registrationMgr: registrationMgr,
		sessions:        make(map[string]*SessionRecord),
		startTime:       time.Now(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", s.handleHealth)
	mux.HandleFunc("/api/v1/sessions", s.handleSessions)
	mux.HandleFunc("/api/v1/registrations", s.handleRegistrations)
	mux.HandleFunc("/api/v1/stats", s.handleStats)
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

// Handler methods
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	uptime := time.Since(s.startTime).Seconds()
	response := map[string]interface{}{
		"status": "ok",
		"uptime": int64(uptime),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sessions)
}

func (s *Server) handleRegistrations(w http.ResponseWriter, r *http.Request) {
	registrations := s.registrationMgr.GetAllRegistrations()

	response := make([]map[string]interface{}, 0)
	for _, reg := range registrations {
		response = append(response, map[string]interface{}{
			"user":    reg.AOR,
			"contact": reg.Contact,
			"expires": reg.Expires,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	s.sessionsMu.RLock()
	activeSessions := len(s.sessions)
	s.sessionsMu.RUnlock()

	registrations := s.registrationMgr.GetAllRegistrations()

	response := map[string]interface{}{
		"total_sessions":      activeSessions,
		"active_sessions":     activeSessions,
		"total_registrations": len(registrations),
		"requests_per_second": 0, // Placeholder
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	response := map[string]interface{}{
		"message": "Shutdown initiated",
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}
