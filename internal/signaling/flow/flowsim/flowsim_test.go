package flowsim

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/sebas/switchboard/internal/signaling/agent"
	"github.com/sebas/switchboard/internal/signaling/dialplan"
)

// testSources builds a tenant to simulate against, validated the way the loader
// would validate it so a broken fixture fails here rather than misleading a test.
func testSources(t *testing.T) Sources {
	t.Helper()

	table := &dialplan.RoutingTable{
		Operator: "user/100",
		Extensions: dialplan.Entries(map[string]string{
			"100": "flow/main",
			"110": "user/110",
		}),
		SymbolicTargets: map[string]string{"support": "user/120"},
		Groups: map[string]dialplan.RingGroup{
			"claims": {Strategy: dialplan.StrategySequential, Members: []string{"user/130"}},
		},
	}

	var set dialplan.FlowSet
	if err := json.Unmarshal([]byte(menuFlow), &set); err != nil {
		t.Fatalf("fixture flows: %v", err)
	}
	if err := dialplan.ValidateFlows("acme", table, &set); err != nil {
		t.Fatalf("fixture is not valid: %v", err)
	}

	routing := dialplan.StaticRouting{"acme": table}
	return Sources{
		Routing: routing,
		Flows:   dialplan.StaticFlows{"acme": &set},
		Policy:  &agent.PolicyConfig{Tenants: map[string]agent.TenantConfig{}},
	}
}

const menuFlow = `{"flows":{"main":{
	"start":"greeting",
	"nodes":{
		"greeting":{"type":"ivr","entry":{"prompt":{"text":"press one"},"max_retries":1},
			"exits":{"1":"claims","timeout":"bye","invalid":"bye","retries_exceeded":"bye"}},
		"claims":{"type":"dial_user","entry":{"target":"group/claims"},
			"exits":{"no_answer":"bye","busy":"bye","rejected":"bye","unavailable":"bye"}},
		"bye":{"type":"hangup","entry":{}}
	}}}}`

func TestFlowTraversalIsReported(t *testing.T) {
	src := testSources(t)

	res, err := Run(context.Background(), src, Request{
		Tenant: "acme", Dialed: "100", Digits: []string{"1"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Routed != RoutedFlow {
		t.Fatalf("expected a flow traversal, got %q (note %q)", res.Routed, res.Note)
	}
	if res.Trace == nil || len(res.Trace.Hops) == 0 {
		t.Fatal("a flow traversal must carry its hops")
	}
	// An IVR node plays its prompt as part of CollectDigits — one operation, so
	// a digit pressed during the prompt is not lost — so the prompt shows up in
	// Collects rather than in Spoken.
	if len(res.Collects) == 0 || res.Collects[0].Prompt.Text != "press one" {
		t.Errorf("the menu prompt should have been collected with: %+v", res.Collects)
	}
	if len(res.Targets) == 0 || res.Targets[0] != "user/130" {
		t.Errorf("choice 1 should ring the claims group: %v", res.Targets)
	}
	if len(res.Events) == 0 {
		t.Error("the ordered event log should describe the call")
	}
}

// A bare destination in the entry mapping is handled without entering a graph,
// so it emits no trace. That is a fact about the engine, and the result has to
// say so rather than looking like an empty traversal.
func TestDirectDialIsDistinguishedFromAnEmptyFlow(t *testing.T) {
	src := testSources(t)

	res, err := Run(context.Background(), src, Request{Tenant: "acme", Dialed: "110"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Handled {
		t.Fatal("a mapped extension must be handled")
	}
	if res.Routed != RoutedDirect {
		t.Errorf("expected a direct dial, got %q", res.Routed)
	}
	if res.Trace != nil {
		t.Error("a direct dial enters no graph, so it has no trace")
	}
	if res.Note == "" {
		t.Error("the result must explain why there is no traversal")
	}
}

func TestUnmatchedCallNamesTheOperator(t *testing.T) {
	src := testSources(t)

	res, err := Run(context.Background(), src, Request{Tenant: "acme", Dialed: "999"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Handled || res.Routed != RoutedNone {
		t.Fatalf("999 matches nothing: %+v", res.Routed)
	}
	if !strings.Contains(res.Note, "user/100") {
		t.Errorf("the note should say where the call actually goes: %q", res.Note)
	}
}

func TestUnknownTenantIsTyped(t *testing.T) {
	src := testSources(t)

	_, err := Run(context.Background(), src, Request{Tenant: "ghost", Dialed: "100"})
	if err == nil {
		t.Fatal("an unknown tenant must be an error")
	}
	if !strings.Contains(err.Error(), "unknown tenant") {
		t.Errorf("the error should be classifiable: %v", err)
	}
}

// Retrieval is the one path that could touch live state. With no Resolver the
// engine declines it before reaching a parking service, so the call simply falls
// through to the entry mapping.
func TestRetrievalIsDeclinedRatherThanAttempted(t *testing.T) {
	src := testSources(t)

	res, err := Run(context.Background(), src, Request{Tenant: "acme", Dialed: "*701"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Handled {
		t.Errorf("retrieval is not simulated, so *701 should match nothing: %+v", res)
	}
}

// The engine's reasoning reaches the caller. A denied destination is explained
// in the log and nowhere else, so losing it would leave "outcome: hangup" with
// no reason attached.
func TestEngineLogIsCaptured(t *testing.T) {
	src := testSources(t)

	res, err := Run(context.Background(), src, Request{
		Tenant: "acme", Dialed: "100", Digits: []string{"1"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Log) == 0 {
		t.Fatal("the engine log should have been captured")
	}
	joined := strings.Join(res.Log, "\n")
	if !strings.Contains(joined, "flow") {
		t.Errorf("the log should describe the traversal: %s", joined)
	}
}

func TestBadDirectionIsRejected(t *testing.T) {
	src := testSources(t)

	if _, err := Run(context.Background(), src, Request{
		Tenant: "acme", Dialed: "100", Direction: "sideways",
	}); err == nil {
		t.Fatal("an unknown direction must be refused")
	}
}

// Simulations share the sources and must not share anything else. The engine
// tracks active calls by ID, so identical IDs would have concurrent runs collide.
func TestConcurrentRunsAreIndependent(t *testing.T) {
	src := testSources(t)

	var wg sync.WaitGroup
	ids := make([]string, 50)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := Run(context.Background(), src, Request{
				Tenant: "acme", Dialed: "100", Digits: []string{"1"},
			})
			if err != nil {
				t.Errorf("Run: %v", err)
				return
			}
			ids[i] = res.CallID
		}(i)
	}
	wg.Wait()

	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" {
			continue
		}
		if seen[id] {
			t.Fatalf("two simulations shared call ID %q", id)
		}
		seen[id] = true
	}
}
