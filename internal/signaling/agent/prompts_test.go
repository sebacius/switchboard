package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// promptTree writes a settings/tenants pair into a temp dir and returns both
// paths, so each test gets an isolated on-disk prompt tree.
func promptTree(t *testing.T, settings string, tenants map[string]string) (string, string) {
	t.Helper()
	root := t.TempDir()
	settingsDir := filepath.Join(root, "config")
	tenantsDir := filepath.Join(root, "tenants")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(tenantsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if settings != "" {
		if err := os.WriteFile(filepath.Join(settingsDir, "settings.md"), []byte(settings), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range tenants {
		if err := os.WriteFile(filepath.Join(tenantsDir, name+".md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return settingsDir, tenantsDir
}

// A loaded tenant's prompt is settings.md followed by its own file. This is the
// text that becomes the system message, so ordering matters: the global rules
// come first and the tenant's specifics follow.
func TestPromptStoreCombinesSettingsAndTenant(t *testing.T) {
	settingsDir, tenantsDir := promptTree(t, "GLOBAL RULES", map[string]string{"acme": "ACME SPECIFICS"})

	store, err := NewPromptStore(settingsDir, tenantsDir)
	if err != nil {
		t.Fatalf("NewPromptStore: %v", err)
	}

	prompt, ok := store.TenantPrompt("acme")
	if !ok {
		t.Fatal("expected acme to be loaded")
	}
	if !strings.HasPrefix(prompt, "GLOBAL RULES") {
		t.Fatalf("expected the global settings first, got %q", prompt)
	}
	if !strings.Contains(prompt, "ACME SPECIFICS") {
		t.Fatalf("expected the tenant prompt to be included, got %q", prompt)
	}
}

// The "no default tenant" rule, at the prompt layer: a tenant with no file of
// its own must NOT inherit settings.md and become admissible. If it did, any
// unrecognized subdomain would get a working generic receptionist.
func TestPromptStoreUnknownTenantNeverInheritsSettings(t *testing.T) {
	settingsDir, tenantsDir := promptTree(t, "GLOBAL RULES", map[string]string{"acme": "ACME"})

	store, err := NewPromptStore(settingsDir, tenantsDir)
	if err != nil {
		t.Fatalf("NewPromptStore: %v", err)
	}

	if _, ok := store.TenantPrompt("ghost"); ok {
		t.Fatal("an unknown tenant must not be loadable from settings.md alone")
	}
	if _, ok := store.TenantPrompt(""); ok {
		t.Fatal("an empty tenant name must never resolve")
	}
}

// An empty tenant file yields an empty combined prompt when there are no global
// settings either, which counts as "not loaded" — admission rejects rather than
// running a supervisor with no instructions at all.
func TestPromptStoreEmptyPromptIsNotLoaded(t *testing.T) {
	settingsDir, tenantsDir := promptTree(t, "", map[string]string{"blank": "   \n"})

	store, err := NewPromptStore(settingsDir, tenantsDir)
	// settings.md is absent, so Reload reports it; the store is still usable.
	if err == nil {
		t.Fatal("expected a reported error for the missing settings.md")
	}

	if _, ok := store.TenantPrompt("blank"); ok {
		t.Fatal("a tenant whose combined prompt is empty must not be admissible")
	}
}

// A prompt edit through the config API takes effect on the next call without a
// restart. This is the seam filemanager.Reload drives.
func TestPromptStoreReloadPicksUpEdits(t *testing.T) {
	settingsDir, tenantsDir := promptTree(t, "GLOBAL", map[string]string{"acme": "BEFORE"})

	store, err := NewPromptStore(settingsDir, tenantsDir)
	if err != nil {
		t.Fatalf("NewPromptStore: %v", err)
	}

	if err := os.WriteFile(filepath.Join(tenantsDir, "acme.md"), []byte("AFTER"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A new tenant added while running must become admissible too.
	if err := os.WriteFile(filepath.Join(tenantsDir, "beta.md"), []byte("BETA"), 0o644); err != nil {
		t.Fatal(err)
	}

	if prompt, _ := store.TenantPrompt("acme"); strings.Contains(prompt, "AFTER") {
		t.Fatal("the cache should not see the edit before a reload")
	}

	if err := store.ReloadSettings(); err != nil {
		t.Fatalf("ReloadSettings: %v", err)
	}

	prompt, ok := store.TenantPrompt("acme")
	if !ok || !strings.Contains(prompt, "AFTER") {
		t.Fatalf("expected the reloaded prompt, got %q (ok=%v)", prompt, ok)
	}
	if _, ok := store.TenantPrompt("beta"); !ok {
		t.Fatal("a tenant added after startup should be loaded by a reload")
	}
}

// Deleting a tenant file removes it from the admissible set: revoking a tenant
// must actually stop its calls being admitted.
func TestPromptStoreReloadDropsDeletedTenant(t *testing.T) {
	settingsDir, tenantsDir := promptTree(t, "GLOBAL", map[string]string{"acme": "ACME", "gone": "GONE"})

	store, err := NewPromptStore(settingsDir, tenantsDir)
	if err != nil {
		t.Fatalf("NewPromptStore: %v", err)
	}
	if _, ok := store.TenantPrompt("gone"); !ok {
		t.Fatal("expected the tenant to load initially")
	}

	if err := os.Remove(filepath.Join(tenantsDir, "gone.md")); err != nil {
		t.Fatal(err)
	}
	if err := store.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	if _, ok := store.TenantPrompt("gone"); ok {
		t.Fatal("a deleted tenant must stop being admissible after a reload")
	}
	if _, ok := store.TenantPrompt("acme"); !ok {
		t.Fatal("the surviving tenant should still be loaded")
	}
}

// A missing tenants directory is reported but leaves a usable store in the
// safest posture: nothing is admissible, so no call is supervised without
// instructions.
func TestPromptStoreMissingDirectoryAdmitsNothing(t *testing.T) {
	store, err := NewPromptStore(filepath.Join(t.TempDir(), "nope"), filepath.Join(t.TempDir(), "nope"))
	if err == nil {
		t.Fatal("expected an error for the missing directories")
	}
	if store == nil {
		t.Fatal("the store must still be usable after a failed load")
	}
	if _, ok := store.TenantPrompt("anything"); ok {
		t.Fatal("nothing should be admissible when no prompts loaded")
	}
	if len(store.Tenants()) != 0 {
		t.Fatalf("expected no tenants, got %v", store.Tenants())
	}
}

// --- Policy config ---

// A missing policy file is the safe default, not an error: a fresh deployment
// must not start out permissive, but it must start.
func TestLoadPolicyConfigMissingFileIsSafeDefault(t *testing.T) {
	cfg, err := LoadPolicyConfig(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("a missing policy file must not be an error: %v", err)
	}
	if cfg.DefaultChannelLimit != DefaultChannelLimitFallback {
		t.Fatalf("expected the fallback channel limit, got %d", cfg.DefaultChannelLimit)
	}

	policy := cfg.TenantPolicyFor("anyone", nil)
	if policy.AllowExternalDial {
		t.Fatal("an unconfigured tenant must not be able to dial externally")
	}
	if policy.MaxExternalUnitsPerDay != 0 {
		t.Fatal("an unconfigured tenant must have no external spend budget")
	}
}

// Malformed JSON IS an error: silently falling back to defaults would turn a
// typo in an allowlist into an unnoticed policy change.
func TestLoadPolicyConfigMalformedIsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPolicyConfig(path); err == nil {
		t.Fatal("malformed policy JSON must be a hard error")
	}
}

func TestLoadPolicyConfigParsesTenants(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	body := `{
	  "default_channel_limit": 4,
	  "tenants": {
	    "acme": {
	      "channel_limit": 9,
	      "allow_external_dial": true,
	      "external_allowlist": ["+1800"],
	      "max_external_units_per_day": 25
	    },
	    "beta": {}
	  }
	}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadPolicyConfig(path)
	if err != nil {
		t.Fatalf("LoadPolicyConfig: %v", err)
	}
	if cfg.DefaultChannelLimit != 4 {
		t.Fatalf("expected default limit 4, got %d", cfg.DefaultChannelLimit)
	}

	limits := cfg.ChannelLimits()
	if limits["acme"] != 9 {
		t.Fatalf("expected acme override 9, got %d", limits["acme"])
	}
	if _, ok := limits["beta"]; ok {
		t.Fatal("a tenant with no positive override must inherit the default, not appear in overrides")
	}

	acme := cfg.TenantPolicyFor("acme", map[string]string{"sales": "user/160"})
	if !acme.AllowExternalDial || acme.MaxExternalUnitsPerDay != 25 {
		t.Fatalf("acme policy not parsed: %+v", acme)
	}
	// Symbolic targets come from the tenant's ROUTING table now, not this file.
	if acme.SymbolicTargets["sales"] != "user/160" {
		t.Fatalf("expected the caller-supplied symbolic targets, got %v", acme.SymbolicTargets)
	}
	if cfg.TenantPolicyFor("beta", nil).AllowExternalDial {
		t.Fatal("a tenant with an empty config must stay default-deny")
	}
}

// A policy.json that still carries symbolic_targets must not start. Two sources
// for one name is the drift moving them was meant to end, and a silent merge
// would let a stale entry here quietly outrank the routing file forever.
func TestLoadPolicyConfigRejectsLeftoverSymbolicTargets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	body := `{"tenants": {"acme": {"symbolic_targets": {"sales": "user/160"}}}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadPolicyConfig(path)
	if err == nil {
		t.Fatal("a leftover symbolic_targets key must be a hard startup error")
	}
	if !strings.Contains(err.Error(), "routing file") {
		t.Fatalf("the error must tell the operator where the entries belong, got: %v", err)
	}
}

// An EMPTY symbolic_targets object is just as stale as a populated one: the
// pointer field exists precisely so "present but empty" is still detected.
func TestLoadPolicyConfigRejectsEmptySymbolicTargets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(`{"tenants": {"acme": {"symbolic_targets": {}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPolicyConfig(path); err == nil {
		t.Fatal("an empty symbolic_targets key must still be a hard startup error")
	}
}
