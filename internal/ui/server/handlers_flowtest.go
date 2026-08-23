package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	types "github.com/sebas/switchboard/api/types/v1"
)

// The Test Call tab: walk a call through the loaded flow graph without placing
// one. It runs on the signaling server, which holds the configuration that is
// actually routing calls — the tab reads what is live, while the other tabs read
// what is on disk, which is why the drift banner matters most here.

// handleFlowTestPartial renders the simulator form.
func (s *Server) handleFlowTestPartial(w http.ResponseWriter, r *http.Request) {
	c := s.findClient(r.URL.Query().Get("server"))
	if c == nil {
		http.Error(w, "No backend configured", http.StatusServiceUnavailable)
		return
	}

	q := r.URL.Query()
	data := FlowTestData{
		Server:    c.Name(),
		Dialed:    q.Get("dialed"),
		Digits:    q.Get("digits"),
		Direction: directionParam(q.Get("direction")),
		Tenant:    q.Get("tenant"),
		// A deep link that names a number runs on load, so a trace pasted into a
		// ticket comes back as a trace rather than as a form to fill in again.
		AutoRun: q.Get("dialed") != "",
	}

	loaded, err := c.LoadedTenants(r.Context())
	if err != nil {
		data.Error = fmt.Sprintf("Failed to load tenants: %v", err)
	} else {
		data.Tenants = loaded.Tenants
		if data.Tenant == "" && len(loaded.Tenants) > 0 {
			data.Tenant = loaded.Tenants[0].Name
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderFlowTest(w, data); err != nil {
		slog.Error("[UI] Failed to render flow test", "error", err)
	}
}

// handleFlowTestRun runs one simulation and renders the result panel.
func (s *Server) handleFlowTestRun(w http.ResponseWriter, r *http.Request) {
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

	req := types.SimulateRequest{
		Tenant:    r.FormValue("tenant"),
		Dialed:    strings.TrimSpace(r.FormValue("dialed")),
		Direction: directionParam(r.FormValue("direction")),
		Caller:    strings.TrimSpace(r.FormValue("caller")),
		Digits:    splitDigits(r.FormValue("digits")),
	}

	data := FlowTestResultData{Server: c.Name(), Request: req}
	if req.Dialed == "" {
		data.Error = "Enter a number to dial."
	} else if result, err := c.SimulateFlow(r.Context(), req); err != nil {
		data.Error = fmt.Sprintf("Simulation failed: %v", err)
	} else {
		data.Result = result
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderFlowTestResult(w, data); err != nil {
		slog.Error("[UI] Failed to render flow test result", "error", err)
	}
}

// splitDigits parses the comma-separated digit script: one entry per menu, in
// the order the caller would press them.
func splitDigits(spec string) []string {
	var out []string
	for _, d := range strings.Split(spec, ",") {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, d)
		}
	}
	return out
}

// directionParam keeps the direction to the three the engine knows, so a typo
// in a bookmark cannot produce a 400 the operator has to decode.
func directionParam(v string) string {
	switch v {
	case "inbound", "outbound":
		return v
	default:
		return "internal"
	}
}
