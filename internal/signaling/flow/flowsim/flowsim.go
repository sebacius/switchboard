// Package flowsim walks a tenant's flow against a fake call.
//
// Authoring a graph otherwise means placing a call to find out what it does.
// This is the fast loop: change the JSON, feed digits, read the path. It uses
// the real engine, the real routing table and the real policy, so what it shows
// is what a call would do — only the media and the SIP legs are faked.
//
// It is deliberately inert, and each of these is a property the code can be
// checked against rather than a promise:
//
//   - No Parking service. Engine.Handle tries *NNN retrieval before anything
//     else, and against a live park service a simulated retrieval would remove a
//     real caller from their slot — and then block forever, because the fake
//     session's context is never canceled.
//   - No Resolver either, which is what makes the above true by construction:
//     retrieval returns before it can reach the parking service at all.
//   - A LEDGER-FREE policy. The authorization verdict is identical; what is
//     dropped is the spend counter, so simulating an external dial cannot burn a
//     tenant's daily budget. A validator that denies service to the thing it
//     validates is worse than no validator.
//   - A throwaway Engine per run. The live engine tracks active calls by call ID,
//     and simulations must not appear in it.
//   - A captured logger. A fake call must not write to the production log — and
//     the engine's log is the only place several outcomes are explained, so the
//     capture is a feature, not just containment.
package flowsim

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sebas/switchboard/internal/signaling/agent"
	"github.com/sebas/switchboard/internal/signaling/dialplan"
	"github.com/sebas/switchboard/internal/signaling/flow"
	"github.com/sebas/switchboard/internal/signaling/flow/flowtest"
)

// ErrUnknownTenant means the tenant has no routing table loaded.
var ErrUnknownTenant = errors.New("unknown tenant")

// MaxDigits bounds a scripted caller. A menu is bounded by its retry counter,
// so a longer script than this describes no call anyone will place.
const MaxDigits = 32

// Timeout bounds one simulation. Every faked operation returns immediately, so
// this only fires on a graph defect — but a handler must not be pinned by one.
const Timeout = 5 * time.Second

// Request is one simulated call.
type Request struct {
	Tenant    string   `json:"tenant"`
	Dialed    string   `json:"dialed"`
	Direction string   `json:"direction"`        // internal|inbound|outbound; default internal
	Caller    string   `json:"caller,omitempty"` // default "102"
	Digits    []string `json:"digits,omitempty"` // one entry per collection, in order
}

// Sources is the configuration to simulate against. Everything here is read
// only; a simulation never writes to any of it.
type Sources struct {
	Routing dialplan.RoutingSource
	Flows   dialplan.FlowSource
	Policy  *agent.PolicyConfig
}

// Result is what the call did.
type Result struct {
	// Handled reports whether the engine owned the call at all.
	Handled bool `json:"handled"`
	// Routed says HOW: "flow" entered a graph, "direct" was a one-node dial from
	// the entry mapping, "none" matched nothing. A direct dial produces no trace,
	// which is a fact about the engine rather than an empty result.
	Routed     string      `json:"routed"`
	Tenant     string      `json:"tenant"`
	Dialed     string      `json:"dialed"`
	Direction  string      `json:"direction"`
	CallID     string      `json:"call_id"`
	Trace      *flow.Trace `json:"trace,omitempty"`
	DurationMs int64       `json:"duration_ms"`
	// Note explains an outcome the trace cannot, such as where an unmatched call
	// would actually go.
	Note string `json:"note,omitempty"`

	Spoken   []string               `json:"spoken"`
	Played   []string               `json:"played"`
	Targets  []string               `json:"dialed_targets"`
	Relayed  []string               `json:"relayed"`
	Hangups  []string               `json:"hangups"`
	Collects []agent.CollectRequest `json:"collects"`
	Events   []flowtest.Event       `json:"events"`
	// Log is the engine's own reasoning. Several outcomes — a destination denied
	// by policy, an exit that is not wired, an entry naming a flow that does not
	// exist — are explained here and nowhere else.
	Log []string `json:"log"`
}

const (
	RoutedFlow   = "flow"
	RoutedDirect = "direct"
	RoutedNone   = "none"
)

// Run walks one simulated call and returns what happened.
func Run(ctx context.Context, src Sources, req Request) (*Result, error) {
	if src.Routing == nil {
		return nil, errors.New("no routing configuration")
	}
	if strings.TrimSpace(req.Tenant) == "" {
		return nil, errors.New("tenant is required")
	}
	if strings.TrimSpace(req.Dialed) == "" {
		return nil, errors.New("dialed digits are required")
	}
	direction, err := parseDirection(req.Direction)
	if err != nil {
		return nil, err
	}
	if len(req.Digits) > MaxDigits {
		return nil, fmt.Errorf("at most %d scripted digit entries (got %d)", MaxDigits, len(req.Digits))
	}

	table, ok := src.Routing.TenantRouting(req.Tenant)
	if !ok {
		return nil, fmt.Errorf("%q: %w", req.Tenant, ErrUnknownTenant)
	}

	caller := req.Caller
	if caller == "" {
		caller = "102"
	}

	log, records := captureLogger()

	var (
		mu     sync.Mutex
		traced *flow.Trace
	)
	engine := flow.New(flow.Config{
		Routing: src.Routing,
		Flows:   src.Flows,
		// nil Resolver and nil Parking: see the package comment. This is the
		// whole reason a simulation cannot touch a real parked call.
		Resolver: nil,
		Parking:  nil,
		BuildPolicy: func(cc agent.CallContext) *agent.Policy {
			return agent.NewPolicy(cc.Tenant,
				src.Policy.TenantPolicyFor(cc.Tenant,
					dialplan.SymbolicTargetsFor(src.Routing, cc.Tenant)),
				log)
		},
		Trace: flow.TraceFunc(func(t flow.Trace) {
			mu.Lock()
			defer mu.Unlock()
			copied := t
			traced = &copied
		}),
		Logger: log,
	})

	sess := flowtest.NewWith(flowtest.Identity{
		CallID:      "sim-" + callSuffix(),
		CallerID:    caller,
		Destination: req.Dialed,
		Domain:      "sim.invalid",
	})
	for _, d := range req.Digits {
		sess.QueueDigits(d, agent.CollectMaxDigits)
	}

	cc := &agent.CallContext{
		Caller:    caller,
		Callee:    req.Dialed,
		Direction: direction,
		Tenant:    req.Tenant,
	}

	simCtx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()

	started := time.Now()
	handled := engine.Handle(simCtx, sess, cc)
	elapsed := time.Since(started)

	mu.Lock()
	trace := traced
	mu.Unlock()

	res := &Result{
		Handled:    handled,
		Tenant:     req.Tenant,
		Dialed:     req.Dialed,
		Direction:  string(direction),
		CallID:     sess.CallID(),
		Trace:      trace,
		DurationMs: elapsed.Milliseconds(),
		Spoken:     sess.Spoken,
		Played:     sess.Played,
		Targets:    sess.Dialed,
		Relayed:    sess.Relayed,
		Hangups:    sess.Hangups,
		Collects:   sess.Collects,
		Events:     sess.Events,
		Log:        records(),
	}

	switch {
	case trace != nil:
		res.Routed = RoutedFlow
	case handled:
		res.Routed = RoutedDirect
		res.Note = "the entry mapping dials this destination directly; no flow graph is involved, so there is no traversal to show"
	default:
		res.Routed = RoutedNone
		res.Note = unmatchedNote(table)
	}
	return res, nil
}

// unmatchedNote says where a call nothing matched would actually end up.
func unmatchedNote(table *dialplan.RoutingTable) string {
	if table != nil && table.Operator != "" {
		return fmt.Sprintf(
			"nothing in the entry mapping matched; the call would fall through to the tenant operator (%s)",
			table.Operator)
	}
	return "nothing in the entry mapping matched, and the tenant has no operator; the call would be rejected with 480"
}

// parseDirection validates the requested direction.
func parseDirection(v string) (agent.Direction, error) {
	switch v {
	case "", string(agent.DirectionInternal):
		return agent.DirectionInternal, nil
	case string(agent.DirectionInbound):
		return agent.DirectionInbound, nil
	case string(agent.DirectionOutbound):
		return agent.DirectionOutbound, nil
	default:
		return "", fmt.Errorf("unknown direction %q (want %q, %q or %q)",
			v, agent.DirectionInternal, agent.DirectionInbound, agent.DirectionOutbound)
	}
}
