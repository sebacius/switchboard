package server

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sebas/switchboard/internal/signaling/dialplan"
)

// The reference file is the fixture on purpose. It carries three paragraphs of
// prose that dialplan.RoutingTable has no field for, including the one
// explaining why DID ownership is split across two files — the least obvious
// thing about the whole configuration model. If an editor can delete that, it
// will, and nobody will notice until somebody needs it.

func referenceRouting(t *testing.T) string {
	t.Helper()
	return readFile(t, filepath.Join(tenantsDir, "devtenant.routing.json"))
}

func mustParseRouting(t *testing.T, content string) *routingDoc {
	t.Helper()
	d, err := parseRoutingDoc(content)
	if err != nil {
		t.Fatalf("parseRoutingDoc: %v", err)
	}
	return d
}

// loads asserts the output is a routing table the loader would accept, through
// the loader's OWN validator rather than a copy of it.
func loads(t *testing.T, content string) {
	t.Helper()
	var table dialplan.RoutingTable
	if err := json.Unmarshal([]byte(content), &table); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, content)
	}
	if err := dialplan.ValidateTable("devtenant", &table, []byte(content)); err != nil {
		t.Fatalf("output would not load: %v\n%s", err, content)
	}
}

// theProse is every comment key in the reference file. Each must survive every
// mutation, byte for byte.
var theProse = []string{
	`"_comment": "Routing data for the small local-testing tenant.`,
	`"_comment_extensions": "Bare destinations are sugar for a one-node dial.`,
	`"_comment_dids": "Step TWO for an inbound call:`,
}

func assertProseIntact(t *testing.T, out string) {
	t.Helper()
	for _, want := range theProse {
		if !strings.Contains(out, want) {
			t.Errorf("a mutation destroyed prose:\n  missing: %s", want)
		}
	}
}

// --- Reading ---

func TestReadingTheReferenceTable(t *testing.T) {
	d := mustParseRouting(t, referenceRouting(t))

	if op, ok := d.Scalar("operator"); !ok || op != "user/100" {
		t.Errorf("operator = %q (%v), want user/100", op, ok)
	}
	if p, ok := d.Scalar("retrieval_prefix"); !ok || p != "*" {
		t.Errorf("retrieval_prefix = %q (%v), want *", p, ok)
	}

	// Document order, not sorted: the author grouped these on purpose.
	ext, err := d.Entries(sectionExtensions)
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	wantOrder := []string{"100", "101", "102", "105", "110", "600", "700"}
	var gotOrder []string
	for _, row := range ext {
		gotOrder = append(gotOrder, row.Key)
	}
	if !reflect.DeepEqual(gotOrder, wantOrder) {
		t.Errorf("extension order = %v, want %v", gotOrder, wantOrder)
	}
	for _, row := range ext {
		if !row.Bare {
			t.Errorf("extension %q should read as a bare destination", row.Key)
		}
	}

	groups, err := d.Groups()
	if err != nil {
		t.Fatalf("Groups: %v", err)
	}
	if len(groups) != 1 || groups[0].Name != "engineering" {
		t.Fatalf("groups = %+v, want one named engineering", groups)
	}
	if groups[0].Strategy != "round-robin" || groups[0].MemberTimeoutMs != 12000 {
		t.Errorf("group = %+v, want round-robin at 12000ms", groups[0])
	}
	if !reflect.DeepEqual(groups[0].Members, []string{"user/101", "user/102"}) {
		t.Errorf("members = %v", groups[0].Members)
	}

	sym, err := d.Symbolic()
	if err != nil {
		t.Fatalf("Symbolic: %v", err)
	}
	if len(sym) != 5 {
		t.Errorf("symbolic targets = %d, want 5", len(sym))
	}
}

// A tenant with no ring groups is ordinary, not an error.
func TestAnAbsentSectionIsNotAnError(t *testing.T) {
	d := mustParseRouting(t, "{\n  \"operator\": \"user/100\"\n}\n")
	rows, err := d.Groups()
	if err != nil {
		t.Fatalf("Groups on a file with none: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("groups = %v, want none", rows)
	}
}

// --- Section isolation: the property the whole file exists for ---

func TestEditingGroupsCannotTouchDIDs(t *testing.T) {
	original := referenceRouting(t)
	d := mustParseRouting(t, original)

	out, err := d.SetGroup("engineering", groupRow{
		Name: "engineering", Strategy: "sequential",
		Members: []string{"user/101", "user/102", "user/105"}, MemberTimeoutMs: 15000,
	})
	if err != nil {
		t.Fatalf("SetGroup: %v", err)
	}
	loads(t, out)
	assertProseIntact(t, out)

	// The dids section, and the paragraph above it, are byte-identical.
	didsBefore, _ := mustParseRouting(t, original).SectionRaw(sectionDIDs)
	didsAfter, _ := mustParseRouting(t, out).SectionRaw(sectionDIDs)
	if didsBefore != didsAfter {
		t.Errorf("editing groups changed dids:\n--- before ---\n%s\n--- after ---\n%s", didsBefore, didsAfter)
	}

	// And the whole diff is inside the groups section.
	added, removed := addedLines(original, out), removedLines(original, out)
	for _, line := range append(added, removed...) {
		if strings.Contains(line, "dids") || strings.Contains(line, "+1555800") {
			t.Errorf("a groups edit touched a DID line: %q", line)
		}
		if strings.Contains(line, "_comment") {
			t.Errorf("a groups edit touched prose: %q", line)
		}
	}
}

func TestEveryMutatorLeavesTheProseAlone(t *testing.T) {
	original := referenceRouting(t)

	mutations := map[string]func(*routingDoc) (string, error){
		"set operator": func(d *routingDoc) (string, error) { return d.SetScalar(keyOperator, "user/105") },
		"set an extension": func(d *routingDoc) (string, error) {
			return d.SetEntry(sectionExtensions, "111", entryRow{Key: "111", Destination: "user/111"})
		},
		"change a DID": func(d *routingDoc) (string, error) {
			return d.SetEntry(sectionDIDs, "+15558001200", entryRow{Key: "+15558001200", Destination: "user/100"})
		},
		"add a DID": func(d *routingDoc) (string, error) {
			return d.SetEntry(sectionDIDs, "+15558001202", entryRow{Key: "+15558001202", Destination: "user/105"})
		},
		"set a symbolic":    func(d *routingDoc) (string, error) { return d.SetSymbolic("billing", "user/102") },
		"rename a symbolic": func(d *routingDoc) (string, error) { return d.RenameKey(sectionSymbolic, "support", "helpdesk") },
		"delete a symbolic": func(d *routingDoc) (string, error) { return d.DeleteKey(sectionSymbolic, "reception") },
		"delete the last ext": func(d *routingDoc) (string, error) {
			return d.DeleteKey(sectionExtensions, "700")
		},
		"delete the first ext": func(d *routingDoc) (string, error) {
			return d.DeleteKey(sectionExtensions, "100")
		},
		"delete a middle ext": func(d *routingDoc) (string, error) {
			return d.DeleteKey(sectionExtensions, "105")
		},
	}

	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			out, err := mutate(mustParseRouting(t, original))
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			assertProseIntact(t, out)
			loads(t, out)
		})
	}
}

// A pattern edit must not re-sort the table under the operator.
func TestRenamingAnExtensionKeepsItsPosition(t *testing.T) {
	original := referenceRouting(t)
	out, err := mustParseRouting(t, original).RenameKey(sectionExtensions, "105", "205")
	if err != nil {
		t.Fatalf("RenameKey: %v", err)
	}
	loads(t, out)

	var got []string
	rows, err := mustParseRouting(t, out).Entries(sectionExtensions)
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	for _, r := range rows {
		got = append(got, r.Key)
	}
	want := []string{"100", "101", "102", "205", "110", "600", "700"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v — the renamed key moved", got, want)
	}
}

// Key order at the top level is the author's and must survive.
func TestTopLevelKeyOrderIsPreserved(t *testing.T) {
	original := referenceRouting(t)
	before := mustParseRouting(t, original).Keys()

	out, err := mustParseRouting(t, original).SetEntry(
		sectionExtensions, "111", entryRow{Key: "111", Destination: "user/111"})
	if err != nil {
		t.Fatalf("SetEntry: %v", err)
	}
	if after := mustParseRouting(t, out).Keys(); !reflect.DeepEqual(before, after) {
		t.Errorf("top-level key order changed\nbefore %v\nafter  %v", before, after)
	}
}

// --- The bare-vs-object rule ---

// renderEntryValue is a second implementation of dialplan.Entry.MarshalJSON's
// rule, so the two are pinned together. If they drift, a round trip starts
// rewriting every simple line into an object.
func TestAWrittenEntryMatchesWhatTheLoaderWouldWrite(t *testing.T) {
	cases := []entryRow{
		{Key: "110", Destination: "user/110"},
		{Key: "600", Destination: "group/engineering"},
		{Key: "700", Destination: "flow/main-ivr"},
		{Key: "9NXXNXXXXXX", Destination: "flow/outbound", Transform: dialplan.Transform{StripDigits: 1}},
		{Key: "8XXX", Destination: "user/100", Transform: dialplan.Transform{Normalize: "e164"}},
	}
	for _, row := range cases {
		text, err := renderEntryValue(row, "    ")
		if err != nil {
			t.Fatalf("renderEntryValue(%q): %v", row.Key, err)
		}

		var parsed dialplan.Entry
		if err := json.Unmarshal([]byte(text), &parsed); err != nil {
			t.Fatalf("%q rendered unparseable JSON %q: %v", row.Key, text, err)
		}
		if parsed.Destination != row.Destination || parsed.Transform != row.Transform {
			t.Errorf("%q round-tripped to %+v, want destination %q transform %+v",
				row.Key, parsed, row.Destination, row.Transform)
		}

		// And the FORM agrees: bare when there is no transform, object when
		// there is, which is exactly what MarshalJSON decides.
		want, err := json.Marshal(parsed)
		if err != nil {
			t.Fatal(err)
		}
		gotBare := strings.HasPrefix(strings.TrimSpace(text), `"`)
		wantBare := strings.HasPrefix(string(want), `"`)
		if gotBare != wantBare {
			t.Errorf("%q rendered as %s, but the loader writes %s", row.Key, text, want)
		}
	}
}

// A simple entry that is edited must stay a single line, not become an object.
func TestEditingASimpleEntryKeepsItOnOneLine(t *testing.T) {
	out, err := mustParseRouting(t, referenceRouting(t)).SetEntry(
		sectionExtensions, "110", entryRow{Key: "110", Destination: "user/199"})
	if err != nil {
		t.Fatalf("SetEntry: %v", err)
	}
	loads(t, out)
	if !strings.Contains(out, `"110": "user/199"`) {
		t.Errorf("a simple entry was not written as a bare destination:\n%s", out)
	}
}

// --- Adding a section a file does not have ---

func TestAddingGroupsToATenantWithoutThem(t *testing.T) {
	// A file shaped like the reference one, minus groups, with the DID prose in
	// place — because the hazard is landing between a comment and its key.
	const noGroups = `{
  "_comment": "a tenant that rings nobody as a group",

  "operator": "user/100",

  "_comment_dids": "step TWO for an inbound call",
  "dids": {
    "+15558001200": "user/100"
  }
}
`
	out, err := mustParseRouting(t, noGroups).SetGroup("engineering", groupRow{
		Name: "engineering", Strategy: "round-robin",
		Members: []string{"user/101", "user/102"}, MemberTimeoutMs: 12000,
	})
	if err != nil {
		t.Fatalf("SetGroup: %v", err)
	}
	loads(t, out)

	// The prose is intact and still above the key it describes.
	comment := strings.Index(out, `"_comment_dids"`)
	dids := strings.Index(out, `"dids"`)
	groups := strings.Index(out, `"groups"`)
	if !(comment >= 0 && comment < dids && dids < groups) {
		t.Errorf("the new section landed between a comment and its key:\n%s", out)
	}

	rows, err := mustParseRouting(t, out).Groups()
	if err != nil || len(rows) != 1 || rows[0].Name != "engineering" {
		t.Errorf("groups = %+v (%v)", rows, err)
	}
}

// --- Formatting ---

// A file written with four spaces must not come back half converted.
func TestTheDocumentsIndentIsKept(t *testing.T) {
	const fourSpace = `{
    "operator": "user/100",
    "extensions": {
        "100": "user/100"
    }
}
`
	out, err := mustParseRouting(t, fourSpace).SetEntry(
		sectionExtensions, "101", entryRow{Key: "101", Destination: "user/101"})
	if err != nil {
		t.Fatalf("SetEntry: %v", err)
	}
	loads(t, out)
	if !strings.Contains(out, "\n        \"101\": \"user/101\"") {
		t.Errorf("the inserted entry does not match the document's indent:\n%s", out)
	}
	if strings.Contains(out, "\n  \"101\"") {
		t.Errorf("the entry was written at two spaces in a four-space file:\n%s", out)
	}
}

// --- The escape hatch ---

// The raw editor and the structured editor are the same splice, so there is only
// one way a file gets written.
func TestSplicingASectionAsRawText(t *testing.T) {
	original := referenceRouting(t)
	out, err := mustParseRouting(t, original).SpliceSection(sectionSymbolic, `{
    "operator": "user/100",
    "billing": "user/102"
  }`)
	if err != nil {
		t.Fatalf("SpliceSection: %v", err)
	}
	loads(t, out)
	assertProseIntact(t, out)

	rows, err := mustParseRouting(t, out).Symbolic()
	if err != nil {
		t.Fatalf("Symbolic: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("symbolic targets = %+v, want the two that were spliced in", rows)
	}
}

// --- Cross-file references ---

// The routing table and the flows file load as ONE unit, so deleting a flow an
// extension points at breaks the tenant. This is what makes offering the delete
// safe: name what breaks, before the round trip.
func TestFindingWhatReferencesAFlow(t *testing.T) {
	d := mustParseRouting(t, referenceRouting(t))

	found := d.ReferencesToFlow("main-ivr")
	want := []string{`dids."+15558001200"`, `extensions."700"`}
	if !reflect.DeepEqual(found, want) {
		t.Errorf("references = %v, want %v", found, want)
	}

	if got := d.ReferencesToFlow("after-hours"); len(got) != 0 {
		t.Errorf("references to an unreferenced flow = %v, want none", got)
	}
}

// --- Refusals ---

func TestAMalformedFileIsRejectedClearly(t *testing.T) {
	if _, err := parseRoutingDoc(`["not", "an", "object"]`); err == nil {
		t.Error("an array should not parse as a routing table")
	}
}

func TestARefusedWriteChangesNothing(t *testing.T) {
	original := referenceRouting(t)
	d := mustParseRouting(t, original)

	if _, err := d.SetEntry(sectionExtensions, "111", entryRow{Key: "111"}); err == nil {
		t.Error("an entry with no destination should be refused")
	}
	if _, err := d.SetGroup("empty", groupRow{Name: "empty", Strategy: "sequential"}); err == nil {
		t.Error("a group with no members should be refused — it can never answer")
	}
	if _, err := d.SetScalar("nonsense", "x"); err == nil {
		t.Error("only the two scalar keys should be settable")
	}
	if d.content != original {
		t.Error("a refused write modified the document")
	}
}

// Setting an empty operator deletes the key. `"operator": ""` reads as a
// decision where there was none, and the loader treats it as absent anyway.
func TestClearingAScalarDeletesTheKey(t *testing.T) {
	out, err := mustParseRouting(t, referenceRouting(t)).SetScalar(keyRetrievalPrefix, "")
	if err != nil {
		t.Fatalf("SetScalar: %v", err)
	}
	loads(t, out)
	if strings.Contains(out, "retrieval_prefix") {
		t.Errorf("the key survived being cleared:\n%s", out)
	}
	assertProseIntact(t, out)
}

// The routing table and the flows file are validated as ONE unit by the loader,
// so a routing edit that orphans a group a flow dials is a broken tenant. This
// runs the mutated table through the same cross-file check the config API does,
// against devtenant's real flows.
func TestAMutatedTableStillValidatesAgainstTheTenantsFlows(t *testing.T) {
	flows := referenceFlows(t)
	var set dialplan.FlowSet
	if err := json.Unmarshal([]byte(flows), &set); err != nil {
		t.Fatalf("flows fixture: %v", err)
	}

	mutations := map[string]func(*routingDoc) (string, error){
		"add an extension": func(d *routingDoc) (string, error) {
			return d.SetEntry(sectionExtensions, "111", entryRow{Key: "111", Destination: "user/111"})
		},
		"retarget a DID": func(d *routingDoc) (string, error) {
			return d.SetEntry(sectionDIDs, "+15558001201", entryRow{Key: "+15558001201", Destination: "user/105"})
		},
		"grow the ring group": func(d *routingDoc) (string, error) {
			return d.SetGroup("engineering", groupRow{
				Name: "engineering", Strategy: "round-robin",
				Members: []string{"user/101", "user/102", "user/105"}, MemberTimeoutMs: 12000,
			})
		},
		"add a symbolic target": func(d *routingDoc) (string, error) {
			return d.SetSymbolic("billing", "user/102")
		},
	}

	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			out, err := mutate(mustParseRouting(t, referenceRouting(t)))
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}

			var table dialplan.RoutingTable
			if err := json.Unmarshal([]byte(out), &table); err != nil {
				t.Fatalf("output is not valid JSON: %v", err)
			}
			if err := dialplan.ValidateTable("devtenant", &table, []byte(out)); err != nil {
				t.Fatalf("output would not load: %v", err)
			}
			if problems := dialplan.CheckFlows("devtenant", &table, &set); problems.HasErrors() {
				t.Fatalf("the edit broke the tenant's flows: %v", problems.Err())
			}
		})
	}
}

// Removing a group a flow rings must be caught by the cross-file check — the
// reason the two files load as one unit. Not a bug in the splice: a bug the
// splice must not be able to hide.
func TestRemovingAGroupAFlowRingsIsCaught(t *testing.T) {
	out, err := mustParseRouting(t, referenceRouting(t)).DeleteKey(sectionGroups, "engineering")
	if err != nil {
		t.Fatalf("DeleteKey: %v", err)
	}

	var table dialplan.RoutingTable
	if err := json.Unmarshal([]byte(out), &table); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}

	var set dialplan.FlowSet
	if err := json.Unmarshal([]byte(referenceFlows(t)), &set); err != nil {
		t.Fatalf("flows fixture: %v", err)
	}

	tableErr := dialplan.ValidateTable("devtenant", &table, []byte(out))
	flowProblems := dialplan.CheckFlows("devtenant", &table, &set)
	if tableErr == nil && !flowProblems.HasErrors() {
		t.Error("deleting a group the flows ring was accepted by both checks")
	}
}
