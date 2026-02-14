package b2bua

import (
	"context"
	"sort"
	"strings"

	"github.com/sebas/switchboard/internal/signaling/location"
)

// Resolver resolves dial targets to SIP URIs.
//
// Implementations:
//   - UserResolver: uses LocationStore
//   - GatewayResolver: uses gateway configuration
//   - DirectResolver: passthrough for sip: URIs
//   - TargetResolver: dispatches to resolvers by prefix
type Resolver interface {
	// Resolve looks up the target and returns resolved contacts.
	// Returns ErrTargetNotFound if the target cannot be resolved.
	// Returns ErrNoContacts if target exists but has no active registrations.
	Resolve(ctx context.Context, target string) (*LookupResult, error)
}

// TargetResolver dispatches to resolvers by target prefix.
// Prefixes are matched longest-first to allow specific prefixes to override general ones.
type TargetResolver struct {
	prefixes []prefixEntry // Sorted by prefix length (longest first)
	fallback Resolver
}

type prefixEntry struct {
	prefix   string
	resolver Resolver
}

// NewTargetResolver creates a new TargetResolver.
func NewTargetResolver() *TargetResolver {
	return &TargetResolver{
		prefixes: make([]prefixEntry, 0),
	}
}

// Register adds a resolver for a specific prefix.
// Call this for each prefix the resolver should handle (e.g., "sip:", "sips:", "user/").
func (r *TargetResolver) Register(prefix string, resolver Resolver) {
	r.prefixes = append(r.prefixes, prefixEntry{prefix: prefix, resolver: resolver})
	// Keep sorted by prefix length (longest first) for correct matching
	sort.Slice(r.prefixes, func(i, j int) bool {
		return len(r.prefixes[i].prefix) > len(r.prefixes[j].prefix)
	})
}

// SetFallback sets the resolver to use when no prefix matches.
// This handles plain extensions like "1001" without any prefix.
func (r *TargetResolver) SetFallback(resolver Resolver) {
	r.fallback = resolver
}

// Resolve dispatches to the appropriate resolver based on target prefix.
func (r *TargetResolver) Resolve(ctx context.Context, target string) (*LookupResult, error) {
	// Check prefixes in order (longest match first due to sorting)
	for _, entry := range r.prefixes {
		if strings.HasPrefix(target, entry.prefix) {
			return entry.resolver.Resolve(ctx, target)
		}
	}

	// Use fallback for targets without a matching prefix
	if r.fallback != nil {
		return r.fallback.Resolve(ctx, target)
	}

	return nil, &LookupError{
		Target: target,
		Reason: "no resolver for target",
		Cause:  ErrTargetNotFound,
	}
}

// DirectResolver passes through SIP URIs without modification.
// Handles targets in the format "sip:user@host:port" or "sips:user@host:port".
type DirectResolver struct{}

// NewDirectResolver creates a new DirectResolver.
func NewDirectResolver() *DirectResolver {
	return &DirectResolver{}
}

// Resolve returns the target as-is as a single contact.
func (r *DirectResolver) Resolve(ctx context.Context, target string) (*LookupResult, error) {
	transport := extractTransport(target)

	return &LookupResult{
		Type:     LookupResultTypeDirect,
		Original: target,
		Contacts: []ResolvedContact{
			{
				URI:       target,
				Priority:  1.0,
				Transport: transport,
				Binding:   nil,
			},
		},
	}, nil
}

// extractTransport extracts the transport parameter from a SIP URI.
func extractTransport(uri string) string {
	lower := strings.ToLower(uri)
	idx := strings.Index(lower, "transport=")
	if idx == -1 {
		return ""
	}

	start := idx + len("transport=")
	end := start
	for end < len(uri) && uri[end] != ';' && uri[end] != '>' && uri[end] != ' ' {
		end++
	}

	if end > start {
		return strings.ToUpper(uri[start:end])
	}

	return ""
}

// UserResolver resolves user targets via LocationStore.
// Handles targets in the format "user/1001" or plain "1001".
type UserResolver struct {
	store  location.LocationStore
	domain string
}

// NewUserResolver creates a new UserResolver.
func NewUserResolver(store location.LocationStore, domain string) *UserResolver {
	return &UserResolver{
		store:  store,
		domain: domain,
	}
}

// Resolve looks up the target in the location store.
func (r *UserResolver) Resolve(ctx context.Context, target string) (*LookupResult, error) {
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

	bindings := r.lookupBindings(extension)
	if len(bindings) == 0 {
		return nil, &LookupError{
			Target: target,
			Reason: "no registrations found",
			Cause:  ErrNoContacts,
		}
	}

	contacts := make([]ResolvedContact, 0, len(bindings))
	for _, b := range bindings {
		contacts = append(contacts, ResolvedContact{
			URI:       b.EffectiveContact(),
			Priority:  b.QValue,
			Transport: b.Transport,
			Binding:   b,
		})
	}

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
	if bindings := r.store.Lookup(aor); len(bindings) > 0 {
		return bindings
	}

	// Try without domain (just the extension as AOR)
	if bindings := r.store.Lookup(extension); len(bindings) > 0 {
		return bindings
	}

	// Try with sip: prefix only
	if bindings := r.store.Lookup("sip:" + extension); len(bindings) > 0 {
		return bindings
	}

	// Fallback: search by user part only.
	// Handles cases where the AOR was stored with a different domain/port.
	if bindings := r.store.LookupByUser(extension); len(bindings) > 0 {
		return bindings
	}

	return nil
}

// buildAOR constructs an AOR from an extension.
func (r *UserResolver) buildAOR(extension string) string {
	if strings.Contains(extension, "@") {
		if strings.HasPrefix(extension, "sip:") {
			return extension
		}
		return "sip:" + extension
	}

	if r.domain != "" {
		return "sip:" + extension + "@" + r.domain
	}

	return "sip:" + extension
}

// Ensure implementations satisfy Resolver interface
var (
	_ Resolver = (*TargetResolver)(nil)
	_ Resolver = (*DirectResolver)(nil)
	_ Resolver = (*UserResolver)(nil)
)
