// Package location manages SIP user location bindings (REGISTER).
package location

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// BindingSource indicates who controls the lifecycle of a binding
type BindingSource string

const (
	// BindingSourceSIP indicates the binding was created via SIP REGISTER
	// and its lifecycle is controlled by this server (normal SIP expiration).
	BindingSourceSIP BindingSource = "sip"

	// BindingSourceAPI indicates the binding was created via REST API
	// and its lifecycle is controlled by an external proxy (e.g., Kamailio, OpenSIPS).
	BindingSourceAPI BindingSource = "api"
)

// Binding represents a SIP user location binding from REGISTER.
// Contains all information needed to route an incoming INVITE to this user.
type Binding struct {
	// Identity
	AOR       string `json:"aor"`        // Address of Record (e.g., "sip:alice@example.com")
	BindingID string `json:"binding_id"` // Unique ID for this binding (hash of contact)

	// Contact information - where to route requests
	ContactURI string `json:"contact_uri"` // Registered Contact URI (e.g., "sip:alice@192.168.1.100:5060")

	// NAT traversal - actual source of REGISTER for symmetric routing
	ReceivedIP   string `json:"received_ip"`   // Source IP of REGISTER request
	ReceivedPort int    `json:"received_port"` // Source port of REGISTER request

	// Transport
	Transport string `json:"transport"` // UDP, TCP, TLS, WS, WSS

	// Path headers (RFC 3327) - for routing through proxies
	Path []string `json:"path,omitempty"` // Path header URIs in order

	// Instance ID (RFC 5626 GRUU support)
	InstanceID string `json:"instance_id,omitempty"` // +sip.instance parameter

	// Priority
	QValue float32 `json:"q,omitempty"` // q-value for contact priority (0.0-1.0)

	// Timing
	Expires      int       `json:"expires"`       // TTL in seconds
	ExpiresAt    time.Time `json:"expires_at"`    // Absolute expiration time
	RegisteredAt time.Time `json:"registered_at"` // When this binding was created/updated

	// RFC 3261 validation
	CallID string `json:"call_id"` // Call-ID from REGISTER (for update validation)
	CSeq   uint32 `json:"cseq"`    // CSeq number (must increase for same Call-ID)

	// Metadata
	UserAgent string `json:"user_agent,omitempty"` // User-Agent header for debugging

	// Source tracking - who controls the lifecycle of this binding
	Source        BindingSource `json:"source,omitempty"`         // "sip" or "api" - who created this binding
	ExternalProxy string        `json:"external_proxy,omitempty"` // Identifier for external system (e.g., "kamailio", "opensips")
}

// GenerateBindingID creates a unique binding ID from contact URI and instance
func GenerateBindingID(contactURI, instanceID string) string {
	data := contactURI
	if instanceID != "" {
		data += ";" + instanceID
	}
	hash := sha256.Sum256([]byte(data))
	return hex.EncodeToString(hash[:8]) // 16 char hex string
}

// IsExpired returns true if the binding has expired
func (b *Binding) IsExpired() bool {
	return time.Now().After(b.ExpiresAt)
}

// TTL returns remaining time until expiration
func (b *Binding) TTL() time.Duration {
	remaining := time.Until(b.ExpiresAt)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// EffectiveContact returns the best URI to use for routing.
// Uses received IP/port if behind NAT, otherwise Contact URI.
// Always preserves the user part from the ContactURI.
func (b *Binding) EffectiveContact() string {
	// If we have received info, use received IP/port for NAT traversal
	// but preserve the user part from ContactURI
	if b.ReceivedIP != "" && b.ReceivedPort > 0 {
		user := extractUserFromURI(b.ContactURI)
		if user != "" {
			return fmt.Sprintf("sip:%s@%s:%d;transport=%s",
				user, b.ReceivedIP, b.ReceivedPort, b.Transport)
		}
		return fmt.Sprintf("sip:%s:%d;transport=%s",
			b.ReceivedIP, b.ReceivedPort, b.Transport)
	}
	return b.ContactURI
}

// extractUserFromURI extracts the user part from a SIP URI.
// Examples:
//   - "sip:1000@domain.com:5060" -> "1000"
//   - "sip:alice@example.com" -> "alice"
//   - "sip:domain.com" -> ""
func extractUserFromURI(uri string) string {
	s := uri
	// Remove scheme
	if len(s) > 4 && s[:4] == "sip:" {
		s = s[4:]
	} else if len(s) > 5 && s[:5] == "sips:" {
		s = s[5:]
	}

	// Find @ separator
	atIdx := -1
	for i, c := range s {
		if c == '@' {
			atIdx = i
			break
		}
	}

	if atIdx == -1 {
		// No user part
		return ""
	}

	return s[:atIdx]
}

// DialogInfo holds information needed for dialog routing from a binding
type DialogInfo struct {
	AOR        string
	ContactURI string
	Transport  string
}

// ToDialogInfo extracts routing information for INVITE
func (b *Binding) ToDialogInfo() *DialogInfo {
	return &DialogInfo{
		AOR:        b.AOR,
		ContactURI: b.EffectiveContact(),
		Transport:  b.Transport,
	}
}

// ValidateCSeq checks if a new CSeq is valid for updating this binding.
// Per RFC 3261, for same Call-ID, CSeq must increase.
func (b *Binding) ValidateCSeq(callID string, cseq uint32) bool {
	if b.CallID != callID {
		// Different Call-ID, any CSeq is valid
		return true
	}
	// Same Call-ID, CSeq must be higher
	return cseq > b.CSeq
}
