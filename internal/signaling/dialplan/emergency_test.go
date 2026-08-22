package dialplan

import (
	"strings"
	"testing"
)

// The failure this exists to catch: a perfectly reasonable outbound prefix
// silently swallows 911, because 9 followed by 11 matches "9.".
func TestOutboundPrefixShadowingEmergencyIsWarned(t *testing.T) {
	table := &RoutingTable{
		Extensions: Entries(map[string]string{"9.": "flow/outbound"}),
	}

	ps := CheckEmergency("acme", table)
	if len(ps) == 0 {
		t.Fatal(`"9." matches 911 and must be warned about`)
	}

	var found bool
	for _, p := range ps {
		if strings.Contains(p.Message, "911") {
			found = true
			if p.Severity != SeverityWarning {
				t.Errorf("severity = %q, want a warning: failing the load over an "+
					"unimplemented feature would stop a lab deployment", p.Severity)
			}
		}
	}
	if !found {
		t.Errorf("the warning should name 911: %+v", ps)
	}
}

// An explicit entry is not shadowing itself.
func TestExplicitEmergencyEntryIsNotWarnedAsShadowing(t *testing.T) {
	table := &RoutingTable{
		Extensions: Entries(map[string]string{"911": "user/150"}),
	}

	for _, p := range CheckEmergency("acme", table) {
		if strings.Contains(p.Message, "shadow") || strings.Contains(p.Message, "also matches") {
			t.Errorf("an explicit 911 entry must not be reported as shadowing: %+v", p)
		}
	}
}

// A tenant that can reach the PSTN but has no emergency entry is worth saying
// out loud.
func TestPSTNCapableTenantWithoutEmergencyIsWarned(t *testing.T) {
	table := &RoutingTable{
		Extensions:      Entries(map[string]string{"100": "user/100"}),
		SymbolicTargets: map[string]string{"afterhours": "+15558001234"},
	}

	ps := CheckEmergency("acme", table)
	if len(ps) == 0 {
		t.Fatal("a tenant with outbound dialing and no emergency entry must be warned")
	}
	if !strings.Contains(ps[0].Message, "not implemented") {
		t.Errorf("the warning should be honest that the feature does not exist: %v", ps[0].Message)
	}
}

// An internal-only tenant has no PSTN to reach, so there is nothing to warn
// about — a warning on every lab fixture would train people to ignore them.
func TestInternalOnlyTenantIsNotWarned(t *testing.T) {
	table := &RoutingTable{
		Extensions:      Entries(map[string]string{"100": "user/100"}),
		SymbolicTargets: map[string]string{"operator": "user/100"},
	}

	if ps := CheckEmergency("acme", table); len(ps) != 0 {
		t.Errorf("an internal-only tenant should produce no emergency warnings: %+v", ps)
	}
}

// Warnings must never fail a load.
func TestEmergencyWarningsDoNotFailValidation(t *testing.T) {
	ps := CheckEmergency("acme", &RoutingTable{
		Extensions: Entries(map[string]string{"9.": "flow/outbound"}),
	})

	if ps.HasErrors() {
		t.Error("emergency findings must be warnings, not errors")
	}
	if ps.Err() != nil {
		t.Error("warnings must not produce a load error")
	}
}
