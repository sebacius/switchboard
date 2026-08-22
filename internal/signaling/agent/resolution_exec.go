package agent

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// CallResolution executes what the Resolver decided. It is the layer that turns
// a Destination into SIP, and it is deliberately separate from the Resolver so
// the decision stays a pure function of (call context, routing table,
// registrations, parking) and can be tested without a media stack.
//
// Every destination it dials goes through the tenant's Policy first — the same
// adjudication a model-issued dial gets. That is what keeps resolution a
// latency optimization rather than a second, unguarded path to the trunk.

// CallResolutionConfig wires the resolution layer.
type CallResolutionConfig struct {
	// Resolver decides. Required; a nil resolver hands every call off.
	Resolver *Resolver

	// Parking executes a *7XX retrieval. Nil disables the retrieval path.
	Parking ParkingService

	// BuildPolicy returns the authorization policy for a call, mirroring the
	// runner's BuildExecutor seam so both paths get the tenant's policy the same
	// way. Required; a nil builder hands every call off, because an
	// unadjudicated forward is not an option.
	BuildPolicy func(CallContext) *Policy

	// Logger is used for outcome logging; nil falls back to slog.Default().
	Logger *slog.Logger
}

// CallResolution runs deterministic resolution for calls. It holds the
// round-robin cursors, so one instance serves the whole process.
type CallResolution struct {
	cfg CallResolutionConfig
	log *slog.Logger

	mu      sync.Mutex
	cursors map[string]int // "tenant/group" → next member index
}

// NewCallResolution builds the resolution layer.
func NewCallResolution(cfg CallResolutionConfig) *CallResolution {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &CallResolution{
		cfg:     cfg,
		log:     log.With("component", "call-resolution"),
		cursors: make(map[string]int),
	}
}

// Handle attempts to resolve and execute the call deterministically. It returns
// true when it took ownership of the call — the caller must NOT start the
// supervisor — and false when the call should be supervised.
//
// A true return means the call is finished by the time Handle returns: Forward
// and ForwardGroup block for the life of the bridge, and the retrieval path
// waits on the session's context.
func (c *CallResolution) Handle(ctx context.Context, sess CallSession, cc *CallContext) bool {
	if c == nil || c.cfg.Resolver == nil {
		return false
	}

	dest, ok := c.cfg.Resolver.Resolve(*cc)
	c.cfg.Resolver.LogDecision(*cc, dest, ok)
	if !ok {
		return false
	}

	if c.cfg.BuildPolicy == nil {
		// No policy means nothing can be adjudicated, and an unadjudicated
		// forward is exactly what this design refuses to do. Decline instead.
		c.log.Warn("resolution declined: no policy builder configured", "tenant", cc.Tenant)
		return false
	}
	policy := c.cfg.BuildPolicy(*cc)

	switch dest.Kind {
	case DestinationEndpoint:
		return c.forwardEndpoint(ctx, sess, *cc, dest, policy)
	case DestinationGroup:
		return c.ringGroup(ctx, sess, *cc, dest, policy)
	case DestinationRetrieve:
		return c.retrieve(ctx, sess, *cc, dest)
	default:
		return false
	}
}

// forwardEndpoint authorizes and forwards to a single resolved endpoint. A
// policy deny declines the call rather than dropping it: the caller deserves to
// be told something, and the deny is already logged by the policy's own decision
// logging.
func (c *CallResolution) forwardEndpoint(ctx context.Context, sess CallSession, cc CallContext, dest Destination, policy *Policy) bool {
	if d := policy.AuthorizeTarget("resolve_dial", dest.Target); !d.Allowed {
		c.log.Warn("resolved destination denied by policy; declining resolution",
			"tenant", cc.Tenant, "target", dest.Target, "reason", d.Reason)
		return false
	}

	if err := sess.Forward(ctx, dest.Target, defaultDialTimeout); err != nil {
		// Forward already relayed the target's own status to the caller (busy,
		// unavailable, timeout). The call is over and it ended the way a PBX
		// ends it — with the endpoint's real response, not an AI apology.
		c.log.Info("resolved forward ended without a bridge",
			"tenant", cc.Tenant, "target", dest.Target, "error", err)
	}
	return true
}

// ringGroup authorizes each member, rings the group, and applies the group's
// no-answer outcome. Members are authorized individually rather than inheriting
// the group's verdict, so a table that puts an external number in a group cannot
// smuggle reach past the tenant's Class of Service.
func (c *CallResolution) ringGroup(ctx context.Context, sess CallSession, cc CallContext, dest Destination, policy *Policy) bool {
	members := c.authorizedMembers(cc, dest, policy)
	if len(members) == 0 {
		c.log.Warn("ring group has no authorized members; handing to supervisor",
			"tenant", cc.Tenant, "group", dest.GroupName)
		return false
	}

	rounds := c.rounds(cc, dest, members)
	timeout := time.Duration(dest.Group.MemberTimeoutMs) * time.Millisecond

	err := sess.ForwardGroup(ctx, rounds, timeout)
	if err == nil {
		return true
	}
	if !errors.Is(err, ErrGroupNoAnswer) {
		// A real failure (no B2BUA, already answered, caller vanished). The call
		// is not in a state to hand to the supervisor cleanly.
		c.log.Warn("ring group failed", "tenant", cc.Tenant, "group", dest.GroupName, "error", err)
		return true
	}

	c.log.Info("ring group unanswered", "tenant", cc.Tenant, "group", dest.GroupName)

	// The group no longer carries its own fallback: what to do when nobody
	// answers belongs to whatever rang the group. Resolution's answer is the
	// tenant operator, and a tenant without one declines so the caller is told
	// something rather than left on 180.
	return c.forwardOperator(ctx, sess, cc, policy)
}

// forwardOperator sends the caller to the tenant's operator. A tenant with no
// operator configured falls through to the supervisor rather than dropping the
// call — "never hang up on the caller" is the floor.
func (c *CallResolution) forwardOperator(ctx context.Context, sess CallSession, cc CallContext, policy *Policy) bool {
	table, ok := c.cfg.Resolver.routing.TenantRouting(cc.Tenant)
	if !ok || table.Operator == "" {
		c.log.Warn("no operator configured; handing to supervisor", "tenant", cc.Tenant)
		return false
	}
	if d := policy.AuthorizeTarget("resolve_operator", table.Operator); !d.Allowed {
		c.log.Warn("operator denied by policy; handing to supervisor",
			"tenant", cc.Tenant, "operator", table.Operator, "reason", d.Reason)
		return false
	}
	if err := sess.Forward(ctx, table.Operator, defaultDialTimeout); err != nil {
		c.log.Info("operator forward ended without a bridge",
			"tenant", cc.Tenant, "operator", table.Operator, "error", err)
	}
	return true
}

// retrieve executes a *7XX pickup and then blocks for the life of the bridge.
// Blocking is what makes the retrieval path equivalent to the supervisor's
// Parked disposition: something has to keep this call's goroutine alive while
// the two parties talk, and here there is no dispatch loop to do it.
func (c *CallResolution) retrieve(ctx context.Context, sess CallSession, cc CallContext, dest Destination) bool {
	if c.cfg.Parking == nil {
		return false
	}

	msg, err := RetrieveParked(ctx, c.cfg.Parking, dest.Slot, sess, c.log)
	if err != nil {
		// The slot was occupied when we resolved and is not now — a race with
		// another retriever, or the parked party hung up. Hand off so the caller
		// is told, rather than leaving them with a silent answered call.
		c.log.Info("deterministic retrieval failed; handing to supervisor",
			"tenant", cc.Tenant, "slot", dest.Slot, "error", err)
		return false
	}

	c.log.Info("deterministic retrieval", "tenant", cc.Tenant, "slot", dest.Slot, "result", msg)

	// watchUnparkedBridge owns teardown; we just stay alive until this call ends.
	<-sess.Context().Done()
	return true
}

// authorizedMembers filters a group's members through the policy, keeping ring
// order. A denied member is dropped rather than failing the group: the other
// members are still a correct answer for the caller.
func (c *CallResolution) authorizedMembers(cc CallContext, dest Destination, policy *Policy) []string {
	members := make([]string, 0, len(dest.Group.Members))
	for _, m := range dest.Group.Members {
		if d := policy.AuthorizeTarget("resolve_group_member", m); !d.Allowed {
			c.log.Warn("ring group member denied by policy",
				"tenant", cc.Tenant, "group", dest.GroupName, "member", m, "reason", d.Reason)
			continue
		}
		members = append(members, m)
	}
	return members
}

// rounds turns a group's members into ring rounds under its strategy.
//
// Both shipped strategies produce single-member rounds — they differ only in
// where the order starts. Round-robin advances a per-(tenant, group) cursor so
// consecutive calls do not always begin with the same person, which is the whole
// point of the strategy. The cursor is per process: with several signaling
// servers, distribution is fair per server rather than globally. Sharing it
// needs durable state this system does not have yet.
func (c *CallResolution) rounds(cc CallContext, dest Destination, members []string) [][]string {
	start := 0
	if dest.Group.Strategy == StrategyRoundRobin {
		start = c.nextCursor(cc.Tenant, dest.GroupName, len(members))
	}

	rounds := make([][]string, 0, len(members))
	for i := range members {
		rounds = append(rounds, []string{members[(start+i)%len(members)]})
	}
	return rounds
}

// nextCursor returns this call's starting index for a round-robin group and
// advances the cursor for the next one.
func (c *CallResolution) nextCursor(tenant, group string, n int) int {
	if n <= 0 {
		return 0
	}
	key := tenant + "/" + group

	c.mu.Lock()
	defer c.mu.Unlock()
	start := c.cursors[key] % n
	c.cursors[key] = (start + 1) % n
	return start
}
