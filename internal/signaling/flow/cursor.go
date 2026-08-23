// Package flow executes a tenant's flow graph for one call.
//
// The Resolver it sits beside is a pure function; an executor cannot be. A flow
// has a position, buffered digits, per-node retry counts and a deadline, all of
// which live for the duration of the call. That state is the Cursor.
//
// Budgets nest — flow ⊃ node ⊃ playback — so that a prompt can be cut without
// ending the node, and a node can end without ending the flow.
package flow

import (
	"context"
	"strings"
	"time"

	"github.com/sebas/switchboard/internal/signaling/agent"
	"github.com/sebas/switchboard/internal/signaling/dialplan"
)

// Hop is one step of a traversal: which node, which exit it produced, and how
// long the caller spent there.
//
// Recording the path rather than only the outcome is what makes "why did this
// caller end up in the operator queue" answerable. Without it the question has
// no answer at all.
type Hop struct {
	Node       string    `json:"node"`
	Type       string    `json:"type"`
	Exit       string    `json:"exit"`
	EnteredAt  time.Time `json:"entered_at"`
	DurationMs int64     `json:"duration_ms"`
	// Detail carries what actually happened — "486 busy", "digit 2", "denied by
	// policy" — because the exit name alone rarely explains itself.
	Detail string `json:"detail,omitempty"`
}

// Cursor is one call's position in a flow.
type Cursor struct {
	CallID string
	Tenant string
	Flow   string

	node    string
	retries map[string]int
	hops    []Hop

	// digits carries a collected value between nodes.
	digits string

	// dialed is what the caller dialed, after the entry mapping's transform.
	dialed string

	// decisions are the authorization verdicts made for this call.
	decisions []Decision

	startedAt time.Time
}

// newCursor starts a cursor at a flow's start node.
func newCursor(callID, tenant, flowName string, def *dialplan.FlowDef) *Cursor {
	return &Cursor{
		CallID:    callID,
		Tenant:    tenant,
		Flow:      flowName,
		node:      def.Start,
		retries:   map[string]int{},
		startedAt: time.Now(),
	}
}

// Node returns the current node ID.
func (c *Cursor) Node() string { return c.node }

// Hops returns the traversal so far.
func (c *Cursor) Hops() []Hop { return c.hops }

// Digits returns the most recently collected digits.
func (c *Cursor) Digits() string { return c.digits }

// Dialed returns what the caller dialed, after the entry transform.
func (c *Cursor) Dialed() string { return c.dialed }

// Path renders the traversal as "greeting -> claims -> vm-130", which is the
// form a person actually reads.
func (c *Cursor) Path() string {
	names := make([]string, 0, len(c.hops))
	for _, h := range c.hops {
		names = append(names, h.Node)
	}
	return strings.Join(names, " -> ")
}

// Elapsed reports how long the call has been in this flow.
func (c *Cursor) Elapsed() time.Duration { return time.Since(c.startedAt) }

// record appends a hop and moves to the next node.
func (c *Cursor) record(nodeID string, nodeType dialplan.NodeType, exit, detail string, enteredAt time.Time, next string) {
	c.hops = append(c.hops, Hop{
		Node:       nodeID,
		Type:       string(nodeType),
		Exit:       exit,
		EnteredAt:  enteredAt,
		DurationMs: time.Since(enteredAt).Milliseconds(),
		Detail:     detail,
	})
	c.node = next
}

// captureDecisions copies the policy's verdicts onto the cursor, so the call
// record shows what was permitted as well as where the call went.
func (c *Cursor) captureDecisions(policy *agent.Policy) {
	if policy == nil {
		return
	}
	c.decisions = c.decisions[:0]
	for _, d := range policy.Decisions() {
		c.decisions = append(c.decisions, Decision{
			Target: d.Target, Allowed: d.Allowed, Reason: d.Reason,
		})
	}
}

// retry increments and returns a node's retry count. This is the ONLY repetition
// a flow allows, and it is bounded per node — which is what keeps the graph
// acyclic while still letting a menu re-prompt.
func (c *Cursor) retry(nodeID string) int {
	c.retries[nodeID]++
	return c.retries[nodeID]
}

// budgets returns the per-node context derived from the flow context.
func nodeContext(flowCtx context.Context, timeoutMs int) (context.Context, context.CancelFunc) {
	if timeoutMs <= 0 {
		return context.WithCancel(flowCtx)
	}
	return context.WithTimeout(flowCtx, time.Duration(timeoutMs)*time.Millisecond)
}

// callContextOf is a small helper so nodes read the tenant identity uniformly.
func callContextOf(cc *agent.CallContext) agent.CallContext {
	if cc == nil {
		return agent.CallContext{}
	}
	return *cc
}
