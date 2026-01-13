package b2bua

import (
	"context"
	"sort"
	"strings"

	"github.com/sebas/switchboard/internal/signaling/location"
)

// UserResolver resolves user targets via LocationStore.
// Handles targets in the format "user/1001" or plain "1001".
type UserResolver struct {
	store  location.LocationStore
	domain string // Default domain for AOR construction
}

// NewUserResolver creates a new UserResolver.
func NewUserResolver(store location.LocationStore, domain string) *UserResolver {
	return &UserResolver{
		store:  store,
		domain: domain,
	}
}

// CanResolve returns true for "user/" prefixed targets or plain extensions.
func (r *UserResolver) CanResolve(target string) bool {
	// Handle "user/1001" format
	if strings.HasPrefix(target, "user/") {
		return true
	}

	// Reject direct SIP URIs
	if strings.HasPrefix(target, "sip:") || strings.HasPrefix(target, "sips:") {
		return false
	}

	// Reject gateway targets
	if strings.HasPrefix(target, "gateway/") || strings.HasPrefix(target, "trunk/") {
		return false
	}

	// Accept plain extensions (numeric or alphanumeric)
	return len(target) > 0
}

// Resolve looks up the target in the location store.
func (r *UserResolver) Resolve(ctx context.Context, target string) (*LookupResult, error) {
	// Extract extension from target
	extension := target
	if strings.HasPrefix(target, "user/") {
		extension = strings.TrimPrefix(target, "user/")
	}

	if extension == "" {
		return nil, &LookupError{
			Target: target,
			Reason: "empty extension",
		}
	}

	// Try direct lookup first (exact AOR match)
	bindings := r.lookupBindings(extension)
	if len(bindings) == 0 {
		return nil, &LookupError{
			Target: target,
			Reason: "no registrations found",
			Cause:  ErrNoContacts,
		}
	}

	// Convert bindings to contacts
	contacts := make([]ResolvedContact, 0, len(bindings))
	for _, b := range bindings {
		contacts = append(contacts, ResolvedContact{
			URI:       b.EffectiveContact(),
			Priority:  b.QValue,
			Transport: b.Transport,
			Binding:   b,
		})
	}

	// Sort by priority (highest first)
	sort.Slice(contacts, func(i, j int) bool {
		return contacts[i].Priority > contacts[j].Priority
	})

	return &LookupResult{
		Type:     LookupResultTypeUser,
		Original: target,
		Contacts: contacts,
	}, nil
}

// lookupBindings searches for bindings matching the extension.
func (r *UserResolver) lookupBindings(extension string) []*location.Binding {
	// Try exact AOR match first (e.g., "sip:1000@domain.com")
	aor := r.buildAOR(extension)
	bindings := r.store.Lookup(aor)
	if len(bindings) > 0 {
		return bindings
	}

	// Try without domain (just the extension as AOR)
	bindings = r.store.Lookup(extension)
	if len(bindings) > 0 {
		return bindings
	}

	// Try with sip: prefix only
	bindings = r.store.Lookup("sip:" + extension)
	if len(bindings) > 0 {
		return bindings
	}

	// Fallback: search by user part only.
	// This handles cases where the AOR was stored with a different domain/port
	// than what we're constructing (e.g., client registered with port in To header:
	// "sip:1000@192.168.1.100:5060" but we're searching for "sip:1000@192.168.1.100").
	// Per RFC 3261 Section 10.3, the AOR comes from the To header as-is.
	bindings = r.store.LookupByUser(extension)
	if len(bindings) > 0 {
		return bindings
	}

	return nil
}

// buildAOR constructs an AOR from an extension.
func (r *UserResolver) buildAOR(extension string) string {
	if strings.Contains(extension, "@") {
		// Already has domain
		if strings.HasPrefix(extension, "sip:") {
			return extension
		}
		return "sip:" + extension
	}

	// Add domain
	if r.domain != "" {
		return "sip:" + extension + "@" + r.domain
	}

	return "sip:" + extension
}

// Ensure UserResolver implements Resolver
var _ Resolver = (*UserResolver)(nil)
