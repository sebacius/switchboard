package trunk

import (
	"os"
	"path/filepath"
	"strings"
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

// The bug this exists to prevent: the gate declined a call the tenant's own
// table would have matched, because the two layers disagreed about whether a
// leading '+' was required. A carrier that signals "15558001200" against a
// "+15558001200" route got 603 at the door.
func TestTenantForDIDToleratesTheLeadingPlus(t *testing.T) {
	routes := NewDIDRoutes(map[string]string{
		"+15558001200": "acme",
		"15558009999":  "beta", // written the other way round
	})

	cases := map[string]string{
		"+15558001200": "acme", // as written
		"15558001200":  "acme", // carrier omitted the +
		"15558009999":  "beta", // as written
		"+15558009999": "beta", // carrier added a +
	}
	for did, want := range cases {
		got, ok := routes.TenantForDID(did)
		if !ok {
			t.Errorf("TenantForDID(%q) found nothing, want %q", did, want)
			continue
		}
		if got != want {
			t.Errorf("TenantForDID(%q) = %q, want %q", did, got, want)
		}
	}
}

// Owning a block of numbers should be one line, not ten thousand.
func TestTenantForDIDMatchesABlock(t *testing.T) {
	routes := NewDIDRoutes(map[string]string{"+1555800XXXX": "acme"})

	for _, did := range []string{"+15558000000", "+15558001200", "+15558009999"} {
		if tenant, ok := routes.TenantForDID(did); !ok || tenant != "acme" {
			t.Errorf("TenantForDID(%q) = %q/%v, want acme", did, tenant, ok)
		}
	}
	// Outside the block nobody owns it.
	if _, ok := routes.TenantForDID("+15558010000"); ok {
		t.Error("a number outside the block must not resolve")
	}
}

// A specific number can be carved out of a block someone else owns, because
// specificity is computed rather than declared.
func TestSpecificDIDBeatsABlock(t *testing.T) {
	routes := NewDIDRoutes(map[string]string{
		"+1555800XXXX": "acme",
		"+15558001200": "beta",
	})

	if tenant, _ := routes.TenantForDID("+15558001200"); tenant != "beta" {
		t.Errorf("the literal should win, got %q", tenant)
	}
	if tenant, _ := routes.TenantForDID("+15558001201"); tenant != "acme" {
		t.Errorf("the block should own the rest, got %q", tenant)
	}
}

// Two tenants claiming the same numbers with neither claim more specific means
// whose calls these are is undefined. That is a startup error, not a coin toss.
func TestOverlappingClaimsFailToLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routes.json")
	body := `{"dids":{"+1555NXX1200":"acme","+1555800XXXX":"beta"}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	_, err := LoadRoutes(path)
	if err == nil {
		t.Fatal("two overlapping claims must fail the load")
	}
	for _, want := range []string{"acme", "beta"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should name both claimants, missing %q: %v", want, err)
		}
	}
}

// A missing routes.json means nothing is routed inbound, which is the safe
// posture — not a startup failure.
func TestMissingRoutesFileRejectsEveryDID(t *testing.T) {
	routes, err := LoadRoutes(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("a missing routes file must not be an error: %v", err)
	}
	if _, ok := routes.TenantForDID("+15558001200"); ok {
		t.Error("with no routes table, every DID must be unmapped")
	}
}
