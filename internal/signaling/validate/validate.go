// Package validate implements the `validate` subcommand.
//
// It shares its checks with the loader rather than reimplementing them, so
// "validate passes but the server refuses to start" is impossible by
// construction. The difference is only in reporting: the loader fails on the
// first problem because it has a call to route, while an operator fixing a
// thirty-node flow wants every problem at once.
package validate

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sebas/switchboard/internal/signaling/agent"
	"github.com/sebas/switchboard/internal/signaling/dialplan"
	"github.com/sebas/switchboard/internal/signaling/trunk"
)

// say writes a line of report output.
//
// A write failure here is not actionable: the destination is the caller's
// writer, usually stdout, and a validator that abandoned its report because a
// pipe closed would be less useful than one that finished. The error is dropped
// deliberately, in one place, rather than at every call site.
func say(out io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(out, format, args...)
}

// Exit codes, following the usual convention so CI can branch on them.
const (
	ExitOK      = 0
	ExitProblem = 1
	ExitUsage   = 2
)

// Run validates a configuration directory and reports every problem.
func Run(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(out)

	routingPath := fs.String("routing-path", "resources/tenants",
		"Directory containing per-tenant <tenant>.routing.json and <tenant>.flows.json files")
	policyPath := fs.String("policy-config", "resources/config/policy.json",
		"Path to tenant Class-of-Service and channel-limit configuration")
	routesPath := fs.String("routes-path", "resources/config/routes.json",
		"Path to the DID -> tenant table")
	quiet := fs.Bool("quiet", false, "Print problems only")

	fs.Usage = func() {
		say(out, "Usage: switchboard-signaling validate [flags]\n")
		say(out, "\nChecks tenant routing tables and flow graphs without starting the server.\n")
		say(out, "\nFlags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}

	problems, err := check(*routingPath, *policyPath, *routesPath)
	if err != nil {
		say(out, "error: %v\n", err)
		return ExitProblem
	}

	return report(out, problems, *routingPath, *quiet)
}

// check loads every tenant and validates it, collecting problems rather than
// stopping at the first.
func check(routingPath, policyPath, routesPath string) (dialplan.Problems, error) {
	policyCfg, err := agent.LoadPolicyConfig(policyPath)
	if err != nil {
		return nil, fmt.Errorf("load policy config: %w", err)
	}

	entries, err := os.ReadDir(routingPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", routingPath, err)
	}

	tenants := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		switch {
		case strings.HasSuffix(name, ".routing.json"):
			tenants[strings.TrimSuffix(name, ".routing.json")] = true
		case strings.HasSuffix(name, ".flows.json"):
			tenants[strings.TrimSuffix(name, ".flows.json")] = true
		}
	}

	names := make([]string, 0, len(tenants))
	for t := range tenants {
		names = append(names, t)
	}
	sort.Strings(names)

	var problems dialplan.Problems
	tables := map[string]*dialplan.RoutingTable{}
	for _, tenant := range names {
		table, set, err := dialplan.ParseTenant(routingPath, tenant)
		if err != nil {
			problems = append(problems, dialplan.Problem{
				Tenant:   tenant,
				Path:     filepath.Join(routingPath, tenant),
				Message:  err.Error(),
				Severity: dialplan.SeverityError,
			})
			continue
		}
		tables[tenant] = table

		// Class of Service is checked with the same policy the call path uses,
		// through the side-effect-free classifier so validating a configuration
		// cannot spend the tenant's daily budget.
		policy := agent.NewPolicy(tenant,
			policyCfg.TenantPolicyFor(tenant, dialplan.SymbolicTargetsFor(
				dialplan.StaticRouting{tenant: table}, tenant)), nil)

		problems = append(problems,
			dialplan.CheckFlowsWithPolicy(tenant, table, set, policyClassifier{policy})...)
	}

	problems = append(problems, checkRoutes(routesPath, tables)...)
	return problems, nil
}

// checkRoutes loads the DID table and cross-checks it against the tenants that
// exist. The cross-check itself lives in the trunk package, so this command and
// the config API cannot disagree about what a bad DID table looks like.
func checkRoutes(routesPath string, tables map[string]*dialplan.RoutingTable) dialplan.Problems {
	routes, err := trunk.LoadRoutes(routesPath)
	if err != nil {
		return dialplan.Problems{{
			Path:     routesPath,
			Message:  err.Error(),
			Severity: dialplan.SeverityError,
		}}
	}
	return trunk.CheckRoutes(routes, tables, routesPath)
}

// policyClassifier adapts agent.Policy to the validator's seam.
type policyClassifier struct{ p *agent.Policy }

func (c policyClassifier) Classify(resolved string) (bool, string) {
	d := c.p.Classify(resolved)
	return d.Allowed, d.Reason
}

// report prints the findings and returns the process exit code.
func report(out io.Writer, problems dialplan.Problems, routingPath string, quiet bool) int {
	var errors, warnings int
	for _, p := range problems {
		switch p.Severity {
		case dialplan.SeverityError:
			errors++
			say(out, "error: %s: %s\n  %s\n", p.Tenant, p.Path, p.Message)
		case dialplan.SeverityWarning:
			warnings++
			say(out, "warning: %s: %s\n  %s\n", p.Tenant, p.Path, p.Message)
		}
	}

	if errors > 0 {
		say(out, "\n%d error(s), %d warning(s) in %s\n", errors, warnings, routingPath)
		return ExitProblem
	}
	if !quiet {
		if warnings > 0 {
			say(out, "\nno errors, %d warning(s) in %s\n", warnings, routingPath)
		} else {
			say(out, "%s is valid\n", routingPath)
		}
	}
	return ExitOK
}
