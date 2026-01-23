package location

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/sebas/switchboard/internal/signaling/store"
)

// ErrIntervalTooBrief is returned when the requested expires value is below the minimum.
// Per RFC 3261 Section 10.3, the registrar should respond with 423 Interval Too Brief
// and include a Min-Expires header with the minimum allowed value.
var ErrIntervalTooBrief = errors.New("interval too brief")

// Store manages user location bindings with TTL support.
// Multiple bindings per AOR are supported (same user, multiple devices).
type Store struct {
	// Primary store: AOR -> map of BindingID -> Binding
	bindings *store.TTLStore[string, map[string]*Binding]

	// Mutex for operations that need to modify binding maps
	mu sync.Mutex

	// Configuration
	defaultExpires int // Default TTL in seconds
	maxExpires     int // Maximum allowed TTL
	minExpires     int // Minimum allowed TTL
}

// StoreConfig contains location store configuration
type StoreConfig struct {
	CleanupInterval time.Duration // How often to clean expired entries
	DefaultExpires  int           // Default TTL in seconds (default: 3600)
	MaxExpires      int           // Maximum TTL in seconds (default: 7200)
	MinExpires      int           // Minimum TTL in seconds (default: 60)
}

// DefaultStoreConfig returns sensible defaults
func DefaultStoreConfig() StoreConfig {
	return StoreConfig{
		CleanupInterval: 30 * time.Second,
		DefaultExpires:  60,  // 1 minute
		MaxExpires:      120, // 2 minutes
		MinExpires:      30,  // 30 seconds
	}
}

// NewStore creates a new location store
func NewStore(cfg StoreConfig) *Store {
	return &Store{
		bindings:       store.NewTTLStore[string, map[string]*Binding](cfg.CleanupInterval),
		defaultExpires: cfg.DefaultExpires,
		maxExpires:     cfg.MaxExpires,
		minExpires:     cfg.MinExpires,
	}
}

// Register adds or updates a binding for an AOR.
// Returns the binding and any error.
func (s *Store) Register(binding *Binding) (*Binding, error) {
	if binding.AOR == "" {
		return nil, fmt.Errorf("AOR cannot be empty")
	}
	if binding.ContactURI == "" {
		return nil, fmt.Errorf("ContactURI cannot be empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Normalize expires
	expires := binding.Expires
	if expires <= 0 {
		expires = s.defaultExpires
	}
	// RFC 3261 Section 10.3: If expires is below the minimum, return an error.
	// The registrar should respond with 423 Interval Too Brief.
	if expires < s.minExpires {
		return nil, ErrIntervalTooBrief
	}
	if expires > s.maxExpires {
		expires = s.maxExpires
	}

	// Generate binding ID if not set
	if binding.BindingID == "" {
		binding.BindingID = GenerateBindingID(binding.ContactURI, binding.InstanceID)
	}

	// Set timing
	now := time.Now()
	binding.Expires = expires
	binding.ExpiresAt = now.Add(time.Duration(expires) * time.Second)
	binding.RegisteredAt = now

	// Get or create bindings map for this AOR
	bindingsMap, exists := s.bindings.Get(binding.AOR)
	if !exists {
		bindingsMap = make(map[string]*Binding)
	}

	// Check CSeq for existing binding with same Call-ID
	if existing, ok := bindingsMap[binding.BindingID]; ok {
		if !existing.ValidateCSeq(binding.CallID, binding.CSeq) {
			return nil, fmt.Errorf("invalid CSeq: must be higher than %d for same Call-ID", existing.CSeq)
		}
	}

	// Store the binding
	bindingsMap[binding.BindingID] = binding

	// Calculate max TTL across all bindings for this AOR
	maxTTL := time.Duration(expires) * time.Second
	for _, b := range bindingsMap {
		if ttl := b.TTL(); ttl > maxTTL {
			maxTTL = ttl
		}
	}

	// Update the store with the new bindings map
	s.bindings.Set(binding.AOR, bindingsMap, maxTTL)

	slog.Info("[LOCATION] Registered",
		"aor", binding.AOR,
		"contact", binding.ContactURI,
		"binding_id", binding.BindingID,
		"expires", expires,
		"transport", binding.Transport,
	)

	return binding, nil
}

// Unregister removes a binding.
// If bindingID is empty and contactURI is "*", removes all bindings for the AOR.
func (s *Store) Unregister(aor string, bindingID string, isWildcard bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if isWildcard {
		// Remove all bindings for this AOR
		s.bindings.Delete(aor)
		slog.Info("[LOCATION] Unregistered all bindings", "aor", aor)
		return nil
	}

	// Get bindings for AOR
	bindingsMap, exists := s.bindings.Get(aor)
	if !exists {
		return fmt.Errorf("no bindings found for AOR: %s", aor)
	}

	// Remove specific binding
	if _, ok := bindingsMap[bindingID]; !ok {
		return fmt.Errorf("binding not found: %s", bindingID)
	}

	delete(bindingsMap, bindingID)

	if len(bindingsMap) == 0 {
		// No more bindings, remove the AOR entry
		s.bindings.Delete(aor)
	} else {
		// Update with remaining bindings
		maxTTL := time.Duration(0)
		for _, b := range bindingsMap {
			if ttl := b.TTL(); ttl > maxTTL {
				maxTTL = ttl
			}
		}
		s.bindings.Set(aor, bindingsMap, maxTTL)
	}

	slog.Info("[LOCATION] Unregistered", "aor", aor, "binding_id", bindingID)
	return nil
}

// Lookup returns all active bindings for an AOR
func (s *Store) Lookup(aor string) []*Binding {
	bindingsMap, exists := s.bindings.Get(aor)
	if !exists {
		return nil
	}

	// Filter expired and collect
	result := make([]*Binding, 0, len(bindingsMap))
	for _, b := range bindingsMap {
		if !b.IsExpired() {
			result = append(result, b)
		}
	}

	return result
}

// LookupOne returns the highest priority non-expired binding for an AOR
func (s *Store) LookupOne(aor string) *Binding {
	bindings := s.Lookup(aor)
	if len(bindings) == 0 {
		return nil
	}

	// Find highest q-value (default is 1.0 if not specified)
	var best *Binding
	bestQ := float32(-1)

	for _, b := range bindings {
		q := b.QValue
		if q == 0 {
			q = 1.0 // RFC 3261: default q is 1.0
		}
		if q > bestQ {
			bestQ = q
			best = b
		}
	}

	return best
}

// List returns all active bindings across all AORs
func (s *Store) List() []*Binding {
	allBindings := s.bindings.All()
	result := make([]*Binding, 0)

	for _, bindingsMap := range allBindings {
		for _, b := range bindingsMap {
			if !b.IsExpired() {
				result = append(result, b)
			}
		}
	}

	return result
}

// ListByAOR returns a map of AOR to bindings
func (s *Store) ListByAOR() map[string][]*Binding {
	allBindings := s.bindings.All()
	result := make(map[string][]*Binding)

	for aor, bindingsMap := range allBindings {
		bindings := make([]*Binding, 0, len(bindingsMap))
		for _, b := range bindingsMap {
			if !b.IsExpired() {
				bindings = append(bindings, b)
			}
		}
		if len(bindings) > 0 {
			result[aor] = bindings
		}
	}

	return result
}

// Count returns the total number of active bindings
func (s *Store) Count() int {
	allBindings := s.bindings.All()
	count := 0
	for _, bindingsMap := range allBindings {
		for _, b := range bindingsMap {
			if !b.IsExpired() {
				count++
			}
		}
	}
	return count
}

// CountAORs returns the number of AORs with active bindings
func (s *Store) CountAORs() int {
	return s.bindings.Len()
}

// Has returns true if the AOR has any active bindings
func (s *Store) Has(aor string) bool {
	return len(s.Lookup(aor)) > 0
}

// LookupByUser searches for bindings where the AOR's user part matches the given user.
// This handles the case where we know the extension (e.g., "1000") but not the exact
// domain/port format used during registration.
// Per RFC 3261, the user part is the portion before '@' in a SIP URI.
func (s *Store) LookupByUser(user string) []*Binding {
	if user == "" {
		return nil
	}

	allBindings := s.bindings.All()
	var result []*Binding

	for aor, bindingsMap := range allBindings {
		// Extract user part from AOR
		aorUser := extractUserFromAOR(aor)
		if aorUser == user {
			for _, b := range bindingsMap {
				if !b.IsExpired() {
					result = append(result, b)
				}
			}
		}
	}

	return result
}

// extractUserFromAOR extracts the user part from a SIP AOR.
// Examples:
//   - "sip:1000@domain.com" -> "1000"
//   - "sip:alice@domain.com:5060" -> "alice"
//   - "1000@domain.com" -> "1000"
//   - "1000" -> "1000"
func extractUserFromAOR(aor string) string {
	// Remove sip: or sips: prefix
	s := aor
	if strings.HasPrefix(s, "sip:") {
		s = s[4:]
	} else if strings.HasPrefix(s, "sips:") {
		s = s[5:]
	}

	// Find the @ separator
	atIdx := strings.Index(s, "@")
	if atIdx == -1 {
		// No domain, the whole thing is the user
		return s
	}

	return s[:atIdx]
}

// Close stops the cleanup goroutine
func (s *Store) Close() {
	s.bindings.Close()
}

// MinExpires returns the minimum allowed expires value in seconds.
// This is used for the Min-Expires header in 423 responses per RFC 3261.
func (s *Store) MinExpires() int {
	return s.minExpires
}
