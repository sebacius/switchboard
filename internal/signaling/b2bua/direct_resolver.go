package b2bua

import (
	"context"
	"strings"
)

// DirectResolver passes through SIP URIs without modification.
// Handles targets in the format "sip:user@host:port" or "sips:user@host:port".
type DirectResolver struct{}

// NewDirectResolver creates a new DirectResolver.
func NewDirectResolver() *DirectResolver {
	return &DirectResolver{}
}

// CanResolve returns true for sip: and sips: URIs.
func (r *DirectResolver) CanResolve(target string) bool {
	return strings.HasPrefix(target, "sip:") || strings.HasPrefix(target, "sips:")
}

// Resolve returns the target as-is as a single contact.
func (r *DirectResolver) Resolve(ctx context.Context, target string) (*LookupResult, error) {
	if !r.CanResolve(target) {
		return nil, &LookupError{
			Target: target,
			Reason: "not a SIP URI",
			Cause:  ErrTargetNotFound,
		}
	}

	// Extract transport from URI params if present
	transport := extractTransport(target)

	return &LookupResult{
		Type:     LookupResultTypeDirect,
		Original: target,
		Contacts: []ResolvedContact{
			{
				URI:       target,
				Priority:  1.0,
				Transport: transport,
				Binding:   nil, // No location binding for direct URIs
			},
		},
	}, nil
}

// extractTransport extracts the transport parameter from a SIP URI.
func extractTransport(uri string) string {
	// Look for transport= parameter
	lower := strings.ToLower(uri)
	idx := strings.Index(lower, "transport=")
	if idx == -1 {
		return ""
	}

	// Extract transport value
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

// Ensure DirectResolver implements Resolver
var _ Resolver = (*DirectResolver)(nil)
