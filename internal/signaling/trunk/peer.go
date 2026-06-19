// Package trunk implements a basic static SIP trunk for Switchboard: configured
// trunk peers (inbound/outbound/both), inbound source classification, tenant-
// tagged outbound egress, and a DID->tenant table. It is deliberately minimal
// and sits behind the Trunk interface so a future change can delegate the heavy
// SIP edge (routing, provider auth) to an external proxy such as Kamailio.
package trunk

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Role describes which directions a trunk peer handles.
type Role string

const (
	RoleInbound  Role = "inbound"
	RoleOutbound Role = "outbound"
	RoleBoth     Role = "both"
)

// Peer is a configured static SIP trunk peer.
type Peer struct {
	Name string `json:"name"`
	Host string `json:"host"`
	Port int    `json:"port"`
	Role Role   `json:"role"`
}

// AcceptsInbound reports whether the peer may originate calls toward us.
func (p *Peer) AcceptsInbound() bool {
	return p.Role == RoleInbound || p.Role == RoleBoth
}

// AcceptsOutbound reports whether the peer may receive egress calls from us.
func (p *Peer) AcceptsOutbound() bool {
	return p.Role == RoleOutbound || p.Role == RoleBoth
}

// PeerStore holds the configured trunk peers and answers ingress/egress queries.
type PeerStore struct {
	peers []*Peer
}

// NewPeerStore builds a PeerStore from a slice of peers.
func NewPeerStore(peers []*Peer) *PeerStore {
	return &PeerStore{peers: peers}
}

// LoadPeers reads trunk peers from a JSON file shaped as a list of peer objects.
// A missing path yields an empty store (trunking disabled), not an error.
func LoadPeers(path string) (*PeerStore, error) {
	if path == "" {
		return NewPeerStore(nil), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return NewPeerStore(nil), nil
		}
		return nil, fmt.Errorf("read trunk peers %s: %w", path, err)
	}
	var peers []*Peer
	if err := json.Unmarshal(data, &peers); err != nil {
		return nil, fmt.Errorf("parse trunk peers %s: %w", path, err)
	}
	for _, p := range peers {
		if p.Role == "" {
			p.Role = RoleBoth
		}
	}
	return NewPeerStore(peers), nil
}

// Count returns the number of configured peers.
func (s *PeerStore) Count() int { return len(s.peers) }

// MatchInbound returns the inbound-capable peer whose host matches the given
// source IP/host, if any.
func (s *PeerStore) MatchInbound(sourceIP string) (*Peer, bool) {
	if sourceIP == "" {
		return nil, false
	}
	for _, p := range s.peers {
		if !p.AcceptsInbound() {
			continue
		}
		if strings.EqualFold(p.Host, sourceIP) {
			return p, true
		}
	}
	return nil, false
}

// EgressPeer returns the first outbound-capable peer, if any.
func (s *PeerStore) EgressPeer() (*Peer, bool) {
	for _, p := range s.peers {
		if p.AcceptsOutbound() {
			return p, true
		}
	}
	return nil, false
}
