package agent

import (
	"io"
	"log/slog"
	"testing"
)

// quietLogger discards policy decision logs in tests.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestPolicyExternalDeniedByDefault(t *testing.T) {
	// External dial off (zero value), symbolic target resolves to an external
	// number → denied because external is not enabled.
	p := NewPolicy("acme", TenantPolicy{
		SymbolicTargets: map[string]string{"support": "+18005551212"},
	}, quietLogger())

	resolved, d := p.AuthorizeDial("support")
	if d.Allowed {
		t.Fatalf("expected deny when external dial disabled, got allow (resolved=%q)", resolved)
	}
	if resolved != "" {
		t.Fatalf("expected empty resolved target on deny, got %q", resolved)
	}
}

func TestPolicyAllowlistedDestinationPermitted(t *testing.T) {
	p := NewPolicy("acme", TenantPolicy{
		AllowExternalDial:      true,
		ExternalAllowlist:      []string{"+1800"},
		MaxExternalUnitsPerDay: 10,
		SymbolicTargets:        map[string]string{"support": "+18005551212"},
	}, quietLogger())

	resolved, d := p.AuthorizeDial("support")
	if !d.Allowed {
		t.Fatalf("expected allow for allowlisted destination, got deny: %s", d.Reason)
	}
	if resolved != "+18005551212" {
		t.Fatalf("expected resolved target +18005551212, got %q", resolved)
	}
}

func TestPolicyBarredClassDeniedEvenWhenExternalEnabled(t *testing.T) {
	// Premium-rate is in the default barred set; even with external enabled and
	// an allowlist that would match, the bar is unconditional.
	p := NewPolicy("acme", TenantPolicy{
		AllowExternalDial:      true,
		ExternalAllowlist:      []string{"+1900"}, // would match, but barred wins
		MaxExternalUnitsPerDay: 10,
		SymbolicTargets:        map[string]string{"premium": "+19005551234"},
	}, quietLogger())

	resolved, d := p.AuthorizeDial("premium")
	if d.Allowed {
		t.Fatalf("expected barred-class deny, got allow (resolved=%q)", resolved)
	}
}

func TestPolicySymbolicTargetResolves(t *testing.T) {
	// An internal symbolic forward resolves to an internal user target and is
	// allowed without needing external enablement.
	p := NewPolicy("acme", TenantPolicy{
		SymbolicTargets: map[string]string{"frontdesk": "user/1001"},
	}, quietLogger())

	resolved, d := p.AuthorizeDial("frontdesk")
	if !d.Allowed {
		t.Fatalf("expected internal symbolic target allowed, got deny: %s", d.Reason)
	}
	if resolved != "user/1001" {
		t.Fatalf("expected resolved user/1001, got %q", resolved)
	}
}

func TestPolicyUnknownSymbolDenied(t *testing.T) {
	// A raw external number the model tries to smuggle through the normal dial
	// tool is not a known symbol → capability narrowing denies it.
	p := NewPolicy("acme", TenantPolicy{
		AllowExternalDial:      true,
		ExternalAllowlist:      []string{"+1800"},
		MaxExternalUnitsPerDay: 10,
	}, quietLogger())

	if resolved, d := p.AuthorizeDial("+18005551212"); d.Allowed {
		t.Fatalf("expected unknown raw number denied via capability narrowing, got allow (resolved=%q)", resolved)
	}
}

func TestPolicySpendBreakerTrips(t *testing.T) {
	p := NewPolicy("acme", TenantPolicy{
		AllowExternalDial:      true,
		ExternalAllowlist:      []string{"+1800"},
		MaxExternalUnitsPerDay: 2,
		SymbolicTargets:        map[string]string{"support": "+18005551212"},
	}, quietLogger())

	// First two authorized external dials consume the budget.
	for i := 0; i < 2; i++ {
		if _, d := p.AuthorizeDial("support"); !d.Allowed {
			t.Fatalf("dial %d: expected allow within budget, got deny: %s", i, d.Reason)
		}
	}
	// Third trips the breaker.
	if _, d := p.AuthorizeDial("support"); d.Allowed {
		t.Fatal("expected spend breaker to trip on the 3rd external dial")
	}
}

func TestPolicyCallerProvidedNumberGated(t *testing.T) {
	// Gated tool disabled → denied even though external dial is enabled.
	off := NewPolicy("acme", TenantPolicy{
		AllowExternalDial:      true,
		ExternalAllowlist:      []string{"+1800"},
		MaxExternalUnitsPerDay: 10,
	}, quietLogger())
	if _, d := off.AuthorizeCallerProvidedDial("+18005551212"); d.Allowed {
		t.Fatal("expected caller-provided dial denied when gate is off")
	}

	// Gate on + allowlisted → permitted, subject to the same COS.
	on := NewPolicy("acme", TenantPolicy{
		AllowExternalDial:         true,
		AllowCallerProvidedNumber: true,
		ExternalAllowlist:         []string{"+1800"},
		MaxExternalUnitsPerDay:    10,
	}, quietLogger())
	resolved, d := on.AuthorizeCallerProvidedDial("+18005551212")
	if !d.Allowed {
		t.Fatalf("expected caller-provided dial allowed when gated on and allowlisted, got deny: %s", d.Reason)
	}
	if resolved != "+18005551212" {
		t.Fatalf("expected resolved %q, got %q", "+18005551212", resolved)
	}
}
