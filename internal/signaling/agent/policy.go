package agent

import (
	"log/slog"
	"strings"
	"sync"

	"github.com/sebas/switchboard/internal/signaling/dialplan"
)

// CONFIG IS NOT AUTHORITY. Everything in this file is the deterministic
// authorization boundary that adjudicates a destination before it is dialed.
//
// The untrusted input used to be a language model's tool call; it is now a
// configuration file — an entry mapping, a flow node, or a ring group member.
// The principle is unchanged: the only inputs to a verdict are the tenant's
// policy and the symbolic target being asked for. Anyone able to edit a routing
// or flow file still cannot grant themselves reach this file does not already
// permit.

// Decision is the verdict of an authorization check: allow or deny, plus a
// machine-stable reason that is logged and, on deny, carried into the call
// record so a refused destination is auditable.
type Decision struct {
	Allowed bool
	// Reason is a short, stable explanation (e.g. "external dial not enabled for
	// tenant"). On a deny it doubles as the conversation-visible result string.
	Reason string
}

// allow / deny are small constructors keeping the engine readable.
func allow(reason string) Decision { return Decision{Allowed: true, Reason: reason} }
func deny(reason string) Decision  { return Decision{Allowed: false, Reason: reason} }

// TenantPolicy is the per-tenant, per-direction Class of Service config. The
// zero value is the safest possible posture: external dialing disabled, no
// allowlist, the default barred classes applied, no symbolic targets, and a
// zero spend limit (which trips immediately on the first external unit). A
// caller must opt in to every capability explicitly.
type TenantPolicy struct {
	// AllowExternalDial gates external destinations entirely. Default false:
	// external dial is default-deny (spec: "External dial denied by default").
	AllowExternalDial bool

	// ExternalAllowlist is the set of permitted external destinations. An entry
	// is matched as a prefix against the resolved target's digits (so "1800" or
	// a full E.164 number both work). Only consulted when AllowExternalDial is
	// true. Empty allowlist with external enabled means "no destination matches"
	// → every external dial is denied (deny-by-omission, not allow-all).
	ExternalAllowlist []string

	// BarredPrefixes are classes always denied even when external dial is enabled
	// and the destination is otherwise allowlisted (premium-rate, satellite,
	// high-risk prefixes). When nil, DefaultBarredPrefixes is applied; pass an
	// explicit empty non-nil slice to bar nothing.
	BarredPrefixes []string

	// SymbolicTargets maps symbolic names (extension names, named forwards) to
	// concrete dial targets. This is capability narrowing: configuration
	// dials "sales" or "front-desk", never a raw external number. A resolved
	// target may itself be internal ("user/1001") or external ("+18005551212"),
	// and external resolutions are still subject to the full COS below.
	SymbolicTargets map[string]string

	// MaxExternalUnitsPerDay is the per-tenant spend circuit breaker: the number
	// of external "units" (calls today; minutes/cost later) permitted before the
	// breaker trips. Zero means no external spend is allowed at all (the safe
	// default); set it explicitly to permit external dialing volume.
	MaxExternalUnitsPerDay int

	// AllowCallerProvidedNumber gates AuthorizeCallerProvidedDial, the one entry
	// point that adjudicates a raw caller-supplied number instead of a symbolic
	// name. Default false, and nothing in the call path reaches it today: no
	// flow node can express a raw number. It survives because the narrowing it
	// enforces is what makes that guarantee checkable.
	AllowCallerProvidedNumber bool
}

// DefaultBarredPrefixes is a sane default set of always-denied classes. These
// are applied when TenantPolicy.BarredPrefixes is nil. They cover common toll-
// fraud vectors: premium-rate (1-900 / 0900 / UK 09), international premium and
// satellite ranges (+882/+883 networks, Inmarsat +870), and well-known high-risk
// country codes used in IRSF (international revenue share fraud) attacks.
var DefaultBarredPrefixes = []string{
	"1900",   // North American premium-rate
	"+1900",  // E.164 form
	"0900",   // common European premium-rate
	"09",     // UK / generic premium-rate
	"+882",   // international networks (premium)
	"+883",   // international networks (premium)
	"+870",   // Inmarsat satellite
	"+888",   // disaster-relief / often abused
	"+247",   // Ascension Island (IRSF high-risk)
	"+252",   // Somalia (IRSF high-risk)
	"+88216", // EMSAT satellite
}

// Policy is the per-tenant authorization engine. It is constructed per call and
// is safe for concurrent use. It NEVER inspects prompt content.
//
// The spend counter deliberately does NOT live here. A Policy is built per call,
// so a counter on it resets on every INVITE and a "per day" limit could never be
// reached — the shipped code had exactly that bug. The counter lives in a
// SpendLedger shared for the life of the process.
type Policy struct {
	tenant string
	cfg    TenantPolicy
	log    *slog.Logger
	spend  *SpendLedger

	// decisions accumulates this call's verdicts so they can be attached to its
	// record. A Policy is built per call, so this is exactly one call's worth.
	decisionsMu sync.Mutex
	decisions   []RecordedDecision

	barred []string // resolved barred prefixes (cfg or defaults)
}

// NewPolicy builds a Policy for one tenant with no shared spend ledger. Every
// authorization still runs, but the spend breaker cannot accumulate across
// calls; use NewPolicyWithLedger in anything that places real calls.
func NewPolicy(tenant string, cfg TenantPolicy, log *slog.Logger) *Policy {
	return NewPolicyWithLedger(tenant, cfg, nil, log)
}

// NewPolicyWithLedger builds a Policy that reports its external spend to a
// ledger shared across calls. A nil logger falls back to slog.Default. The
// barred set is the tenant's explicit list, or the defaults when the tenant left
// it nil.
func NewPolicyWithLedger(tenant string, cfg TenantPolicy, spend *SpendLedger, log *slog.Logger) *Policy {
	if log == nil {
		log = slog.Default()
	}
	barred := cfg.BarredPrefixes
	if barred == nil {
		barred = DefaultBarredPrefixes
	}
	return &Policy{
		tenant: tenant,
		cfg:    cfg,
		spend:  spend,
		log:    log.With("tenant", tenant, "component", "tool-policy"),
		barred: barred,
	}
}

// AuthorizeDial adjudicates a dial whose target is a SYMBOLIC name — the only
// kind a flow can express. It resolves the symbol deterministically
// and runs the resolved target through the full COS. It returns the resolved
// concrete target (empty on deny) and the Decision. Configuration can never express
// a raw external number here — an unrecognized symbol that is not an internal
// "user/..." target is denied, which is what makes capability narrowing real.
func (p *Policy) AuthorizeDial(symbolicTarget string) (resolvedTarget string, d Decision) {
	target := strings.TrimSpace(symbolicTarget)
	if target == "" {
		d = deny("dial target is empty")
		p.logDecision("dial", symbolicTarget, "", d)
		return "", d
	}

	// Capability narrowing: a symbolic name resolves to its configured concrete
	// target. Internal "user/..." targets are also allowed through directly
	// (they cannot reach an external trunk). Anything else the model emits is
	// rejected before it can become an external number.
	resolved, ok := p.resolveSymbol(target)
	if !ok {
		d = deny("unknown dial target: only configured extensions and named forwards are dialable")
		p.logDecision("dial", symbolicTarget, "", d)
		return "", d
	}

	d = p.authorizeResolved(resolved)
	p.logDecision("dial", symbolicTarget, resolved, d)
	if !d.Allowed {
		return "", d
	}
	return resolved, d
}

// AuthorizeCallerProvidedDial adjudicates the separate, hard-gated tool that
// dials a raw caller-provided number (no symbolic narrowing). It is denied
// outright unless the tenant explicitly enabled it, and is otherwise subject to
// the identical COS as a resolved external target.
func (p *Policy) AuthorizeCallerProvidedDial(rawNumber string) (resolvedTarget string, d Decision) {
	number := strings.TrimSpace(rawNumber)
	if number == "" {
		d = deny("dial target is empty")
		p.logDecision("dial_caller_number", rawNumber, "", d)
		return "", d
	}
	if !p.cfg.AllowCallerProvidedNumber {
		d = deny("dialing a caller-provided number is not permitted for this tenant")
		p.logDecision("dial_caller_number", rawNumber, "", d)
		return "", d
	}
	d = p.authorizeResolved(number)
	p.logDecision("dial_caller_number", rawNumber, number, d)
	if !d.Allowed {
		return "", d
	}
	return number, d
}

// resolveSymbol maps a symbolic name to a concrete target. Internal "user/..."
// targets pass through unchanged (they never reach a trunk). A name in
// SymbolicTargets resolves to its configured target. Everything else fails
// resolution, so a raw external number can never enter by this route.
func (p *Policy) resolveSymbol(symbol string) (string, bool) {
	if strings.HasPrefix(symbol, "user/") {
		return symbol, true
	}
	if resolved, ok := p.cfg.SymbolicTargets[symbol]; ok {
		return strings.TrimSpace(resolved), true
	}
	return "", false
}

// AuthorizeTarget adjudicates a concrete, already-resolved target — one the
// deterministic resolver produced from the tenant's routing table, or one ring
// group member about to be dialed. It is the same adjudication a model-issued
// dial gets after symbol resolution, which is what keeps the resolver a
// performance optimization rather than a second trust path.
func (p *Policy) AuthorizeTarget(tool, resolved string) Decision {
	target := strings.TrimSpace(resolved)
	if target == "" {
		d := deny("dial target is empty")
		p.logDecision(tool, resolved, "", d)
		return d
	}
	d := p.authorizeResolved(target)
	p.logDecision(tool, resolved, target, d)
	return d
}

// Classify applies Class of Service to a concrete, already-resolved target and
// returns the verdict WITHOUT consuming spend. Internal targets are always
// permitted; external targets run default-deny → barred-class → allowlist.
//
// The split from Consume is what makes load-time validation possible. Checking
// every external destination in a tenant's configuration must not spend that
// tenant's daily budget — a validator that denial-of-services the thing it
// validates is worse than no validator.
func (p *Policy) Classify(resolved string) Decision {
	if isInternalTarget(resolved) {
		return allow("internal target")
	}

	// External from here on.
	if !p.cfg.AllowExternalDial {
		return deny("external dial not enabled for tenant")
	}

	digits := normalizeDigits(resolved)

	// Barred classes are denied even when external is enabled and even if an
	// allowlist entry would otherwise match — the bar is unconditional.
	if prefix, barred := matchPrefix(digits, resolved, p.barred); barred {
		return deny("destination is in a barred class (prefix " + prefix + ")")
	}

	// Allowlist gating: with external enabled, only allowlisted destinations pass.
	if _, ok := matchPrefix(digits, resolved, p.cfg.ExternalAllowlist); !ok {
		return deny("destination not in tenant allowlist")
	}

	return allow("external destination authorized")
}

// Consume charges one unit of external spend, returning false when the breaker
// has tripped. Only an authorized EXTERNAL dial consumes; internal targets cost
// nothing.
func (p *Policy) Consume(resolved string) bool {
	if isInternalTarget(resolved) {
		return true
	}
	if p.spend == nil {
		// No ledger: nothing accumulates, so nothing can trip.
		return true
	}
	return p.spend.Consume(p.tenant, p.cfg.MaxExternalUnitsPerDay)
}

// authorizeResolved is Classify followed by Consume: the full check an actual
// dial must pass.
func (p *Policy) authorizeResolved(resolved string) Decision {
	if d := p.Classify(resolved); !d.Allowed {
		return d
	}
	if !p.Consume(resolved) {
		return deny("tenant external spend limit reached")
	}
	return allow("external destination authorized")
}

// RecordedDecision is one authorization verdict, for the call record.
type RecordedDecision struct {
	Target  string
	Allowed bool
	Reason  string
}

// recordDecision keeps a verdict for the call record.
func (p *Policy) recordDecision(resolved string, d Decision) {
	p.decisionsMu.Lock()
	defer p.decisionsMu.Unlock()
	p.decisions = append(p.decisions, RecordedDecision{
		Target: resolved, Allowed: d.Allowed, Reason: d.Reason,
	})
}

// Decisions returns the verdicts made for this call, so they can be written
// into its record alongside the traversal — the path and why it was allowed to
// go that way, readable together.
func (p *Policy) Decisions() []RecordedDecision {
	p.decisionsMu.Lock()
	defer p.decisionsMu.Unlock()
	return append([]RecordedDecision(nil), p.decisions...)
}

// isInternalTarget reports whether a resolved target is an internal directory
// reference that cannot reach an external trunk.
//
// A "group/..." target is internal for the same reason a "user/..." one is: a
// ring group is a named set of endpoints from the tenant's routing table, and
// the loader rejects a table whose group does not exist. It still reaches no
// trunk by itself — the ring engine authorizes every member through
// AuthorizeTarget before dialing it, so a member that IS external is adjudicated
// on its own merits rather than inheriting the group's verdict.
func isInternalTarget(target string) bool {
	if _, isGroup := dialplan.IsGroupTarget(target); isGroup {
		return true
	}
	return strings.HasPrefix(target, "user/")
}

// normalizeDigits strips formatting from a dial string so prefix matching is
// stable across "+1 (800) 555-1212" and "+18005551212". The leading '+' is
// preserved because barred/allowlist entries distinguish E.164 form.
func normalizeDigits(target string) string {
	var b strings.Builder
	for i, r := range target {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' && i == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// matchPrefix reports whether the target matches any entry in patterns as a
// prefix. It tests both the normalized digits and the raw target so entries can
// be written in either form. Returns the matching pattern for logging.
func matchPrefix(digits, raw string, patterns []string) (string, bool) {
	for _, pat := range patterns {
		pat = strings.TrimSpace(pat)
		if pat == "" {
			continue
		}
		if strings.HasPrefix(digits, pat) || strings.HasPrefix(raw, pat) {
			return pat, true
		}
	}
	return "", false
}

// logDecision emits the authorization verdict for audit. Every verdict — allow
// and deny — is logged so denied actions are surfaced as fraud signals, and
// recorded against the call so they are durable rather than living only in
// process output (spec: tool-authorization "Decision logging").
func (p *Policy) logDecision(tool, target, resolved string, d Decision) {
	p.recordDecision(resolved, d)

	verdict := "deny"
	if d.Allowed {
		verdict = "allow"
	}
	p.log.Info("tool authorization decision",
		"tool", tool,
		"target", target,
		"resolved", resolved,
		"decision", verdict,
		"reason", d.Reason,
	)
}
