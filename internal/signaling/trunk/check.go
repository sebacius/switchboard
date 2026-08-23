package trunk

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sebas/switchboard/internal/signaling/dialplan"
)

// The cross-checks live here rather than in the validate subcommand because two
// callers need them: `switchboard validate` before the server starts, and the
// config API before it writes a file. A copy in each would be free to drift, and
// an editor that disagrees with the loader is worse than no editor.

// CheckRoutes cross-checks the global DID -> tenant table against the tenants
// that actually exist. label names the routes file in messages.
//
// A DID naming a tenant with no routing file is the shipped bug this check was
// written for: the call is attributed to a tenant nobody has configured, so it
// reaches a 404 at 2am rather than a message at startup.
func CheckRoutes(routes *DIDRoutes, tables map[string]*dialplan.RoutingTable, label string) dialplan.Problems {
	var ps dialplan.Problems
	if routes == nil {
		return ps
	}

	mapped := routes.All()
	dids := make([]string, 0, len(mapped))
	for did := range mapped {
		dids = append(dids, did)
	}
	sort.Strings(dids)

	// Forward: every routed DID must name a tenant that exists.
	for _, did := range dids {
		tenant := mapped[did]
		if _, ok := tables[tenant]; ok {
			continue
		}
		ps = append(ps, dialplan.Problem{
			Tenant: tenant,
			Path:   "routes.dids." + did,
			Message: fmt.Sprintf(
				"DID %q routes to tenant %q, which has no routing file; a call to this number "+
					"would be attributed to a tenant that does not exist and rejected with 404",
				did, tenant),
			Severity: dialplan.SeverityError,
		})
	}

	// Reverse: a tenant that handles a DID nobody sends it will never see a
	// call. This is the likeliest operator mistake — adding the number to the
	// tenant file and forgetting the global one.
	//
	// Only LITERAL keys are checked. Deciding whether one pattern contains
	// another is a harder problem than this warning is worth, so a tenant using
	// patterns for its DIDs simply is not cross-checked, rather than being
	// checked badly.
	tenants := make([]string, 0, len(tables))
	for tenant := range tables {
		tenants = append(tenants, tenant)
	}
	sort.Strings(tenants)

	for _, tenant := range tenants {
		table := tables[tenant]
		if table == nil {
			continue
		}
		own := make([]string, 0, len(table.DIDs))
		for did := range table.DIDs {
			own = append(own, did)
		}
		sort.Strings(own)

		for _, did := range own {
			if !isLiteralDID(did) {
				continue
			}
			if owner, ok := routes.TenantForDID(did); ok && owner == tenant {
				continue
			}
			ps = append(ps, dialplan.Problem{
				Tenant: tenant,
				Path:   "dids." + did,
				Message: fmt.Sprintf(
					"tenant handles DID %q but %s does not route that number to it, so no call "+
						"will ever arrive on it", did, filepath.Base(label)),
				Severity: dialplan.SeverityWarning,
			})
		}
	}

	return ps
}

// isLiteralDID reports whether a key is a plain number rather than a pattern.
func isLiteralDID(did string) bool {
	for _, r := range strings.TrimPrefix(did, "+") {
		if r < '0' || r > '9' {
			return false
		}
	}
	return did != "" && did != "+"
}

// CheckPeers reports peers that are inert or contradictory.
//
// Nothing here blocks a load — LoadPeers accepts all of it — so everything but a
// peer with no address is a warning. The point is the silent failures: a peer
// with an unrecognized role matches neither AcceptsInbound nor AcceptsOutbound,
// so it sits in the file doing nothing, looking configured.
func CheckPeers(peers *PeerStore) dialplan.Problems {
	var ps dialplan.Problems
	if peers == nil {
		return ps
	}

	byHost := map[string][]string{}
	for i, p := range peers.All() {
		if p == nil {
			continue
		}
		path := fmt.Sprintf("peers[%d]", i)
		label := p.Name
		if label == "" {
			label = fmt.Sprintf("#%d", i)
		}

		if p.Name == "" {
			ps = append(ps, dialplan.Problem{
				Path:     path + ".name",
				Message:  "peer has no name; nothing in a log or a CDR will say which trunk a call used",
				Severity: dialplan.SeverityError,
			})
		}
		if p.Host == "" {
			ps = append(ps, dialplan.Problem{
				Path:     path + ".host",
				Message:  fmt.Sprintf("peer %s has no host, so it can neither be matched on ingress nor dialed on egress", label),
				Severity: dialplan.SeverityError,
			})
		}
		if p.Port <= 0 {
			ps = append(ps, dialplan.Problem{
				Path:     path + ".port",
				Message:  fmt.Sprintf("peer %s sets no port; the default is provider-specific, so say which one", label),
				Severity: dialplan.SeverityWarning,
			})
		}
		switch p.Role {
		case RoleInbound, RoleOutbound, RoleBoth:
		default:
			ps = append(ps, dialplan.Problem{
				Path: path + ".role",
				Message: fmt.Sprintf(
					"peer %s has role %q, which is neither %q, %q nor %q; it will accept no inbound "+
						"call and take no egress call", label, p.Role, RoleInbound, RoleOutbound, RoleBoth),
				Severity: dialplan.SeverityWarning,
			})
		}
		if p.Host != "" {
			byHost[p.Host] = append(byHost[p.Host], label)
		}
	}

	hosts := make([]string, 0, len(byHost))
	for host := range byHost {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	for _, host := range hosts {
		names := byHost[host]
		if len(names) < 2 {
			continue
		}
		ps = append(ps, dialplan.Problem{
			Path: "peers",
			Message: fmt.Sprintf(
				"peers %s share host %q; inbound matching takes the first one, so the others "+
					"never match", strings.Join(names, ", "), host),
			Severity: dialplan.SeverityWarning,
		})
	}

	return ps
}
