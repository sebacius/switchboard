// Package location manages SIP user location bindings (REGISTER).
package location

// LocationStore defines the interface for SIP location/registration storage.
// This allows for different implementations (in-memory, Redis, database, etc.)
// and enables proper dependency injection for testing.
type LocationStore interface {
	// Register adds or updates a binding for an AOR.
	// Returns the binding with normalized values (timing, binding ID) and any error.
	// Returns ErrIntervalTooBrief if the requested expires is below the minimum.
	Register(binding *Binding) (*Binding, error)

	// Unregister removes a binding.
	// If isWildcard is true, removes all bindings for the AOR.
	// Otherwise, removes only the specific binding identified by bindingID.
	Unregister(aor string, bindingID string, isWildcard bool) error

	// Lookup returns all active (non-expired) bindings for an AOR.
	// Returns nil if no bindings exist.
	Lookup(aor string) []*Binding

	// LookupOne returns the highest priority non-expired binding for an AOR.
	// Priority is determined by q-value (higher is better, default 1.0).
	// Returns nil if no bindings exist.
	LookupOne(aor string) *Binding

	// List returns all active bindings across all AORs.
	List() []*Binding

	// ListByAOR returns all active bindings grouped by AOR.
	ListByAOR() map[string][]*Binding

	// Count returns the total number of active bindings.
	Count() int

	// CountAORs returns the number of AORs with active bindings.
	CountAORs() int

	// Has returns true if the AOR has any active bindings.
	Has(aor string) bool

	// LookupByUser searches for bindings where the AOR's user part matches the given user.
	// This is useful when the exact domain/port in the AOR is unknown.
	// For example, LookupByUser("1000") would match "sip:1000@domain.com:5060".
	LookupByUser(user string) []*Binding

	// MinExpires returns the minimum allowed expires value in seconds.
	// This is used for the Min-Expires header in 423 responses per RFC 3261.
	MinExpires() int

	// Close releases resources used by the store.
	Close()
}

// Ensure Store implements LocationStore
var _ LocationStore = (*Store)(nil)
