// Command flow-smoke walks a tenant's flow against a fake call, feeding digits
// from stdin and printing the traversal.
//
// Authoring a graph otherwise means placing a call to find out what it does.
// This is the fast loop: change the JSON, press keys, read the path. It uses
// the real engine and the real validator, so what it shows is what a call would
// do — only the media and the SIP legs are faked.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/sebas/switchboard/internal/signaling/agent"
	"github.com/sebas/switchboard/internal/signaling/dialplan"
	"github.com/sebas/switchboard/internal/signaling/flow"
	"github.com/sebas/switchboard/internal/signaling/flow/flowtest"
)

func main() {
	routingPath := flag.String("routing-path", "resources/tenants", "Directory containing tenant configuration")
	policyPath := flag.String("policy-config", "resources/config/policy.json", "Path to policy configuration")
	tenant := flag.String("tenant", "devtenant", "Tenant to walk")
	dialed := flag.String("dialed", "", "Digits to dial (required)")
	digits := flag.String("digits", "", "Comma-separated digits to feed to menus, in order")
	direction := flag.String("direction", "internal", "Call direction: internal, inbound or outbound")
	flag.Parse()

	if *dialed == "" {
		fmt.Fprintln(os.Stderr, "error: --dialed is required")
		flag.Usage()
		os.Exit(2)
	}

	table, set, err := dialplan.LoadTenant(*routingPath, *tenant)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	policyCfg, err := agent.LoadPolicyConfig(*policyPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	routing := dialplan.StaticRouting{*tenant: table}
	engine := flow.New(flow.Config{
		Routing:  routing,
		Flows:    staticFlows{*tenant: set},
		Resolver: agent.NewResolver(routing, everyoneRegistered{}, nil, quiet()),
		BuildPolicy: func(cc agent.CallContext) *agent.Policy {
			return agent.NewPolicy(cc.Tenant,
				policyCfg.TenantPolicyFor(cc.Tenant, dialplan.SymbolicTargetsFor(routing, cc.Tenant)),
				quiet())
		},
		Trace:  flow.TraceFunc(printTrace),
		Logger: quiet(),
	})

	sess := flowtest.New()
	for _, d := range scriptedDigits(*digits) {
		sess.QueueDigits(d, agent.CollectMaxDigits)
	}

	cc := &agent.CallContext{
		Caller:    "102",
		Callee:    *dialed,
		Direction: agent.Direction(*direction),
		Tenant:    *tenant,
	}

	fmt.Printf("dialing %q as %s for tenant %s\n\n", *dialed, *direction, *tenant)
	handled := engine.Handle(context.Background(), sess, cc)

	if !handled {
		fmt.Println("NOT ROUTED: nothing in the entry mapping matched.")
		fmt.Println("The call would fall through to the tenant operator.")
		os.Exit(1)
	}

	fmt.Println()
	report(sess)
}

// scriptedDigits parses the --digits list, also accepting them from stdin so a
// session can be driven interactively.
func scriptedDigits(spec string) []string {
	if spec != "" {
		var out []string
		for _, d := range strings.Split(spec, ",") {
			if d = strings.TrimSpace(d); d != "" {
				out = append(out, d)
			}
		}
		return out
	}

	stat, err := os.Stdin.Stat()
	if err != nil || stat.Mode()&os.ModeCharDevice != 0 {
		// A terminal with no --digits: the caller presses nothing.
		return nil
	}

	var out []string
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			out = append(out, line)
		}
	}
	return out
}

func printTrace(t flow.Trace) {
	fmt.Printf("flow %q, %d hop(s), %dms\n", t.Flow, len(t.Hops), t.DurationMs())
	for i, h := range t.Hops {
		fmt.Printf("  %d. %-16s %-14s --%s--> %s\n", i+1, h.Node, "("+h.Type+")", h.Exit, h.Detail)
	}
	fmt.Printf("\npath: %s\noutcome: %s\n", t.Path, t.Outcome)
}

func report(sess *flowtest.Session) {
	if len(sess.Spoken) > 0 {
		fmt.Println("\nspoken:")
		for _, s := range sess.Spoken {
			fmt.Printf("  %q\n", s)
		}
	}
	if len(sess.Dialed) > 0 {
		fmt.Println("\ndialed:")
		for _, d := range sess.Dialed {
			fmt.Printf("  %s\n", d)
		}
	}
	if len(sess.Relayed) > 0 {
		fmt.Println("\nrelayed to caller:")
		for _, r := range sess.Relayed {
			fmt.Printf("  %s\n", r)
		}
	}
}

type staticFlows map[string]*dialplan.FlowSet

func (f staticFlows) TenantFlows(tenant string) (*dialplan.FlowSet, bool) {
	set, ok := f[tenant]
	return set, ok && set != nil
}

// everyoneRegistered treats every extension as reachable, since a smoke test is
// about the graph rather than who happens to be at their desk.
type everyoneRegistered struct{}

func (everyoneRegistered) IsRegistered(user, domain string) bool { return true }

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
