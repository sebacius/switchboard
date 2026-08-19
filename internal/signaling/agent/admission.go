package agent

import (
	"sync"
)

// Admission is the deterministic gate between the router and the runner
// (design #9, spec: call-admission). It runs BEFORE the INVITE is answered and
// BEFORE any LLM round-trip: it is pure decision + slot acquisition, no I/O. Two
// checks, in order:
//
//  1. Preflight — the call's tenant must resolve to a loaded, non-empty prompt.
//     An unloaded tenant is a hard reject (no default tenant). Group 7 maps the
//     rejection to a 4xx; admission performs NO SIP here.
//  2. Channel limit — a per-tenant counting semaphore caps concurrent supervised
//     calls. At the limit the call is rejected (group 7 → 486 Busy). Under the
//     limit a slot is acquired and returned as an idempotent Release the runner's
//     teardown funnel calls exactly once.
//
// The concurrency bound is both cost control and SLA protection: it keeps the
// first-turn LLM call (which runs inside the INVITE transaction) from queueing
// past Timer B under load — calls reject fast rather than dying in the timeout.

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

// Stable rejection reasons. Group 7 maps these to SIP responses:
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
	prompts PromptSource

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
func NewAdmission(prompts PromptSource, defaultLimit int, overrides map[string]int) *Admission {
	if defaultLimit <= 0 {
		defaultLimit = 1
	}
	return &Admission{
		prompts:      prompts,
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

// Admit runs preflight then attempts to acquire a channel slot for the call's
// tenant. It is the single entry point group 7 calls before answering. It does
// no I/O and never engages the model: a rejection here is final and pre-answer.
func (a *Admission) Admit(cc CallContext) AdmissionResult {
	// Preflight: the tenant must resolve to a loaded, non-empty prompt. The
	// router already guarantees a non-empty tenant on its happy path, but
	// admission re-checks rather than trust its caller.
	if _, ok := a.prompts.TenantPrompt(cc.Tenant); !ok {
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
