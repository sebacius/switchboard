package agent

import (
	"path/filepath"
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
			t.Errorf("tenant %s offers the model no dialable names", tenant)
		}
	}
}

// policy.json must no longer carry symbolic targets, and must still parse.
func TestShippedPolicyConfigLoads(t *testing.T) {
	cfg, err := LoadPolicyConfig(repoResources(t, "config", "policy.json"))
	if err != nil {
		t.Fatalf("the shipped policy config must load: %v", err)
	}
	if cfg.DefaultChannelLimit <= 0 {
		t.Fatalf("expected a positive default channel limit, got %d", cfg.DefaultChannelLimit)
	}
}

// Every symbolic target the model may dial must resolve to something real: an
// endpoint, or a group that exists. A dangling name looks to the model like a
// working destination and to the caller like silence.
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
