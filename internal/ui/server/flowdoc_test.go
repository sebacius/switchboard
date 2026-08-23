package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sebas/switchboard/internal/signaling/dialplan"
)

// tenantsDir is the checked-in configuration the editor is tested against. The
// reference file is the one an operator actually opens first, so round-tripping
// anything else would be testing a fixture rather than the thing that matters.
const tenantsDir = "../../../resources/tenants"

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// devtenantTable loads the routing table a devtenant flow is validated against,
// because a flow is only valid with respect to the tenant that owns it.
func devtenantTable(t *testing.T) *dialplan.RoutingTable {
	t.Helper()
	var table dialplan.RoutingTable
	if err := json.Unmarshal([]byte(readFile(t, filepath.Join(tenantsDir, "devtenant.routing.json"))), &table); err != nil {
		t.Fatalf("parse devtenant routing table: %v", err)
	}
	return &table
}

// validates fails the test if a flows file would not load.
func validates(t *testing.T, tenant, content string, table *dialplan.RoutingTable) {
	t.Helper()
	var set dialplan.FlowSet
	if err := json.Unmarshal([]byte(content), &set); err != nil {
		t.Fatalf("the serialized file is not valid JSON: %v\n%s", err, content)
	}
	if err := dialplan.CheckFlows(tenant, table, &set).Err(); err != nil {
		t.Fatalf("the serialized file would not load: %v\n%s", err, content)
	}
}

// semantics decodes a document generically, so two files are compared by what
// they mean rather than by how they are spelled.
func semantics(t *testing.T, content string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(content), &out); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	return out
}

// withoutLayout removes the key the editor adds, which is the one intended
// difference between a file and its round trip.
func withoutLayout(doc map[string]any) map[string]any {
	flows, ok := doc["flows"].(map[string]any)
	if !ok {
		return doc
	}
	for _, raw := range flows {
		if flow, ok := raw.(map[string]any); ok {
			delete(flow, "_layout")
		}
	}
	return doc
}

// The property the whole design rests on: open a flow, save it untouched, and
// the file still means exactly what it meant. Anything less and an operator who
// clicks into the editor to look at a graph has silently changed how calls are
// routed.
func TestRoundTripPreservesTheReferenceFlow(t *testing.T) {
	original := readFile(t, filepath.Join(tenantsDir, "devtenant.flows.json"))

	doc, err := parseFlowsDoc(original)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	graph, err := doc.Graph("main-ivr")
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	out, err := doc.Splice("main-ivr", graph)
	if err != nil {
		t.Fatalf("splice: %v", err)
	}

	validates(t, "devtenant", out, devtenantTable(t))

	want := withoutLayout(semantics(t, original))
	got := withoutLayout(semantics(t, out))
	if !reflect.DeepEqual(want, got) {
		t.Errorf("round trip changed what the file means\n--- before ---\n%s\n--- after ---\n%s", original, out)
	}
}

// The graph the canvas receives has to be the flow, not an approximation of it.
func TestGraphReadsTheReferenceFlow(t *testing.T) {
	doc, err := parseFlowsDoc(readFile(t, filepath.Join(tenantsDir, "devtenant.flows.json")))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := doc.FlowNames(); !reflect.DeepEqual(got, []string{"main-ivr"}) {
		t.Fatalf("flow names = %v", got)
	}

	graph, err := doc.Graph("main-ivr")
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	if graph.Start != "greeting" {
		t.Errorf("start = %q, want greeting", graph.Start)
	}
	if len(graph.Nodes) != 6 {
		t.Fatalf("got %d nodes, want 6", len(graph.Nodes))
	}
	if graph.Nodes[0].ID != "greeting" {
		t.Errorf("first node = %q; document order should be preserved", graph.Nodes[0].ID)
	}

	// The menu's digit exits come first, then the outcomes, so an ivr node's
	// ports read the way the menu does.
	var names []string
	for _, exit := range graph.Nodes[0].Exits {
		names = append(names, exit.Name)
	}
	if !reflect.DeepEqual(names, []string{"1", "2", "invalid", "retries_exceeded", "timeout"}) {
		t.Errorf("greeting exits = %v", names)
	}
}

// twoFlows is a document with a comment at every scope and a flow that must
// come through an edit untouched.
const twoFlows = `{
  "_comment": "Why this tenant is shaped this way.",

  "flows": {
    "main-ivr": {
      "start": "hello",
      "_comment_nodes": "answered is terminal and must not be declared.",
      "nodes": {
        "hello": {
          "type": "tts",
          "entry": { "text": "Hello." },
          "exits": { "done": "bye" }
        },
        "bye": {
          "type": "hangup",
          "entry": { "cause": "normal_clearing" }
        }
      }
    },

    "after-hours": {
      "start": "closed",
      "nodes": {
        "closed": {
          "type": "tts",
          "entry": { "text": "We are closed." },
          "exits": { "done": "gone" }
        },
        "gone": { "type": "hangup" }
      }
    }
  }
}
`

// Editing one flow must not touch another. Byte splicing makes that structural
// rather than a matter of being careful, so the test asserts the sibling's
// exact text survives — not merely its meaning.
func TestEditingOneFlowLeavesTheOthersAlone(t *testing.T) {
	doc, err := parseFlowsDoc(twoFlows)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	graph, err := doc.Graph("main-ivr")
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	graph.Nodes[0].X, graph.Nodes[0].Y = 400, 200

	out, err := doc.Splice("main-ivr", graph)
	if err != nil {
		t.Fatalf("splice: %v", err)
	}

	sibling := `"after-hours": {
      "start": "closed",
      "nodes": {
        "closed": {
          "type": "tts",
          "entry": { "text": "We are closed." },
          "exits": { "done": "gone" }
        },
        "gone": { "type": "hangup" }
      }
    }`
	if !strings.Contains(out, sibling) {
		t.Errorf("the untouched flow was rewritten:\n%s", out)
	}
	if !strings.Contains(out, `"_comment": "Why this tenant is shaped this way."`) {
		t.Errorf("the document comment was lost:\n%s", out)
	}
	if !strings.Contains(out, `"_comment_nodes": "answered is terminal and must not be declared."`) {
		t.Errorf("the flow comment was lost:\n%s", out)
	}
	if !strings.Contains(out, `"hello": [400, 200]`) {
		t.Errorf("the layout was not written:\n%s", out)
	}
}

// A layout is invisible to the loader. If it were not, a cosmetic edit could
// change how calls route, which is the one thing an editor must never do.
func TestLayoutDoesNotChangeWhatTheValidatorSees(t *testing.T) {
	doc, err := parseFlowsDoc(twoFlows)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	graph, err := doc.Graph("main-ivr")
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	out, err := doc.Splice("main-ivr", graph)
	if err != nil {
		t.Fatalf("splice: %v", err)
	}

	table := &dialplan.RoutingTable{Operator: "user/100"}
	before := dialplan.CheckFlows("acme", table, flowSetFrom(t, twoFlows))
	after := dialplan.CheckFlows("acme", table, flowSetFrom(t, out))
	if len(before) != len(after) {
		t.Fatalf("adding a layout changed the validator's findings: %d before, %d after", len(before), len(after))
	}
	if err := after.Err(); err != nil {
		t.Fatalf("a laid-out flow no longer loads: %v", err)
	}

	// And it comes back.
	reopened, err := parseFlowsDoc(out)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	regraph, err := reopened.Graph("main-ivr")
	if err != nil {
		t.Fatalf("re-graph: %v", err)
	}
	for _, node := range regraph.Nodes {
		if node.ID == "hello" && (node.X != graph.Nodes[0].X || node.Y != graph.Nodes[0].Y) {
			t.Errorf("position did not survive the round trip: got (%d,%d), want (%d,%d)",
				node.X, node.Y, graph.Nodes[0].X, graph.Nodes[0].Y)
		}
	}
}

func flowSetFrom(t *testing.T, content string) *dialplan.FlowSet {
	t.Helper()
	var set dialplan.FlowSet
	if err := json.Unmarshal([]byte(content), &set); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	return &set
}

// Coordinates for a node nobody can point at are a puzzle for the next reader.
func TestStaleLayoutEntriesArePruned(t *testing.T) {
	const doc = `{
  "flows": {
    "main": {
      "start": "hello",
      "_layout": { "hello": [10, 20], "removed-long-ago": [99, 99] },
      "nodes": {
        "hello": { "type": "hangup" }
      }
    }
  }
}
`
	parsed, err := parseFlowsDoc(doc)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	graph, err := parsed.Graph("main")
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	out, err := parsed.Splice("main", graph)
	if err != nil {
		t.Fatalf("splice: %v", err)
	}
	if strings.Contains(out, "removed-long-ago") {
		t.Errorf("coordinates for a node that no longer exists were kept:\n%s", out)
	}
	if !strings.Contains(out, `"hello": [10, 20]`) {
		t.Errorf("a live node's position was lost:\n%s", out)
	}
}

// A layout is a convenience. A broken one must cost nothing.
func TestUnusableLayoutFallsBackToComputedPlacement(t *testing.T) {
	const doc = `{
  "flows": {
    "main": {
      "start": "hello",
      "_layout": { "hello": "somewhere", "ghost": [1, 2] },
      "nodes": {
        "hello": { "type": "hangup" }
      }
    }
  }
}
`
	parsed, err := parseFlowsDoc(doc)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	graph, err := parsed.Graph("main")
	if err != nil {
		t.Fatalf("a malformed layout stopped the flow being read: %v", err)
	}
	if len(graph.Nodes) != 1 {
		t.Fatalf("got %d nodes, want 1", len(graph.Nodes))
	}
	if graph.Nodes[0].X != layoutOriginX || graph.Nodes[0].Y != layoutOriginY {
		t.Errorf("start node placed at (%d,%d), want the computed origin (%d,%d)",
			graph.Nodes[0].X, graph.Nodes[0].Y, layoutOriginX, layoutOriginY)
	}
}

// A flow that has never been opened should still read left to right.
func TestComputedPlacementFollowsGraphDepth(t *testing.T) {
	doc, err := parseFlowsDoc(readFile(t, filepath.Join(tenantsDir, "devtenant.flows.json")))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	graph, err := doc.Graph("main-ivr")
	if err != nil {
		t.Fatalf("graph: %v", err)
	}

	at := map[string]graphNode{}
	for _, node := range graph.Nodes {
		at[node.ID] = node
	}

	// greeting -> ring-sales -> to-operator -> closing -> bye is depth 0..4.
	depths := []struct {
		id    string
		depth int
	}{
		{"greeting", 0}, {"ring-sales", 1}, {"ring-engineering", 1},
		{"to-operator", 1}, {"closing", 2}, {"bye", 3},
	}
	for _, want := range depths {
		node, ok := at[want.id]
		if !ok {
			t.Fatalf("node %q is missing", want.id)
		}
		if got := layoutOriginX + want.depth*layoutColumnW; node.X != got {
			t.Errorf("%s: x = %d, want %d (BFS depth %d)", want.id, node.X, got, want.depth)
		}
	}
	if at["greeting"].X >= at["bye"].X {
		t.Error("the start node should be left of the node that ends the call")
	}
}

// An exit the operator has not wired is left out, so the validator answers with
// the message that says what to do about it.
func TestAnUnwiredExitIsRefusedWithTheHelpfulMessage(t *testing.T) {
	const doc = `{
  "flows": {
    "main": {
      "start": "ring",
      "nodes": {
        "ring": {
          "type": "dial_user",
          "entry": { "target": "user/100", "timeout_ms": 20000 },
          "exits": {
            "no_answer": "bye", "busy": "bye", "rejected": "bye", "unavailable": "bye"
          }
        },
        "bye": { "type": "hangup" }
      }
    }
  }
}
`
	parsed, err := parseFlowsDoc(doc)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	graph, err := parsed.Graph("main")
	if err != nil {
		t.Fatalf("graph: %v", err)
	}

	// Unwire "busy", the way deleting a connection on the canvas does.
	for i, exit := range graph.Nodes[0].Exits {
		if exit.Name == "busy" {
			graph.Nodes[0].Exits[i].Target = ""
		}
	}
	out, err := parsed.Splice("main", graph)
	if err != nil {
		t.Fatalf("splice: %v", err)
	}
	if strings.Contains(out, `"busy"`) {
		t.Errorf("an unwired exit was written out:\n%s", out)
	}

	err = dialplan.CheckFlows("acme", &dialplan.RoutingTable{Operator: "user/100"}, flowSetFrom(t, out)).Err()
	if err == nil {
		t.Fatal("an unwired exit was accepted")
	}
	if !strings.Contains(err.Error(), "does not wire its \"busy\" exit") {
		t.Errorf("the refusal does not say which exit is unwired: %v", err)
	}
}

// A file written with four spaces should not come back half-converted.
func TestTheDocumentsIndentationIsKept(t *testing.T) {
	const fourSpace = `{
    "flows": {
        "main": {
            "start": "bye",
            "nodes": {
                "bye": { "type": "hangup" }
            }
        }
    }
}
`
	parsed, err := parseFlowsDoc(fourSpace)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.indent != "    " {
		t.Fatalf("indent = %q, want four spaces", parsed.indent)
	}
	graph, err := parsed.Graph("main")
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	out, err := parsed.Splice("main", graph)
	if err != nil {
		t.Fatalf("splice: %v", err)
	}
	if !strings.Contains(out, "\n            \"_layout\": {\n                \"bye\": [") {
		t.Errorf("the inserted layout does not use the file's own indentation:\n%s", out)
	}
}

// The property that makes the editor safe to open out of curiosity: looking at
// a flow, or nudging one node, must not rewrite the parts nobody touched. A
// reformat is not harmless — it buries the real change in a diff nobody can
// review, and it does it to a file that decides how calls are routed.
func TestALayoutOnlySaveTouchesOnlyTheLayout(t *testing.T) {
	original := readFile(t, filepath.Join(tenantsDir, "devtenant.flows.json"))

	doc, err := parseFlowsDoc(original)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	graph, err := doc.Graph("main-ivr")
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	graph.Nodes[0].X, graph.Nodes[0].Y = 480, 260

	out, err := doc.Splice("main-ivr", graph)
	if err != nil {
		t.Fatalf("splice: %v", err)
	}

	// Every node in the reference file keeps its exact text, inline entries and
	// all, because none of them changed.
	for _, verbatim := range []string{
		`"entry": { "target": "user/110", "timeout_ms": 20000 },`,
		`"entry": { "target": "group/engineering", "timeout_ms": 20000 },`,
		`"exits": { "done": "bye" }`,
		"\"timeout\": \"to-operator\",\n            \"invalid\": \"to-operator\",",
	} {
		if !strings.Contains(out, verbatim) {
			t.Errorf("a node nobody edited was reformatted; expected to still find:\n%s", verbatim)
		}
	}

	// The only new text is the layout.
	added := addedLines(original, out)
	for _, line := range added {
		if !strings.Contains(line, "_layout") && !strings.Contains(line, "[") && !strings.Contains(line, "}") {
			t.Errorf("a layout-only save added an unrelated line: %q", line)
		}
	}
	if len(removedLines(original, out)) != 0 {
		t.Errorf("a layout-only save removed lines: %v", removedLines(original, out))
	}
}

// Editing one node must not reformat its neighbors.
func TestEditingOneNodeLeavesTheOthersVerbatim(t *testing.T) {
	original := readFile(t, filepath.Join(tenantsDir, "devtenant.flows.json"))

	doc, err := parseFlowsDoc(original)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	graph, err := doc.Graph("main-ivr")
	if err != nil {
		t.Fatalf("graph: %v", err)
	}
	for i := range graph.Nodes {
		if graph.Nodes[i].ID == "ring-sales" {
			graph.Nodes[i].Entry = []byte(`{"target": "user/111", "timeout_ms": 20000}`)
		}
	}

	out, err := doc.Splice("main-ivr", graph)
	if err != nil {
		t.Fatalf("splice: %v", err)
	}
	if !strings.Contains(out, `"user/111"`) {
		t.Errorf("the edit was not written:\n%s", out)
	}
	if !strings.Contains(out, `"entry": { "target": "group/engineering", "timeout_ms": 20000 },`) {
		t.Error("editing one node reformatted a node next to it")
	}
	validates(t, "devtenant", out, devtenantTable(t))
}

// addedLines and removedLines compare two documents line by line, which is how
// an operator reviewing the change will see it.
func addedLines(before, after string) []string {
	old := map[string]int{}
	for _, line := range strings.Split(before, "\n") {
		old[line]++
	}
	var out []string
	for _, line := range strings.Split(after, "\n") {
		if old[line] > 0 {
			old[line]--
			continue
		}
		out = append(out, line)
	}
	return out
}

func removedLines(before, after string) []string { return addedLines(after, before) }

// A document the editor cannot make sense of should say so rather than render
// an empty canvas that looks like an empty tenant.
func TestAFileWithNoFlowsKeyIsRejectedClearly(t *testing.T) {
	if _, err := parseFlowsDoc(`{"tenants": {}}`); err == nil {
		t.Fatal("a file with no flows key was accepted")
	} else if !strings.Contains(err.Error(), "flows") {
		t.Errorf("the error does not mention what is missing: %v", err)
	}
	if _, err := parseFlowsDoc("not json"); err == nil {
		t.Fatal("a file that is not JSON was accepted")
	}
}

// Saving a flow that vanished underneath the editor must be refused, not
// silently recreated over whatever replaced it.
func TestSplicingAFlowThatIsGoneIsRefused(t *testing.T) {
	parsed, err := parseFlowsDoc(twoFlows)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := parsed.Splice("renamed-since", flowGraph{Name: "renamed-since"}); err == nil {
		t.Fatal("splicing a flow the document does not have was accepted")
	}
}
