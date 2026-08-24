package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	types "github.com/sebas/switchboard/api/types/v1"
)

// routingBackend serves a tenant's two files plus routes.json, which is what the
// routing tab needs to answer its three questions: which pattern wins, whether a
// DID ever arrives, and what nothing reaches.
type routingBackend struct {
	routing string
	flows   string
	routes  string
	// noRoutes makes routes.json unreadable, so the DID column has to abstain
	// rather than guess.
	noRoutes bool

	wrote   string
	written bool
	reject  []types.ConfigProblem
}

func (b *routingBackend) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/config/tenants", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]types.TenantFile{{Name: "devtenant", HasFlows: true}})
	})
	mux.HandleFunc("/api/v1/config/tenants/", func(w http.ResponseWriter, r *http.Request) {
		file := r.URL.Query().Get("file")
		switch r.Method {
		case http.MethodGet:
			content := b.routing
			if file == "flows" {
				content = b.flows
			}
			_ = json.NewEncoder(w).Encode(types.TenantFile{Name: "devtenant", Content: content})
		case http.MethodPut:
			var body types.FileContent
			_ = json.NewDecoder(r.Body).Decode(&body)
			if len(b.reject) > 0 {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_ = json.NewEncoder(w).Encode(types.ConfigRejection{
					Error: "invalid", Tenant: "devtenant", File: "routing", Problems: b.reject,
				})
				return
			}
			b.wrote, b.written = body.Content, true
			b.routing = body.Content
			_ = json.NewEncoder(w).Encode(types.ConfigSaved{Status: "ok", File: "routing"})
		}
	})
	mux.HandleFunc("/api/v1/config/files/", func(w http.ResponseWriter, r *http.Request) {
		if b.noRoutes {
			http.Error(w, "not configured", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(types.GlobalFile{Name: "routes", Content: b.routes, Exists: true})
	})
	return mux
}

func referenceRoutes(t *testing.T) string {
	t.Helper()
	return readFile(t, filepath.Join("..", "..", "..", "resources", "config", "routes.json"))
}

func routingUI(t *testing.T, b *routingBackend) *testUI {
	t.Helper()
	return newTestServerWith(t, b.handler())
}

// The entry table exists to answer "which of these wins", which requires showing
// them in the order the router tries them — and that order is nowhere in the
// file, because specificity is computed rather than declared.
func TestTheEntryTableIsInMatchOrder(t *testing.T) {
	b := &routingBackend{
		routing: `{
  "operator": "user/100",
  "extensions": {
    "1XX": "user/100",
    "100": "user/101",
    "1NX": "user/102"
  }
}
`,
		routes: `{"dids": {}}`,
	}
	ui := routingUI(t, b)

	data := ui.routingVMFor(t, "devtenant")
	var order []string
	for _, row := range data.Extensions {
		order = append(order, row.Key)
	}
	// Narrowest first: the literal, then the one with N, then the one with X.
	want := []string{"100", "1NX", "1XX"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("match order = %v, want %v", order, want)
	}
	if data.Extensions[0].Rank != 1 {
		t.Errorf("the first row has rank %d, want 1", data.Extensions[0].Rank)
	}
	// The vector is shown as the REASON for the rank, not a score to trust.
	if data.Extensions[0].Specificity != "1·1·1" {
		t.Errorf("specificity = %q, want the per-position vector", data.Extensions[0].Specificity)
	}
}

// Every ambiguous pair, not just the first. The loader stops at one because it
// is refusing to start; an operator fixing a table wants them all in one pass.
func TestEveryAmbiguousPairIsReportedBeforeTheSave(t *testing.T) {
	b := &routingBackend{
		routing: `{
  "extensions": {
    "NX1": "user/100",
    "XN1": "user/101",
    "N1X": "user/102"
  }
}
`,
		routes: `{"dids": {}}`,
	}
	data := routingUI(t, b).routingVMFor(t, "devtenant")

	if len(data.Conflicts) < 2 {
		t.Fatalf("conflicts = %v, want every ambiguous pair", data.Conflicts)
	}
	flagged := map[string]bool{}
	for _, row := range data.Extensions {
		if row.Conflicting {
			flagged[row.Key] = true
		}
	}
	for _, want := range []string{"NX1", "XN1"} {
		if !flagged[want] {
			t.Errorf("%q is in a conflicting pair but its row is not flagged", want)
		}
	}
}

// dids says what HAPPENS to a call; routes.json already said WHOSE it is. Three
// verdicts, because the validator that asks this question on a routes.json write
// deliberately abstains on patterns.
func TestDIDOwnershipHasThreeVerdicts(t *testing.T) {
	b := &routingBackend{
		routing: `{
  "dids": {
    "+15558001200": "user/100",
    "+19995550000": "user/100",
    "+1555801XXXX": "user/105"
  }
}
`,
		routes: referenceRoutes(t),
	}
	data := routingUI(t, b).routingVMFor(t, "devtenant")

	got := map[string]string{}
	for _, row := range data.DIDs {
		got[row.Key] = row.Ownership
	}
	want := map[string]string{
		// routes.json sends this one here, by a literal.
		"+15558001200": "routed",
		// Nothing routes this one here, so no call will ever arrive on it.
		"+19995550000": "unrouted",
		// A pattern. Deciding whether one pattern contains another is harder
		// than this check is worth, so it abstains rather than guessing.
		"+1555801XXXX": "pattern",
	}
	for did, verdict := range want {
		if got[did] != verdict {
			t.Errorf("%s = %q, want %q", did, got[did], verdict)
		}
	}
}

// A number routed here by a PATTERN in routes.json is still routed here. Testing
// only literals on both sides would miss that routes.json holds blocks.
func TestADIDRoutedHereByAPatternIsRoutedHere(t *testing.T) {
	b := &routingBackend{
		routing: `{"dids": {"+15558001201": "user/100"}}`,
		routes:  referenceRoutes(t),
	}
	data := routingUI(t, b).routingVMFor(t, "devtenant")
	if len(data.DIDs) != 1 || data.DIDs[0].Ownership != "routed" {
		t.Errorf("ownership = %+v, want routed via the +1555800XXXX block", data.DIDs)
	}
}

// Not being able to read routes.json is different from a number not being
// routed. Saying so is better than showing a verdict that is a guess.
func TestUnreadableRoutesAbstainsRatherThanAccusing(t *testing.T) {
	b := &routingBackend{
		routing:  `{"dids": {"+15558001200": "user/100"}}`,
		noRoutes: true,
	}
	data := routingUI(t, b).routingVMFor(t, "devtenant")

	if !data.RoutesUnavailable {
		t.Error("the page does not report that routes.json could not be read")
	}
	if data.DIDs[0].Ownership != "" {
		t.Errorf("ownership = %q, want no verdict at all", data.DIDs[0].Ownership)
	}
}

// A ring group nothing rings is dead weight that reads like live configuration.
func TestAGroupNothingRingsIsMarked(t *testing.T) {
	b := &routingBackend{
		routing: `{
  "extensions": {"600": "group/live"},
  "groups": {
    "live":   {"strategy": "sequential", "members": ["user/101"]},
    "orphan": {"strategy": "sequential", "members": ["user/102"]}
  }
}
`,
		routes: `{"dids": {}}`,
	}
	data := routingUI(t, b).routingVMFor(t, "devtenant")

	got := map[string]bool{}
	for _, g := range data.Groups {
		got[g.Name] = g.Unreached
	}
	if got["live"] {
		t.Error("a group an extension rings was marked unreached")
	}
	if !got["orphan"] {
		t.Error("a group nothing rings was not marked")
	}
}

// A group reached only from a FLOW is reached. The two files load as one unit,
// so looking at the routing table alone would call it dead.
func TestAGroupRungOnlyByAFlowIsReached(t *testing.T) {
	b := &routingBackend{
		routing: `{
  "extensions": {"700": "flow/main"},
  "groups": {"eng": {"strategy": "sequential", "members": ["user/101"]}}
}
`,
		flows: `{"flows": {"main": {"start": "d", "nodes": {
  "d": {"type": "dial_user", "entry": {"target": "group/eng"},
        "exits": {"no_answer": "b", "busy": "b", "rejected": "b", "unavailable": "b"}},
  "b": {"type": "hangup"}
}}}}
`,
		routes: `{"dids": {}}`,
	}
	data := routingUI(t, b).routingVMFor(t, "devtenant")
	if len(data.Groups) != 1 || data.Groups[0].Unreached {
		t.Errorf("a group rung by a flow was marked unreached: %+v", data.Groups)
	}
}

// Symbolic targets are the tenant's externally dialable surface, so nothing
// referencing one from inside the table is NORMAL. What is worth flagging is a
// target pointing at something this tenant does not have.
func TestSymbolicTargetsAreNotMarkedUnusedButDanglingOnesAreFlagged(t *testing.T) {
	b := &routingBackend{
		routing: `{
  "symbolic_targets": {
    "sales":   "user/110",
    "eng":     "group/engineering",
    "nowhere": "flow/does-not-exist"
  },
  "groups": {"engineering": {"strategy": "sequential", "members": ["user/101"]}}
}
`,
		routes: `{"dids": {}}`,
	}
	data := routingUI(t, b).routingVMFor(t, "devtenant")

	got := map[string]string{}
	for _, row := range data.Symbolic {
		got[row.Name] = row.Unresolved
	}
	// A user is a REGISTRATION, not a line in this file, so the config cannot
	// know whether one exists and must not pretend to.
	if got["sales"] != "" {
		t.Errorf("a user destination was questioned: %q", got["sales"])
	}
	if got["eng"] != "" {
		t.Errorf("a group this tenant defines was flagged: %q", got["eng"])
	}
	if got["nowhere"] == "" {
		t.Error("a symbolic target pointing at a flow that does not exist was not flagged")
	}
}

// --- Writing ---

// Only the named section's bytes are replaced, so an operator editing groups
// cannot damage the paragraph above dids.
func TestSavingASectionSplicesOnlyThatSection(t *testing.T) {
	original := referenceRouting(t)
	b := &routingBackend{routing: original, flows: referenceFlows(t), routes: referenceRoutes(t)}
	ui := routingUI(t, b)

	rec := ui.postForm(t, "/admin/tenants/devtenant/routing/section?server=test", url.Values{
		"section": {"groups"},
		"content": {`{
    "engineering": {
      "strategy": "sequential",
      "members": ["user/101", "user/102", "user/105"],
      "member_timeout_ms": 15000
    }
  }`},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !b.written {
		t.Fatal("the save never reached the validated write path")
	}
	mustSay(t, rec.Body.String(), savedFileMsg("devtenant", "routing"), "a landed write is reported")

	// The prose and every other section are byte-identical.
	for _, prose := range theProse {
		if !strings.Contains(b.wrote, prose) {
			t.Errorf("the write destroyed prose: %s", prose)
		}
	}
	beforeDIDs, _ := mustParseRouting(t, original).SectionRaw(sectionDIDs)
	afterDIDs, _ := mustParseRouting(t, b.wrote).SectionRaw(sectionDIDs)
	if beforeDIDs != afterDIDs {
		t.Error("a groups edit changed the dids section")
	}
	if !strings.Contains(b.wrote, "sequential") {
		t.Error("the edit did not land")
	}
}

// The server decides. A refusal comes back with its problems and nothing on disk.
func TestARefusedSectionSaveWritesNothing(t *testing.T) {
	b := &routingBackend{
		routing: referenceRouting(t), flows: referenceFlows(t), routes: referenceRoutes(t),
		reject: []types.ConfigProblem{{
			Path:     "routing",
			Message:  `ring group "engineering" has unknown strategy "nonsense"`,
			Severity: "error",
		}},
	}
	ui := routingUI(t, b)

	body := ui.postForm(t, "/admin/tenants/devtenant/routing/section?server=test", url.Values{
		"section": {"groups"},
		"content": {`{"engineering": {"strategy": "nonsense", "members": []}}`},
	}).Body.String()

	if b.written {
		t.Error("a refused write reached disk")
	}
	mustSay(t, body, msgNotSaved, "a refusal must say nothing was saved")
	mustSay(t, body, "unknown strategy", "the operator has to be told what is wrong")
}

// The live check is advisory and stateless: it reads no file and writes nothing.
func TestTheLiveCheckReportsConflictsAsTheyAreTyped(t *testing.T) {
	ui := routingUI(t, &routingBackend{routing: `{}`, routes: `{"dids": {}}`})

	body := ui.postForm(t, "/admin/tenants/devtenant/routing/check?server=test", url.Values{
		"pattern": {"NX1", "XN1"},
	}).Body.String()
	mustSay(t, body, "NX1", "the conflicting pair must be named")
	mustSay(t, body, "XN1", "both halves of the pair, not just one")

	clean := ui.postForm(t, "/admin/tenants/devtenant/routing/check?server=test", url.Values{
		"pattern": {"100", "1XX", "1."},
	}).Body.String()
	mustNotSay(t, clean, "neither more specific", "these have a strict ordering")
	mustSay(t, clean, "Match order", "the order is what the operator came for")
}

// --allow-global-config-writes is off by default, and the deployment page has to
// keep saying so. policy.json is the authorization boundary and this API is
// unauthenticated, which is exactly why the flag exists — a restructure that
// quietly made the page look editable would be the worst kind of regression.
func TestTheDeploymentPageRespectsTheWriteFlag(t *testing.T) {
	globals := func(writable bool) http.Handler {
		mux := http.NewServeMux()
		mux.HandleFunc("/api/v1/config/files", func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode([]types.GlobalFile{{
				Name: "policy", Title: "Class of Service", Exists: true,
				Configured: true, Writable: writable, Activation: "restart",
			}})
		})
		return mux
	}

	t.Run("read-only when the flag is off", func(t *testing.T) {
		ui := newTestServerWith(t, globals(false))
		data := ui.globalsVM(t.Context(), ui.clients[0])
		if len(data.Files) != 1 {
			t.Fatalf("files = %+v", data.Files)
		}
		if data.Files[0].Writable {
			t.Error("the page reports a file as writable that the server will refuse to write")
		}
	})

	t.Run("writable when the flag is on", func(t *testing.T) {
		ui := newTestServerWith(t, globals(true))
		data := ui.globalsVM(t.Context(), ui.clients[0])
		if !data.Files[0].Writable {
			t.Error("the page hides an edit the server would accept")
		}
	})

	// And the page renders either way, on a hard GET at its own URL.
	ui := newTestServerWith(t, globals(false))
	rec := ui.get(t, "/admin/deployment?server=test")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "restart") {
		t.Error("the page does not say these files need a restart to take effect")
	}
}
