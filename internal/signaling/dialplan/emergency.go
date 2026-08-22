package dialplan

import (
	"fmt"
	"sort"
	"strings"
)

// Emergency calling is NOT implemented, and these checks do not implement it.
// They warn, because the alternative is silence about a compliance liability
// this change makes worse.
//
// Nothing special-cases 911 anywhere. A tenant with the documented-safe
// `allow_external_dial: false` cannot dial it at all; one with external enabled
// and an empty allowlist cannot either. Digit maps add a new way to get it
// wrong: a perfectly reasonable "9." outbound pattern silently swallows 911,
// because 9 followed by 11 matches it.
//
// Kari's Law requires direct 911 dialing with no prefix plus on-site
// notification; RAY BAUM'S Act requires a dispatchable location. Emergency
// routing must bypass Class of Service entirely and be un-configurable — it
// cannot be something a tenant is able to misconfigure. That is its own change,
// and it should land before this system carries production traffic.
//
// These are warnings rather than errors on purpose: failing a load would stop a
// lab deployment from starting over a feature that does not exist yet.

// emergencyNumbers are the short codes worth warning about. Deliberately a
// small, well-known set rather than a configurable one — a tenant able to edit
// the list is a tenant able to remove 911 from it.
var emergencyNumbers = []string{"911", "112", "999", "000", "111"}

// CheckEmergency reports emergency-calling hazards in a tenant's entry mapping.
func CheckEmergency(tenant string, table *RoutingTable) Problems {
	if table == nil {
		return nil
	}

	var ps Problems
	warn := func(path, msg string) {
		ps = append(ps, Problem{
			Tenant: tenant, Path: path, Message: msg, Severity: SeverityWarning,
		})
	}

	patterns := make([]string, 0, len(table.Extensions))
	for raw := range table.Extensions {
		patterns = append(patterns, raw)
	}
	sort.Strings(patterns)

	// 1. A pattern that swallows an emergency number without being one.
	for _, raw := range patterns {
		p, err := CompilePattern(raw)
		if err != nil {
			continue
		}
		for _, number := range emergencyNumbers {
			if !p.Matches(number) || raw == number {
				continue
			}
			warn("extensions."+raw, fmt.Sprintf(
				"pattern %q also matches the emergency number %s, which would route an emergency "+
					"call to %q. Emergency calling is not implemented; until it is, add an explicit "+
					"%q entry so the pattern cannot shadow it",
				raw, number, table.Extensions[raw], number))
		}
	}

	// 2. A tenant that can reach the PSTN but has no emergency entry at all.
	if tenantCanReachPSTN(table) && !hasEmergencyEntry(table) {
		warn("extensions", fmt.Sprintf(
			"tenant has outbound dialing but no entry for any of %s. Emergency calling is not "+
				"implemented in Switchboard: a caller dialing 911 from this tenant reaches nothing",
			strings.Join(emergencyNumbers, ", ")))
	}

	return ps
}

// tenantCanReachPSTN reports whether the tenant names any external destination.
func tenantCanReachPSTN(table *RoutingTable) bool {
	for _, dest := range table.SymbolicTargets {
		if looksLikeRawNumber(dest) {
			return true
		}
	}
	return false
}

// hasEmergencyEntry reports whether an emergency number is explicitly mapped.
func hasEmergencyEntry(table *RoutingTable) bool {
	for _, number := range emergencyNumbers {
		if _, ok := table.Extensions[number]; ok {
			return true
		}
	}
	return false
}
