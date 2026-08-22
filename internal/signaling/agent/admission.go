package agent

import (
	"sync"
)

// Admission is the deterministic gate in front of every call (spec:
// call-admission). It performs NO SIP and no I/O — pure decision plus slot
// acquisition — and runs exactly once, before any media is allocated:
//
//  1. The call's tenant must be loaded. An unattributable call is a hard reject
//     (there is no default tenant).
//  2. A per-tenant counting semaphore must have a free channel.
//
// The channel limit is CAPACITY control. It used to be justified by first-turn
// LLM latency — bounding concurrent model calls so they could not queue past SIP
// Timer B — and that justification died with the supervisor. What remains is
// physical: every call holds an RTP port, a media session, and a handler
// goroutine blocked for the life of the call, and something has to bound how
// many of those one tenant can take.
//
// Because the scarce resource is now a port rather than a model, the slot is
// taken BEFORE the media session is created, and it is taken for every call
// rather than only for the ones that reached a particular subsystem.

// AdmissionResult is the verdict of Admit. When Admitted is true, Release frees
// the acquired channel slot and MUST be called exactly once by the teardown
// funnel; it is safe to call more than once (idempotent) and is always non-nil
// so callers can defer it unconditionally. When Admitted is false, Reason is a
// stable, machine-readable explanation group 7 maps to a SIP failure response,
// and Release is a no-op (no slot was taken).
type AdmissionResult struct {
	Admitted bool
	Reason   string
	Release  func()
}

// Stable rejection reasons. The INVITE handler maps these to SIP responses:
// reasonTenantNotLoaded → 4xx, reasonChannelLimit → 486 Busy.
const (
	reasonTenantNotLoaded = "tenant not loaded"
	reasonChannelLimit    = "tenant at channel limit"
)

// noopRelease is the Release returned on a rejection: nothing was acquired, so
// freeing is a no-op. Shared because it is stateless.
func noopRelease() {}

// Admission enforces preflight + per-tenant channel limits. It is safe for
// concurrent use: the per-tenant counters are guarded by a single mutex (the
// counting semaphore is a guarded map, not a channel, because the limits are
// per-tenant and discovered dynamically). One Admission instance serves all
// tenants for the lifetime of the process.
type Admission struct {
	// routing is what makes a tenant "loaded": having somewhere for its calls to
	// go is now the whole definition.
	routing RoutingSource

	// defaultLimit is the per-tenant concurrency cap when no override applies.
	defaultLimit int
	// overrides holds per-tenant limits that supersede defaultLimit. Read-only
	// after construction, so it needs no locking.
	overrides map[string]int

	mu     sync.Mutex
	active map[string]int // tenant → in-flight admitted calls
}

// NewAdmission builds an Admission. defaultLimit is the fallback per-tenant
// concurrency cap; a value <= 0 is clamped to 1 so a misconfiguration never
// silently admits unbounded calls. overrides may be nil; a per-tenant override
// <= 0 is ignored (the default applies) for the same reason.
func NewAdmission(routing RoutingSource, defaultLimit int, overrides map[string]int) *Admission {
	if defaultLimit <= 0 {
		defaultLimit = 1
	}
	return &Admission{
		routing:      routing,
		defaultLimit: defaultLimit,
		overrides:    overrides,
		active:       make(map[string]int),
	}
}

// limitFor returns the effective concurrency limit for a tenant: a positive
// override if present, else the default.
func (a *Admission) limitFor(tenant string) int {
	if n, ok := a.overrides[tenant]; ok && n > 0 {
		return n
	}
	return a.defaultLimit
}

// Admit is the single gate every call passes through, before the media session
// is created: the tenant must be known, and a channel slot must be free.
//
// A tenant is loaded if it has a routing table. That is the whole definition
// now — a tenant with nowhere to send calls is not a tenant this system can
// serve.
func (a *Admission) Admit(cc CallContext) AdmissionResult {
	if !a.tenantLoaded(cc.Tenant) {
		return AdmissionResult{Admitted: false, Reason: reasonTenantNotLoaded, Release: noopRelease}
	}

	// Channel limit: acquire a slot under the lock so the check-and-increment is
	// atomic (no two concurrent calls can both pass at the boundary).
	a.mu.Lock()
	if a.active[cc.Tenant] >= a.limitFor(cc.Tenant) {
		a.mu.Unlock()
		return AdmissionResult{Admitted: false, Reason: reasonChannelLimit, Release: noopRelease}
	}
	a.active[cc.Tenant]++
	a.mu.Unlock()

	return AdmissionResult{
		Admitted: true,
		Release:  a.releaseFor(cc.Tenant),
	}
}

// tenantLoaded reports whether the system has routing configuration for this
// tenant.
func (a *Admission) tenantLoaded(tenant string) bool {
	if tenant == "" || a.routing == nil {
		return false
	}
	_, ok := a.routing.TenantRouting(tenant)
	return ok
}

// releaseFor returns an idempotent Release closure that frees exactly one slot
// for the tenant on its first invocation and does nothing thereafter. sync.Once
// guards against teardown running more than once (BYE + ctx timeout can both
// converge on it) so the counter never under-counts.
func (a *Admission) releaseFor(tenant string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			if a.active[tenant] > 0 {
				a.active[tenant]--
			}
			a.mu.Unlock()
		})
	}
}

// Active returns the current in-flight admitted-call count for a tenant. It is
// primarily for tests and observability; it takes the lock for a consistent read.
func (a *Admission) Active(tenant string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.active[tenant]
}
