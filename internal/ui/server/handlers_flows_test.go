package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	types "github.com/sebas/switchboard/api/types/v1"
	"github.com/sebas/switchboard/internal/ui/client"
)

// fakeSignaling stands in for the signaling server's config API. It records
// what a write carried, so a test can assert not only that the editor called
// the validated path but what it put through it.
type fakeSignaling struct {
	flows string
	// reject, when set, is the refusal the server answers a write with — the
	// case that matters most, because nothing may reach disk.
	reject []types.ConfigProblem
	// warn rides along with an accepted write.
	warn []types.ConfigProblem

	wrote   string
	written bool
}

func (f *fakeSignaling) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/config/tenants", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]types.TenantFile{
			{Name: "devtenant", HasFlows: true},
			{Name: "routing-only", HasFlows: false},
		})
	})
	mux.HandleFunc("/api/v1/config/tenants/", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(types.TenantFile{Name: "devtenant", Content: f.flows})
		case http.MethodPut:
			var body types.FileContent
			_ = json.NewDecoder(r.Body).Decode(&body)
			if len(f.reject) > 0 {
				// Refused: the candidate never becomes the file.
				w.WriteHeader(http.StatusUnprocessableEntity)
				_ = json.NewEncoder(w).Encode(types.ConfigRejection{
					Error: "invalid", Tenant: "devtenant", File: "flows", Problems: f.reject,
				})
				return
			}
			f.wrote, f.written = body.Content, true
			f.flows = body.Content
			_ = json.NewEncoder(w).Encode(types.ConfigSaved{Status: "ok", File: "flows", Warnings: f.warn})
		}
	})
	return mux
}

// devtenant's only flow, and the one every test here edits.
const (
	testTenant = "devtenant"
	testFlow   = "main-ivr"
)

// uiFor builds a UI server pointed at a fake backend.
func uiFor(t *testing.T, backend *fakeSignaling) *Server {
	t.Helper()
	api := httptest.NewServer(backend.handler())
	t.Cleanup(api.Close)

	tmpl, err := NewTemplates()
	if err != nil {
		t.Fatalf("templates: %v", err)
	}
	return &Server{
		clients:   []*client.Client{client.NewClient("test", api.URL)},
		templates: tmpl,
	}
}

func referenceFlows(t *testing.T) string {
	t.Helper()
	return readFile(t, filepath.Join(tenantsDir, "devtenant.flows.json"))
}

// The tab has to render the real reference flow, with the graph and the node
// catalog both in the page — the catalog because the palette and the
// inspector are generated from it rather than from a hand-written list.
func TestFlowsPartialRendersTheReferenceFlow(t *testing.T) {
	backend := &fakeSignaling{flows: referenceFlows(t)}
	s := uiFor(t, backend)

	rec := httptest.NewRecorder()
	s.handleConfigFlowsPartial(rec, httptest.NewRequest(http.MethodGet, "/admin/config/partials/flows?server=test", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()

	for _, want := range []string{
		`name="tenant"`, `name="flow"`, "devtenant", "main-ivr",
		`id="drawflow"`, `id="flow-palette"`, `id="flow-inspector"`,
		`id="tenant-input"`, `id="flow-inspector-panel"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the page does not contain %q", want)
		}
	}
	// A tenant with no graph must not be offered: the canvas would have nothing
	// to draw and the option would promise otherwise.
	if strings.Contains(body, "routing-only") {
		t.Error("a tenant with no flows was offered in the picker")
	}
	// The graph reaches the page with its nodes and the start node named.
	for _, want := range []string{`"greeting"`, `"ring-sales"`, `"retries_exceeded"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the serialized graph is missing %q", want)
		}
	}
	// The catalog comes from Go, so the page carries the exit contract it did
	// not have to restate.
	if !strings.Contains(body, `"accepts_digit_exits"`) || !strings.Contains(body, `"terminal_exits"`) {
		t.Error("the node catalog was not serialized into the page")
	}
	// A terminal exit is never a port.
	if strings.Contains(body, `{"name":"answered"`) {
		t.Error("a terminal exit was rendered as a connectable exit")
	}
}

// graphFor reads a flow through the same model the browser is handed, so a test
// posts what the canvas would post.
func graphFor(t *testing.T, content string) flowGraph {
	t.Helper()
	doc, err := parseFlowsDoc(content)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	graph, err := doc.Graph(testFlow)
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	return graph
}

func postSave(t *testing.T, s *Server, content string, graph flowGraph) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(graph)
	if err != nil {
		t.Fatalf("marshal graph: %v", err)
	}
	form := url.Values{
		"tenant":  {testTenant},
		"flow":    {testFlow},
		"content": {content},
		"graph":   {string(raw)},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/config/flows/save?server=test", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	rec := httptest.NewRecorder()
	s.handleConfigFlowsSave(rec, req)
	return rec
}

// Moving a node must save the position and change nothing else.
func TestSavingAMovedNodeWritesOnlyTheLayout(t *testing.T) {
	original := referenceFlows(t)
	backend := &fakeSignaling{flows: original}
	s := uiFor(t, backend)

	graph := graphFor(t, original)
	graph.Nodes[0].X, graph.Nodes[0].Y = 480, 260

	rec := postSave(t, s, original, graph)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !backend.written {
		t.Fatal("the save never reached the validated write path")
	}
	if !strings.Contains(rec.Body.String(), "Saved devtenant.flows.json") {
		t.Error("the page does not report the save")
	}
	if !strings.Contains(backend.wrote, `"greeting": [480, 260]`) {
		t.Errorf("the position was not written:\n%s", backend.wrote)
	}

	// What the file MEANS is untouched: same nodes, same exits, same entries.
	want := withoutLayout(semantics(t, original))
	got := withoutLayout(semantics(t, backend.wrote))
	if !jsonEqual(want, got) {
		t.Errorf("moving a node changed what the flow means\n%s", backend.wrote)
	}

	// The write landed, so a second save must splice into the saved document.
	if !strings.Contains(rec.Body.String(), "480") {
		t.Error("the editor did not come back showing what was saved")
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// The refusal is the important path: the operator has to see which exit is
// wrong, and nothing may reach disk.
func TestARefusedSaveShowsTheProblemsAndWritesNothing(t *testing.T) {
	original := referenceFlows(t)
	backend := &fakeSignaling{
		flows: original,
		reject: []types.ConfigProblem{{
			Path:     "flows.main-ivr.nodes.ring-sales.exits",
			Message:  `dial_user node does not wire its "busy" exit; every outcome must name a node`,
			Severity: "error",
		}},
	}
	s := uiFor(t, backend)

	graph := graphFor(t, original)
	for i := range graph.Nodes {
		if graph.Nodes[i].ID != "ring-sales" {
			continue
		}
		for j, exit := range graph.Nodes[i].Exits {
			if exit.Name == "busy" {
				graph.Nodes[i].Exits[j].Target = ""
			}
		}
	}

	rec := postSave(t, s, original, graph)
	body := rec.Body.String()

	if backend.written {
		t.Error("a refused write reached disk")
	}
	if !strings.Contains(body, "Nothing was saved.") {
		t.Error("the page does not say the write was refused")
	}
	if !strings.Contains(body, "does not wire its") || !strings.Contains(body, "busy") {
		t.Errorf("the refusal does not name the unwired exit:\n%s", body)
	}
	if !strings.Contains(body, "flows.main-ivr.nodes.ring-sales.exits") {
		t.Error("the problem is not shown against its path")
	}
	// The operator's work is still on the canvas.
	if !strings.Contains(body, `"ring-sales"`) {
		t.Error("the graph was lost when the save was refused")
	}
}

// Warnings are saved findings, not blockers, and must not read like a refusal.
func TestWarningsRideAlongWithASuccess(t *testing.T) {
	original := referenceFlows(t)
	backend := &fakeSignaling{
		flows: original,
		warn: []types.ConfigProblem{{
			Path: "flows.main-ivr.nodes.closing", Message: "this prompt is very long", Severity: "warning",
		}},
	}
	s := uiFor(t, backend)

	rec := postSave(t, s, original, graphFor(t, original))
	body := rec.Body.String()

	if !strings.Contains(body, "Saved, but worth a look:") {
		t.Error("a warning was not shown")
	}
	if strings.Contains(body, "Nothing was saved.") {
		t.Error("a saved file was reported as refused")
	}
}

// A save marks the configuration as drifted so the banner re-checks; without it
// an operator would edit a flow and see no sign it is not yet routing calls.
func TestASaveTellsThePageToRecheckDrift(t *testing.T) {
	original := referenceFlows(t)
	backend := &fakeSignaling{flows: original}
	s := uiFor(t, backend)

	rec := postSave(t, s, original, graphFor(t, original))
	if got := rec.Header().Get("HX-Trigger"); got != "config-saved" {
		t.Errorf("HX-Trigger = %q, want config-saved", got)
	}
}

// Designating a different start node is a one-word change to the file.
func TestChangingTheStartNodeIsSaved(t *testing.T) {
	original := referenceFlows(t)
	backend := &fakeSignaling{flows: original}
	s := uiFor(t, backend)

	graph := graphFor(t, original)
	graph.Start = "closing"

	postSave(t, s, original, graph)
	if !backend.written {
		t.Fatal("nothing was written")
	}
	if !strings.Contains(backend.wrote, `"start": "closing"`) {
		t.Errorf("the new start node was not saved:\n%s", backend.wrote)
	}
}

// The tenant picker is JavaScript now, and a tenant whose file defines no flows
// renders no canvas — so the picker's script has to live outside that branch.
// Otherwise the one tenant with an empty flows file becomes a place the editor
// cannot be steered out of.
func TestTheTenantPickerWorksWithoutAFlowToDraw(t *testing.T) {
	s := uiFor(t, &fakeSignaling{flows: `{"flows": {}}`})

	rec := httptest.NewRecorder()
	s.handleConfigFlowsPartial(rec, httptest.NewRequest(http.MethodGet, "/admin/config/partials/flows?server=test", nil))
	body := rec.Body.String()

	if !strings.Contains(body, "defines no flows") {
		t.Errorf("the empty-flows state was not rendered:\n%s", body)
	}
	if strings.Contains(body, `id="drawflow"`) {
		t.Error("a canvas was rendered for a file with no flows")
	}
	for _, want := range []string{`id="tenant-input"`, "wireTenantPicker", "const TENANTS"} {
		if !strings.Contains(body, want) {
			t.Errorf("the tenant picker is not usable here: missing %q", want)
		}
	}
}

// A tenant with no graph anywhere gets an explanation, not a blank canvas.
func TestNoTenantHasFlows(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]types.TenantFile{{Name: "routing-only", HasFlows: false}})
	}))
	defer api.Close()

	tmpl, err := NewTemplates()
	if err != nil {
		t.Fatalf("templates: %v", err)
	}
	s := &Server{clients: []*client.Client{client.NewClient("test", api.URL)}, templates: tmpl}

	rec := httptest.NewRecorder()
	s.handleConfigFlowsPartial(rec, httptest.NewRequest(http.MethodGet, "/admin/config/partials/flows?server=test", nil))

	body := rec.Body.String()
	if !strings.Contains(body, "No tenant has a flow graph yet.") {
		t.Errorf("the empty state was not rendered:\n%s", body)
	}
	if strings.Contains(body, `id="drawflow"`) {
		t.Error("an empty canvas was rendered for a tenant with no flows")
	}
}

// The save handler is a write. A GET must not be able to reach it.
func TestSaveRejectsNonPost(t *testing.T) {
	s := uiFor(t, &fakeSignaling{flows: referenceFlows(t)})
	rec := httptest.NewRecorder()
	s.handleConfigFlowsSave(rec, httptest.NewRequest(http.MethodGet, "/admin/config/flows/save?server=test", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

// The Flows tab must survive a page reload by URL, which means the tab name is
// in the allowlist and the flow name is forwarded to the partial.
func TestTheFlowsTabIsReachableByURL(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/admin/config?tab=flows&tenant=devtenant&flow=main-ivr", nil)
	if got := tabQuery(req); !strings.Contains(got, "flow=main-ivr") || !strings.Contains(got, "tenant=devtenant") {
		t.Errorf("tabQuery = %q; the flow does not survive the round trip through the page", got)
	}
}
