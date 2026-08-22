package agent

import (
	"io"
	"log/slog"
	"testing"
	"time"
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
	p := NewPolicyWithLedger("acme", TenantPolicy{
		AllowExternalDial:      true,
		ExternalAllowlist:      []string{"+1800"},
		MaxExternalUnitsPerDay: 2,
		SymbolicTargets:        map[string]string{"support": "+18005551212"},
	}, NewSpendLedger(), quietLogger())

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

// The bug this ledger exists to fix: a Policy is built per call, so a counter
// living on it reset on every INVITE and max_external_units_per_day could never
// be reached. The budget must survive across calls or the breaker is decorative.
func TestSpendBreakerAccumulatesAcrossCalls(t *testing.T) {
	cfg := TenantPolicy{
		AllowExternalDial:      true,
		ExternalAllowlist:      []string{"+1800"},
		MaxExternalUnitsPerDay: 2,
		SymbolicTargets:        map[string]string{"support": "+18005551212"},
	}
	ledger := NewSpendLedger()

	// Each iteration is a separate call, with its own freshly built Policy.
	allowed := 0
	for i := 0; i < 5; i++ {
		p := NewPolicyWithLedger("acme", cfg, ledger, quietLogger())
		if _, d := p.AuthorizeDial("support"); d.Allowed {
			allowed++
		}
	}

	if allowed != 2 {
		t.Fatalf("allowed %d external dials across 5 calls, want 2 — the daily cap must "+
			"survive the per-call Policy", allowed)
	}
	if got := ledger.Spent("acme"); got != 2 {
		t.Errorf("ledger reports %d units spent, want 2", got)
	}
}

// One tenant exhausting its budget must not affect another.
func TestSpendIsPerTenant(t *testing.T) {
	ledger := NewSpendLedger()
	for i := 0; i < 3; i++ {
		ledger.Consume("acme", 2)
	}

	if ledger.Spent("acme") != 2 {
		t.Errorf("acme should be capped at 2, got %d", ledger.Spent("acme"))
	}
	if !ledger.Consume("other", 2) {
		t.Error("another tenant must still have its own budget")
	}
}

// Counters reset when the day rolls over, or a busy tenant would be locked out
// permanently after one heavy day.
func TestSpendResetsDaily(t *testing.T) {
	day := time.Date(2026, 8, 22, 23, 0, 0, 0, time.UTC)
	ledger := NewSpendLedger()
	ledger.now = func() time.Time { return day }

	if !ledger.Consume("acme", 1) {
		t.Fatal("first unit should be allowed")
	}
	if ledger.Consume("acme", 1) {
		t.Fatal("second unit should trip the cap")
	}

	day = day.Add(2 * time.Hour) // past midnight UTC
	if !ledger.Consume("acme", 1) {
		t.Error("the budget must reset on the new day")
	}
}

// A tenant that never configured a cap does not thereby get an unlimited one:
// default-deny applies to spend as it does to everything else.
func TestUnsetCapPermitsNoExternalSpend(t *testing.T) {
	ledger := NewSpendLedger()
	if ledger.Consume("acme", 0) {
		t.Error("a cap of zero must permit no external spend")
	}
}

// Classification must be free of side effects, or validating a configuration at
// load would spend the tenant's budget doing it.
func TestClassifyDoesNotConsumeBudget(t *testing.T) {
	cfg := TenantPolicy{
		AllowExternalDial:      true,
		ExternalAllowlist:      []string{"+1800"},
		MaxExternalUnitsPerDay: 2,
	}
	ledger := NewSpendLedger()
	p := NewPolicyWithLedger("acme", cfg, ledger, quietLogger())

	for i := 0; i < 50; i++ {
		if d := p.Classify("+18005551212"); !d.Allowed {
			t.Fatalf("classification %d should allow: %s", i, d.Reason)
		}
	}

	if got := ledger.Spent("acme"); got != 0 {
		t.Fatalf("classification spent %d units; validating a config must cost nothing", got)
	}
	// The budget is intact for real calls.
	if !p.Consume("+18005551212") {
		t.Error("the full budget must still be available after classification")
	}
}
