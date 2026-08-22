package agent

import "testing"

// CallContext is the identity every layer shares. These are the only fields
// that survived the supervisor, and the direction constants are what gates
// internal-only capabilities like call retrieval.
func TestCallContextCarriesCallIdentity(t *testing.T) {
	cc := CallContext{
		Caller:    "102",
		Callee:    "105",
		Direction: DirectionInternal,
		Tenant:    "acme",
	}

	if cc.Caller != "102" || cc.Callee != "105" {
		t.Errorf("caller/callee = %q/%q, want 102/105", cc.Caller, cc.Callee)
	}
	if cc.Tenant != "acme" {
		t.Errorf("tenant = %q, want acme", cc.Tenant)
	}
	if cc.Direction != DirectionInternal {
		t.Errorf("direction = %q, want %q", cc.Direction, DirectionInternal)
	}
}

func TestDirectionsAreDistinct(t *testing.T) {
	seen := map[Direction]bool{}
	for _, d := range []Direction{DirectionInternal, DirectionInbound, DirectionOutbound} {
		if d == "" {
			t.Error("a direction constant is empty")
		}
		if seen[d] {
			t.Errorf("duplicate direction value %q", d)
		}
		seen[d] = true
	}
}
