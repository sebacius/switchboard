package agent

import (
	"sync"
)

// Admission is the deterministic gate around the supervisor (spec:
// call-admission). It performs NO SIP and no I/O — pure decision plus slot
// acquisition — and it now runs in TWO places, because deterministic resolution
// sits between them:
//
//  1. Preflight, before resolution — the call's tenant must be loaded at all.
//     An unattributable call is a hard reject (there is no default tenant).
//  2. Admit, at the hand-off to the supervisor — the tenant's prompt must be
//     non-empty, and a per-tenant counting semaphore must have a free channel.
//
// Splitting them matters. The channel limit bounds concurrent SUPERVISED calls:
// it exists to stop the first-turn LLM call from queueing past SIP Timer B, and
// to cap spend. A deterministically resolved call makes no LLM request, so
// charging it a channel would mean a tenant at its LLM limit gets 486 Busy on a
// plain extension dial that costs nothing — a capacity regression caused by a
// latency control. For the same reason a tenant with a routing table but no
// prompt can still route: it simply cannot be supervised.

// PromptSource resolves a tenant's combined prompt (settings.md + tenant.md).
// It is the seam admission uses for preflight; real loading from disk/config is
// group 7's concern. TenantPrompt returns the combined prompt and whether it
// exists and is non-empty — a tenant with no prompt is "not loaded".
type PromptSource interface {
	TenantPrompt(tenant string) (string, bool)
}

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
// the two tenant reasons → 4xx, reasonChannelLimit → 486 Busy.
const (
	reasonTenantNotLoaded = "tenant not loaded"
	reasonNoPrompt        = "tenant has no prompt to supervise with"
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
	prompts PromptSource
	// routing lets a tenant count as "loaded" on the strength of its routing
	// table alone. Optional: nil means only a prompt makes a tenant loaded.
	routing RoutingSource

	// defaultLimit is the per-tenant concurrency cap when no override applies.
	defaultLimit int
	// overrides holds per-tenant limits that supersede defaultLimit. Read-only
	// after construction, so it needs no locking.
	overrides map[string]int

	mu     sync.Mutex
	active map[string]int // tenant → in-flight admitted calls
}

// NewAdmission builds an Admission. prompts is required (preflight needs it).
// defaultLimit is the fallback per-tenant concurrency cap; a value <= 0 is
// clamped to 1 so a misconfiguration never silently admits unbounded calls.
// overrides may be nil; a per-tenant override <= 0 is ignored (the default
// applies) for the same reason.
func NewAdmission(prompts PromptSource, routing RoutingSource, defaultLimit int, overrides map[string]int) *Admission {
	if defaultLimit <= 0 {
		defaultLimit = 1
	}
	return &Admission{
		prompts:      prompts,
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

// Preflight is the check that runs BEFORE deterministic resolution: is this call
// attributable to a tenant this system knows at all? A tenant is loaded if it
// has a prompt, a routing table, or both — a tenant with only a routing table
// can be routed but not supervised, and that is a legitimate configuration.
//
// It takes no slot: nothing has been committed yet.
func (a *Admission) Preflight(cc CallContext) AdmissionResult {
	if a.tenantLoaded(cc.Tenant) {
		return AdmissionResult{Admitted: true, Release: noopRelease}
	}
	return AdmissionResult{Admitted: false, Reason: reasonTenantNotLoaded, Release: noopRelease}
}

// tenantLoaded reports whether the system knows this tenant by either half of
// its configuration.
func (a *Admission) tenantLoaded(tenant string) bool {
	if tenant == "" {
		return false
	}
	if _, ok := a.prompts.TenantPrompt(tenant); ok {
		return true
	}
	if a.routing != nil {
		if _, ok := a.routing.TenantRouting(tenant); ok {
			return true
		}
	}
	return false
}

// Admit is the gate at the hand-off to the supervisor: the tenant must have a
// prompt to supervise with, and a channel slot must be free. It is reached only
// after deterministic resolution declined the call, so everything it rejects is
// a call that genuinely needed the model.
func (a *Admission) Admit(cc CallContext) AdmissionResult {
	// A tenant with no prompt cannot be supervised. Resolution has already had
	// its chance, so this is the end of the road for the call.
	if _, ok := a.prompts.TenantPrompt(cc.Tenant); !ok {
		return AdmissionResult{Admitted: false, Reason: reasonNoPrompt, Release: noopRelease}
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

// releaseFor returns an idempotent Release closure that frees exactly one slot
// for the tenant on its first invocation and does nothing thereafter. sync.Once
// guards against the teardown funnel calling it more than once (BYE + ctx
// timeout can both converge on teardown) so the counter never under-counts.
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
