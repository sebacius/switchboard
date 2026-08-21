package agent

import (
	"encoding/json"
	"fmt"
	"os"
)

// PolicyConfig is the on-disk Class-of-Service and capacity configuration
// (design #9 + #10). It is the deterministic half of the security boundary: the
// model can never edit it, and everything it does not explicitly grant is
// denied. A missing file is NOT an error — it yields the safe default posture
// (no external dialing anywhere, default channel limit) so a fresh deployment
// cannot accidentally start out permissive.
type PolicyConfig struct {
	// DefaultChannelLimit caps concurrent supervised calls per tenant when the
	// tenant sets no override. Zero falls back to DefaultChannelLimitFallback.
	DefaultChannelLimit int `json:"default_channel_limit"`

	// Tenants holds per-tenant capacity and COS settings, keyed by tenant name
	// (the subdomain label for internal/outbound, the DID's tenant for inbound).
	Tenants map[string]TenantConfig `json:"tenants"`
}

// TenantConfig is one tenant's capacity and Class of Service. Every field is
// opt-in: the zero value is a tenant that can take calls and route internally,
// but cannot reach any external destination.
type TenantConfig struct {
	// ChannelLimit overrides the default concurrent-call cap. Zero or negative
	// means "use the default".
	ChannelLimit int `json:"channel_limit"`

	// AllowExternalDial enables external destinations at all. Default false.
	AllowExternalDial bool `json:"allow_external_dial"`

	// ExternalAllowlist is the prefix allowlist consulted when external dialing
	// is enabled. An empty list with external enabled denies everything —
	// deny-by-omission, never allow-all.
	ExternalAllowlist []string `json:"external_allowlist"`

	// BarredPrefixes overrides DefaultBarredPrefixes. Omit to inherit the
	// defaults; set an explicit empty array to bar nothing (rarely correct).
	BarredPrefixes []string `json:"barred_prefixes"`

	// SymbolicTargets is RETIRED from this file. It now lives in the tenant's
	// routing table (<tenant>.routing.json) so the deterministic resolver and the
	// model-facing capability narrowing read the same names from one place.
	//
	// The field is kept only to DETECT a stale config: a pointer distinguishes
	// "absent" from "present but empty", and LoadPolicyConfig refuses to start
	// when it is present. Two sources for one name is exactly the drift moving it
	// was meant to end, and a silent merge would let a stale entry here quietly
	// outrank the routing file for the life of a deployment.
	SymbolicTargets *map[string]string `json:"symbolic_targets"`

	// MaxExternalUnitsPerDay is the spend circuit breaker. Zero permits no
	// external spend at all.
	MaxExternalUnitsPerDay int `json:"max_external_units_per_day"`

	// AllowCallerProvidedNumber gates the separate hard-gated tool that dials a
	// raw caller-supplied number. Default false.
	AllowCallerProvidedNumber bool `json:"allow_caller_provided_number"`
}

// DefaultChannelLimitFallback is the per-tenant concurrency cap applied when
// neither the tenant nor the file specifies one. It is deliberately modest: the
// limit protects the first-turn LLM call from queueing past SIP Timer B, so a
// too-high value degrades call setup for everyone rather than rejecting one call.
const DefaultChannelLimitFallback = 10

// LoadPolicyConfig reads the COS/capacity file. A missing path or empty path
// yields the safe defaults rather than an error; malformed JSON IS an error,
// because silently falling back to defaults would turn a typo in an allowlist
// into a policy change nobody noticed.
func LoadPolicyConfig(path string) (*PolicyConfig, error) {
	cfg := &PolicyConfig{
		DefaultChannelLimit: DefaultChannelLimitFallback,
		Tenants:             map[string]TenantConfig{},
	}
	if path == "" {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read policy config %s: %w", path, err)
	}

	var parsed PolicyConfig
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("parse policy config %s: %w", path, err)
	}

	if parsed.DefaultChannelLimit > 0 {
		cfg.DefaultChannelLimit = parsed.DefaultChannelLimit
	}
	if parsed.Tenants != nil {
		cfg.Tenants = parsed.Tenants
	}

	// Fail loudly on a config that still carries symbolic targets here. Starting
	// anyway would leave the operator believing those names are dialable when the
	// routing file is what actually decides.
	for name, t := range cfg.Tenants {
		if t.SymbolicTargets != nil {
			return nil, fmt.Errorf(
				"%s: tenant %q still defines \"symbolic_targets\"; move those entries to the "+
					"tenant's routing file (<tenants-path>/%s.routing.json) under \"symbolic_targets\" "+
					"and remove the key here", path, name, name)
		}
	}
	return cfg, nil
}

// ChannelLimits renders the per-tenant overrides in the shape NewAdmission
// expects. Tenants with no positive override are omitted so the default applies.
func (c *PolicyConfig) ChannelLimits() map[string]int {
	limits := make(map[string]int, len(c.Tenants))
	for name, t := range c.Tenants {
		if t.ChannelLimit > 0 {
			limits[name] = t.ChannelLimit
		}
	}
	return limits
}

// TenantPolicyFor returns the authorization policy for a tenant. An unknown
// tenant gets the zero TenantPolicy — the safest posture, not an error: the
// router already proved the tenant exists, and a tenant with no COS entry
// simply has no external reach.
//
// symbolicTargets comes from the tenant's routing table, not from this file. The
// parameter is explicit rather than a stored dependency so every construction
// site has to say where the names came from — there is exactly one right answer,
// and the compiler now asks the question.
func (c *PolicyConfig) TenantPolicyFor(tenant string, symbolicTargets map[string]string) TenantPolicy {
	t, ok := c.Tenants[tenant]
	if !ok {
		return TenantPolicy{SymbolicTargets: symbolicTargets}
	}
	return TenantPolicy{
		AllowExternalDial:         t.AllowExternalDial,
		ExternalAllowlist:         t.ExternalAllowlist,
		BarredPrefixes:            t.BarredPrefixes,
		SymbolicTargets:           symbolicTargets,
		MaxExternalUnitsPerDay:    t.MaxExternalUnitsPerDay,
		AllowCallerProvidedNumber: t.AllowCallerProvidedNumber,
	}
}
