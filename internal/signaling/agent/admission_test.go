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

func TestAdmitUnloadedTenantRejected(t *testing.T) {
	a := NewAdmission(fakePrompts{"acme": "be helpful"}, 10, nil)

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
	a := NewAdmission(fakePrompts{"acme": "be helpful"}, 2, nil)

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
	a := NewAdmission(fakePrompts{"acme": "x"}, 1, nil)

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
	a := NewAdmission(fakePrompts{"acme": "x"}, 1, nil)

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
	a := NewAdmission(fakePrompts{"acme": "x"}, 1, nil)

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
	a := NewAdmission(fakePrompts{"acme": "x", "big": "y"}, 1, map[string]int{"big": 3})

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
	a := NewAdmission(fakePrompts{"acme": "x"}, limit, nil)

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
