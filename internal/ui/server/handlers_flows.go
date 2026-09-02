package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"

	types "github.com/sebas/switchboard/api/types/v1"
	"github.com/sebas/switchboard/internal/signaling/dialplan"
	"github.com/sebas/switchboard/internal/ui/client"
)

// The Flows tab: a tenant's flow graph as a graph.
//
// The tab draws and edits; it does not adjudicate. Saving goes through the same
// PutTenantFile the raw JSON editor uses, so the signaling server validates the
// candidate against the tenant's routing table and refuses it whole if it would
// not load. What the canvas knows about the exit contract it learns from
// dialplan.NodeSchema(), serialized into the page — never from a catalog
// written out again in JavaScript, which would be a second copy of the contract
// with its own opinion about what is true.
//
// The catalog is compiled into this binary rather than fetched from the
// backend, which assumes the UI and the signaling server it edits are built
// from the same source. They ship together, so that holds; if a UI ever had to
// drive an older signaling server, the schema would need to come over the API
// instead, because the palette would otherwise offer a node type the server
// does not know.

// errNoBackend is the one failure that is the request's problem rather than the
// page's. Everything a tenant's own configuration can get wrong belongs in the
// view model's Error field, because saying so IS what the page is for.
var errNoBackend = errors.New("No backend configured")

// handleConfigFlowsPartial renders the editor for one tenant's flow.
func (s *Server) handleConfigFlowsPartial(w http.ResponseWriter, r *http.Request) {
	data, err := s.flowsVM(r.Context(), r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	s.renderFlows(w, data)
}

// flowsVM builds the flow editor's view model for a read.
//
// Separated from rendering so a test can assert on what the page was told
// rather than on the markup that told it. Those are different properties: which
// tenants are offered and which flow is selected is a decision this code makes,
// while the id a canvas div carries is a choice the template makes, and a test
// that conflates them fails when a class name changes.
func (s *Server) flowsVM(ctx context.Context, r *http.Request) (ConfigFlowsData, error) {
	c := s.findClient(r.URL.Query().Get("server"))
	if c == nil {
		return ConfigFlowsData{}, errNoBackend
	}

	q := r.URL.Query()
	data := ConfigFlowsData{Server: c.Name(), Tenant: q.Get("tenant"), Flow: q.Get("flow")}
	data.Schema = schemaJSON()

	tenants, err := c.ListTenants(ctx)
	if err != nil {
		data.Error = fmt.Sprintf("Failed to load tenants: %v", err)
		return data, nil
	}

	// Only tenants that HAVE flows. A tenant routing entirely by direct mapping
	// has no graph to draw, and offering it would promise something the canvas
	// cannot deliver.
	data.Tenants = tenantsWithFlows(tenants)
	if len(data.Tenants) == 0 {
		return data, nil
	}
	if data.Tenant == "" || !hasTenant(data.Tenants, data.Tenant) {
		data.Tenant = data.Tenants[0].Name
	}

	content, err := c.GetTenantFile(ctx, data.Tenant, "flows")
	if err != nil {
		data.Error = fmt.Sprintf("Failed to load %s.flows.json: %v", data.Tenant, err)
		return data, nil
	}
	data.Content = content

	if err := loadGraph(&data); err != nil {
		data.Error = err.Error()
	}
	return data, nil
}

// flowsVMScoped builds the flow editor for ONE tenant, named by the URL.
//
// It deliberately does not call ListTenants and never elects a fallback tenant.
// The unscoped editor picks the first tenant that has flows, which is right when
// the tenant is a picker value and wrong when it is the path: a tenant with no
// flows file must say so, not quietly draw somebody else's graph under its own
// breadcrumb.
func (s *Server) flowsVMScoped(ctx context.Context, c *client.Client, tenant, flow string) ConfigFlowsData {
	data := ConfigFlowsData{Server: c.Name(), Tenant: tenant, Flow: flow, Scoped: true}
	data.Schema = schemaJSON()

	content, err := c.GetTenantFile(ctx, tenant, "flows")
	if err != nil {
		data.Error = fmt.Sprintf("Failed to load %s.flows.json: %v", tenant, err)
		return data
	}
	data.Content = content

	if err := loadGraph(&data); err != nil {
		data.Error = err.Error()
	}
	return data
}

// handleConfigFlowsSave splices the edited flow back into the tenant's file and
// puts it through the validated write path.
func (s *Server) handleConfigFlowsSave(w http.ResponseWriter, r *http.Request) {
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

	data := ConfigFlowsData{
		Server: c.Name(),
		Tenant: r.FormValue("tenant"),
		Flow:   r.FormValue("flow"),
		// The document the operator was looking at, not whatever is on disk
		// now. Splicing into a file that changed underneath would silently
		// discard someone else's edit; splicing into the one they opened makes
		// last-write-wins a stated behavior rather than an accident.
		Content: normalizeContent(r.FormValue("content")),
		Schema:  schemaJSON(),
	}

	// The tenant list is rebuilt on every render so the pickers survive a
	// refusal — an operator whose save was rejected should not also lose the
	// ability to switch flows.
	if tenants, err := c.ListTenants(r.Context()); err == nil {
		data.Tenants = tenantsWithFlows(tenants)
	}

	var graph flowGraph
	if err := json.Unmarshal([]byte(r.FormValue("graph")), &graph); err != nil {
		data.Error = fmt.Sprintf("The editor sent something this server could not read: %v", err)
		_ = loadGraph(&data)
		s.renderFlows(w, data)
		return
	}

	doc, err := parseFlowsDoc(data.Content)
	if err != nil {
		data.Error = fmt.Sprintf("Failed to read %s.flows.json: %v", data.Tenant, err)
		s.renderFlows(w, data)
		return
	}
	spliced, err := doc.Splice(data.Flow, graph)
	if err != nil {
		data.Error = err.Error()
		_ = loadGraph(&data)
		s.renderFlows(w, data)
		return
	}

	warnings, err := c.PutTenantFile(r.Context(), data.Tenant, "flows", spliced)
	if err != nil {
		// Nothing reached disk, so the editor keeps showing the document the
		// operator opened and the graph they were working on.
		applyFlowsWriteError(&data, err, "Failed to save flows")
		data.Graph = graphJSON(graph)
		data.Flows = doc.FlowNames()
		s.renderFlows(w, data)
		return
	}

	// The write landed: the saved document becomes the one a further edit
	// splices into, so a second save does not splice into a stale file.
	data.Content = spliced
	data.Success = savedFileMsg(data.Tenant, "flows")
	data.Warnings = problemData(warnings)
	if err := loadGraph(&data); err != nil {
		data.Error = err.Error()
	}
	configSaved(w)
	s.renderFlows(w, data)
}

// loadGraph parses the tenant's flows file and fills in the flow list and the
// selected flow's graph.
func loadGraph(data *ConfigFlowsData) error {
	if data.Content == "" {
		return nil
	}
	doc, err := parseFlowsDoc(data.Content)
	if err != nil {
		return fmt.Errorf("Failed to read %s.flows.json: %v", data.Tenant, err)
	}

	data.Flows = doc.FlowNames()
	if len(data.Flows) == 0 {
		data.Flow = ""
		return nil
	}
	if data.Flow == "" || !doc.Has(data.Flow) {
		data.Flow = data.Flows[0]
	}

	graph, err := doc.Graph(data.Flow)
	if err != nil {
		return fmt.Errorf("Failed to read flow %q: %v", data.Flow, err)
	}
	data.Graph = graphJSON(graph)
	return nil
}

// graphJSON serializes a graph for the canvas.
func graphJSON(graph flowGraph) template.JS {
	out, err := json.Marshal(graph)
	if err != nil {
		return template.JS("null")
	}
	return template.JS(out)
}

// schemaJSON serializes the node catalog for the palette and the inspector.
func schemaJSON() template.JS {
	out, err := json.Marshal(dialplan.NodeSchema())
	if err != nil {
		return template.JS("[]")
	}
	return template.JS(out)
}

// tenantsWithFlows keeps only the tenants that have a graph to draw. Both the
// read and the save path need the same list, and the save path needs it so the
// pickers survive a refusal.
func tenantsWithFlows(tenants []types.TenantFile) []TenantFileData {
	var out []TenantFileData
	for _, t := range tenants {
		if !t.HasFlows {
			continue
		}
		out = append(out, TenantFileData{
			Name: t.Name, Size: t.Size, Modified: t.Modified, HasFlows: t.HasFlows,
		})
	}
	return out
}

// hasTenant reports whether a name is in the list, so a stale bookmark falls
// back to a tenant that exists rather than rendering an empty editor.
func hasTenant(tenants []TenantFileData, name string) bool {
	for _, t := range tenants {
		if t.Name == name {
			return true
		}
	}
	return false
}

// applyFlowsWriteError renders a refused write into the editor's data. It is
// applyWriteError for this page: the data structs differ because the pages do,
// but a refusal has to read the same way everywhere an operator can cause one.
func applyFlowsWriteError(data *ConfigFlowsData, err error, prefix string) {
	var rejected *client.ConfigRejectedError
	if errors.As(err, &rejected) {
		data.Error = msgNotSaved
		data.Problems = problemData(rejected.Problems())
		return
	}
	data.Error = fmt.Sprintf("%s: %v", prefix, err)
}

func (s *Server) renderFlows(w http.ResponseWriter, data ConfigFlowsData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.RenderConfigFlows(w, data); err != nil {
		slog.Error("[UI] Failed to render flow editor", "error", err)
	}
}
