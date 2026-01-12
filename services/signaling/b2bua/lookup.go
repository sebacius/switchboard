package b2bua

import (
	"context"

	"github.com/sebas/switchboard/services/signaling/location"
)

// LookupResultType indicates how a target was resolved.
type LookupResultType int

const (
	// LookupResultTypeUser indicates resolution via location service.
	// Target format: "user/1001" or just "1001"
	LookupResultTypeUser LookupResultType = iota

	// LookupResultTypeGateway indicates resolution via gateway configuration.
	// Target format: "gateway/carrier-name" or "trunk/carrier-name"
	LookupResultTypeGateway

	// LookupResultTypeDirect indicates a direct SIP URI passthrough.
	// Target format: "sip:user@host:port" or "sips:user@host:port"
	LookupResultTypeDirect
)

// String returns the string representation of the lookup result type.
func (t LookupResultType) String() string {
	switch t {
	case LookupResultTypeUser:
		return "User"
	case LookupResultTypeGateway:
		return "Gateway"
	case LookupResultTypeDirect:
		return "Direct"
	default:
		return "Unknown"
	}
}

// LookupResult contains the resolved target(s) for outbound dialing.
//
// A single target may resolve to multiple contacts (e.g., user with multiple
// registrations). Contacts are sorted by priority (highest first).
type LookupResult struct {
	// Type indicates how the target was resolved.
	Type LookupResultType

	// Original is the raw target string that was looked up.
	Original string

	// Contacts contains the resolved SIP URIs, sorted by priority.
	// For user lookups: from location service bindings
	// For gateway lookups: from gateway configuration
	// For direct: single entry matching original
	Contacts []ResolvedContact

	// Metadata contains type-specific additional information.
	// For gateways: authentication credentials, caller ID rules, etc.
	Metadata map[string]string
}

// ResolvedContact represents a single resolved SIP target.
type ResolvedContact struct {
	// URI is the SIP URI to dial.
	// Example: "sip:alice@192.168.1.100:5060;transport=udp"
	URI string

	// Priority is the q-value (0.0 to 1.0, higher = more preferred).
	// Default is 1.0 if not specified.
	Priority float32

	// Transport indicates the preferred transport (UDP, TCP, TLS, WS, WSS).
	// Empty string means use default (UDP).
	Transport string

	// Binding is the original location binding if resolved from location service.
	// nil for gateway and direct lookups.
	Binding *location.Binding
}

// PrimaryContact returns the highest-priority contact, or empty if none.
func (r *LookupResult) PrimaryContact() ResolvedContact {
	if len(r.Contacts) == 0 {
		return ResolvedContact{}
	}
	return r.Contacts[0]
}

// HasContacts returns true if at least one contact was resolved.
func (r *LookupResult) HasContacts() bool {
	return len(r.Contacts) > 0
}

// Resolver resolves dial targets to SIP URIs.
//
// Implementations:
//   - UserResolver: uses LocationStore
//   - GatewayResolver: uses gateway configuration
//   - DirectResolver: passthrough for sip: URIs
//   - ChainResolver: tries multiple resolvers in order
type Resolver interface {
	// Resolve looks up the target and returns resolved contacts.
	// Returns ErrTargetNotFound if the target cannot be resolved.
	// Returns ErrNoContacts if target exists but has no active registrations.
	Resolve(ctx context.Context, target string) (*LookupResult, error)

	// CanResolve returns true if this resolver handles the target format.
	// Used by ChainResolver to skip inapplicable resolvers.
	CanResolve(target string) bool
}

// GatewayConfig contains configuration for an outbound gateway/trunk.
type GatewayConfig struct {
	// Name is the gateway identifier (e.g., "carrier-a", "pstn-out").
	Name string

	// Host is the gateway SIP address.
	Host string

	// Port is the gateway SIP port (default 5060).
	Port int

	// Transport is the preferred transport (UDP, TCP, TLS).
	Transport string

	// Username for digest authentication (optional).
	Username string

	// Password for digest authentication (optional).
	Password string

	// Realm for digest authentication (optional).
	Realm string

	// CallerIDMode controls how caller ID is set.
	// "passthrough": use original From header
	// "override": use CallerIDNumber/CallerIDName
	// "anonymous": set anonymous caller ID
	CallerIDMode string

	// CallerIDNumber is the caller ID number when mode is "override".
	CallerIDNumber string

	// CallerIDName is the caller ID name when mode is "override".
	CallerIDName string

	// Prefix to strip from dialed number before sending.
	StripPrefix string

	// Prefix to add to dialed number after stripping.
	AddPrefix string

	// MaxChannels limits concurrent calls (0 = unlimited).
	MaxChannels int

	// Priority for load balancing (higher = preferred).
	Priority int

	// Enabled controls whether this gateway is available.
	Enabled bool
}

// GatewayStore manages gateway configurations.
type GatewayStore interface {
	// Get returns a gateway by name.
	Get(name string) (*GatewayConfig, bool)

	// List returns all enabled gateways.
	List() []*GatewayConfig

	// ListByPriority returns gateways sorted by priority (descending).
	ListByPriority() []*GatewayConfig
}
