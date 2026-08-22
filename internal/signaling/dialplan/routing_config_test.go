package dialplan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The shipped resources are configuration, and configuration that does not load
// is a deployment that does not route. These load the real files rather than a
// fixture, so a typo in a tenant's routing table fails here instead of on a call.

func repoResources(t *testing.T, parts ...string) string {
	t.Helper()
	return filepath.Join(append([]string{"..", "..", "..", "resources"}, parts...)...)
}

func TestShippedRoutingTablesLoad(t *testing.T) {
	store, err := NewRoutingStore(repoResources(t, "tenants"))
	if err != nil {
		t.Fatalf("the shipped routing tables must load: %v", err)
	}

	tenants := store.Tenants()
	if len(tenants) == 0 {
		t.Fatal("expected at least one shipped routing table")
	}

	for _, tenant := range tenants {
		table, ok := store.TenantRouting(tenant)
		if !ok {
			t.Fatalf("tenant %s listed but not retrievable", tenant)
		}
		// An operator is what keeps an unknown tool name and an unanswered group
		// from dropping a caller, so a shipped tenant should have one.
		if table.Operator == "" {
			t.Errorf("tenant %s has no operator: an unknown tool would have nowhere to go", tenant)
		}
		if len(table.SymbolicTargets) == 0 {
			t.Errorf("tenant %s defines no dialable names", tenant)
		}
	}
}

// Every symbolic target must resolve to something real: an endpoint, or a group
// that exists. A dangling name looks like a working destination in config and
// like silence to the caller.
func TestShippedSymbolicTargetsResolve(t *testing.T) {
	store, err := NewRoutingStore(repoResources(t, "tenants"))
	if err != nil {
		t.Fatalf("load routing tables: %v", err)
	}

	for _, tenant := range store.Tenants() {
		table, _ := store.TenantRouting(tenant)
		for name, dest := range table.SymbolicTargets {
			if group, isGroup := IsGroupTarget(dest); isGroup {
				if _, ok := table.Group(group); !ok {
					t.Errorf("tenant %s: symbolic target %q names missing group %q", tenant, name, group)
				}
				continue
			}
			if dest == "" {
				t.Errorf("tenant %s: symbolic target %q resolves to nothing", tenant, name)
			}
		}
	}
}

// Configuration written for the LLM supervisor must fail by name. "unknown
// destination" would send an operator hunting through a diff for a value that
// was correct one release ago; the error has to say what was removed and what
// replaced it.
func TestRetiredSupervisorVocabularyIsRejectedByName(t *testing.T) {
	cases := []struct {
		name     string
		file     string
		mentions []string
	}{
		{
			name: "assistant destination",
			file: `{"extensions":{"600":"assistant"}}`,
			// Name the value, say it is gone, and say where to go instead.
			mentions: []string{"assistant", "supervisor was removed", "flow"},
		},
		{
			name:     "group no_answer",
			file:     `{"groups":{"claims":{"strategy":"sequential","members":["user/130"],"no_answer":"supervisor"}}}`,
			mentions: []string{"no_answer", "claims", "dial_user"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "acme"+routingFileSuffix)
			if err := os.WriteFile(path, []byte(tc.file), 0o600); err != nil {
				t.Fatalf("write fixture: %v", err)
			}

			_, err := NewRoutingStore(dir)
			if err == nil {
				t.Fatal("retired vocabulary must fail the load")
			}
			for _, want := range tc.mentions {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error must mention %q, got: %v", want, err)
				}
			}
		})
	}
}

// A reload that fails must leave the previously loaded configuration in force.
// A bad edit taking the tenant's routing down with it is the failure mode this
// store exists to prevent.
func TestFailedReloadKeepsPreviousTables(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "acme"+routingFileSuffix)
	good := `{"operator":"user/100","extensions":{"100":"user/100"}}`
	if err := os.WriteFile(path, []byte(good), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	store, err := NewRoutingStore(dir)
	if err != nil {
		t.Fatalf("initial load: %v", err)
	}
	if _, ok := store.TenantRouting("acme"); !ok {
		t.Fatal("acme should have loaded")
	}

	if err := os.WriteFile(path, []byte(`{"extensions":{"600":"assistant"}}`), 0o600); err != nil {
		t.Fatalf("write bad fixture: %v", err)
	}
	if err := store.ReloadSettings(); err == nil {
		t.Fatal("a table with retired vocabulary must fail the reload")
	}

	table, ok := store.TenantRouting("acme")
	if !ok {
		t.Fatal("the failed reload dropped the tenant; the previous tables must stay in force")
	}
	if table.Operator != "user/100" {
		t.Errorf("operator = %q, want the previously loaded user/100", table.Operator)
	}
}

// The shipped flows are configuration, and a flow that does not load is a
// deployment that cannot route the calls it promises to. This loads the real
// files rather than a fixture.
func TestShippedFlowsLoad(t *testing.T) {
	store, err := NewRoutingStore(repoResources(t, "tenants"))
	if err != nil {
		t.Fatalf("the shipped configuration must load: %v", err)
	}

	set, ok := store.TenantFlows("devtenant")
	if !ok {
		t.Fatal("devtenant should have flows")
	}
	if _, ok := set.Flow("main-ivr"); !ok {
		t.Fatalf("expected the main-ivr flow, got %v", set.Names())
	}
}

// A tenant's routing table and flows must load and validate as ONE unit. If they
// were swapped in independently there would be a window where a flow points at a
// ring group the other file just removed.
func TestRoutingAndFlowsValidateTogether(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	// The flow dials a group the routing table does not define.
	write("acme"+routingFileSuffix, `{"operator":"user/100","groups":{}}`)
	write("acme"+flowFileSuffix, `{"flows":{"main":{
		"start":"d",
		"nodes":{
			"d":{"type":"dial_user","entry":{"target":"group/claims"},
				"exits":{"no_answer":"bye","busy":"bye","rejected":"bye","unavailable":"bye"}},
			"bye":{"type":"hangup","entry":{}}
		}}}}`)

	_, err := NewRoutingStore(dir)
	if err == nil {
		t.Fatal("a flow dialing an undefined group must fail the load")
	}
	if !strings.Contains(err.Error(), "claims") {
		t.Errorf("error should name the missing group: %v", err)
	}
}

// Flows are optional: a tenant may route entirely by direct mapping.
func TestTenantWithoutFlowsLoads(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "acme"+routingFileSuffix),
		[]byte(`{"operator":"user/100","extensions":{"100":"user/100"}}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	store, err := NewRoutingStore(dir)
	if err != nil {
		t.Fatalf("a tenant with no flows must load: %v", err)
	}
	if _, ok := store.TenantFlows("acme"); ok {
		t.Error("a tenant with no flow file should report no flows")
	}
	if _, ok := store.TenantRouting("acme"); !ok {
		t.Error("its routing table should still be loaded")
	}
}

// A flow file with no routing table beside it is a misconfiguration, not a
// tenant: flows need the table to name their operator and ring groups.
func TestFlowsWithoutRoutingTableIsRejected(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "acme"+flowFileSuffix),
		[]byte(`{"flows":{}}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := NewRoutingStore(dir)
	if err == nil {
		t.Fatal("a flow file with no routing table must fail the load")
	}
	if !strings.Contains(err.Error(), routingFileSuffix) {
		t.Errorf("error should name the file that is missing: %v", err)
	}
}
