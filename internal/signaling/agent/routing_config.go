package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// The per-tenant routing table is the structured half of a tenant's config: the
// data a call is routed BY, as opposed to tenant.md, which is the judgement a
// call is handled WITH.
//
// Why it exists (design #3): routing data used to live in the tenant prompt as
// prose — a staff directory, an intent→department table, ring group members, an
// extension numbering plan — while policy.json defined a handful of symbolic
// targets. The model was re-deriving routing from a markdown table on every
// turn, inside the INVITE transaction. Moving it here lets the resolver answer
// deterministically, and makes the resolver and the model-facing symbolic
// targets read the SAME table so a name cannot mean two things.
//
// The table is data, never authority: every destination it produces still goes
// through Policy.AuthorizeDial.

// routingGroupPrefix marks a destination as a named ring group in this tenant's
// table ("group/claims"), as opposed to a concrete endpoint ("user/130").
const routingGroupPrefix = "group/"

// retiredAssistantTarget is the sentinel that used to mean "hand this call to
// the LLM supervisor". It is kept only so the loader can reject it by name.
const retiredAssistantTarget = "assistant"

// routingFileSuffix is the per-tenant routing file's extension, so "default" is
// described by default.routing.json.
const routingFileSuffix = ".routing.json"

// DefaultRetrievalPrefix is the call-retrieval dial prefix when a tenant does
// not set one. Dialing it followed by a slot number picks up a parked call:
// slots start at 700 (parking.SlotMin), so "*701" retrieves slot 701.
//
// The prefix is just the star, not "*7". The digits after it ARE the slot ID,
// which keeps the dialed string, the unpark tool's argument, and the slot's own
// identifier one value rather than three that have to agree.
const DefaultRetrievalPrefix = "*"

// DefaultMemberTimeoutMs bounds how long one ring group member is given to
// answer before a sequential strategy moves on. It is per member, not per group.
const DefaultMemberTimeoutMs = 15000

// RingStrategy is how a ring group distributes a call across its members.
type RingStrategy string

const (
	// StrategySequential tries members in configured order, each for the group's
	// per-member timeout, until one answers.
	StrategySequential RingStrategy = "sequential"
	// StrategyRoundRobin fans out from a rotating start position so the same
	// member does not take every call.
	StrategyRoundRobin RingStrategy = "round-robin"
)

// RingGroup is one named group of endpoints rung together under a strategy.
type RingGroup struct {
	// Strategy is "sequential" or "round-robin". Required.
	Strategy RingStrategy `json:"strategy"`
	// Members are concrete dial targets in ring order (e.g. "user/130").
	// Required and non-empty: a group with no members can never answer.
	Members []string `json:"members"`
	// MemberTimeoutMs bounds one member's ring. Zero inherits
	// DefaultMemberTimeoutMs.
	MemberTimeoutMs int `json:"member_timeout_ms"`
}

// RoutingTable is one tenant's structured routing data.
type RoutingTable struct {
	// Operator is the destination used as the unknown-tool fallback and as the
	// "operator" no-answer outcome. Empty means the tenant has no operator, and
	// both paths degrade to keeping the call alive rather than transferring.
	Operator string `json:"operator"`

	// RetrievalPrefix is the dial prefix for picking up a parked call ("*7").
	// Empty inherits DefaultRetrievalPrefix.
	RetrievalPrefix string `json:"retrieval_prefix"`

	// Extensions maps a dialed extension to a destination: a concrete endpoint
	// ("user/110"), a ring group ("group/claims"), or the assistant.
	Extensions map[string]string `json:"extensions"`

	// SymbolicTargets maps the names the model may dial to destinations. This is
	// the capability narrowing tool-authorization enforces; it lives here so the
	// resolver and the model resolve a name identically.
	SymbolicTargets map[string]string `json:"symbolic_targets"`

	// DIDs maps an inbound DID to a destination within this tenant. The DID→
	// tenant step happens earlier, in routes.json (basic-sip-trunk); this is the
	// DID→destination step inside the tenant.
	DIDs map[string]string `json:"dids"`

	// Groups holds this tenant's ring groups by name.
	Groups map[string]RingGroup `json:"groups"`
}

// retrievalPrefix returns the tenant's call-retrieval prefix, defaulted.
func (t *RoutingTable) retrievalPrefix() string {
	if t == nil || strings.TrimSpace(t.RetrievalPrefix) == "" {
		return DefaultRetrievalPrefix
	}
	return strings.TrimSpace(t.RetrievalPrefix)
}

// Group returns a named ring group with its defaults applied.
func (t *RoutingTable) Group(name string) (RingGroup, bool) {
	if t == nil {
		return RingGroup{}, false
	}
	g, ok := t.Groups[name]
	if !ok {
		return RingGroup{}, false
	}
	if g.MemberTimeoutMs <= 0 {
		g.MemberTimeoutMs = DefaultMemberTimeoutMs
	}
	return g, true
}

// IsGroupTarget reports whether a destination string names a ring group, and
// returns the bare group name.
func IsGroupTarget(target string) (string, bool) {
	if name, ok := strings.CutPrefix(target, routingGroupPrefix); ok && name != "" {
		return name, true
	}
	return "", false
}

// validate rejects a routing table that would fail confusingly at call time.
// The load path is the right place to be strict: a malformed group discovered
// mid-call is an unanswered phone, while a malformed group discovered at load
// is a log line an operator can act on.
func (t *RoutingTable) validate(tenant string, raw []byte) error {
	for name, g := range t.Groups {
		switch g.Strategy {
		case StrategySequential, StrategyRoundRobin:
		default:
			return fmt.Errorf("tenant %s: ring group %q has unknown strategy %q (want %q or %q)",
				tenant, name, g.Strategy, StrategySequential, StrategyRoundRobin)
		}
		if len(g.Members) == 0 {
			return fmt.Errorf("tenant %s: ring group %q has no members", tenant, name)
		}
	}

	// Configuration written for the LLM supervisor must fail loudly and by name.
	// "unknown destination" would send an operator hunting through a diff for a
	// value that used to be correct; saying what was removed, and what replaced
	// it, is the difference between a five-minute fix and an afternoon.
	if err := t.rejectRetiredVocabulary(tenant, raw); err != nil {
		return err
	}

	// A destination naming a group that does not exist is a routing dead end;
	// catch it at load rather than as a call that resolves to nothing.
	check := func(kind string, m map[string]string) error {
		for key, dest := range m {
			if name, ok := IsGroupTarget(dest); ok {
				if _, exists := t.Groups[name]; !exists {
					return fmt.Errorf("tenant %s: %s %q routes to unknown ring group %q", tenant, kind, key, name)
				}
			}
		}
		return nil
	}
	if err := check("extension", t.Extensions); err != nil {
		return err
	}
	if err := check("symbolic target", t.SymbolicTargets); err != nil {
		return err
	}
	return check("DID", t.DIDs)
}

// rejectRetiredVocabulary fails a table that still speaks the LLM supervisor's
// language. Both values below were valid configuration one release ago, so the
// error has to say what happened rather than merely that the value is unknown.
//
// It reads the raw bytes because these keys no longer exist on the structs —
// encoding/json discards unknown fields silently, which is precisely how a
// tenant would keep "working" while routing calls somewhere the operator never
// intended.
func (t *RoutingTable) rejectRetiredVocabulary(tenant string, raw []byte) error {
	for _, m := range []map[string]string{t.Extensions, t.SymbolicTargets, t.DIDs} {
		for key, dest := range m {
			if strings.TrimSpace(dest) == retiredAssistantTarget {
				return fmt.Errorf(
					"tenant %s: %q routes to %q, which is no longer a valid destination — "+
						"the LLM supervisor was removed; route it to an extension, a ring group, or a flow",
					tenant, key, retiredAssistantTarget)
			}
		}
	}

	var probe struct {
		Groups map[string]struct {
			NoAnswer string `json:"no_answer"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		// The table already parsed once; a probe failure means nothing to report.
		return nil
	}
	for name, g := range probe.Groups {
		if strings.TrimSpace(g.NoAnswer) == "" {
			continue
		}
		return fmt.Errorf(
			"tenant %s: ring group %q sets no_answer=%q, which is no longer configured on the group — "+
				"a group's fallback now belongs to whatever rang it, so put it on the dial_user node's "+
				"no_answer exit (or the tenant operator)",
			tenant, name, g.NoAnswer)
	}
	return nil
}

// RoutingSource resolves a tenant's routing table. It is the seam the resolver
// and the policy wiring depend on so tests can supply a table without touching
// disk. A tenant with no table is reported as not-found, which means nothing
// resolves deterministically for it and every call goes to the supervisor.
type RoutingSource interface {
	TenantRouting(tenant string) (*RoutingTable, bool)
}

// RoutingStore loads and caches per-tenant routing tables from the tenants
// directory. It is safe for concurrent use and is reloaded through the same
// config-API path as the prompts, so a routing edit takes effect without a
// restart.
type RoutingStore struct {
	tenantsDir string

	mu     sync.RWMutex
	tables map[string]*RoutingTable
}

// NewRoutingStore builds a store over the tenants directory and performs an
// initial load. Unlike the prompt store, a load error here is worth surfacing
// loudly: an unparseable routing file means a tenant silently loses every
// deterministic route, which looks exactly like "the AI decided to handle it".
func NewRoutingStore(tenantsDir string) (*RoutingStore, error) {
	s := &RoutingStore{
		tenantsDir: tenantsDir,
		tables:     make(map[string]*RoutingTable),
	}
	return s, s.Reload()
}

// Reload re-reads every <tenant>.routing.json from the tenants directory,
// replacing the cache atomically. A malformed or invalid file fails the reload
// and leaves the previous cache in place — a bad edit must not silently strip a
// live tenant's routing.
func (s *RoutingStore) Reload() error {
	entries, err := os.ReadDir(s.tenantsDir)
	if err != nil {
		if os.IsNotExist(err) {
			// No tenants directory at all: nothing routes deterministically.
			s.mu.Lock()
			s.tables = make(map[string]*RoutingTable)
			s.mu.Unlock()
			return nil
		}
		return fmt.Errorf("read tenants dir %s: %w", s.tenantsDir, err)
	}

	tables := make(map[string]*RoutingTable)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), routingFileSuffix) {
			continue
		}
		path := filepath.Join(s.tenantsDir, entry.Name())
		tenant := strings.TrimSuffix(entry.Name(), routingFileSuffix)

		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read routing file %s: %w", path, err)
		}
		var table RoutingTable
		if err := json.Unmarshal(data, &table); err != nil {
			return fmt.Errorf("parse routing file %s: %w", path, err)
		}
		if err := table.validate(tenant, data); err != nil {
			return fmt.Errorf("invalid routing file %s: %w", path, err)
		}
		tables[tenant] = &table
	}

	s.mu.Lock()
	s.tables = tables
	s.mu.Unlock()
	return nil
}

// ReloadSettings satisfies the file manager's reloader seam, so an edit made
// through the config API takes effect on the next call without a restart.
func (s *RoutingStore) ReloadSettings() error { return s.Reload() }

// TenantRouting implements RoutingSource.
func (s *RoutingStore) TenantRouting(tenant string) (*RoutingTable, bool) {
	if tenant == "" {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tables[tenant]
	return t, ok
}

// Tenants returns the names of every tenant with a routing table, sorted. It is
// for observability and tests.
func (s *RoutingStore) Tenants() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.tables))
	for name := range s.tables {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// StaticRouting is an in-memory RoutingSource for tests and the smoke harness.
type StaticRouting map[string]*RoutingTable

// TenantRouting implements RoutingSource over the map.
func (r StaticRouting) TenantRouting(tenant string) (*RoutingTable, bool) {
	t, ok := r[tenant]
	if !ok || t == nil {
		return nil, false
	}
	return t, true
}

// SymbolicTargetsFor returns a tenant's symbolic targets from its routing table,
// or nil when the tenant has no table. It is the single accessor the policy
// wiring uses, so "what the model may dial" and "what the resolver resolves"
// cannot come from different files.
func SymbolicTargetsFor(src RoutingSource, tenant string) map[string]string {
	if src == nil {
		return nil
	}
	table, ok := src.TenantRouting(tenant)
	if !ok {
		return nil
	}
	return table.SymbolicTargets
}
