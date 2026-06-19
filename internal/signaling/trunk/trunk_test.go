package trunk

import (
	"testing"

	"github.com/emiago/sipgo/sip"
)

func TestMatchInbound(t *testing.T) {
	store := NewPeerStore([]*Peer{
		{Name: "provider-a", Host: "203.0.113.10", Port: 5060, Role: RoleInbound},
		{Name: "egress-only", Host: "203.0.113.20", Port: 5060, Role: RoleOutbound},
		{Name: "both", Host: "203.0.113.30", Port: 5060, Role: RoleBoth},
	})

	tests := []struct {
		name string
		src  string
		want bool
		peer string
	}{
		{"inbound peer matches", "203.0.113.10", true, "provider-a"},
		{"both peer matches inbound", "203.0.113.30", true, "both"},
		{"outbound-only peer is not inbound", "203.0.113.20", false, ""},
		{"unknown source no match", "198.51.100.5", false, ""},
		{"empty source no match", "", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			peer, ok := store.MatchInbound(tt.src)
			if ok != tt.want {
				t.Fatalf("MatchInbound(%q) ok = %v, want %v", tt.src, ok, tt.want)
			}
			if ok && peer.Name != tt.peer {
				t.Fatalf("MatchInbound(%q) peer = %q, want %q", tt.src, peer.Name, tt.peer)
			}
		})
	}
}

func TestEgressPeer(t *testing.T) {
	withEgress := NewPeerStore([]*Peer{
		{Name: "in", Host: "203.0.113.10", Role: RoleInbound},
		{Name: "out", Host: "203.0.113.20", Role: RoleOutbound},
	})
	if peer, ok := withEgress.EgressPeer(); !ok || peer.Name != "out" {
		t.Fatalf("EgressPeer() = %v, %v; want peer 'out', true", peer, ok)
	}

	inboundOnly := NewPeerStore([]*Peer{
		{Name: "in", Host: "203.0.113.10", Role: RoleInbound},
	})
	if _, ok := inboundOnly.EgressPeer(); ok {
		t.Fatal("EgressPeer() found an egress peer in an inbound-only store")
	}
}

func TestTenantForDID(t *testing.T) {
	routes := NewDIDRoutes(map[string]string{
		"+15551234567": "acme_support",
	})

	if tenant, ok := routes.TenantForDID("+15551234567"); !ok || tenant != "acme_support" {
		t.Fatalf("TenantForDID(mapped) = %q, %v; want 'acme_support', true", tenant, ok)
	}
	if _, ok := routes.TenantForDID("+15559999999"); ok {
		t.Fatal("TenantForDID(unmapped) returned a tenant; want no default")
	}
	if _, ok := routes.TenantForDID(""); ok {
		t.Fatal("TenantForDID(empty) returned a tenant")
	}
}

func TestApplyTenantIdentity(t *testing.T) {
	req := sip.NewRequest(sip.INVITE, sip.Uri{Scheme: "sip", User: "100", Host: "203.0.113.20"})
	req.AppendHeader(&sip.FromHeader{
		Address: sip.Uri{Scheme: "sip", User: "100", Host: "switchboard.local"},
		Params:  sip.NewParams(),
	})

	ApplyTenantIdentity(req, "acme", "")

	from := req.From()
	if from == nil {
		t.Fatal("From header missing")
	}
	if from.Address.Host != "acme" {
		t.Fatalf("From host = %q, want %q", from.Address.Host, "acme")
	}
	hdr := req.GetHeader(DefaultTenantHeader)
	if hdr == nil {
		t.Fatalf("%s header not set", DefaultTenantHeader)
	}
	if hdr.Value() != "acme" {
		t.Fatalf("%s = %q, want %q", DefaultTenantHeader, hdr.Value(), "acme")
	}
}
