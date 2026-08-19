package trunk

import "github.com/emiago/sipgo/sip"

// Trunk is the seam between the call layer and SIP trunk handling. The static
// PeerStore-backed implementation lives in this package; a future change can
// provide a Kamailio-backed implementation without changing callers.
type Trunk interface {
	// MatchInbound returns the inbound-capable peer matching a source IP, if any.
	MatchInbound(sourceIP string) (*Peer, bool)
	// EgressPeer returns an outbound-capable peer, if any.
	EgressPeer() (*Peer, bool)
	// ApplyTenantIdentity stamps tenant identity onto an outbound request.
	ApplyTenantIdentity(req *sip.Request, tenant string)
}

// staticTrunk adapts a PeerStore to the Trunk interface.
type staticTrunk struct {
	peers        *PeerStore
	tenantHeader string
}

// NewStaticTrunk builds a Trunk backed by a static PeerStore. An empty
// tenantHeader defaults to DefaultTenantHeader.
func NewStaticTrunk(peers *PeerStore, tenantHeader string) Trunk {
	if tenantHeader == "" {
		tenantHeader = DefaultTenantHeader
	}
	return &staticTrunk{peers: peers, tenantHeader: tenantHeader}
}

func (t *staticTrunk) MatchInbound(sourceIP string) (*Peer, bool) {
	return t.peers.MatchInbound(sourceIP)
}

func (t *staticTrunk) EgressPeer() (*Peer, bool) {
	return t.peers.EgressPeer()
}

func (t *staticTrunk) ApplyTenantIdentity(req *sip.Request, tenant string) {
	ApplyTenantIdentity(req, tenant, t.tenantHeader)
}
