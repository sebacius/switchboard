package agent

import (
	"sync"
	"testing"
)

// loaded builds a RoutingSource in which each named tenant has a routing table.
// Having somewhere to send calls is the whole definition of a loaded tenant.
func loaded(tenants ...string) StaticRouting {
	r := StaticRouting{}
	for _, name := range tenants {
		r[name] = &RoutingTable{Operator: "user/100"}
	}
	return r
}

func ccFor(tenant string) CallContext {
	return CallContext{Caller: "102", Callee: "103", Direction: DirectionInternal, Tenant: tenant}
}

// Admit is the single gate, so an unknown tenant is rejected here — there is no
// default tenant and nowhere to send the call.
func TestAdmitUnloadedTenantRejected(t *testing.T) {
	a := NewAdmission(loaded("acme"), 10, nil)

	res := a.Admit(ccFor("ghost"))
	if res.Admitted {
		t.Fatalf("expected reject for unloaded tenant")
	}
	if res.Reason != reasonTenantNotLoaded {
		t.Errorf("reason = %q, want %q", res.Reason, reasonTenantNotLoaded)
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
	a := NewAdmission(loaded("acme"), 2, nil)

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
	a := NewAdmission(loaded("acme"), 1, nil)

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
	a := NewAdmission(loaded("acme"), 1, nil)

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
	a := NewAdmission(loaded("acme"), 1, nil)

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
	a := NewAdmission(loaded("acme", "big"), 1, map[string]int{"big": 3})

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
	a := NewAdmission(loaded("acme"), limit, nil)

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
// A tenant is loaded on the strength of its routing table alone.
func TestAdmitAcceptsRoutingOnlyTenant(t *testing.T) {
	a := NewAdmission(loaded("acme"), 10, nil)

	res := a.Admit(ccFor("acme"))
	if !res.Admitted {
		t.Fatalf("expected admit for a tenant with a routing table, got %q", res.Reason)
	}
	res.Release()
}

// An empty tenant string is never attributable.
func TestAdmitRejectsEmptyTenant(t *testing.T) {
	a := NewAdmission(loaded("acme"), 10, nil)

	if res := a.Admit(ccFor("")); res.Admitted {
		t.Fatalf("expected reject for an empty tenant")
	}
}

// A rejected call must not leave a slot consumed behind it.
func TestRejectionConsumesNoChannel(t *testing.T) {
	a := NewAdmission(loaded("acme"), 1, nil)

	res := a.Admit(ccFor("ghost"))
	if res.Admitted {
		t.Fatalf("expected reject")
	}
	res.Release()

	if got := a.Active("ghost"); got != 0 {
		t.Errorf("active(ghost) = %d, want 0", got)
	}
	// The one slot acme has must still be available.
	if ok := a.Admit(ccFor("acme")); !ok.Admitted {
		t.Errorf("a rejected call for another tenant consumed acme's slot")
	}
}

// The slot is taken before any media is allocated, so a tenant at its limit is
// turned away without consuming an RTP port. This is the whole reason admission
// moved ahead of CreateSession: what it bounds is physical now, not a model.
func TestAdmitIsTheGateBeforeMediaAllocation(t *testing.T) {
	a := NewAdmission(loaded("acme"), 1, nil)

	first := a.Admit(ccFor("acme"))
	if !first.Admitted {
		t.Fatalf("first call should be admitted")
	}

	second := a.Admit(ccFor("acme"))
	if second.Admitted {
		t.Fatal("the second concurrent call must be rejected at the limit")
	}
	if second.Reason != reasonChannelLimit {
		t.Errorf("reason = %q, want %q so the handler maps it to 486", second.Reason, reasonChannelLimit)
	}

	// Freeing the first call must make the slot available again.
	first.Release()
	if third := a.Admit(ccFor("acme")); !third.Admitted {
		t.Error("releasing the slot must let the next call in")
	}
}
