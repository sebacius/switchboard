package flow

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/sebas/switchboard/internal/signaling/agent"
	"github.com/sebas/switchboard/internal/signaling/dialplan"
	"github.com/sebas/switchboard/internal/signaling/flow/flowtest"
)

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// testEngine builds an engine over a tenant with the given entry mapping and
// flows, and a policy that permits internal destinations.
func testEngine(t *testing.T, extensions map[string]string, flowsJSON string) *Engine {
	t.Helper()

	table := &dialplan.RoutingTable{
		Operator:        "user/100",
		Extensions:      dialplan.Entries(extensions),
		SymbolicTargets: map[string]string{"sales": "user/110", "afterhours": "+18005551212"},
		Groups: map[string]dialplan.RingGroup{
			"claims": {Strategy: dialplan.StrategySequential, Members: []string{"user/130", "user/131"}},
		},
	}

	var set dialplan.FlowSet
	if flowsJSON != "" {
		if err := json.Unmarshal([]byte(flowsJSON), &set); err != nil {
			t.Fatalf("fixture is not valid JSON: %v", err)
		}
		if err := dialplan.ValidateFlows("acme", table, &set); err != nil {
			t.Fatalf("fixture must be a valid flow: %v", err)
		}
	}

	routing := dialplan.StaticRouting{"acme": table}
	return New(Config{
		Routing: routing,
		Flows:   dialplan.StaticFlows{"acme": &set},
		BuildPolicy: func(cc agent.CallContext) *agent.Policy {
			return agent.NewPolicy(cc.Tenant, agent.TenantPolicy{
				AllowExternalDial:      true,
				ExternalAllowlist:      []string{"+1800"},
				MaxExternalUnitsPerDay: 10,
				SymbolicTargets:        table.SymbolicTargets,
			}, quiet())
		},
		Logger: quiet(),
	})
}

func internalCall(callee string) *agent.CallContext {
	return &agent.CallContext{
		Caller: "102", Callee: callee,
		Direction: agent.DirectionInternal, Tenant: "acme",
	}
}

const menuFlow = `{"flows":{"main":{
	"start":"greeting",
	"nodes":{
		"greeting":{"type":"ivr","entry":{
			"prompt":{"text":"Press 1 for sales, 2 for claims."},
			"timeout_ms":5000,"max_retries":1},
			"exits":{"1":"ring-sales","2":"ring-claims",
				"timeout":"operator","invalid":"operator","retries_exceeded":"operator"}},
		"ring-sales":{"type":"dial_user","entry":{"target":"user/110","timeout_ms":20000},
			"exits":{"no_answer":"voicemail-ish","busy":"voicemail-ish",
				"rejected":"voicemail-ish","unavailable":"voicemail-ish"}},
		"ring-claims":{"type":"dial_user","entry":{"target":"group/claims","timeout_ms":20000},
			"exits":{"no_answer":"operator","busy":"operator",
				"rejected":"operator","unavailable":"operator"}},
		"operator":{"type":"dial_user","entry":{"target":"user/100","timeout_ms":20000},
			"exits":{"no_answer":"bye","busy":"bye","rejected":"bye","unavailable":"bye"}},
		"voicemail-ish":{"type":"tts","entry":{"text":"Sorry, nobody is available."},
			"exits":{"done":"bye"}},
		"bye":{"type":"hangup","entry":{"cause":"normal_clearing"}}
	}}}}`

// A digit takes the exit it names.
func TestMenuDigitSelection(t *testing.T) {
	e := testEngine(t, map[string]string{"100": "flow/main"}, menuFlow)
	sess := flowtest.New().
		QueueDigits("1", agent.CollectMaxDigits).
		QueueDial(agent.DialAnswered, 200)

	if !e.Handle(context.Background(), sess, internalCall("100")) {
		t.Fatal("the engine must own a call that entered a flow")
	}
	if len(sess.Dialed) != 1 || sess.Dialed[0] != "user/110" {
		t.Fatalf("pressing 1 should ring sales, dialed %v", sess.Dialed)
	}
}

// Pressing nothing takes the timeout exit, which a flow routes separately from
// invalid input.
func TestMenuTimeoutTakesTimeoutExit(t *testing.T) {
	e := testEngine(t, map[string]string{"100": "flow/main"}, menuFlow)
	// Two attempts: the node retries once, then gives up.
	sess := flowtest.New().
		QueueDigits("", agent.CollectFirstDigitTimeout).
		QueueDigits("", agent.CollectFirstDigitTimeout).
		QueueDial(agent.DialAnswered, 200)

	e.Handle(context.Background(), sess, internalCall("100"))

	if len(sess.Dialed) != 1 || sess.Dialed[0] != "user/100" {
		t.Fatalf("a timed-out menu should reach the operator, dialed %v", sess.Dialed)
	}
}

// A digit the menu does not declare is invalid input, and the node re-prompts up
// to its bound before giving up.
func TestMenuRetryExhaustion(t *testing.T) {
	e := testEngine(t, map[string]string{"100": "flow/main"}, menuFlow)
	sess := flowtest.New().
		QueueDigits("9", agent.CollectMaxDigits).
		QueueDigits("9", agent.CollectMaxDigits).
		QueueDial(agent.DialAnswered, 200)

	e.Handle(context.Background(), sess, internalCall("100"))

	if len(sess.Collects) != 2 {
		t.Fatalf("max_retries 1 means two attempts, got %d", len(sess.Collects))
	}
	// The second attempt discards type-ahead, since the question was re-asked.
	if !sess.Collects[1].FlushBuffer {
		t.Error("a re-prompt must flush digits aimed at the previous prompt")
	}
	if len(sess.Dialed) != 1 || sess.Dialed[0] != "user/100" {
		t.Fatalf("after exhausting retries the caller should reach the operator, dialed %v", sess.Dialed)
	}
}

// THE test for this change, on the PRE-ANSWER path where relaying is possible.
//
// A flow whose first node dials has not answered, so the dial forwards. If it
// relayed the callee's 486 the caller's call would be over and the next node
// could never run — which is exactly why ForwardOutcome exists.
func TestPreAnswerDialFailureRelaysNothing(t *testing.T) {
	const dialFirst = `{"flows":{"main":{
		"start":"ring",
		"nodes":{
			"ring":{"type":"dial_user","entry":{"target":"user/110","timeout_ms":20000},
				"exits":{"no_answer":"apology","busy":"apology",
					"rejected":"apology","unavailable":"apology"}},
			"apology":{"type":"tts","entry":{"text":"Nobody is available."},"exits":{"done":"bye"}},
			"bye":{"type":"hangup","entry":{}}
		}}}}`

	e := testEngine(t, map[string]string{"100": "flow/main"}, dialFirst)
	sess := flowtest.New().QueueDial(agent.DialBusy, 486)

	e.Handle(context.Background(), sess, internalCall("100"))

	if len(sess.Relayed) != 0 {
		t.Fatalf("a flow that continues must relay nothing, relayed %v", sess.Relayed)
	}
	if len(sess.Spoken) != 1 {
		t.Fatalf("the busy exit should reach the apology node, spoke %v", sess.Spoken)
	}
}

// The same rule after a media node, where the dial bridges instead of
// forwarding: the outcome must still be classified rather than collapsed.
func TestPostAnswerDialFailureContinues(t *testing.T) {
	e := testEngine(t, map[string]string{"100": "flow/main"}, menuFlow)
	sess := flowtest.New().
		QueueDigits("1", agent.CollectMaxDigits).
		QueueDial(agent.DialBusy, 486)

	e.Handle(context.Background(), sess, internalCall("100"))

	if len(sess.Relayed) != 0 {
		t.Fatalf("a flow that continues must relay nothing to the caller, relayed %v", sess.Relayed)
	}
	if len(sess.Spoken) != 1 || !strings.Contains(sess.Spoken[0], "nobody is available") {
		t.Fatalf("the busy exit should reach the apology node, spoke %v", sess.Spoken)
	}
	if len(sess.Hangups) == 0 {
		t.Error("the flow should end at the hangup node")
	}
}

// Each dial outcome takes its own exit.
func TestDialOutcomesTakeTheirExits(t *testing.T) {
	cases := []struct {
		name   string
		result agent.DialResult
	}{
		{"busy", agent.DialBusy},
		{"no answer", agent.DialNoAnswer},
		{"rejected", agent.DialRejected},
		{"unavailable", agent.DialUnavailable},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := testEngine(t, map[string]string{"100": "flow/main"}, menuFlow)
			sess := flowtest.New().
				QueueDigits("1", agent.CollectMaxDigits).
				QueueDial(tc.result, 0)

			e.Handle(context.Background(), sess, internalCall("100"))

			// Every failure exit on ring-sales leads to the same apology node.
			if len(sess.Spoken) != 1 {
				t.Fatalf("%s should reach the apology node, spoke %v", tc.name, sess.Spoken)
			}
		})
	}
}

// A ring group tries its members, moving on when one does not answer. After a
// menu the call is answered, so members are tried in order rather than rung
// together — there is no forward path once media is owned.
func TestRingGroupTriesEachMember(t *testing.T) {
	e := testEngine(t, map[string]string{"100": "flow/main"}, menuFlow)
	sess := flowtest.New().
		QueueDigits("2", agent.CollectMaxDigits).
		QueueDial(agent.DialBusy, 486). // user/130 is on the phone
		QueueDial(agent.DialAnswered, 200)

	e.Handle(context.Background(), sess, internalCall("100"))

	if len(sess.Dialed) != 2 {
		t.Fatalf("a busy first member must not end the group, dialed %v", sess.Dialed)
	}
	if sess.Dialed[0] != "user/130" || sess.Dialed[1] != "user/131" {
		t.Errorf("members should be tried in configured order, got %v", sess.Dialed)
	}
}

// Every member being unreachable takes the group's failure exit rather than
// connecting the caller to silence.
func TestRingGroupExhaustedTakesFailureExit(t *testing.T) {
	e := testEngine(t, map[string]string{"100": "flow/main"}, menuFlow)
	sess := flowtest.New().
		QueueDigits("2", agent.CollectMaxDigits).
		QueueDial(agent.DialBusy, 486).
		QueueDial(agent.DialBusy, 486).
		QueueDial(agent.DialAnswered, 200) // the operator picks up

	e.Handle(context.Background(), sess, internalCall("100"))

	if len(sess.Dialed) != 3 {
		t.Fatalf("both members then the operator should be tried, dialed %v", sess.Dialed)
	}
	if sess.Dialed[2] != "user/100" {
		t.Errorf("an exhausted group should reach the operator, got %v", sess.Dialed)
	}
}

// A caller who hangs up mid-menu must unwind, and the cursor must be released.
func TestCallerAbandonsMidMenu(t *testing.T) {
	e := testEngine(t, map[string]string{"100": "flow/main"}, menuFlow)
	sess := flowtest.New().QueueDigits("", agent.CollectCanceled)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the caller is already gone

	e.Handle(ctx, sess, internalCall("100"))

	if got := e.Active(); len(got) != 0 {
		t.Fatalf("the cursor must be released when a caller abandons, still active: %v", got)
	}
}

// A bare destination behaves exactly as deterministic resolution always did.
func TestBareDestinationForwards(t *testing.T) {
	e := testEngine(t, map[string]string{"110": "user/110"}, "")
	sess := flowtest.New()

	if !e.Handle(context.Background(), sess, internalCall("110")) {
		t.Fatal("a bare destination must be handled")
	}
	if len(sess.Dialed) != 1 || sess.Dialed[0] != "user/110" {
		t.Fatalf("dialed %v, want user/110", sess.Dialed)
	}
	if sess.HasAnswered() {
		t.Error("a one-node dial must not answer the call")
	}
}

// Nothing mapped means the engine declines, letting the caller decide.
func TestUnmappedDestinationDeclines(t *testing.T) {
	e := testEngine(t, map[string]string{"110": "user/110"}, "")

	if e.Handle(context.Background(), flowtest.New(), internalCall("999")) {
		t.Fatal("an unmapped destination must not be claimed")
	}
}

// The traversal is recorded, because "why did this caller end up here" has no
// other answer.
func TestTraversalIsRecorded(t *testing.T) {
	var traces []Trace
	e := testEngine(t, map[string]string{"100": "flow/main"}, menuFlow)
	e.cfg.Trace = TraceFunc(func(tr Trace) { traces = append(traces, tr) })

	sess := flowtest.New().
		QueueDigits("1", agent.CollectMaxDigits).
		QueueDial(agent.DialBusy, 486)

	e.Handle(context.Background(), sess, internalCall("100"))

	if len(traces) != 1 {
		t.Fatalf("expected one trace, got %d", len(traces))
	}
	tr := traces[0]
	for _, want := range []string{"greeting", "ring-sales", "voicemail-ish", "bye"} {
		if !strings.Contains(tr.Path, want) {
			t.Errorf("path %q should include %q", tr.Path, want)
		}
	}
	// The detail says WHY, not just where.
	var sawBusy bool
	for _, h := range tr.Hops {
		if h.Exit == "busy" && strings.Contains(h.Detail, "486") {
			sawBusy = true
		}
	}
	if !sawBusy {
		t.Errorf("the record should show the busy exit and its SIP code: %+v", tr.Hops)
	}
}

// A denied external destination takes the denied exit rather than dialing.
func TestDeniedExternalTakesDeniedExit(t *testing.T) {
	const flowJSON = `{"flows":{"main":{
		"start":"out",
		"nodes":{
			"out":{"type":"dial_external","entry":{"target":"blocked","timeout_ms":10000},
				"exits":{"no_answer":"bye","busy":"bye","denied":"apology","failed":"bye"}},
			"apology":{"type":"tts","entry":{"text":"That is not permitted."},"exits":{"done":"bye"}},
			"bye":{"type":"hangup","entry":{}}
		}}}}`

	table := &dialplan.RoutingTable{
		Extensions:      dialplan.Entries(map[string]string{"100": "flow/main"}),
		SymbolicTargets: map[string]string{"blocked": "+19005551212"},
	}
	var set dialplan.FlowSet
	if err := json.Unmarshal([]byte(flowJSON), &set); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	e := New(Config{
		Routing: dialplan.StaticRouting{"acme": table},
		Flows:   dialplan.StaticFlows{"acme": &set},
		BuildPolicy: func(cc agent.CallContext) *agent.Policy {
			// External dialing off: the shipped default posture.
			return agent.NewPolicy(cc.Tenant, agent.TenantPolicy{
				SymbolicTargets: table.SymbolicTargets,
			}, quiet())
		},
		Logger: quiet(),
	})

	sess := flowtest.New()
	e.Handle(context.Background(), sess, internalCall("100"))

	if len(sess.Dialed) != 0 {
		t.Fatalf("a denied destination must not be dialed, dialed %v", sess.Dialed)
	}
	if len(sess.Spoken) != 1 {
		t.Fatalf("the denied exit should be taken, spoke %v", sess.Spoken)
	}
}

// A leg with no DTMF transport degrades by a declared exit rather than waiting
// for digits that cannot arrive.
func TestNoDTMFTransportDegrades(t *testing.T) {
	e := testEngine(t, map[string]string{"100": "flow/main"}, menuFlow)
	sess := flowtest.New().
		QueueDigits("", agent.CollectNoDTMFTransport).
		QueueDial(agent.DialAnswered, 200)

	e.Handle(context.Background(), sess, internalCall("100"))

	if len(sess.Collects) != 1 {
		t.Fatalf("a leg with no DTMF must not be re-prompted, collected %d times", len(sess.Collects))
	}
	if len(sess.Dialed) != 1 || sess.Dialed[0] != "user/100" {
		t.Fatalf("it should fall through to the operator, dialed %v", sess.Dialed)
	}
}

// A denied destination must be auditable per-call, not only in process output:
// the path and why it was permitted have to read together.
func TestDecisionsAreRecordedAgainstTheCall(t *testing.T) {
	const flowJSON = `{"flows":{"main":{
		"start":"ring",
		"nodes":{
			"ring":{"type":"dial_user","entry":{"target":"user/110","timeout_ms":20000},
				"exits":{"no_answer":"bye","busy":"bye","rejected":"bye","unavailable":"bye"}},
			"bye":{"type":"hangup","entry":{}}
		}}}}`

	var traces []Trace
	e := testEngine(t, map[string]string{"100": "flow/main"}, flowJSON)
	e.cfg.Trace = TraceFunc(func(tr Trace) { traces = append(traces, tr) })

	sess := flowtest.New().QueueDial(agent.DialAnswered, 200)
	e.Handle(context.Background(), sess, internalCall("100"))

	if len(traces) != 1 {
		t.Fatalf("expected one trace, got %d", len(traces))
	}
	if len(traces[0].Decisions) == 0 {
		t.Fatal("the authorization verdict should be recorded against the call")
	}
	d := traces[0].Decisions[0]
	if d.Target != "user/110" || !d.Allowed {
		t.Errorf("expected an allow for user/110, got %+v", d)
	}
}
