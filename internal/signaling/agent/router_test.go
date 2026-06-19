package agent

import (
	"testing"

	"github.com/sebas/switchboard/internal/signaling/trunk"
)

// fakeDirectory is a test Directory: a set of registered AORs and a set of
// registered user parts. IsRegistered mirrors the real adapter's logic (exact
// AOR, then user fallback).
type fakeDirectory struct {
	aors  map[string]bool
	users map[string]bool
}

func (f fakeDirectory) IsRegistered(aor, user string) bool {
	if aor != "" && f.aors[aor] {
		return true
	}
	if user != "" && f.users[user] {
		return true
	}
	return false
}

// newRouter builds a Router over a fake directory and a real (static) trunk and
// DID table, so the trunk/DID wiring is exercised, not stubbed.
func newRouter(dir Directory, peers []*trunk.Peer, dids map[string]string) *Router {
	t := trunk.NewStaticTrunk(trunk.NewPeerStore(peers), "")
	return NewRouter(dir, t, trunk.NewDIDRoutes(dids))
}

func TestRouteInboundFromTrunkPeerResolvesDIDTenant(t *testing.T) {
	peers := []*trunk.Peer{{Name: "carrier", Host: "203.0.113.7", Role: trunk.RoleInbound}}
	dir := fakeDirectory{} // nobody registered: source classification must win.
	r := newRouter(dir, peers, map[string]string{"18005551212": "acme"})

	cc, ok := r.Route(RouteInput{
		Caller:   "external",
		Callee:   "18005551212",
		SourceIP: "203.0.113.7",
	})
	if !ok {
		t.Fatalf("expected admit, got ok=false")
	}
	if cc.Direction != DirectionInbound {
		t.Errorf("direction = %q, want inbound", cc.Direction)
	}
	if cc.Tenant != "acme" {
		t.Errorf("tenant = %q, want acme", cc.Tenant)
	}
}

func TestRouteInboundUnmappedDIDFails(t *testing.T) {
	peers := []*trunk.Peer{{Name: "carrier", Host: "203.0.113.7", Role: trunk.RoleInbound}}
	r := newRouter(fakeDirectory{}, peers, map[string]string{"18005551212": "acme"})

	if _, ok := r.Route(RouteInput{
		Caller:   "external",
		Callee:   "19998887777", // not in the DID table
		SourceIP: "203.0.113.7",
	}); ok {
		t.Fatalf("expected fail for unmapped DID, got ok=true")
	}
}

func TestRouteInternalWhenBothRegistered(t *testing.T) {
	dir := fakeDirectory{
		aors:  map[string]bool{"sip:102@acme.switchboard.com": true},
		users: map[string]bool{"103": true},
	}
	r := newRouter(dir, nil, nil)

	cc, ok := r.Route(RouteInput{
		Caller:   "102",
		FromAOR:  "sip:102@acme.switchboard.com",
		FromHost: "acme.switchboard.com",
		Callee:   "103",
	})
	if !ok {
		t.Fatalf("expected admit, got ok=false")
	}
	if cc.Direction != DirectionInternal {
		t.Errorf("direction = %q, want internal", cc.Direction)
	}
	if cc.Tenant != "acme" {
		t.Errorf("tenant = %q, want acme", cc.Tenant)
	}
}

func TestRouteOutboundWhenCallerRegisteredCalleeNot(t *testing.T) {
	dir := fakeDirectory{
		users: map[string]bool{"102": true}, // caller registered, callee not.
	}
	r := newRouter(dir, nil, nil)

	cc, ok := r.Route(RouteInput{
		Caller:   "102",
		FromHost: "acme.switchboard.com",
		Callee:   "15551234567",
	})
	if !ok {
		t.Fatalf("expected admit, got ok=false")
	}
	if cc.Direction != DirectionOutbound {
		t.Errorf("direction = %q, want outbound", cc.Direction)
	}
	if cc.Tenant != "acme" {
		t.Errorf("tenant = %q, want acme", cc.Tenant)
	}
}

func TestRouteNeitherRegisteredNorTrunkFails(t *testing.T) {
	r := newRouter(fakeDirectory{}, nil, nil)

	if _, ok := r.Route(RouteInput{
		Caller:   "stranger",
		FromHost: "evil.example.com",
		Callee:   "102",
		SourceIP: "198.51.100.9", // not a trunk peer
	}); ok {
		t.Fatalf("expected fail for unknown source, got ok=true")
	}
}

func TestRouteOutboundMissingFromHostFails(t *testing.T) {
	dir := fakeDirectory{users: map[string]bool{"102": true}}
	r := newRouter(dir, nil, nil)

	if _, ok := r.Route(RouteInput{
		Caller:   "102",
		FromHost: "", // no host → no tenant, no default
		Callee:   "15551234567",
	}); ok {
		t.Fatalf("expected fail for missing From host, got ok=true")
	}
}

func TestRouteTrunkSourceOverridesRegisteredFrom(t *testing.T) {
	// A spoofed From claiming to be a registered user must NOT downgrade an
	// inbound trunk call: the source IP is the stronger, unforgeable signal.
	peers := []*trunk.Peer{{Name: "carrier", Host: "203.0.113.7", Role: trunk.RoleInbound}}
	dir := fakeDirectory{users: map[string]bool{"102": true}}
	r := newRouter(dir, peers, map[string]string{"18005551212": "acme"})

	cc, ok := r.Route(RouteInput{
		Caller:   "102",
		FromHost: "evil.example.com",
		Callee:   "18005551212",
		SourceIP: "203.0.113.7",
	})
	if !ok {
		t.Fatalf("expected admit, got ok=false")
	}
	if cc.Direction != DirectionInbound {
		t.Errorf("direction = %q, want inbound", cc.Direction)
	}
	if cc.Tenant != "acme" {
		t.Errorf("tenant = %q, want acme (from DID, not subdomain)", cc.Tenant)
	}
}

func TestSubdomainTenant(t *testing.T) {
	tests := []struct {
		host     string
		want     string
		wantOK   bool
		describe string
	}{
		{"acme.switchboard.com", "acme", true, "multi-label host"},
		{"acme.x.com", "acme", true, "short multi-label host"},
		{"acme", "acme", true, "bare label (no dot)"},
		{"", "", false, "empty host"},
		{".switchboard.com", "", false, "leading dot, empty label"},
		{"  acme.x.com  ", "acme", true, "trimmed whitespace"},
	}
	for _, tt := range tests {
		t.Run(tt.describe, func(t *testing.T) {
			got, ok := subdomainTenant(tt.host)
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("subdomainTenant(%q) = (%q, %v), want (%q, %v)",
					tt.host, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
