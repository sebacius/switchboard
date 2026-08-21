package agent

import (
	"sync"
	"testing"
)

// fakePrompts is a test PromptSource: a set of loaded tenants whose prompt is
// non-empty. A tenant absent from the map is "not loaded".
type fakePrompts map[string]string

func (f fakePrompts) TenantPrompt(tenant string) (string, bool) {
	p, ok := f[tenant]
	if !ok || p == "" {
		return "", false
	}
	return p, true
}

func ccFor(tenant string) CallContext {
	return CallContext{Caller: "102", Callee: "103", Direction: DirectionInternal, Tenant: tenant}
}

// Admit is the SUPERVISION gate, so its rejection for an unknown tenant is
// "nothing to supervise with" — the tenant-not-loaded check now belongs to
// Preflight, which runs earlier and before deterministic resolution.
func TestAdmitUnloadedTenantRejected(t *testing.T) {
	a := NewAdmission(fakePrompts{"acme": "be helpful"}, nil, 10, nil)

	res := a.Admit(ccFor("ghost"))
	if res.Admitted {
		t.Fatalf("expected reject for unloaded tenant")
	}
	if res.Reason != reasonNoPrompt {
		t.Errorf("reason = %q, want %q", res.Reason, reasonNoPrompt)
	}
	if res.Release == nil {
		t.Errorf("Release must be non-nil even on reject")
	}
	res.Release() // must not panic
	if got := a.Active("ghost"); got != 0 {
		t.Errorf("active(ghost) = %d, want 0", got)
	}
}

func TestAdmitLoadedTenantWithinLimit(t *testing.T) {
	a := NewAdmission(fakePrompts{"acme": "be helpful"}, nil, 2, nil)

	res := a.Admit(ccFor("acme"))
	if !res.Admitted {
		t.Fatalf("expected admit, got reject: %s", res.Reason)
	}
	if got := a.Active("acme"); got != 1 {
		t.Fatalf("active = %d, want 1", got)
	}

	res.Release()
	if got := a.Active("acme"); got != 0 {
		t.Errorf("active after release = %d, want 0", got)
	}
}

func TestAdmitAtLimitRejected(t *testing.T) {
	a := NewAdmission(fakePrompts{"acme": "x"}, nil, 1, nil)

	first := a.Admit(ccFor("acme"))
	if !first.Admitted {
		t.Fatalf("first admit failed: %s", first.Reason)
	}

	second := a.Admit(ccFor("acme"))
	if second.Admitted {
		t.Fatalf("expected reject at limit")
	}
	if second.Reason != reasonChannelLimit {
		t.Errorf("reason = %q, want %q", second.Reason, reasonChannelLimit)
	}
	// The rejected call's no-op Release must not free the in-flight slot.
	second.Release()
	if got := a.Active("acme"); got != 1 {
		t.Errorf("active = %d, want 1 (rejected release must not free a slot)", got)
	}
}

func TestReleaseFreesSlotForSubsequentAdmit(t *testing.T) {
	a := NewAdmission(fakePrompts{"acme": "x"}, nil, 1, nil)

	first := a.Admit(ccFor("acme"))
	if !first.Admitted {
		t.Fatalf("first admit failed")
	}
	if a.Admit(ccFor("acme")).Admitted {
		t.Fatalf("expected reject while slot held")
	}

	first.Release()

	third := a.Admit(ccFor("acme"))
	if !third.Admitted {
		t.Fatalf("expected admit after release, got reject: %s", third.Reason)
	}
}

func TestReleaseIsIdempotent(t *testing.T) {
	a := NewAdmission(fakePrompts{"acme": "x"}, nil, 1, nil)

	res := a.Admit(ccFor("acme"))
	if !res.Admitted {
		t.Fatalf("admit failed")
	}
	// Calling Release many times must free exactly one slot, never under-count.
	res.Release()
	res.Release()
	res.Release()
	if got := a.Active("acme"); got != 0 {
		t.Errorf("active = %d, want 0", got)
	}

	// A second admit must succeed and a second double-release must not corrupt
	// the count of an unrelated, still-held call.
	held := a.Admit(ccFor("acme"))
	if !held.Admitted {
		t.Fatalf("expected admit after idempotent release")
	}
	res.Release() // stale release of the first call: must be a no-op now
	if got := a.Active("acme"); got != 1 {
		t.Errorf("active = %d, want 1 (stale release must not free the held slot)", got)
	}
	held.Release()
}

func TestPerTenantOverride(t *testing.T) {
	a := NewAdmission(fakePrompts{"acme": "x", "big": "y"}, nil, 1, map[string]int{"big": 3})

	// "acme" uses the default of 1.
	if !a.Admit(ccFor("acme")).Admitted {
		t.Fatalf("acme first admit failed")
	}
	if a.Admit(ccFor("acme")).Admitted {
		t.Fatalf("acme expected reject at default limit 1")
	}

	// "big" overrides to 3.
	for i := 0; i < 3; i++ {
		if !a.Admit(ccFor("big")).Admitted {
			t.Fatalf("big admit %d failed", i)
		}
	}
	if a.Admit(ccFor("big")).Admitted {
		t.Fatalf("big expected reject at override limit 3")
	}
}

// TestAdmitConcurrent stresses the gate under -race: many goroutines admit and
// release against a high-limit tenant; the active count must settle to zero and
// never exceed the limit.
func TestAdmitConcurrent(t *testing.T) {
	const limit = 50
	const goroutines = 200
	a := NewAdmission(fakePrompts{"acme": "x"}, nil, limit, nil)

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := a.Admit(ccFor("acme"))
			if got := a.Active("acme"); got > limit {
				t.Errorf("active = %d exceeds limit %d", got, limit)
			}
			if res.Admitted {
				// Release twice to also exercise idempotency under contention.
				res.Release()
				res.Release()
			}
		}()
	}
	wg.Wait()

	if got := a.Active("acme"); got != 0 {
		t.Errorf("final active = %d, want 0", got)
	}
}

// Preflight runs before deterministic resolution and asks only "do we know this
// tenant at all". A tenant we cannot attribute is rejected there.
func TestPreflightRejectsUnknownTenant(t *testing.T) {
	a := NewAdmission(fakePrompts{"acme": "be helpful"}, nil, 10, nil)

	res := a.Preflight(ccFor("ghost"))
	if res.Admitted {
		t.Fatal("an unattributable call must not pass preflight")
	}
	if res.Reason != reasonTenantNotLoaded {
		t.Errorf("reason = %q, want %q", res.Reason, reasonTenantNotLoaded)
	}
	if res.Release == nil {
		t.Error("Release must be non-nil even on reject")
	}
}

// A tenant with a routing table but no prompt is loaded: it can be ROUTED even
// though it cannot be supervised. Rejecting it at preflight would mean an
// extension dial fails because nobody wrote the AI a personality.
func TestPreflightAcceptsRoutingOnlyTenant(t *testing.T) {
	routing := StaticRouting{"routed": {Extensions: map[string]string{"105": "user/105"}}}
	a := NewAdmission(fakePrompts{}, routing, 10, nil)

	if res := a.Preflight(ccFor("routed")); !res.Admitted {
		t.Fatalf("a routing-only tenant must pass preflight, got %q", res.Reason)
	}
	// ...but it still cannot be supervised.
	if res := a.Admit(ccFor("routed")); res.Admitted {
		t.Fatal("a tenant with no prompt must not be admitted to supervision")
	}
}

// The channel limit is charged only at the supervision hand-off, so a tenant
// sitting at its limit must still be able to take deterministically routed
// calls. This asserts the accounting half: Preflight never consumes a slot.
func TestPreflightDoesNotConsumeAChannel(t *testing.T) {
	a := NewAdmission(fakePrompts{"acme": "x"}, nil, 1, nil)

	for range 5 {
		if res := a.Preflight(ccFor("acme")); !res.Admitted {
			t.Fatalf("preflight must not be capacity-limited, got %q", res.Reason)
		}
	}
	if got := a.Active("acme"); got != 0 {
		t.Fatalf("preflight consumed %d channels, want 0", got)
	}
	if res := a.Admit(ccFor("acme")); !res.Admitted {
		t.Fatal("the single channel must still be free for a supervised call")
	}
}
