package dialplan

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

// routingFlowPrefix marks a destination as entering a flow graph
// ("flow/main-ivr"), as opposed to dialing something directly.
const routingFlowPrefix = "flow/"

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

	// Extensions maps dialed digits to a destination: a concrete endpoint
	// ("user/110"), a ring group ("group/claims"), or a flow ("flow/main-ivr").
	//
	// Keys may be digit-map patterns as well as literals, because extension
	// ranges and number plans cannot be enumerated. The most specific match wins,
	// computed rather than declared — see digitmap.go.
	Extensions map[string]Entry `json:"extensions"`

	// SymbolicTargets maps dialable names to destinations. This is the
	// capability narrowing tool-authorization enforces; it lives here so every
	// path resolves a name identically.
	SymbolicTargets map[string]string `json:"symbolic_targets"`

	// DIDs maps an inbound DID to a destination within this tenant. The DID→
	// tenant step happens earlier, in routes.json (basic-sip-trunk); this is the
	// DID→destination step inside the tenant.
	DIDs map[string]Entry `json:"dids"`

	// Groups holds this tenant's ring groups by name.
	Groups map[string]RingGroup `json:"groups"`

	// extensionMap and didMap are the compiled digit maps, built at load so a
	// call costs a match rather than a compile. compileOnce lets a table built
	// in code — a test fixture, say — match correctly without a load path.
	compileOnce  sync.Once
	extensionMap *DigitMap
	didMap       *DIDMap
}

// ensureCompiled builds the digit maps if the table was constructed directly
// rather than loaded. A compile error here cannot be returned, but it also
// cannot be new: the load path rejects it first, and a hand-built table with a
// bad pattern simply matches nothing.
func (t *RoutingTable) ensureCompiled() {
	t.compileOnce.Do(func() {
		if t.extensionMap == nil {
			t.extensionMap, _ = CompileDigitMap(t.Extensions)
		}
		if t.didMap == nil {
			t.didMap, _ = CompileDIDMap(t.DIDs)
		}
	})
}

// MatchExtension resolves dialed digits against the tenant's entry mapping,
// returning the most specific match.
func (t *RoutingTable) MatchExtension(dialed string) (string, bool) {
	if t == nil {
		return "", false
	}
	t.ensureCompiled()
	return t.extensionMap.Lookup(dialed)
}

// MatchExtensionWithDigits also returns the dialled digits after the matching
// entry's transform.
func (t *RoutingTable) MatchExtensionWithDigits(dialed string) (string, string, bool) {
	if t == nil {
		return "", "", false
	}
	t.ensureCompiled()
	return t.extensionMap.LookupWithDigits(dialed)
}

// MatchDID resolves an inbound DID against the tenant's DID mapping.
//
// The '+'-tolerance and pattern support live in DIDMap, which the trunk-level
// DID -> tenant table uses too, so the two lookups a DID passes through cannot
// disagree about whether a number matches.
func (t *RoutingTable) MatchDID(dialed string) (string, bool) {
	if t == nil {
		return "", false
	}
	t.ensureCompiled()

	dest, _, ok := t.MatchDIDWithDigits(dialed)
	return dest, ok
}

// MatchDIDWithDigits also returns the dialled digits after the transform.
func (t *RoutingTable) MatchDIDWithDigits(dialed string) (string, string, bool) {
	if t == nil {
		return "", "", false
	}
	t.ensureCompiled()
	return t.didMap.LookupWithDigits(dialed)
}

// togglePlus returns the other E.164 form of a number: "+1555..." <-> "1555...".
func togglePlus(dialed string) (string, bool) {
	dialed = strings.TrimSpace(dialed)
	if dialed == "" {
		return "", false
	}
	if strings.HasPrefix(dialed, "+") {
		return strings.TrimPrefix(dialed, "+"), true
	}
	return "+" + dialed, true
}

// compile builds the digit maps. Ambiguity is rejected here, at load, where an
// operator can act on it.
func (t *RoutingTable) compile(tenant string) error {
	extensions, err := CompileDigitMap(t.Extensions)
	if err != nil {
		return fmt.Errorf("tenant %s: extensions: %w", tenant, err)
	}
	dids, err := CompileDIDMap(t.DIDs)
	if err != nil {
		return fmt.Errorf("tenant %s: dids: %w", tenant, err)
	}
	t.compileOnce.Do(func() {
		t.extensionMap, t.didMap = extensions, dids
	})
	return nil
}

// RetrievalPrefixOrDefault returns the tenant's call-retrieval prefix, defaulted.
func (t *RoutingTable) RetrievalPrefixOrDefault() string {
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

// IsFlowTarget reports whether a destination enters a flow, and if so its name.
// A bare destination such as "user/110" stays valid as sugar for a one-node
// dial, so simple configurations never have to write a graph.
func IsFlowTarget(target string) (string, bool) {
	if !strings.HasPrefix(target, routingFlowPrefix) {
		return "", false
	}
	name := strings.TrimSpace(strings.TrimPrefix(target, routingFlowPrefix))
	return name, name != ""
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

	// Patterns compile — and ambiguity is rejected — at load.
	if err := t.compile(tenant); err != nil {
		return err
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
	check := func(kind string, m map[string]Entry) error {
		for key, entry := range m {
			if name, ok := IsGroupTarget(entry.Destination); ok {
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
	if err := checkSymbolic("symbolic target", t.SymbolicTargets, t.Groups, tenant); err != nil {
		return err
	}
	return check("DID", t.DIDs)
}

// checkSymbolic is the same check over the plain string map of symbolic
// targets, which take no transform.
func checkSymbolic(kind string, m map[string]string, groups map[string]RingGroup, tenant string) error {
	for key, dest := range m {
		if name, ok := IsGroupTarget(dest); ok {
			if _, exists := groups[name]; !exists {
				return fmt.Errorf("tenant %s: %s %q routes to unknown ring group %q", tenant, kind, key, name)
			}
		}
	}
	return nil
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
	entryMaps := []map[string]Entry{t.Extensions, t.DIDs}
	for _, m := range entryMaps {
		for key, entry := range m {
			if strings.TrimSpace(entry.Destination) == retiredAssistantTarget {
				return fmt.Errorf(
					"tenant %s: %q routes to %q, which is no longer a valid destination — "+
						"the LLM supervisor was removed; route it to an extension, a ring group, or a flow",
					tenant, key, retiredAssistantTarget)
			}
		}
	}
	for _, m := range []map[string]string{t.SymbolicTargets} {
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
// resolves for it at all.
type RoutingSource interface {
	TenantRouting(tenant string) (*RoutingTable, bool)
}

// FlowSource resolves a tenant's flows. Separate from RoutingSource so a caller
// that only routes need not depend on the flow vocabulary.
type FlowSource interface {
	TenantFlows(tenant string) (*FlowSet, bool)
}

// RoutingStore loads and caches per-tenant routing tables and flows from the
// tenants directory. It is safe for concurrent use and is reloaded through the
// config API, so an edit takes effect without a restart.
//
// A tenant's two files are loaded and validated as ONE unit. That is what makes
// cross-file validation meaningful: a flow may reference a ring group, so the
// two must never be swapped in independently or there is a window where a flow
// points at a group the other file just deleted.
type RoutingStore struct {
	tenantsDir string

	mu     sync.RWMutex
	tables map[string]*RoutingTable
	flows  map[string]*FlowSet
}

// NewRoutingStore builds a store over the tenants directory and performs an
// initial load. A load error is worth surfacing loudly: an unparseable routing
// file means a tenant has nowhere to send calls, and answering a call we cannot
// route is worse than not starting.
func NewRoutingStore(tenantsDir string) (*RoutingStore, error) {
	s := &RoutingStore{
		tenantsDir: tenantsDir,
		tables:     make(map[string]*RoutingTable),
		flows:      make(map[string]*FlowSet),
	}
	return s, s.Reload()
}

// Reload re-reads every tenant's configuration, replacing the cache atomically.
// A malformed or invalid file fails the reload and leaves the previous cache in
// force — a bad edit must never strip a live tenant's routing, which is the most
// valuable property this store has.
func (s *RoutingStore) Reload() error {
	entries, err := os.ReadDir(s.tenantsDir)
	if err != nil {
		if os.IsNotExist(err) {
			// No tenants directory at all: nothing routes.
			s.mu.Lock()
			s.tables = make(map[string]*RoutingTable)
			s.flows = make(map[string]*FlowSet)
			s.mu.Unlock()
			return nil
		}
		return fmt.Errorf("read tenants dir %s: %w", s.tenantsDir, err)
	}

	// Collect the tenants named by either file first, so a tenant with flows and
	// no routing table is a validation error rather than a silently ignored file.
	tenants := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		switch {
		case strings.HasSuffix(entry.Name(), routingFileSuffix):
			tenants[strings.TrimSuffix(entry.Name(), routingFileSuffix)] = true
		case strings.HasSuffix(entry.Name(), flowFileSuffix):
			tenants[strings.TrimSuffix(entry.Name(), flowFileSuffix)] = true
		}
	}

	tables := make(map[string]*RoutingTable)
	flows := make(map[string]*FlowSet)
	for tenant := range tenants {
		table, flowSet, err := s.loadTenant(tenant)
		if err != nil {
			return err
		}
		tables[tenant] = table
		if flowSet != nil {
			flows[tenant] = flowSet
		}
	}

	s.mu.Lock()
	s.tables = tables
	s.flows = flows
	s.mu.Unlock()
	return nil
}

// ValidateTable runs a routing table's own checks. Exported so the config API
// validates a proposed edit with the loader's rules rather than a copy.
func ValidateTable(tenant string, t *RoutingTable, raw []byte) error {
	return t.validate(tenant, raw)
}

// LoadTenant reads and validates one tenant's configuration from a directory.
// Exported so the validate subcommand runs exactly the checks the loader does,
// rather than a second implementation that can drift from it.
func LoadTenant(dir, tenant string) (*RoutingTable, *FlowSet, error) {
	s := &RoutingStore{tenantsDir: dir}
	return s.loadTenant(tenant)
}

// ParseTenant reads a tenant's files WITHOUT running the graph checks.
//
// It exists for the validator, which reports every problem rather than the
// first: the loader is fail-closed by design and collapses a flow's problems
// into a single error, which is right for a server with a call to route and
// wrong for an operator fixing a thirty-node graph.
func ParseTenant(dir, tenant string) (*RoutingTable, *FlowSet, error) {
	routingPath := filepath.Join(dir, tenant+routingFileSuffix)
	flowPath := filepath.Join(dir, tenant+flowFileSuffix)

	data, err := os.ReadFile(routingPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, fmt.Errorf(
				"tenant %s has %s but no %s: flows need a routing table to name their operator and ring groups",
				tenant, tenant+flowFileSuffix, tenant+routingFileSuffix)
		}
		return nil, nil, fmt.Errorf("read routing file %s: %w", routingPath, err)
	}
	var table RoutingTable
	if err := json.Unmarshal(data, &table); err != nil {
		return nil, nil, fmt.Errorf("parse routing file %s: %w", routingPath, err)
	}
	// Table-level checks still fail fast: a malformed routing table makes every
	// flow problem beneath it meaningless.
	if err := table.validate(tenant, data); err != nil {
		return nil, nil, err
	}

	flowData, err := os.ReadFile(flowPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &table, nil, nil
		}
		return nil, nil, fmt.Errorf("read flow file %s: %w", flowPath, err)
	}
	var set FlowSet
	if err := json.Unmarshal(flowData, &set); err != nil {
		return nil, nil, fmt.Errorf("parse flow file %s: %w", flowPath, err)
	}
	return &table, &set, nil
}

// loadTenant reads and validates one tenant's routing table and flows together.
// Both files are parsed before either is validated, so validation can see across
// them — a flow dialing a ring group is checked against the table that defines
// it, in the same pass.
func (s *RoutingStore) loadTenant(tenant string) (*RoutingTable, *FlowSet, error) {
	routingPath := filepath.Join(s.tenantsDir, tenant+routingFileSuffix)
	flowPath := filepath.Join(s.tenantsDir, tenant+flowFileSuffix)

	data, err := os.ReadFile(routingPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, fmt.Errorf(
				"tenant %s has %s but no %s: flows need a routing table to name their operator and ring groups",
				tenant, tenant+flowFileSuffix, tenant+routingFileSuffix)
		}
		return nil, nil, fmt.Errorf("read routing file %s: %w", routingPath, err)
	}
	var table RoutingTable
	if err := json.Unmarshal(data, &table); err != nil {
		return nil, nil, fmt.Errorf("parse routing file %s: %w", routingPath, err)
	}
	if err := table.validate(tenant, data); err != nil {
		return nil, nil, fmt.Errorf("invalid routing file %s: %w", routingPath, err)
	}

	flowData, err := os.ReadFile(flowPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Flows are optional: a tenant may route entirely by direct mapping.
			return &table, nil, nil
		}
		return nil, nil, fmt.Errorf("read flow file %s: %w", flowPath, err)
	}
	var set FlowSet
	if err := json.Unmarshal(flowData, &set); err != nil {
		return nil, nil, fmt.Errorf("parse flow file %s: %w", flowPath, err)
	}
	if err := ValidateFlows(tenant, &table, &set); err != nil {
		return nil, nil, fmt.Errorf("invalid flow file %s: %w", flowPath, err)
	}

	return &table, &set, nil
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

// TenantFlows returns a tenant's flows, if it has any.
func (s *RoutingStore) TenantFlows(tenant string) (*FlowSet, bool) {
	if tenant == "" {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	set, ok := s.flows[tenant]
	return set, ok && set != nil
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
