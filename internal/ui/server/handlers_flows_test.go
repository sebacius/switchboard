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
	"github.com/sebas/switchboard/internal/ui/config"
)

// These tests assert on two different kinds of thing, deliberately kept apart.
//
// What the handler DECIDED — which tenants are offered, which flow is selected,
// what reached the canvas — is asserted against the view model, because those
// are decisions this package makes. What the page SAYS to an operator is
// asserted against the rendered body, but through named messages rather than
// inline sentences, so rewording is not a test change.
//
// What is deliberately NOT asserted is structure: element ids, class names and
// JavaScript symbol names. A test that pins those fails on every rename, and a
// rename is indistinguishable in review from a regression that removed the
// thing. One smoke test per page keeps the anchor the canvas mounts on.

// fakeSignaling stands in for the signaling server's config API. It records
// what a write carried, so a test can assert not only that the editor called
// the validated path but what it put through it.
type fakeSignaling struct {
	flows string
	// tenants, when set, replaces the default listing.
	tenants []types.TenantFile
	// reject, when set, is the refusal the server answers a write with — the
	// case that matters most, because nothing may reach disk.
	reject []types.ConfigProblem
	// warn rides along with an accepted write.
	warn []types.ConfigProblem

	wrote   string
	written bool
}

func (f *fakeSignaling) tenantList() []types.TenantFile {
	if f.tenants != nil {
		return f.tenants
	}
	return []types.TenantFile{
		{Name: "devtenant", HasFlows: true},
		{Name: "routing-only", HasFlows: false},
	}
}

func (f *fakeSignaling) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/config/tenants", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(f.tenantList())
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

// testUI is a UI server built the way the binary builds it — through NewServer,
// so requests go through the real route table.
//
// The previous helper constructed Server as a struct literal and called handler
// methods directly, which meant no test ever exercised a route. That was
// tolerable while every URL was a literal string in one mux; it stops being
// tolerable once paths carry wildcards, because then routing is logic.
type testUI struct {
	*Server
	handler http.Handler
}

func newTestServer(t *testing.T, backend *fakeSignaling) *testUI {
	t.Helper()
	return newTestServerWith(t, backend.handler())
}

// newTestServerWith builds the UI against any fake signaling API, so a test can
// bring a backend shaped for what it is testing.
func newTestServerWith(t *testing.T, backend http.Handler) *testUI {
	t.Helper()
	api := httptest.NewServer(backend)
	t.Cleanup(api.Close)

	s, err := NewServer(&config.Config{
		Port:     0,
		BindAddr: "127.0.0.1",
		Backends: []config.Backend{{Name: "test", Address: api.URL}},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return &testUI{Server: s, handler: s.httpServer.Handler}
}

// routingVMFor builds the routing tab's view model without rendering it.
func (ui *testUI) routingVMFor(t *testing.T, tenant string) RoutingPageData {
	t.Helper()
	return ui.routingVM(t.Context(), ui.clients[0], tenant)
}

func (ui *testUI) get(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	ui.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func (ui *testUI) postForm(t *testing.T, target string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	ui.handler.ServeHTTP(rec, req)
	return rec
}

// flowsVMFor builds the view model the way a request would, without rendering.
func (ui *testUI) flowsVMFor(t *testing.T, query string) ConfigFlowsData {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/admin/config/partials/flows?"+query, nil)
	data, err := ui.flowsVM(req.Context(), req)
	if err != nil {
		t.Fatalf("flowsVM: %v", err)
	}
	return data
}

// mustSay asserts the page reports something, naming why it matters so a
// failure explains the property rather than the string.
func mustSay(t *testing.T, body, want, why string) {
	t.Helper()
	if !strings.Contains(body, want) {
		t.Errorf("the page does not say %q — %s", want, why)
	}
}

func mustNotSay(t *testing.T, body, want, why string) {
	t.Helper()
	if strings.Contains(body, want) {
		t.Errorf("the page says %q, but %s", want, why)
	}
}

func referenceFlows(t *testing.T) string {
	t.Helper()
	return readFile(t, filepath.Join(tenantsDir, "devtenant.flows.json"))
}

// --- What the handler decided ---

// The editor must offer the tenants that have a graph, and only those: a tenant
// routing entirely by direct mapping has nothing for the canvas to draw, and
// offering it would promise otherwise.
func TestFlowsViewModelOffersOnlyTenantsWithGraphs(t *testing.T) {
	ui := newTestServer(t, &fakeSignaling{flows: referenceFlows(t)})
	data := ui.flowsVMFor(t, "server=test")

	var names []string
	for _, tn := range data.Tenants {
		names = append(names, tn.Name)
	}
	if len(names) != 1 || names[0] != testTenant {
		t.Errorf("tenants offered = %v, want only %q", names, testTenant)
	}
}

// With nothing asked for, the editor picks a tenant and a flow rather than
// rendering an empty canvas and waiting.
func TestFlowsViewModelSelectsATenantAndFlow(t *testing.T) {
	ui := newTestServer(t, &fakeSignaling{flows: referenceFlows(t)})
	data := ui.flowsVMFor(t, "server=test")

	if data.Tenant != testTenant {
		t.Errorf("Tenant = %q, want %q", data.Tenant, testTenant)
	}
	if data.Flow != testFlow {
		t.Errorf("Flow = %q, want %q", data.Flow, testFlow)
	}
	if data.Error != "" {
		t.Errorf("unexpected error: %s", data.Error)
	}
}

// The graph reaches the page as data, with its nodes and its exits.
func TestFlowsViewModelCarriesTheGraph(t *testing.T) {
	ui := newTestServer(t, &fakeSignaling{flows: referenceFlows(t)})
	data := ui.flowsVMFor(t, "server=test")

	var graph flowGraph
	if err := json.Unmarshal([]byte(data.Graph), &graph); err != nil {
		t.Fatalf("the serialized graph is not valid JSON: %v", err)
	}
	if graph.Start != "greeting" {
		t.Errorf("Start = %q, want greeting", graph.Start)
	}

	// Node presence and exits are tracked separately: hangup declares no exits
	// at all, so keying nodes off their exits would lose it.
	present := map[string]bool{}
	byID := map[string][]string{}
	for _, n := range graph.Nodes {
		present[n.ID] = true
		for _, e := range n.Exits {
			byID[n.ID] = append(byID[n.ID], e.Name)
		}
	}
	for _, node := range []string{"greeting", "ring-sales", "ring-engineering", "to-operator", "closing", "bye"} {
		if !present[node] {
			t.Errorf("the graph is missing node %q", node)
		}
	}
	if !contains(byID["greeting"], "retries_exceeded") {
		t.Errorf("greeting's exits = %v, want retries_exceeded among them", byID["greeting"])
	}
	// A terminal exit ends the flow, so it is never something to connect.
	for id, exits := range byID {
		for _, name := range exits {
			if name == "answered" || name == "accepted" {
				t.Errorf("node %q offers terminal exit %q as connectable", id, name)
			}
		}
	}
}

// The node catalog comes from Go, so the page never restates the exit contract.
func TestFlowsViewModelCarriesTheNodeCatalog(t *testing.T) {
	ui := newTestServer(t, &fakeSignaling{flows: referenceFlows(t)})
	data := ui.flowsVMFor(t, "server=test")

	var schema []struct {
		Type              string   `json:"type"`
		Exits             []string `json:"exits"`
		TerminalExits     []string `json:"terminal_exits"`
		AcceptsDigitExits bool     `json:"accepts_digit_exits"`
	}
	if err := json.Unmarshal([]byte(data.Schema), &schema); err != nil {
		t.Fatalf("the serialized catalog is not valid JSON: %v", err)
	}
	if len(schema) == 0 {
		t.Fatal("the node catalog did not reach the page")
	}

	digitAccepting := map[string]bool{}
	terminals := map[string][]string{}
	for _, e := range schema {
		digitAccepting[e.Type] = e.AcceptsDigitExits
		terminals[e.Type] = e.TerminalExits
	}
	if !digitAccepting["ivr"] {
		t.Error("the catalog does not report ivr as accepting digit exits")
	}
	if !contains(terminals["dial_user"], "answered") {
		t.Errorf("dial_user terminal exits = %v, want answered among them", terminals["dial_user"])
	}
}

// A tenant whose flows file declares nothing must remain a place the editor can
// be steered out of: there is no canvas, but there is still a way to change
// tenant. Otherwise one empty file becomes a dead end.
func TestATenantWithNoFlowsIsStillNavigable(t *testing.T) {
	ui := newTestServer(t, &fakeSignaling{flows: `{"flows": {}}`})
	data := ui.flowsVMFor(t, "server=test")

	if len(data.Tenants) == 0 {
		t.Fatal("no tenant was offered, so the editor cannot be steered anywhere")
	}
	if data.HasFlow() {
		t.Error("HasFlow is true for a file that declares no flows")
	}
	if data.Flow != "" {
		t.Errorf("Flow = %q, want empty", data.Flow)
	}

	body := ui.get(t, "/admin/config/partials/flows?server=test").Body.String()
	mustSay(t, body, msgTenantDefinesNoFlows, "the empty-flows state has to be explained")
	mustNotSay(t, body, `id="drawflow"`, "there is no flow to draw")

	// This is the one structural assertion kept on purpose. The property is that
	// the tenant picker sits OUTSIDE the has-a-flow branch, and there is no
	// data-level proxy for it: the picker is a JavaScript-driven input with no
	// form field, so nothing about the view model can tell you it rendered. The
	// bug it guards is a dead end — a tenant whose flows file is empty becomes a
	// page you cannot steer out of.
	mustSay(t, body, `id="tenant-input"`, "the tenant picker must render even with no flow to draw")
	mustSay(t, body, `name="flow"`, "the flow selector is the form contract and must survive")
}

// A deployment with no graphs anywhere gets an explanation, not a blank canvas.
func TestNoTenantHasFlows(t *testing.T) {
	ui := newTestServer(t, &fakeSignaling{
		tenants: []types.TenantFile{{Name: "routing-only", HasFlows: false}},
	})
	if data := ui.flowsVMFor(t, "server=test"); len(data.Tenants) != 0 {
		t.Errorf("tenants offered = %v, want none", data.Tenants)
	}

	body := ui.get(t, "/admin/config/partials/flows?server=test").Body.String()
	mustSay(t, body, msgNoTenantHasFlows, "an operator needs to know why there is nothing here")
	mustNotSay(t, body, `id="drawflow"`, "there is no flow to draw")
}

// --- What the page renders ---

// One smoke test per page: it renders, and the canvas has something to mount on.
func TestTheFlowsPageRenders(t *testing.T) {
	ui := newTestServer(t, &fakeSignaling{flows: referenceFlows(t)})
	rec := ui.get(t, "/admin/config/partials/flows?server=test")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	mustSay(t, body, `id="drawflow"`, "the canvas mounts on it")
	mustSay(t, body, testTenant, "the selected tenant should be named on the page")
	mustSay(t, body, testFlow, "the selected flow should be named on the page")
	mustNotSay(t, body, "routing-only", "a tenant with no graph must not be offered")
}

// --- Saving ---

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

func (ui *testUI) save(t *testing.T, content string, graph flowGraph) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(graph)
	if err != nil {
		t.Fatalf("marshal graph: %v", err)
	}
	return ui.postForm(t, "/admin/config/flows/save?server=test", url.Values{
		"tenant":  {testTenant},
		"flow":    {testFlow},
		"content": {content},
		"graph":   {string(raw)},
	})
}

// Moving a node must save the position and change nothing else.
func TestSavingAMovedNodeWritesOnlyTheLayout(t *testing.T) {
	original := referenceFlows(t)
	backend := &fakeSignaling{flows: original}
	ui := newTestServer(t, backend)

	graph := graphFor(t, original)
	graph.Nodes[0].X, graph.Nodes[0].Y = 480, 260

	rec := ui.save(t, original, graph)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !backend.written {
		t.Fatal("the save never reached the validated write path")
	}
	mustSay(t, rec.Body.String(), savedFileMsg(testTenant, "flows"), "a landed write has to be reported")
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

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
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
	ui := newTestServer(t, backend)

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

	body := ui.save(t, original, graph).Body.String()

	if backend.written {
		t.Error("a refused write reached disk")
	}
	mustSay(t, body, msgProblemsHeading, "a refusal must say nothing was saved before saying why")
	mustSay(t, body, "does not wire its", "the operator has to be told what is wrong")
	mustSay(t, body, "flows.main-ivr.nodes.ring-sales.exits", "a problem is only actionable against its path")
	// The operator's work is still on the canvas.
	mustSay(t, body, `"ring-sales"`, "the graph must survive a refusal")
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
	ui := newTestServer(t, backend)

	body := ui.save(t, original, graphFor(t, original)).Body.String()
	mustSay(t, body, msgWarningsHeading, "a warning has to be surfaced")
	mustNotSay(t, body, msgProblemsHeading, "the file WAS saved")
}

// A save marks the configuration as drifted so the banner re-checks; without it
// an operator would edit a flow and see no sign it is not yet routing calls.
func TestASaveTellsThePageToRecheckDrift(t *testing.T) {
	original := referenceFlows(t)
	ui := newTestServer(t, &fakeSignaling{flows: original})

	rec := ui.save(t, original, graphFor(t, original))
	if got := rec.Header().Get("HX-Trigger"); got != "config-saved" {
		t.Errorf("HX-Trigger = %q, want config-saved", got)
	}
}

// Designating a different start node is a one-word change to the file.
func TestChangingTheStartNodeIsSaved(t *testing.T) {
	original := referenceFlows(t)
	backend := &fakeSignaling{flows: original}
	ui := newTestServer(t, backend)

	graph := graphFor(t, original)
	graph.Start = "closing"

	ui.save(t, original, graph)
	if !backend.written {
		t.Fatal("nothing was written")
	}
	if !strings.Contains(backend.wrote, `"start": "closing"`) {
		t.Errorf("the new start node was not saved:\n%s", backend.wrote)
	}
}

// The save handler is a write. A GET must not be able to reach it.
func TestSaveRejectsNonPost(t *testing.T) {
	ui := newTestServer(t, &fakeSignaling{flows: referenceFlows(t)})
	rec := ui.get(t, "/admin/config/flows/save?server=test")
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
