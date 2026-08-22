package flow

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/sebas/switchboard/internal/signaling/agent"
	"github.com/sebas/switchboard/internal/signaling/dialplan"
)

// Config wires the engine to the rest of the system.
type Config struct {
	// Routing supplies each tenant's entry mapping and ring groups.
	Routing dialplan.RoutingSource
	// Flows supplies each tenant's graphs.
	Flows dialplan.FlowSource
	// Resolver handles the single-hop cases — a registered extension, a mapped
	// DID, a ring group — that need no graph at all.
	Resolver *agent.Resolver
	// Parking serves *7XX retrieval, which is evaluated before any flow.
	Parking agent.ParkingService
	// BuildPolicy adjudicates every destination a flow produces.
	BuildPolicy func(agent.CallContext) *agent.Policy
	// Trace records completed traversals. Optional.
	Trace TraceSink
	// Logger is required in practice; nil falls back to the default.
	Logger *slog.Logger
}

// Engine executes flows.
type Engine struct {
	cfg Config
	log *slog.Logger

	mu     sync.Mutex
	active map[string]*Cursor
}

// New builds an Engine.
func New(cfg Config) *Engine {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Engine{
		cfg:    cfg,
		log:    log.With("component", "flow"),
		active: make(map[string]*Cursor),
	}
}

// Handle routes one call, returning true when the engine owns it and the call is
// finished.
//
// The signature and contract are deliberately those of the resolver it replaces,
// so the INVITE handler changed by one line. It BLOCKS for the life of the call:
// sipgo terminates the transaction when its handler returns, and a terminated
// transaction silently swallows every later response, so a flow that spawned a
// goroutine and returned could never deliver a 200 OK or a final status.
func (e *Engine) Handle(ctx context.Context, sess agent.CallSession, cc *agent.CallContext) bool {
	if e == nil || cc == nil {
		return false
	}
	callCtx := callContextOf(cc)

	// Call retrieval is evaluated BEFORE the entry mapping, so a tenant cannot
	// accidentally shadow *701 with a pattern. It is not a node: retrieval takes
	// over the call entirely and there is nothing after it to route to.
	if handled := e.tryRetrieval(ctx, sess, callCtx); handled {
		return true
	}

	table, ok := e.tenantTable(callCtx.Tenant)
	if !ok {
		return false
	}

	dest, matched := e.entryFor(table, callCtx)
	if !matched {
		// Nothing claims this call; the caller decides what to do about it.
		return false
	}

	flowName, isFlow := dialplan.IsFlowTarget(dest)
	if !isFlow {
		// A bare destination is sugar for a one-node dial, and behaves exactly
		// as deterministic resolution always did.
		return e.runSingleDial(ctx, sess, callCtx, dest)
	}

	set, ok := e.cfg.Flows.TenantFlows(callCtx.Tenant)
	if !ok {
		e.log.Warn("entry names a flow but the tenant has none",
			"tenant", callCtx.Tenant, "flow", flowName)
		return false
	}
	def, ok := set.Flow(flowName)
	if !ok {
		e.log.Warn("entry names an unknown flow", "tenant", callCtx.Tenant, "flow", flowName)
		return false
	}

	return e.runFlow(ctx, sess, callCtx, flowName, def)
}

// runFlow walks the graph until a terminal outcome.
func (e *Engine) runFlow(ctx context.Context, sess agent.CallSession, cc agent.CallContext,
	flowName string, def *dialplan.FlowDef) bool {

	cursor := newCursor(sess.CallID(), cc.Tenant, flowName, def)
	e.track(cursor)
	defer e.release(cursor)

	flowCtx, cancel := context.WithTimeout(ctx, time.Duration(def.FlowTimeout())*time.Millisecond)
	defer cancel()

	e.log.Info("flow entered",
		"call_id", cursor.CallID, "tenant", cc.Tenant, "flow", flowName, "start", def.Start)

	policy := e.policyFor(cc)

	for hop := 0; ; hop++ {
		// Acyclicity is proved at load, so this is unreachable in a correct
		// system. It exists so a defect in validation drops a call rather than
		// running one forever.
		if hop >= dialplan.MaxHops {
			e.log.Error("flow exceeded the hop limit; abandoning",
				"call_id", cursor.CallID, "flow", flowName, "path", cursor.Path())
			e.finish(sess, cursor, "hop limit exceeded")
			return true
		}

		if err := flowCtx.Err(); err != nil {
			// The caller hung up, or the whole-flow deadline expired.
			e.log.Info("flow ended before completing",
				"call_id", cursor.CallID, "flow", flowName, "path", cursor.Path(), "reason", err)
			e.finish(sess, cursor, "abandoned")
			return true
		}

		node, ok := def.Nodes[cursor.Node()]
		if !ok || node == nil {
			e.log.Error("flow reached a node that does not exist",
				"call_id", cursor.CallID, "node", cursor.Node())
			e.finish(sess, cursor, "missing node")
			return true
		}

		enteredAt := time.Now()
		outcome := e.runNode(flowCtx, sess, cc, cursor, node, policy)
		cursor.captureDecisions(policy)

		next := node.Exits[outcome.exit]
		cursor.record(cursor.Node(), node.Type, outcome.exit, outcome.detail, enteredAt, next)

		if outcome.terminal {
			e.log.Info("flow complete",
				"call_id", cursor.CallID, "flow", flowName,
				"path", cursor.Path(), "outcome", outcome.exit)
			e.emitTrace(cursor, outcome.exit)
			return true
		}

		if next == "" {
			// The validator rejects unwired exits, so this means the graph
			// changed under a live call.
			e.log.Error("exit is not wired",
				"call_id", cursor.CallID, "node", node.Type, "exit", outcome.exit)
			e.finish(sess, cursor, "unwired exit")
			return true
		}
	}
}

// finish ends a call the flow could not complete, relaying something honest.
func (e *Engine) finish(sess agent.CallSession, cursor *Cursor, reason string) {
	if !sess.HasAnswered() {
		// Nothing has been relayed yet, so the caller can still receive a real
		// final status rather than silence.
		_ = sess.Hangup(reason)
	} else {
		_ = sess.Hangup(reason)
	}
	e.emitTrace(cursor, reason)
}

// runSingleDial handles a bare destination: the one-node dial that a literal
// entry mapping is sugar for.
func (e *Engine) runSingleDial(ctx context.Context, sess agent.CallSession, cc agent.CallContext, dest string) bool {
	policy := e.policyFor(cc)
	if policy == nil {
		e.log.Warn("no policy builder configured; declining", "tenant", cc.Tenant)
		return false
	}

	if d := policy.AuthorizeTarget("flow_entry_dial", dest); !d.Allowed {
		e.log.Warn("entry destination denied by policy",
			"tenant", cc.Tenant, "target", dest, "reason", d.Reason)
		return false
	}

	// Forward relays the destination's own status, which is right for a call
	// with nowhere else to go.
	if err := sess.Forward(ctx, dest, 0); err != nil {
		e.log.Info("entry dial ended", "tenant", cc.Tenant, "target", dest, "error", err)
	}
	return true
}

// tryRetrieval handles *7XX pickup.
func (e *Engine) tryRetrieval(ctx context.Context, sess agent.CallSession, cc agent.CallContext) bool {
	if e.cfg.Resolver == nil {
		return false
	}
	dest, ok := e.cfg.Resolver.Resolve(cc)
	if !ok || dest.Kind != agent.DestinationRetrieve {
		return false
	}

	slot, err := agent.RetrieveParked(ctx, e.cfg.Parking, dest.Slot, sess, e.log)
	if err != nil {
		e.log.Warn("call retrieval failed", "slot", dest.Slot, "error", err)
		return false
	}

	e.log.Info("parked call retrieved", "slot", slot, "call_id", sess.CallID())
	<-sess.Context().Done()
	return true
}

// entryFor resolves dialed digits against the tenant's entry mapping.
func (e *Engine) entryFor(table *dialplan.RoutingTable, cc agent.CallContext) (string, bool) {
	if cc.Direction == agent.DirectionInbound {
		if dest, ok := table.MatchDID(cc.Callee); ok {
			return dest, true
		}
		return "", false
	}
	return table.MatchExtension(cc.Callee)
}

func (e *Engine) tenantTable(tenant string) (*dialplan.RoutingTable, bool) {
	if e.cfg.Routing == nil {
		return nil, false
	}
	return e.cfg.Routing.TenantRouting(tenant)
}

func (e *Engine) policyFor(cc agent.CallContext) *agent.Policy {
	if e.cfg.BuildPolicy == nil {
		return nil
	}
	return e.cfg.BuildPolicy(cc)
}

// track and release bound a cursor's lifetime. Releasing on every exit path is
// what stops a caller who abandons mid-menu from leaking state for the life of
// the process.
func (e *Engine) track(c *Cursor) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.active[c.CallID] = c
}

func (e *Engine) release(c *Cursor) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.active, c.CallID)
}

// Active returns the calls currently in a flow, for observability.
func (e *Engine) Active() map[string]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]string, len(e.active))
	for id, c := range e.active {
		out[id] = fmt.Sprintf("%s:%s", c.Flow, c.Node())
	}
	return out
}

// emitTrace hands a completed traversal to the sink.
func (e *Engine) emitTrace(cursor *Cursor, outcome string) {
	if e.cfg.Trace == nil {
		return
	}
	e.cfg.Trace.Record(Trace{
		CallID:    cursor.CallID,
		Tenant:    cursor.Tenant,
		Flow:      cursor.Flow,
		Outcome:   outcome,
		Path:      cursor.Path(),
		Hops:      cursor.Hops(),
		Started:   cursor.startedAt,
		Ended:     time.Now(),
		Decisions: cursor.decisions,
	})
}

// describeDigits renders collected digits for a hop detail.
func describeDigits(digits string) string {
	if digits == "" {
		return "no digits"
	}
	return "digits " + strings.TrimSpace(digits)
}
