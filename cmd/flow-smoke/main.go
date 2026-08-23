// Command flow-smoke walks a tenant's flow against a fake call, feeding digits
// from stdin and printing the traversal.
//
// Authoring a graph otherwise means placing a call to find out what it does.
// This is the fast loop: change the JSON, press keys, read the path. The walk
// itself lives in internal/signaling/flow/flowsim, which is also what the
// signaling server's /api/v1/flow/simulate endpoint calls — one harness, so the
// CLI and the web UI cannot disagree about what a flow does.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/sebas/switchboard/internal/signaling/agent"
	"github.com/sebas/switchboard/internal/signaling/dialplan"
	"github.com/sebas/switchboard/internal/signaling/flow"
	"github.com/sebas/switchboard/internal/signaling/flow/flowsim"
)

func main() {
	routingPath := flag.String("routing-path", "resources/tenants", "Directory containing tenant configuration")
	policyPath := flag.String("policy-config", "resources/config/policy.json", "Path to policy configuration")
	tenant := flag.String("tenant", "devtenant", "Tenant to walk")
	dialed := flag.String("dialed", "", "Digits to dial (required)")
	digits := flag.String("digits", "", "Comma-separated digits to feed to menus, in order")
	direction := flag.String("direction", "internal", "Call direction: internal, inbound or outbound")
	verbose := flag.Bool("verbose", false, "Also print the engine's own log, which is where a denied destination is explained")
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

	src := flowsim.Sources{
		Routing: dialplan.StaticRouting{*tenant: table},
		Flows:   dialplan.StaticFlows{*tenant: set},
		Policy:  policyCfg,
	}

	fmt.Printf("dialing %q as %s for tenant %s\n\n", *dialed, *direction, *tenant)

	res, err := flowsim.Run(context.Background(), src, flowsim.Request{
		Tenant:    *tenant,
		Dialed:    *dialed,
		Direction: *direction,
		Digits:    scriptedDigits(*digits),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if res.Trace != nil {
		printTrace(*res.Trace)
	}

	if !res.Handled {
		fmt.Println("NOT ROUTED: nothing in the entry mapping matched.")
		fmt.Println("The call would fall through to the tenant operator.")
		if *verbose {
			printLog(res.Log)
		}
		os.Exit(1)
	}

	fmt.Println()
	report(res)
	if *verbose {
		printLog(res.Log)
	}
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

func report(res *flowsim.Result) {
	if len(res.Spoken) > 0 {
		fmt.Println("\nspoken:")
		for _, s := range res.Spoken {
			fmt.Printf("  %q\n", s)
		}
	}
	if len(res.Targets) > 0 {
		fmt.Println("\ndialed:")
		for _, d := range res.Targets {
			fmt.Printf("  %s\n", d)
		}
	}
	if len(res.Relayed) > 0 {
		fmt.Println("\nrelayed to caller:")
		for _, r := range res.Relayed {
			fmt.Printf("  %s\n", r)
		}
	}
}

// printLog shows the engine's reasoning, which is the only place a denied
// destination or an unwired exit is explained.
func printLog(records []string) {
	if len(records) == 0 {
		return
	}
	fmt.Println("\nengine log:")
	for _, r := range records {
		fmt.Printf("  %s\n", r)
	}
}
