package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/sebas/switchboard/internal/ui/client"
	"github.com/sebas/switchboard/internal/ui/config"
)

// Server provides the UI HTTP server that aggregates data from multiple backends
type Server struct {
	config     *config.Config
	httpServer *http.Server
	clients    []*client.Client
	templates  *Templates
	startTime  time.Time
}

// NewServer creates a new UI server
func NewServer(cfg *config.Config) (*Server, error) {
	s := &Server{
		config:    cfg,
		startTime: time.Now(),
	}

	// Create clients for each backend
	s.clients = make([]*client.Client, 0, len(cfg.Backends))
	for _, backend := range cfg.Backends {
		c := client.NewClient(backend.Name, backend.Address)
		s.clients = append(s.clients, c)
		slog.Info("[UI] Added backend", "name", backend.Name, "address", backend.Address)
	}

	// Initialize templates
	var err error
	s.templates, err = NewTemplates()
	if err != nil {
		return nil, fmt.Errorf("load templates: %w", err)
	}

	// Set up routes
	mux := http.NewServeMux()

	// Admin UI routes
	mux.HandleFunc("/", s.handleDashboard)
	mux.HandleFunc("/admin/partials/stats", s.handleStatsPartial)
	mux.HandleFunc("/admin/partials/backends", s.handleBackendsPartial)
	mux.HandleFunc("/admin/partials/registrations", s.handleRegistrationsPartial)
	mux.HandleFunc("/admin/partials/dialogs", s.handleDialogsPartial)
	mux.HandleFunc("/admin/partials/sessions", s.handleSessionsPartial)
	mux.HandleFunc("/admin/partials/rtpmanagers", s.handleRtpManagersPartial)

	// RTP Manager drain control endpoints
	mux.HandleFunc("/admin/rtpmanagers/drain-modal", s.handleDrainModal)
	mux.HandleFunc("/admin/rtpmanagers/drain", s.handleDrain)
	mux.HandleFunc("/admin/rtpmanagers/cancel-drain", s.handleCancelDrain)

	// Health check
	mux.HandleFunc("/health", s.handleHealth)

	addr := fmt.Sprintf("%s:%d", cfg.BindAddr, cfg.Port)
	s.httpServer = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	return s, nil
}

// Start begins listening for HTTP requests
func (s *Server) Start() error {
	slog.Info("[UI] Starting HTTP server", "addr", s.httpServer.Addr)
	go func() {
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("[UI] Server error", "error", err)
		}
	}()
	return nil
}

// Stop gracefully shuts down the server
func (s *Server) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.httpServer.Shutdown(ctx)
}

// handleHealth returns the health status of the UI server
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"status":"ok","uptime":%d}`, int64(time.Since(s.startTime).Seconds()))
}

// handleDashboard renders the main admin dashboard
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	data := s.buildTemplateData(r.Context())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderDashboard(w, data); err != nil {
		slog.Error("[UI] Failed to render dashboard", "error", err)
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
	}
}

// handleStatsPartial renders the stats cards partial for HTMX
func (s *Server) handleStatsPartial(w http.ResponseWriter, r *http.Request) {
	data := s.buildTemplateData(r.Context())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderStats(w, data); err != nil {
		slog.Error("[UI] Failed to render stats partial", "error", err)
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
	}
}

// handleBackendsPartial renders the backends status partial for HTMX
func (s *Server) handleBackendsPartial(w http.ResponseWriter, r *http.Request) {
	data := s.buildTemplateData(r.Context())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderBackends(w, data); err != nil {
		slog.Error("[UI] Failed to render backends partial", "error", err)
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
	}
}

// handleRegistrationsPartial renders the registrations table partial for HTMX
func (s *Server) handleRegistrationsPartial(w http.ResponseWriter, r *http.Request) {
	data := s.buildTemplateData(r.Context())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderRegistrations(w, data); err != nil {
		slog.Error("[UI] Failed to render registrations partial", "error", err)
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
	}
}

// handleDialogsPartial renders the dialogs table partial for HTMX
func (s *Server) handleDialogsPartial(w http.ResponseWriter, r *http.Request) {
	data := s.buildTemplateData(r.Context())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderDialogs(w, data); err != nil {
		slog.Error("[UI] Failed to render dialogs partial", "error", err)
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
	}
}

// handleSessionsPartial renders the sessions table partial for HTMX
func (s *Server) handleSessionsPartial(w http.ResponseWriter, r *http.Request) {
	data := s.buildTemplateData(r.Context())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderSessions(w, data); err != nil {
		slog.Error("[UI] Failed to render sessions partial", "error", err)
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
	}
}

// handleRtpManagersPartial renders the RTP managers section partial for HTMX
func (s *Server) handleRtpManagersPartial(w http.ResponseWriter, r *http.Request) {
	data := s.buildTemplateData(r.Context())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderRtpManagers(w, data); err != nil {
		slog.Error("[UI] Failed to render rtpmanagers partial", "error", err)
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
	}
}

// buildTemplateData fetches data from all backends and aggregates it
func (s *Server) buildTemplateData(ctx context.Context) TemplateData {
	uptime := time.Since(s.startTime)
	uptimeStr := formatUptime(uptime)

	data := TemplateData{
		Title: "Switchboard Admin",
		Health: HealthData{
			Status: "ok",
			Uptime: uptimeStr,
		},
		Stats:         StatsData{},
		Backends:      make([]BackendData, 0, len(s.clients)),
		RtpManagers:   make([]RtpManagerData, 0),
		Registrations: make([]RegistrationData, 0),
		Dialogs:       make([]DialogData, 0),
		Sessions:      make([]SessionData, 0),
		MultiBackend:  len(s.clients) > 1,
	}

	// Fetch data from all backends concurrently
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, c := range s.clients {
		wg.Add(1)
		go func(c *client.Client) {
			defer wg.Done()
			s.fetchBackendData(ctx, c, &data, &mu)
		}(c)
	}

	wg.Wait()
	return data
}

// fetchBackendData fetches all data from a single backend
func (s *Server) fetchBackendData(ctx context.Context, c *client.Client, data *TemplateData, mu *sync.Mutex) {
	backendName := c.Name()
	backendData := BackendData{
		Name:    backendName,
		Address: c.BaseURL(),
		Status:  "offline",
	}

	// Fetch health
	health, err := c.Health(ctx)
	if err != nil {
		slog.Debug("[UI] Backend health check failed", "backend", backendName, "error", err)
		mu.Lock()
		data.Backends = append(data.Backends, backendData)
		mu.Unlock()
		return
	}

	backendData.Status = health.Status
	backendData.Uptime = formatUptime(time.Duration(health.Uptime) * time.Second)

	// Fetch stats
	stats, err := c.Stats(ctx)
	if err != nil {
		slog.Debug("[UI] Backend stats fetch failed", "backend", backendName, "error", err)
	} else {
		mu.Lock()
		data.Stats.ActiveSessions += stats.ActiveSessions
		data.Stats.TotalRegistrations += stats.TotalRegistrations
		data.Stats.TotalBindings += stats.TotalBindings
		data.Stats.ActiveDialogs += stats.ActiveDialogs
		mu.Unlock()
	}

	// Fetch registrations
	regs, err := c.Registrations(ctx)
	if err != nil {
		slog.Debug("[UI] Backend registrations fetch failed", "backend", backendName, "error", err)
	} else {
		mu.Lock()
		for _, r := range regs {
			expiresAt, _ := time.Parse(time.RFC3339, r.ExpiresAt)
			ttl := time.Until(expiresAt)
			ttlStr := "expired"
			if ttl > 0 {
				ttlStr = formatDuration(int(ttl.Seconds()))
			}
			registeredAt, _ := time.Parse(time.RFC3339, r.RegisteredAt)

			data.Registrations = append(data.Registrations, RegistrationData{
				Server:       backendName,
				AOR:          r.AOR,
				ContactURI:   r.ContactURI,
				Transport:    r.Transport,
				ReceivedIP:   r.ReceivedIP,
				ReceivedPort: r.ReceivedPort,
				Expires:      r.Expires,
				TTL:          ttlStr,
				UserAgent:    r.UserAgent,
				RegisteredAt: registeredAt.Format("15:04:05"),
			})
		}
		mu.Unlock()
	}

	// Fetch dialogs
	dialogs, err := c.Dialogs(ctx)
	if err != nil {
		slog.Debug("[UI] Backend dialogs fetch failed", "backend", backendName, "error", err)
	} else {
		mu.Lock()
		for _, d := range dialogs {
			data.Dialogs = append(data.Dialogs, DialogData{
				Server:          backendName,
				CallID:          d.CallID,
				Direction:       d.Direction,
				State:           d.State,
				LocalURI:        d.LocalURI,
				RemoteURI:       d.RemoteURI,
				RemoteAddr:      d.RemoteAddr,
				RemotePort:      d.RemotePort,
				Duration:        formatDuration(d.Duration),
				CreatedAt:       d.CreatedAt,
				TerminateReason: d.TerminateReason,
			})
		}
		mu.Unlock()
	}

	// Fetch sessions
	sessions, err := c.Sessions(ctx)
	if err != nil {
		slog.Debug("[UI] Backend sessions fetch failed", "backend", backendName, "error", err)
	} else {
		mu.Lock()
		for _, sess := range sessions {
			data.Sessions = append(data.Sessions, SessionData{
				Server:     backendName,
				CallID:     sess.CallID,
				ClientAddr: sess.ClientAddr,
				ClientPort: sess.ClientPort,
				ServerAddr: sess.ServerAddr,
				ServerPort: sess.ServerPort,
				Duration:   formatDuration(sess.Duration),
				Status:     sess.Status,
			})
		}
		mu.Unlock()
	}

	// Fetch RTP managers
	rtpManagers, err := c.RtpManagers(ctx)
	if err != nil {
		slog.Debug("[UI] Backend rtpmanagers fetch failed", "backend", backendName, "error", err)
	} else {
		mu.Lock()
		for _, m := range rtpManagers.Members {
			status := "Unhealthy"
			if m.Healthy {
				status = "Healthy"
			}
			data.RtpManagers = append(data.RtpManagers, RtpManagerData{
				Server:       backendName,
				NodeID:       m.NodeID,
				Address:      m.Address,
				Healthy:      m.Healthy,
				Status:       status,
				DrainState:   m.DrainState,
				SessionCount: m.SessionCount,
			})
		}
		mu.Unlock()
	}

	mu.Lock()
	data.Backends = append(data.Backends, backendData)
	mu.Unlock()
}

// formatUptime formats a duration for display
func formatUptime(d time.Duration) string {
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	secs := int(d.Seconds()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm", days, hours, mins)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm %ds", hours, mins, secs)
	}
	if mins > 0 {
		return fmt.Sprintf("%dm %ds", mins, secs)
	}
	return fmt.Sprintf("%ds", secs)
}

// formatDuration formats seconds for display
func formatDuration(seconds int) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%dm %ds", seconds/60, seconds%60)
	}
	hours := seconds / 3600
	mins := (seconds % 3600) / 60
	secs := seconds % 60
	return fmt.Sprintf("%dh %dm %ds", hours, mins, secs)
}

// handleDrainModal renders the drain confirmation modal
func (s *Server) handleDrainModal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	server := r.URL.Query().Get("server")
	nodeID := r.URL.Query().Get("nodeId")
	address := r.URL.Query().Get("address")
	sessionCountStr := r.URL.Query().Get("sessions")

	sessionCount, _ := strconv.Atoi(sessionCountStr)

	data := DrainModalData{
		Server:       server,
		NodeID:       nodeID,
		Address:      address,
		SessionCount: sessionCount,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderDrainModal(w, data); err != nil {
		slog.Error("[UI] Failed to render drain modal", "error", err)
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
	}
}

// handleDrain handles the drain operation (start drain or direct disable)
func (s *Server) handleDrain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	server := r.URL.Query().Get("server")
	nodeID := r.URL.Query().Get("nodeId")
	mode := r.URL.Query().Get("mode") // "graceful" or "aggressive"

	if server == "" || nodeID == "" {
		http.Error(w, "Missing server or nodeId", http.StatusBadRequest)
		return
	}

	// Find the client for the specified server
	var targetClient *client.Client
	for _, c := range s.clients {
		if c.Name() == server {
			targetClient = c
			break
		}
	}

	if targetClient == nil {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	// Call the drain API
	_, err := targetClient.StartDrain(r.Context(), nodeID, mode)
	if err != nil {
		slog.Error("[UI] Failed to start drain", "server", server, "nodeId", nodeID, "error", err)
		// Return an error toast/message via HTMX
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, `<div class="text-red-400 text-sm">Failed to start drain: %s</div>`, err.Error())
		return
	}

	// Return updated RTP managers partial to refresh the view
	w.Header().Set("HX-Trigger", "drainStarted")
	data := s.buildTemplateData(r.Context())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderRtpManagers(w, data); err != nil {
		slog.Error("[UI] Failed to render rtpmanagers partial", "error", err)
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
	}
}

// handleCancelDrain cancels an in-progress drain operation
func (s *Server) handleCancelDrain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	server := r.URL.Query().Get("server")
	nodeID := r.URL.Query().Get("nodeId")

	if server == "" || nodeID == "" {
		http.Error(w, "Missing server or nodeId", http.StatusBadRequest)
		return
	}

	// Find the client for the specified server
	var targetClient *client.Client
	for _, c := range s.clients {
		if c.Name() == server {
			targetClient = c
			break
		}
	}

	if targetClient == nil {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	// Call the cancel drain API
	err := targetClient.CancelDrain(r.Context(), nodeID)
	if err != nil {
		slog.Error("[UI] Failed to cancel drain", "server", server, "nodeId", nodeID, "error", err)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, `<div class="text-red-400 text-sm">Failed to cancel drain: %s</div>`, err.Error())
		return
	}

	// Return updated RTP managers partial to refresh the view
	w.Header().Set("HX-Trigger", "drainCancelled")
	data := s.buildTemplateData(r.Context())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderRtpManagers(w, data); err != nil {
		slog.Error("[UI] Failed to render rtpmanagers partial", "error", err)
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
	}
}
