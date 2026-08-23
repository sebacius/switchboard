package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/sebas/switchboard/internal/signaling/flow/flowsim"
)

// Walking a flow against a fake call, served from the configuration this server
// is actually routing with.
//
// These live under /api/v1/flow rather than /api/v1/config because they read the
// LOADED configuration, while every /config route reads disk. Conflating the two
// is how an operator ends up asking "I saved it, why is the trace unchanged" —
// the answer being that a save is not a reload.

// simConcurrency bounds simultaneous simulations. Each is microseconds of work,
// but this port has no authentication and a held Run button must not compete
// with call handling on a busy node. Over the limit is 429, not a queue: an
// operator would rather be told to wait than watch a spinner.
const simConcurrency = 4

// FlowSimProvider walks a flow against the loaded configuration.
type FlowSimProvider interface {
	Simulate(ctx context.Context, req flowsim.Request) (*flowsim.Result, error)
	// LoadedTenants lists what is in force right now, which is why the dropdown
	// is built from it rather than from the tenants directory: a tenant saved
	// but not reloaded is not yet routing calls, and should not look as if it is.
	LoadedTenants() LoadedTenants
}

// LoadedTenant is one tenant the server currently routes for.
type LoadedTenant struct {
	Name     string   `json:"name"`
	Flows    []string `json:"flows"`
	Operator string   `json:"operator"`
}

// LoadedTenants is the loaded routing configuration, with the stamp that says
// how current it is.
type LoadedTenants struct {
	Tenants  []LoadedTenant `json:"tenants"`
	LoadedAt string         `json:"loaded_at"`
}

// SetFlowSimProvider wires the flow simulator.
func (s *Server) SetFlowSimProvider(p FlowSimProvider) {
	s.flowSim = p
	s.simSlots = make(chan struct{}, simConcurrency)
}

// handleFlowTenants handles GET /api/v1/flow/tenants.
func (s *Server) handleFlowTenants(w http.ResponseWriter, r *http.Request) {
	if s.flowSim == nil {
		http.Error(w, "Flow simulation not configured", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.writeJSON(w, s.flowSim.LoadedTenants())
}

// handleFlowSimulate handles POST /api/v1/flow/simulate.
func (s *Server) handleFlowSimulate(w http.ResponseWriter, r *http.Request) {
	if s.flowSim == nil {
		http.Error(w, "Flow simulation not configured", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	select {
	case s.simSlots <- struct{}{}:
		defer func() { <-s.simSlots }()
	default:
		http.Error(w, "Too many simulations in flight; try again", http.StatusTooManyRequests)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}
	var req flowsim.Request
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}

	result, err := s.flowSim.Simulate(r.Context(), req)
	if err != nil {
		if errors.Is(err, flowsim.ErrUnknownTenant) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if errors.Is(err, context.DeadlineExceeded) {
			http.Error(w, err.Error(), http.StatusGatewayTimeout)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// A call the engine did not own is a legitimate answer to the operator's
	// question, not an HTTP failure.
	slog.Debug("[API] Flow simulated", "tenant", req.Tenant, "dialed", req.Dialed, "routed", result.Routed)
	s.writeJSON(w, result)
}
