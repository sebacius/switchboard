package agent

import (
	"log/slog"
	"strings"
	"sync"
)

// The model is UNTRUSTED. Everything in this file is the deterministic
// authorization boundary that adjudicates a tool call before it executes
// (design #10). It NEVER reads prompt/conversation content — the only inputs are
// the tenant policy config and the symbolic target the model emitted. Prompt
// injection therefore cannot move any verdict here.

// Decision is the verdict of an authorization check: allow or deny, plus a
// machine-stable reason that is both logged and (on deny) surfaced to the model
// as the tool result so it can self-correct.
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

	// SymbolicTargets maps model-emitted symbolic names (extension names, named
	// forwards) to concrete dial targets. This is capability narrowing: the model
	// dials "sales" or "front-desk", never a raw external number. A resolved
	// target may itself be internal ("user/1001") or external ("+18005551212"),
	// and external resolutions are still subject to the full COS below.
	SymbolicTargets map[string]string

	// MaxExternalUnitsPerDay is the per-tenant spend circuit breaker: the number
	// of external "units" (calls today; minutes/cost later) permitted before the
	// breaker trips. Zero means no external spend is allowed at all (the safe
	// default); set it explicitly to permit external dialing volume.
	MaxExternalUnitsPerDay int

	// AllowCallerProvidedNumber gates the separate, hard-gated tool that dials a
	// caller-supplied number (bypassing symbolic narrowing). Default false. The
	// normal dial tool can never express a raw external number; only this gated
	// tool can, and it is still subject to the same COS.
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

// Policy is the per-tenant authorization engine. It is constructed per call (it
// holds the tenant policy and the per-tenant spend counter) and is safe for
// concurrent use. It NEVER inspects prompt content.
type Policy struct {
	tenant string
	cfg    TenantPolicy
	log    *slog.Logger

	mu            sync.Mutex
	externalUnits int      // spend counter consumed by authorized external dials
	barred        []string // resolved barred prefixes (cfg or defaults)
}

// NewPolicy builds a Policy for one tenant. A nil logger falls back to
// slog.Default. The barred set is the tenant's explicit list, or the defaults
// when the tenant left it nil.
func NewPolicy(tenant string, cfg TenantPolicy, log *slog.Logger) *Policy {
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
		log:    log.With("tenant", tenant, "component", "tool-policy"),
		barred: barred,
	}
}

// AuthorizeDial adjudicates a dial whose target is a SYMBOLIC name the model
// emitted through the normal dial tool. It resolves the symbol deterministically
// and runs the resolved target through the full COS. It returns the resolved
// concrete target (empty on deny) and the Decision. The model can never express
// a raw external number here — an unrecognised symbol that is not an internal
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

// resolveSymbol maps a model-emitted symbol to a concrete target. Internal
// "user/..." targets pass through unchanged (they never reach a trunk). A name
// in SymbolicTargets resolves to its configured target. Everything else fails
// resolution so a raw external number can never enter through the normal tool.
func (p *Policy) resolveSymbol(symbol string) (string, bool) {
	if strings.HasPrefix(symbol, "user/") {
		return symbol, true
	}
	if resolved, ok := p.cfg.SymbolicTargets[symbol]; ok {
		return strings.TrimSpace(resolved), true
	}
	return "", false
}

// authorizeResolved applies COS to a concrete, already-resolved target. Internal
// targets are always permitted; external targets run default-deny → barred-class
// → allowlist → spend-breaker.
func (p *Policy) authorizeResolved(resolved string) Decision {
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

	// Spend circuit breaker: trip when consuming this unit would exceed the cap.
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.externalUnits >= p.cfg.MaxExternalUnitsPerDay {
		return deny("tenant external spend limit reached")
	}
	p.externalUnits++
	return allow("external destination authorized")
}

// isInternalTarget reports whether a resolved target is an internal directory
// reference that cannot reach an external trunk.
func isInternalTarget(target string) bool {
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

// logDecision emits the authorization verdict for audit. Every verdict —
// allow and deny — is logged so denied actions are surfaced as fraud signals.
//
// TODO(cdr): once the structured call record exists, write (tool, target,
// resolved, decision, reason) into the CDR here too so denied dials are
// auditable per-call as potential toll-fraud / injection signals (spec:
// tool-authorization "Decision logging"). For now slog is the only sink.
func (p *Policy) logDecision(tool, target, resolved string, d Decision) {
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
