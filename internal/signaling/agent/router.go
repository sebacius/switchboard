package agent

import (
	"strings"

	"github.com/sebas/switchboard/internal/signaling/location"
	"github.com/sebas/switchboard/internal/signaling/trunk"
)

// The router is the deterministic layer between the SIP INVITE and the runner.
// It runs BEFORE any LLM call: it classifies the call's direction (a trust
// gradient, design #8) and resolves the tenant (no default, design #9). It takes
// plain inputs — never a sip.Request — so it is fully testable and so the SIP
// wiring (group 7) can populate RouteInput from the INVITE without dragging
// sipgo into this package.

// RouteInput is the deterministic, sipgo-free input to the router. Group 7
// populates it from the INVITE's From/To URIs and the transport source IP.
type RouteInput struct {
	// Caller is the From-URI user part (e.g. "102").
	Caller string
	// FromAOR is the full From AOR ("sip:102@acme.switchboard.com") used for an
	// exact registration lookup.
	FromAOR string
	// FromHost is the From-URI host ("acme.switchboard.com"), whose leftmost
	// label selects the tenant for internal/outbound calls.
	FromHost string
	// Callee is the To-URI user part: the dialed extension for internal/outbound,
	// or the DID for inbound.
	Callee string
	// SourceIP is the transport-layer source host of the INVITE, used to match a
	// configured trunk peer.
	SourceIP string
}

// Directory answers "is this AOR/user a registered directory user". It is the
// minimal seam the router depends on so tests can substitute a fake without the
// real location store. Group 7 satisfies it with DirectoryFromLocation over the
// live LocationStore.
type Directory interface {
	// IsRegistered reports whether the AOR has an active binding, or (failing an
	// exact AOR match) whether the user part has any binding. The user fallback
	// handles callees expressed as a bare extension with no known host.
	IsRegistered(aor, user string) bool
}

// locationDirectory adapts a location.LocationStore to Directory.
type locationDirectory struct {
	store location.LocationStore
}

// DirectoryFromLocation adapts the live LocationStore to the Directory seam the
// router needs. An AOR is "registered" when it has an exact binding (Has) or,
// failing that, when its user part has any binding (LookupByUser) — the latter
// covers a bare-extension callee whose exact host we do not know.
func DirectoryFromLocation(store location.LocationStore) Directory {
	return &locationDirectory{store: store}
}

func (d *locationDirectory) IsRegistered(aor, user string) bool {
	if aor != "" && d.store.Has(aor) {
		return true
	}
	if user != "" && len(d.store.LookupByUser(user)) > 0 {
		return true
	}
	return false
}

// Router classifies direction and resolves tenant deterministically. It holds
// no per-call state and is safe for concurrent use.
type Router struct {
	dir    Directory
	trunk  trunk.Trunk
	routes *trunk.DIDRoutes
}

// NewRouter builds a Router over the directory, trunk, and DID table. All three
// are required; callers that disable trunking still pass an empty PeerStore-
// backed trunk and an empty DIDRoutes (their loaders return empty, not nil).
func NewRouter(dir Directory, t trunk.Trunk, routes *trunk.DIDRoutes) *Router {
	return &Router{dir: dir, trunk: t, routes: routes}
}

// Route classifies the call and resolves its tenant. It returns the completed
// CallContext and ok=true only when BOTH direction and tenant resolve. On any
// failure it returns ok=false and never invents a tenant — an unrecognized
// source (neither trunk peer nor registered user) and an unmapped DID / missing
// subdomain are all hard fails the admission/ingress layer turns into a reject.
func (r *Router) Route(in RouteInput) (CallContext, bool) {
	dir, ok := r.classify(in)
	if !ok {
		return CallContext{}, false
	}

	tenant, ok := r.resolveTenant(dir, in)
	if !ok {
		return CallContext{}, false
	}

	return CallContext{
		Caller:    in.Caller,
		Callee:    in.Callee,
		Direction: dir,
		Tenant:    tenant,
	}, true
}

// classify applies the direction trust gradient (design #8). The order matters:
// a trunk-peer source is inbound regardless of who the From claims to be (the
// strongest signal, since the source IP is not forgeable by the caller's prompt).
func (r *Router) classify(in RouteInput) (Direction, bool) {
	if _, ok := r.trunk.MatchInbound(in.SourceIP); ok {
		return DirectionInbound, true
	}

	callerRegistered := r.dir.IsRegistered(in.FromAOR, in.Caller)
	if !callerRegistered {
		// Neither a trunk peer nor a registered directory user: the router does
		// not invent a tenant for an unknown source.
		return "", false
	}

	// Caller is a registered directory user. Whether the callee is a directory
	// user decides internal vs. outbound. The callee has no AOR in RouteInput
	// (we only know its user part for a bare extension), so we look it up by user.
	if r.dir.IsRegistered("", in.Callee) {
		return DirectionInternal, true
	}
	return DirectionOutbound, true
}

// resolveTenant resolves the tenant with no default (design #9). Inbound tenant
// comes from the DID table; internal/outbound from the From-host subdomain.
func (r *Router) resolveTenant(dir Direction, in RouteInput) (string, bool) {
	switch dir {
	case DirectionInbound:
		return r.routes.TenantForDID(in.Callee)
	case DirectionInternal, DirectionOutbound:
		return subdomainTenant(in.FromHost)
	default:
		return "", false
	}
}

// subdomainTenant extracts the leftmost label of a host as the tenant name
// ("acme.switchboard.com" → "acme"). A host with no label (empty, or a bare
// "." ) yields false: there is no default tenant.
func subdomainTenant(host string) (string, bool) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", false
	}
	label := host
	if i := strings.IndexByte(host, '.'); i >= 0 {
		label = host[:i]
	}
	if label == "" {
		return "", false
	}
	return label, true
}
