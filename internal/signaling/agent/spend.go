package agent

import (
	"sync"
	"time"
)

// SpendLedger is the per-tenant external-spend counter, shared for the life of
// the process and bucketed by day.
//
// It exists as its own type because of a bug in the shipped code: the counter
// lived on Policy, and a Policy is built per call, so max_external_units_per_day
// reset on every INVITE. A per-call counter can never reach a per-day limit, so
// the breaker had never once tripped. Hoisting the state here is what makes the
// limit mean what it says.
//
// The day boundary is UTC. A tenant-local calendar would be more precise, but it
// would also mean carrying a timezone per tenant to make a spend cap marginally
// more intuitive; the cap is a safety limit, not an invoice.
type SpendLedger struct {
	// now is injectable so the rollover is testable without waiting a day.
	now func() time.Time

	mu    sync.Mutex
	day   string
	units map[string]int
}

// NewSpendLedger builds an empty ledger.
func NewSpendLedger() *SpendLedger {
	return &SpendLedger{now: time.Now, units: make(map[string]int)}
}

// Consume charges one unit against a tenant's daily cap, returning false when
// the cap is already reached. A cap of zero or less means no external spend is
// permitted at all, which is consistent with the default-deny posture: a tenant
// that never set a limit does not thereby get an unlimited one.
func (l *SpendLedger) Consume(tenant string, cap int) bool {
	if l == nil {
		return true
	}
	if cap <= 0 {
		return false
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	l.rollLocked()

	if l.units[tenant] >= cap {
		return false
	}
	l.units[tenant]++
	return true
}

// Spent reports a tenant's usage in the current day, for observability.
func (l *SpendLedger) Spent(tenant string) int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rollLocked()
	return l.units[tenant]
}

// rollLocked resets every counter when the UTC day changes. Callers hold mu.
func (l *SpendLedger) rollLocked() {
	today := l.now().UTC().Format("2006-01-02")
	if l.day == today {
		return
	}
	l.day = today
	l.units = make(map[string]int)
}
