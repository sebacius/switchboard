package dialplan

import (
	"encoding/json"
	"strings"
	"testing"
)

// flowsFrom parses a flow file body, failing the test if it is not even JSON.
func flowsFrom(t *testing.T, body string) *FlowSet {
	t.Helper()
	var set FlowSet
	if err := json.Unmarshal([]byte(body), &set); err != nil {
		t.Fatalf("fixture is not valid JSON: %v", err)
	}
	return &set
}

// testTable is a routing table with the destinations the fixtures dial.
func testTable() *RoutingTable {
	return &RoutingTable{
		Operator:        "user/100",
		SymbolicTargets: map[string]string{"sales": "user/110", "answering-service": "+15558001234"},
		Groups: map[string]RingGroup{
			"claims": {Strategy: StrategySequential, Members: []string{"user/130"}},
		},
	}
}

// checkOne validates and returns the single error message, failing if the count
// is not exactly one — a test that asserts on "some error" tends to keep passing
// for the wrong reason.
func checkOne(t *testing.T, body string) string {
	t.Helper()
	ps := CheckFlows("acme", testTable(), flowsFrom(t, body))
	var errs Problems
	for _, p := range ps {
		if p.Severity == SeverityError {
			errs = append(errs, p)
		}
	}
	if len(errs) != 1 {
		t.Fatalf("expected exactly one error, got %d: %v", len(errs), errs)
	}
	return errs[0].Message
}

func mustBeValid(t *testing.T, body string) {
	t.Helper()
	if ps := CheckFlows("acme", testTable(), flowsFrom(t, body)); ps.HasErrors() {
		t.Fatalf("expected a valid flow, got: %v", ps)
	}
}

// The shape a real menu takes: greet, collect a digit, ring a group, fall back
// to a human, and hang up. If this does not validate, nothing else matters.
func TestRealisticMenuValidates(t *testing.T) {
	mustBeValid(t, `{"flows":{"main":{
		"start":"greeting",
		"nodes":{
			"greeting":{"type":"ivr","entry":{
				"prompt":{"text":"Press 1 for sales, 2 for claims.","voice":"alloy"},
				"timeout_ms":5000,"max_retries":2,"terminator":"#"},
				"exits":{"1":"ring-sales","2":"ring-claims",
					"timeout":"to-operator","invalid":"to-operator","retries_exceeded":"bye"}},
			"ring-sales":{"type":"dial_user","entry":{"target":"user/110","timeout_ms":20000},
				"exits":{"no_answer":"bye","busy":"bye","rejected":"bye","unavailable":"bye"}},
			"ring-claims":{"type":"dial_user","entry":{"target":"group/claims","timeout_ms":20000},
				"exits":{"no_answer":"to-operator","busy":"to-operator",
					"rejected":"to-operator","unavailable":"to-operator"}},
			"to-operator":{"type":"dial_user","entry":{"target":"user/100","timeout_ms":20000},
				"exits":{"no_answer":"bye","busy":"bye","rejected":"bye","unavailable":"bye"}},
			"bye":{"type":"hangup","entry":{"cause":"normal_clearing"}}
		}}}}`)
}

// The guarantee: a flow cannot loop between nodes, and the error names the path
// so it can be fixed rather than hunted for.
func TestInterNodeCycleIsRejectedWithItsPath(t *testing.T) {
	msg := checkOne(t, `{"flows":{"main":{
		"start":"a",
		"nodes":{
			"a":{"type":"tts","entry":{"text":"one"},"exits":{"done":"b"}},
			"b":{"type":"tts","entry":{"text":"two"},"exits":{"done":"a"}}
		}}}}`)

	if !strings.Contains(msg, "cycle") {
		t.Fatalf("error should name the problem as a cycle: %s", msg)
	}
	for _, want := range []string{"a", "b", "->"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error should show the cycle path, missing %q: %s", want, msg)
		}
	}
}

// Retrying inside a node is the sanctioned form of repetition, and must not be
// mistaken for a cycle.
func TestIVRRetryIsNotACycle(t *testing.T) {
	mustBeValid(t, `{"flows":{"main":{
		"start":"menu",
		"nodes":{
			"menu":{"type":"ivr","entry":{"prompt":{"text":"Press 1."},"max_retries":3},
				"exits":{"1":"bye","timeout":"bye","invalid":"bye","retries_exceeded":"bye"}},
			"bye":{"type":"hangup","entry":{}}
		}}}}`)
}

func TestUnwiredExitIsRejected(t *testing.T) {
	// dial_user must say what happens on every outcome; "busy" is missing.
	msg := checkOne(t, `{"flows":{"main":{
		"start":"d",
		"nodes":{
			"d":{"type":"dial_user","entry":{"target":"user/110"},
				"exits":{"no_answer":"bye","rejected":"bye","unavailable":"bye"}},
			"bye":{"type":"hangup","entry":{}}
		}}}}`)

	if !strings.Contains(msg, "busy") {
		t.Fatalf("error should name the unwired exit: %s", msg)
	}
}

func TestUndeclaredExitIsRejected(t *testing.T) {
	msg := checkOne(t, `{"flows":{"main":{
		"start":"d",
		"nodes":{
			"d":{"type":"tts","entry":{"text":"hi"},"exits":{"done":"bye","nope":"bye"}},
			"bye":{"type":"hangup","entry":{}}
		}}}}`)

	if !strings.Contains(msg, "nope") {
		t.Fatalf("error should name the bogus exit: %s", msg)
	}
}

// The terminal exits end the flow, so declaring one is a misunderstanding worth
// correcting at load rather than a branch that silently never fires.
func TestDeclaringATerminalExitIsRejected(t *testing.T) {
	msg := checkOne(t, `{"flows":{"main":{
		"start":"d",
		"nodes":{
			"d":{"type":"dial_user","entry":{"target":"user/110"},
				"exits":{"answered":"bye","no_answer":"bye","busy":"bye","rejected":"bye","unavailable":"bye"}},
			"bye":{"type":"hangup","entry":{}}
		}}}}`)

	if !strings.Contains(msg, "terminal") {
		t.Fatalf("error should explain that the exit is terminal: %s", msg)
	}
}

func TestDanglingExitTargetIsRejected(t *testing.T) {
	msg := checkOne(t, `{"flows":{"main":{
		"start":"a",
		"nodes":{"a":{"type":"tts","entry":{"text":"hi"},"exits":{"done":"ghost"}}}
	}}}`)

	if !strings.Contains(msg, "ghost") {
		t.Fatalf("error should name the missing node: %s", msg)
	}
}

func TestUnreachableNodeIsRejected(t *testing.T) {
	msg := checkOne(t, `{"flows":{"main":{
		"start":"a",
		"nodes":{
			"a":{"type":"hangup","entry":{}},
			"orphan":{"type":"hangup","entry":{}}
		}}}}`)

	if !strings.Contains(msg, "orphan") || !strings.Contains(msg, "unreachable") {
		t.Fatalf("error should name the unreachable node: %s", msg)
	}
}

// A typo in an entry field must not default the real field to zero. A menu that
// should wait five seconds silently waiting none is the exact failure this
// prevents.
func TestUnknownEntryFieldIsRejected(t *testing.T) {
	msg := checkOne(t, `{"flows":{"main":{
		"start":"m",
		"nodes":{
			"m":{"type":"ivr","entry":{"prompt":{"text":"hi"},"timout_ms":5000},
				"exits":{"1":"m2","timeout":"m2","invalid":"m2","retries_exceeded":"m2"}},
			"m2":{"type":"hangup","entry":{}}
		}}}}`)

	if !strings.Contains(msg, "timout_ms") {
		t.Fatalf("error should name the unknown field: %s", msg)
	}
}

func TestUnknownNodeTypeIsRejected(t *testing.T) {
	msg := checkOne(t, `{"flows":{"main":{
		"start":"a","nodes":{"a":{"type":"voicemail","entry":{}}}}}}`)

	if !strings.Contains(msg, "voicemail") {
		t.Fatalf("error should name the unknown type: %s", msg)
	}
	if !strings.Contains(msg, "dial_user") {
		t.Errorf("error should list the valid types so the operator knows the alternatives: %s", msg)
	}
}

// Capability narrowing, enforced at load: a flow file cannot express a raw
// external number no matter who edits it.
func TestDialExternalRejectsARawNumber(t *testing.T) {
	msg := checkOne(t, `{"flows":{"main":{
		"start":"d",
		"nodes":{
			"d":{"type":"dial_external","entry":{"target":"+15559991234"},
				"exits":{"no_answer":"bye","busy":"bye","denied":"bye","failed":"bye"}},
			"bye":{"type":"hangup","entry":{}}
		}}}}`)

	if !strings.Contains(msg, "symbolic") {
		t.Fatalf("error should explain that only symbolic names are dialable: %s", msg)
	}
}

func TestDialExternalAcceptsASymbolicTarget(t *testing.T) {
	mustBeValid(t, `{"flows":{"main":{
		"start":"d",
		"nodes":{
			"d":{"type":"dial_external","entry":{"target":"answering-service"},
				"exits":{"no_answer":"bye","busy":"bye","denied":"bye","failed":"bye"}},
			"bye":{"type":"hangup","entry":{}}
		}}}}`)
}

func TestDialUserRejectsAnUnknownGroup(t *testing.T) {
	msg := checkOne(t, `{"flows":{"main":{
		"start":"d",
		"nodes":{
			"d":{"type":"dial_user","entry":{"target":"group/nope"},
				"exits":{"no_answer":"bye","busy":"bye","rejected":"bye","unavailable":"bye"}},
			"bye":{"type":"hangup","entry":{}}
		}}}}`)

	if !strings.Contains(msg, "nope") {
		t.Fatalf("error should name the missing group: %s", msg)
	}
}

// A menu with no digit exits can only ever time out, which is never what was
// meant.
func TestIVRWithoutDigitExitsIsRejected(t *testing.T) {
	msg := checkOne(t, `{"flows":{"main":{
		"start":"m",
		"nodes":{
			"m":{"type":"ivr","entry":{"prompt":{"text":"hi"}},
				"exits":{"timeout":"bye","invalid":"bye","retries_exceeded":"bye"}},
			"bye":{"type":"hangup","entry":{}}
		}}}}`)

	if !strings.Contains(msg, "digit") {
		t.Fatalf("error should explain that no selection can be accepted: %s", msg)
	}
}

func TestPromptMustNameExactlyOneSource(t *testing.T) {
	msg := checkOne(t, `{"flows":{"main":{
		"start":"m",
		"nodes":{
			"m":{"type":"ivr","entry":{"prompt":{"text":"hi","file":"greeting.wav"}},
				"exits":{"1":"bye","timeout":"bye","invalid":"bye","retries_exceeded":"bye"}},
			"bye":{"type":"hangup","entry":{}}
		}}}}`)

	if !strings.Contains(msg, "more than one") {
		t.Fatalf("error should say the prompt is ambiguous: %s", msg)
	}
}

// Attended transfer is a separate feature, not a mode of this one. Silently
// performing a blind transfer instead would be the worst outcome.
func TestAttendedTransferIsRejectedByName(t *testing.T) {
	msg := checkOne(t, `{"flows":{"main":{
		"start":"t",
		"nodes":{
			"t":{"type":"transfer","entry":{"target":"user/100","mode":"attended"},
				"exits":{"failed":"bye"}},
			"bye":{"type":"hangup","entry":{}}
		}}}}`)

	if !strings.Contains(msg, "attended") {
		t.Fatalf("error should name the unsupported mode: %s", msg)
	}
}

// Every problem is reported, not just the first: a flow with four mistakes
// should take one pass to fix.
func TestEveryProblemIsReported(t *testing.T) {
	ps := CheckFlows("acme", testTable(), flowsFrom(t, `{"flows":{"main":{
		"start":"a",
		"nodes":{
			"a":{"type":"tts","entry":{"text":"hi"},"exits":{"done":"ghost"}},
			"b":{"type":"tts","entry":{"text":"two"},"exits":{"done":"nowhere"}}
		}}}}`))

	if len(ps) < 3 {
		t.Fatalf("expected the dangling targets and the unreachable node, got %d: %v", len(ps), ps)
	}
}

func TestFlowWithoutStartIsRejected(t *testing.T) {
	msg := checkOne(t, `{"flows":{"main":{"nodes":{"a":{"type":"hangup","entry":{}}}}}}`)
	if !strings.Contains(msg, "start") {
		t.Fatalf("error should mention the missing start: %s", msg)
	}
}

// Termination is the property the acyclicity rule exists to buy. It is not
// checked directly: "the graph is acyclic" plus "every non-terminal exit is
// wired" means every path must reach a node with no outgoing edges, and the only
// such nodes are hangup or a dial whose remaining exits all end the flow. This
// walks the shipped flow to assert that reasoning holds in practice.
func TestEveryPathTerminates(t *testing.T) {
	set := flowsFrom(t, `{"flows":{"main":{
		"start":"greeting",
		"nodes":{
			"greeting":{"type":"ivr","entry":{"prompt":{"text":"Press 1."},"max_retries":2},
				"exits":{"1":"ring","timeout":"bye","invalid":"bye","retries_exceeded":"bye"}},
			"ring":{"type":"dial_user","entry":{"target":"user/110"},
				"exits":{"no_answer":"bye","busy":"bye","rejected":"bye","unavailable":"bye"}},
			"bye":{"type":"hangup","entry":{}}
		}}}}`)
	if ps := CheckFlows("acme", testTable(), set); ps.HasErrors() {
		t.Fatalf("fixture should be valid: %v", ps)
	}

	flow := set.Flows["main"]
	for _, id := range sortedNodeIDs(flow) {
		// Walk from every node; an acyclic graph means this always halts.
		seen := reachableFrom(flow, id)
		endsSomewhere := false
		for reached := range seen {
			if len(edgesOf(flow, reached)) == 0 {
				endsSomewhere = true
				break
			}
		}
		if !endsSomewhere {
			t.Errorf("no path from %q reaches a node that ends the call", id)
		}
	}
}

// denyExternal is a Class of Service that permits internal targets only — the
// shipped default posture.
type denyExternal struct{ calls int }

func (d *denyExternal) Classify(resolved string) (bool, string) {
	d.calls++
	if strings.HasPrefix(resolved, "user/") {
		return true, ""
	}
	return false, "external dial not enabled for tenant"
}

// A flow that could never place its call should say so at load, not at 2am.
func TestClassOfServiceIsCheckedAtLoad(t *testing.T) {
	set := flowsFrom(t, `{"flows":{"main":{
		"start":"d",
		"nodes":{
			"d":{"type":"dial_external","entry":{"target":"answering-service"},
				"exits":{"no_answer":"bye","busy":"bye","denied":"bye","failed":"bye"}},
			"bye":{"type":"hangup","entry":{}}
		}}}}`)

	cos := &denyExternal{}
	ps := CheckFlowsWithPolicy("acme", testTable(), set, cos)

	if !ps.HasErrors() {
		t.Fatal("a destination the tenant may not dial must fail validation")
	}
	// The symbolic name resolves through the table before being adjudicated, so
	// the operator sees the number their policy actually refused.
	if !strings.Contains(ps.Err().Error(), "+15558001234") {
		t.Errorf("error should name the resolved destination: %v", ps.Err())
	}
}

// Each ring group member is adjudicated on its own merits rather than
// inheriting the group's verdict.
func TestGroupMembersAreClassifiedIndividually(t *testing.T) {
	table := testTable()
	table.Groups["mixed"] = RingGroup{
		Strategy: StrategySequential,
		Members:  []string{"user/130", "+15559990000"},
	}
	set := flowsFrom(t, `{"flows":{"main":{
		"start":"d",
		"nodes":{
			"d":{"type":"dial_user","entry":{"target":"group/mixed"},
				"exits":{"no_answer":"bye","busy":"bye","rejected":"bye","unavailable":"bye"}},
			"bye":{"type":"hangup","entry":{}}
		}}}}`)

	cos := &denyExternal{}
	ps := CheckFlowsWithPolicy("acme", table, set, cos)

	if !ps.HasErrors() {
		t.Fatal("the external group member must be refused")
	}
	if cos.calls != 2 {
		t.Errorf("expected both members to be classified individually, got %d calls", cos.calls)
	}
}

// Structural validation without a policy must not silently skip the dial checks
// it can still do.
func TestValidationWithoutPolicyStillChecksStructure(t *testing.T) {
	set := flowsFrom(t, `{"flows":{"main":{
		"start":"d",
		"nodes":{"d":{"type":"tts","entry":{"text":"hi"},"exits":{"done":"ghost"}}}
	}}}`)

	if ps := CheckFlowsWithPolicy("acme", testTable(), set, nil); !ps.HasErrors() {
		t.Fatal("a dangling exit must fail even with no policy configured")
	}
}
