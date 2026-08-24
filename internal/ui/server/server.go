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
	mux.HandleFunc("/admin/partials/parkedcalls", s.handleParkedCallsPartial)

	// RTP Manager drain control endpoints
	mux.HandleFunc("/admin/rtpmanagers/drain-modal", s.handleDrainModal)
	mux.HandleFunc("/admin/rtpmanagers/drain", s.handleDrain)
	mux.HandleFunc("/admin/rtpmanagers/cancel-drain", s.handleCancelDrain)

	// Parked calls control endpoints
	mux.HandleFunc("/admin/park/unpark", s.handleForceUnpark)

	// Pages. Every sidebar entry is one of these: a real URL that renders a
	// complete page on a hard GET and survives a refresh.
	mux.HandleFunc("/admin/tenants", s.handleTenantsPage)
	mux.HandleFunc("/admin/deployment", s.handleDeploymentPage)
	mux.HandleFunc("/admin/audio", s.handleAudioPage)
	mux.HandleFunc("/admin/live/{which}", s.handleLivePage)

	// Configuration management
	mux.HandleFunc("/admin/config", s.handleConfigPage)
	mux.HandleFunc("/admin/config/partials/tenants", s.handleConfigTenantsPartial)
	mux.HandleFunc("/admin/config/tenants/edit", s.handleConfigTenantEdit)
	mux.HandleFunc("/admin/config/tenants/new", s.handleConfigTenantNew)
	mux.HandleFunc("/admin/config/tenants/save", s.handleConfigTenantSave)
	mux.HandleFunc("/admin/config/tenants/create", s.handleConfigTenantCreate)
	mux.HandleFunc("/admin/config/tenants/delete", s.handleConfigTenantDelete)
	mux.HandleFunc("/admin/config/partials/globals", s.handleConfigGlobalsPartial)
	mux.HandleFunc("/admin/config/globals/edit", s.handleConfigGlobalEdit)
	mux.HandleFunc("/admin/config/globals/save", s.handleConfigGlobalSave)
	mux.HandleFunc("/admin/config/partials/reload-banner", s.handleReloadBanner)
	mux.HandleFunc("/admin/config/partials/audio", s.handleConfigAudioPartial)
	mux.HandleFunc("/admin/config/partials/flows", s.handleConfigFlowsPartial)
	mux.HandleFunc("/admin/config/flows/save", s.handleConfigFlowsSave)
	mux.HandleFunc("/admin/config/partials/flowtest", s.handleFlowTestPartial)
	mux.HandleFunc("/admin/config/flowtest/run", s.handleFlowTestRun)
	mux.HandleFunc("/admin/config/reload", s.handleConfigReload)

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

	data := s.buildTemplateData(r.Context(), want{stats: true})
	data.Layout = s.layoutFor(r, "overview", "Switchboard Admin", "SIP IVR Dashboard")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderDashboard(w, data); err != nil {
		slog.Error("[UI] Failed to render dashboard", "error", err)
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
	}
}

// handleStatsPartial renders the stats cards partial for HTMX
func (s *Server) handleStatsPartial(w http.ResponseWriter, r *http.Request) {
	data := s.buildTemplateData(r.Context(), want{stats: true})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderStats(w, data); err != nil {
		slog.Error("[UI] Failed to render stats partial", "error", err)
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
	}
}

// handleBackendsPartial renders the backends status partial for HTMX
func (s *Server) handleBackendsPartial(w http.ResponseWriter, r *http.Request) {
	data := s.buildTemplateData(r.Context(), want{})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderBackends(w, data); err != nil {
		slog.Error("[UI] Failed to render backends partial", "error", err)
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
	}
}

// handleRegistrationsPartial renders the registrations table partial for HTMX
func (s *Server) handleRegistrationsPartial(w http.ResponseWriter, r *http.Request) {
	data := s.buildTemplateData(r.Context(), want{registrations: true})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderRegistrations(w, data); err != nil {
		slog.Error("[UI] Failed to render registrations partial", "error", err)
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
	}
}

// handleDialogsPartial renders the dialogs table partial for HTMX
func (s *Server) handleDialogsPartial(w http.ResponseWriter, r *http.Request) {
	data := s.buildTemplateData(r.Context(), want{dialogs: true})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderDialogs(w, data); err != nil {
		slog.Error("[UI] Failed to render dialogs partial", "error", err)
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
	}
}

// handleSessionsPartial renders the sessions table partial for HTMX
func (s *Server) handleSessionsPartial(w http.ResponseWriter, r *http.Request) {
	data := s.buildTemplateData(r.Context(), want{sessions: true})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderSessions(w, data); err != nil {
		slog.Error("[UI] Failed to render sessions partial", "error", err)
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
	}
}

// handleRtpManagersPartial renders the RTP managers section partial for HTMX
func (s *Server) handleRtpManagersPartial(w http.ResponseWriter, r *http.Request) {
	data := s.buildTemplateData(r.Context(), want{rtpManagers: true})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderRtpManagers(w, data); err != nil {
		slog.Error("[UI] Failed to render rtpmanagers partial", "error", err)
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
	}
}

// buildTemplateData fetches data from all backends and aggregates it
func (s *Server) buildTemplateData(ctx context.Context, w want) TemplateData {
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
		ParkedCalls:   make([]ParkedCallData, 0),
		MultiBackend:  len(s.clients) > 1,
	}

	// Fetch data from all backends concurrently
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, c := range s.clients {
		wg.Add(1)
		go func(c *client.Client) {
			defer wg.Done()
			s.fetchBackendData(ctx, c, w, &data, &mu)
		}(c)
	}

	wg.Wait()
	return data
}

// want says which resources a render actually needs.
//
// fetchBackendData used to fetch all seven, sequentially, per backend, on EVERY
// partial poll — so a dialogs refresh every three seconds paid for
// registrations, sessions, parked calls and RTP managers as well, and threw them
// away. Splitting the monitors onto their own pages made that visible rather
// than making it worse: each page now asks for one thing.
//
// Health is always fetched. It gates the rest — an unreachable backend has
// nothing else to say — and it is what the backend list is built from.
type want struct {
	stats         bool
	registrations bool
	dialogs       bool
	sessions      bool
	rtpManagers   bool
	parkedCalls   bool
}

// wantAll is the dashboard's historical appetite, kept for anything that really
// does show everything.
func wantAll() want {
	return want{stats: true, registrations: true, dialogs: true,
		sessions: true, rtpManagers: true, parkedCalls: true}
}

// fetchBackendData fetches the wanted data from a single backend.
//
// The wanted fetchers run CONCURRENTLY. They are independent GETs against one
// server and the sequencing they used to have bought nothing — it just made the
// slowest one additive with the others.
func (s *Server) fetchBackendData(ctx context.Context, c *client.Client, w want, data *TemplateData, mu *sync.Mutex) {
	backendName := c.Name()
	backendData := BackendData{
		Name:    backendName,
		Address: c.BaseURL(),
		Status:  "offline",
	}

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

	var wg sync.WaitGroup
	run := func(wanted bool, fetch func()) {
		if !wanted {
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			fetch()
		}()
	}

	run(w.stats, func() { s.fetchStats(ctx, c, data, mu) })
	run(w.registrations, func() { s.fetchRegistrations(ctx, c, data, mu) })
	run(w.dialogs, func() { s.fetchDialogs(ctx, c, data, mu) })
	run(w.sessions, func() { s.fetchSessions(ctx, c, data, mu) })
	run(w.rtpManagers, func() { s.fetchRtpManagers(ctx, c, data, mu) })
	run(w.parkedCalls, func() { s.fetchParkedCalls(ctx, c, data, mu) })
	wg.Wait()

	mu.Lock()
	data.Backends = append(data.Backends, backendData)
	mu.Unlock()
}

// Each fetcher below reads one resource from one backend and merges it. They log
// at debug and return on failure rather than propagating: one backend being down
// must not blank the rows the others returned.

func (s *Server) fetchStats(ctx context.Context, c *client.Client, data *TemplateData, mu *sync.Mutex) {
	stats, err := c.Stats(ctx)
	if err != nil {
		slog.Debug("[UI] Backend stats fetch failed", "backend", c.Name(), "error", err)
		return
	}
	mu.Lock()
	defer mu.Unlock()
	data.Stats.ActiveSessions += stats.ActiveSessions
	data.Stats.TotalRegistrations += stats.TotalRegistrations
	data.Stats.TotalBindings += stats.TotalBindings
	data.Stats.ActiveDialogs += stats.ActiveDialogs
}

func (s *Server) fetchRegistrations(ctx context.Context, c *client.Client, data *TemplateData, mu *sync.Mutex) {
	regs, err := c.Registrations(ctx)
	if err != nil {
		slog.Debug("[UI] Backend registrations fetch failed", "backend", c.Name(), "error", err)
		return
	}
	mu.Lock()
	defer mu.Unlock()
	for _, r := range regs {
		expiresAt, _ := time.Parse(time.RFC3339, r.ExpiresAt)
		ttl := time.Until(expiresAt)
		ttlStr := "expired"
		if ttl > 0 {
			ttlStr = formatDuration(int(ttl.Seconds()))
		}
		registeredAt, _ := time.Parse(time.RFC3339, r.RegisteredAt)

		data.Registrations = append(data.Registrations, RegistrationData{
			Server:       c.Name(),
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
}

func (s *Server) fetchDialogs(ctx context.Context, c *client.Client, data *TemplateData, mu *sync.Mutex) {
	dialogs, err := c.Dialogs(ctx)
	if err != nil {
		slog.Debug("[UI] Backend dialogs fetch failed", "backend", c.Name(), "error", err)
		return
	}
	mu.Lock()
	defer mu.Unlock()
	for _, d := range dialogs {
		data.Dialogs = append(data.Dialogs, DialogData{
			Server:          c.Name(),
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
}

func (s *Server) fetchSessions(ctx context.Context, c *client.Client, data *TemplateData, mu *sync.Mutex) {
	sessions, err := c.Sessions(ctx)
	if err != nil {
		slog.Debug("[UI] Backend sessions fetch failed", "backend", c.Name(), "error", err)
		return
	}
	mu.Lock()
	defer mu.Unlock()
	for _, sess := range sessions {
		data.Sessions = append(data.Sessions, SessionData{
			Server:     c.Name(),
			CallID:     sess.CallID,
			ClientAddr: sess.ClientAddr,
			ClientPort: sess.ClientPort,
			ServerAddr: sess.ServerAddr,
			ServerPort: sess.ServerPort,
			Duration:   formatDuration(sess.Duration),
			Status:     sess.Status,
		})
	}
}

func (s *Server) fetchRtpManagers(ctx context.Context, c *client.Client, data *TemplateData, mu *sync.Mutex) {
	rtpManagers, err := c.RtpManagers(ctx)
	if err != nil {
		slog.Debug("[UI] Backend rtpmanagers fetch failed", "backend", c.Name(), "error", err)
		return
	}
	mu.Lock()
	defer mu.Unlock()
	for _, m := range rtpManagers.Members {
		status := "Unhealthy"
		if m.Healthy {
			status = "Healthy"
		}
		data.RtpManagers = append(data.RtpManagers, RtpManagerData{
			Server:       c.Name(),
			NodeID:       m.NodeID,
			Address:      m.Address,
			Healthy:      m.Healthy,
			Status:       status,
			DrainState:   m.DrainState,
			SessionCount: m.SessionCount,
		})
	}
}

func (s *Server) fetchParkedCalls(ctx context.Context, c *client.Client, data *TemplateData, mu *sync.Mutex) {
	parkedCalls, err := c.ParkedCalls(ctx)
	if err != nil {
		slog.Debug("[UI] Backend parked calls fetch failed", "backend", c.Name(), "error", err)
		return
	}
	mu.Lock()
	defer mu.Unlock()
	for _, p := range parkedCalls {
		parkedAt, _ := time.Parse(time.RFC3339, p.ParkedAt)
		data.ParkedCalls = append(data.ParkedCalls, ParkedCallData{
			Server:   c.Name(),
			SlotID:   p.ID,
			CallID:   p.CallID,
			Duration: formatDuration(p.DurationSeconds),
			ParkedBy: p.ParkedBy,
			ParkedAt: parkedAt.Format("15:04:05"),
		})
	}
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

	targetClient := s.findClientStrict(server)
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
	data := s.buildTemplateData(r.Context(), want{rtpManagers: true})
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

	targetClient := s.findClientStrict(server)
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
	data := s.buildTemplateData(r.Context(), want{rtpManagers: true})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderRtpManagers(w, data); err != nil {
		slog.Error("[UI] Failed to render rtpmanagers partial", "error", err)
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
	}
}

// handleParkedCallsPartial renders the parked calls section partial for HTMX
func (s *Server) handleParkedCallsPartial(w http.ResponseWriter, r *http.Request) {
	data := s.buildTemplateData(r.Context(), want{parkedCalls: true})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderParkedCalls(w, data); err != nil {
		slog.Error("[UI] Failed to render parkedcalls partial", "error", err)
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
	}
}

// handleForceUnpark handles force-unparking a call from a parking slot
func (s *Server) handleForceUnpark(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	server := r.URL.Query().Get("server")
	slotID := r.URL.Query().Get("slotId")

	if server == "" || slotID == "" {
		http.Error(w, "Missing server or slotId", http.StatusBadRequest)
		return
	}

	targetClient := s.findClientStrict(server)
	if targetClient == nil {
		http.Error(w, "Server not found", http.StatusNotFound)
		return
	}

	// Call the force unpark API
	err := targetClient.ForceUnpark(r.Context(), slotID)
	if err != nil {
		slog.Error("[UI] Failed to force unpark", "server", server, "slotId", slotID, "error", err)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(w, `<div class="text-red-400 text-sm">Failed to unpark call: %s</div>`, err.Error())
		return
	}

	// Return updated parked calls partial to refresh the view
	w.Header().Set("HX-Trigger", "callUnparked")
	data := s.buildTemplateData(r.Context(), want{parkedCalls: true})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderParkedCalls(w, data); err != nil {
		slog.Error("[UI] Failed to render parkedcalls partial", "error", err)
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
	}
}
