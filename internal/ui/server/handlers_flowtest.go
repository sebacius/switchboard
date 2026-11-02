package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	types "github.com/sebas/switchboard/api/types/v1"
	"github.com/sebas/switchboard/internal/ui/client"
)

// The Test Call tab: walk a call through the loaded flow graph without placing
// one. It runs on the signaling server, which holds the configuration that is
// actually routing calls — the tab reads what is live, while the other tabs read
// what is on disk, which is why the drift banner matters most here.

// handleFlowTestPartial renders the simulator form.
func (s *Server) handleFlowTestPartial(w http.ResponseWriter, r *http.Request) {
	c := s.requireBackendPartial(w)
	if c == nil {
		return
	}

	data := s.flowTestVM(r.Context(), c, r.URL.Query(), "")

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderFlowTest(w, data); err != nil {
		slog.Error("[UI] Failed to render flow test", "error", err)
	}
}

// flowTestVM builds the simulator's view model for a read.
//
// One builder, two entry points: the config editor's fragment and the tenant
// workspace's tab. A non-empty scoped names the tenant from the URL path, which
// pins the simulation and suppresses the tenant picker.
func (s *Server) flowTestVM(ctx context.Context, c *client.Client, q url.Values, scoped string) FlowTestData {
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
	if scoped != "" {
		data.Tenant, data.Scoped = scoped, true
	}

	loaded, err := c.LoadedTenants(ctx)
	if err != nil {
		data.Failure = loadFailed(c, "the loaded tenant list", err)
		return data
	}
	data.Tenants = loaded.Tenants
	// Only the unscoped form elects a default. When the path named the tenant,
	// substituting a different one would simulate a call nobody asked about.
	if !data.Scoped && data.Tenant == "" && len(loaded.Tenants) > 0 {
		data.Tenant = loaded.Tenants[0].Name
	}
	return data
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
