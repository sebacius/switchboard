package filemanager

import (
	"fmt"
	"sort"

	"github.com/sebas/switchboard/internal/signaling/agent"
	"github.com/sebas/switchboard/internal/signaling/dialplan"
	"github.com/sebas/switchboard/internal/signaling/trunk"
)

// A global file is validated by the real loader and the real cross-checks, the
// same ones `switchboard validate` runs. Anything else would let the editor and
// the server disagree about whether a file is good, and the editor is the one
// the operator believes.

// validateGlobal checks a proposed deployment-wide file.
func (fm *FileManager) validateGlobal(kind GlobalKind, content string) dialplan.Problems {
	fail := func(path, msg string) dialplan.Problems {
		return dialplan.Problems{{
			Path:     path,
			Message:  msg,
			Severity: dialplan.SeverityError,
		}}
	}

	switch kind {
	case KindPolicy:
		cfg, err := agent.ParsePolicyConfig([]byte(content), string(kind))
		if err != nil {
			return fail(string(kind), err.Error())
		}
		return fm.checkPolicy(cfg)

	case KindRoutes:
		routes, err := trunk.ParseRoutes([]byte(content), string(kind))
		if err != nil {
			return fail(string(kind), err.Error())
		}
		return trunk.CheckRoutes(routes, fm.tenantTables(), "routes.json")

	case KindTrunkPeers:
		peers, err := trunk.ParsePeers([]byte(content), string(kind))
		if err != nil {
			return fail(string(kind), err.Error())
		}
		return trunk.CheckPeers(peers)

	default:
		return fail(string(kind), "unknown configuration file")
	}
}

// checkPolicy reports the ways a parseable policy still fails to mean what its
// author meant. None of these block a load, so all of them are warnings.
func (fm *FileManager) checkPolicy(cfg *agent.PolicyConfig) dialplan.Problems {
	var ps dialplan.Problems
	tables := fm.tenantTables()

	for _, name := range sortedTenantNames(cfg.Tenants) {
		t := cfg.Tenants[name]
		path := "tenants." + name

		if _, ok := tables[name]; !ok {
			ps = append(ps, dialplan.Problem{
				Tenant: name,
				Path:   path,
				Message: fmt.Sprintf(
					"policy grants a Class of Service to tenant %q, which has no routing file; "+
						"it can never take a call", name),
				Severity: dialplan.SeverityWarning,
			})
		}

		if t.AllowExternalDial && len(t.ExternalAllowlist) == 0 {
			ps = append(ps, dialplan.Problem{
				Tenant: name,
				Path:   path + ".external_allowlist",
				Message: "external dialing is enabled with an empty allowlist, which denies every " +
					"destination; add prefixes, or turn allow_external_dial off and say so",
				Severity: dialplan.SeverityWarning,
			})
		}

		if t.AllowExternalDial && t.MaxExternalUnitsPerDay == 0 {
			ps = append(ps, dialplan.Problem{
				Tenant: name,
				Path:   path + ".max_external_units_per_day",
				Message: "external dialing is enabled but the daily spend cap is zero, so the " +
					"first external call is refused by the breaker",
				Severity: dialplan.SeverityWarning,
			})
		}
	}
	return ps
}

// tenantTables loads every tenant's routing table for the cross-checks. A
// tenant that does not parse is skipped rather than failing the global file:
// the operator is editing routes.json, and a broken tenant file is a different
// problem reported by a different editor.
func (fm *FileManager) tenantTables() map[string]*dialplan.RoutingTable {
	tables := map[string]*dialplan.RoutingTable{}
	tenants, err := fm.ListTenants()
	if err != nil {
		return tables
	}
	for _, t := range tenants {
		table, err := fm.existingTable(t.Name)
		if err != nil {
			continue
		}
		tables[t.Name] = table
	}
	return tables
}

// sortedTenantNames keeps problem order stable across runs.
func sortedTenantNames(tenants map[string]agent.TenantConfig) []string {
	names := make([]string, 0, len(tenants))
	for name := range tenants {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
