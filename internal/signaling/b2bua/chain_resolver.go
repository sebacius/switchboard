package b2bua

import (
	"context"
	"errors"

	"github.com/sebas/switchboard/internal/signaling/location"
)

// ChainResolver tries multiple resolvers in order until one succeeds.
// It short-circuits on the first resolver that can handle the target format.
type ChainResolver struct {
	resolvers []Resolver
}

// NewChainResolver creates a new ChainResolver with the given resolvers.
// Resolvers are tried in order; use most specific first.
func NewChainResolver(resolvers ...Resolver) *ChainResolver {
	return &ChainResolver{
		resolvers: resolvers,
	}
}

// CanResolve returns true if any resolver can handle the target.
func (r *ChainResolver) CanResolve(target string) bool {
	for _, resolver := range r.resolvers {
		if resolver.CanResolve(target) {
			return true
		}
	}
	return false
}

// Resolve tries each resolver that can handle the target until one succeeds.
func (r *ChainResolver) Resolve(ctx context.Context, target string) (*LookupResult, error) {
	var lastErr error

	for _, resolver := range r.resolvers {
		if !resolver.CanResolve(target) {
			continue
		}

		result, err := resolver.Resolve(ctx, target)
		if err == nil {
			return result, nil
		}

		// Remember the error but try next resolver
		lastErr = err

		// If target was found but has no contacts, don't try other resolvers
		if errors.Is(err, ErrNoContacts) {
			return nil, err
		}
	}

	if lastErr != nil {
		return nil, lastErr
	}

	return nil, &LookupError{
		Target: target,
		Reason: "no resolver could handle target",
		Cause:  ErrTargetNotFound,
	}
}

// DefaultResolver returns a ChainResolver with standard resolvers.
// Order: DirectResolver -> UserResolver
// Gateway resolver is not included by default (requires gateway store).
func DefaultResolver(locationStore location.LocationStore, domain string) *ChainResolver {
	return NewChainResolver(
		NewDirectResolver(),
		NewUserResolver(locationStore, domain),
	)
}

// Ensure ChainResolver implements Resolver
var _ Resolver = (*ChainResolver)(nil)
