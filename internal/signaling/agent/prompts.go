package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// The prompt store is the concrete PromptSource behind admission's preflight
// (design #9) and the runner's per-call system message. A tenant's prompt is
// the global settings.md followed by that tenant's tenants/<name>.md.
//
// Preflight semantics matter: "tenant not loaded" is a hard reject with no LLM
// round-trip and no default tenant, so TenantPrompt returns ok=false whenever
// the tenant file is missing or its combined prompt is empty. A tenant that
// exists only as a DID-table entry, with no prompt on disk, is NOT admissible.
//
// The prompt is treated as SEMI-PUBLIC (design #10): a caller may be able to
// coax it out of the model, so tenant.md must never hold secrets.

// PromptStore loads and caches tenant prompts from disk. It is safe for
// concurrent use: calls read the cache under an RWMutex while the file manager
// may reload it after an edit through the config API.
type PromptStore struct {
	settingsDir string
	tenantsDir  string

	mu       sync.RWMutex
	settings string            // cached settings.md content
	tenants  map[string]string // tenant name → cached tenant.md content
}

// NewPromptStore builds a store over the settings and tenants directories and
// performs an initial load. A load error is returned but the store stays usable
// (it simply has nothing cached, so every tenant fails preflight) — that is the
// safe posture: no prompts means no admitted calls, not unsupervised calls.
func NewPromptStore(settingsDir, tenantsDir string) (*PromptStore, error) {
	s := &PromptStore{
		settingsDir: settingsDir,
		tenantsDir:  tenantsDir,
		tenants:     make(map[string]string),
	}
	return s, s.Reload()
}

// Reload re-reads settings.md and every tenant .md from disk, replacing the
// cache atomically. It is the hook the file manager calls after a config-API
// edit so a prompt change takes effect without a restart. A call in flight
// keeps the prompt it was admitted with (the runner copies it per call).
func (s *PromptStore) Reload() error {
	settings, settingsErr := os.ReadFile(filepath.Join(s.settingsDir, "settings.md"))

	tenants := make(map[string]string)
	entries, dirErr := os.ReadDir(s.tenantsDir)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.tenantsDir, entry.Name()))
		if err != nil {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".md")
		tenants[name] = strings.TrimSpace(string(data))
	}

	s.mu.Lock()
	s.settings = strings.TrimSpace(string(settings))
	s.tenants = tenants
	s.mu.Unlock()

	// Report the first real problem, but only after the cache is installed.
	if dirErr != nil {
		return fmt.Errorf("read tenants dir %s: %w", s.tenantsDir, dirErr)
	}
	if settingsErr != nil {
		return fmt.Errorf("read settings.md in %s: %w", s.settingsDir, settingsErr)
	}
	return nil
}

// ReloadSettings satisfies the file manager's reloader seam. It is a full
// reload: an edit to settings.md and an edit to a tenant file arrive through the
// same API, and reloading everything keeps the two in step.
func (s *PromptStore) ReloadSettings() error { return s.Reload() }

// TenantPrompt returns the combined system prompt for a tenant and whether the
// tenant is loaded. It implements PromptSource. A tenant is loaded only when it
// has its own file AND the combined prompt is non-empty — settings.md alone is
// never enough, because that would let an unknown tenant inherit the global
// prompt and be admitted (exactly the "no default tenant" rule).
func (s *PromptStore) TenantPrompt(tenant string) (string, bool) {
	if tenant == "" {
		return "", false
	}

	s.mu.RLock()
	settings := s.settings
	tenantPrompt, known := s.tenants[tenant]
	s.mu.RUnlock()

	if !known {
		return "", false
	}

	var parts []string
	if settings != "" {
		parts = append(parts, settings)
	}
	if tenantPrompt != "" {
		parts = append(parts, tenantPrompt)
	}

	combined := strings.Join(parts, "\n\n")
	if combined == "" {
		return "", false
	}
	return combined, true
}

// Tenants returns the names of every loaded tenant. It is for observability and
// tests; the order is unspecified.
func (s *PromptStore) Tenants() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.tenants))
	for name := range s.tenants {
		names = append(names, name)
	}
	return names
}

// StaticPrompts is an in-memory PromptSource for tests and the smoke harness,
// where reading a real settings/tenants tree would be noise.
type StaticPrompts map[string]string

// TenantPrompt implements PromptSource over the map: present and non-empty is
// loaded, anything else is not.
func (p StaticPrompts) TenantPrompt(tenant string) (string, bool) {
	prompt, ok := p[tenant]
	if !ok || strings.TrimSpace(prompt) == "" {
		return "", false
	}
	return prompt, true
}
