package trunk

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/sebas/switchboard/internal/signaling/dialplan"
)

// DIDRoutes answers the first of the two questions an inbound DID raises:
// WHOSE number is this? The second — what should happen to a call to it — is
// the tenant's own business, and lives in its routing table.
//
// They are separate files because a tenant can edit its routing table through
// the config API. If tenants declared their own DIDs, one could add another's
// number and start receiving their calls, so this binding lives where no tenant
// can write it.
//
// There is intentionally no default tenant: an unmapped DID is rejected by the
// caller rather than falling through to somebody.
type DIDRoutes struct {
	// raw is the table as written, for validation and diagnostics.
	raw map[string]string
	// dids is the compiled matcher. It is the SAME type the tenant-level DID
	// lookup uses, which is the point: a number the tenant's table would match
	// must not be turned away at the door for being written without a '+'.
	dids *dialplan.DIDMap
}

// routesFile is the on-disk shape of routes.json.
type routesFile struct {
	DIDs map[string]string `json:"dids"`
}

// NewDIDRoutes builds a DIDRoutes from a DID->tenant map.
//
// A malformed pattern is dropped rather than returned, because this constructor
// cannot fail and callers that care use LoadRoutes, which validates. This
// mirrors how a hand-built RoutingTable compiles.
func NewDIDRoutes(dids map[string]string) *DIDRoutes {
	routes, _ := newDIDRoutes(dids)
	return routes
}

// newDIDRoutes builds a DIDRoutes, reporting a compile failure.
func newDIDRoutes(dids map[string]string) (*DIDRoutes, error) {
	if dids == nil {
		dids = map[string]string{}
	}
	compiled, err := dialplan.CompileDIDMap(dialplan.Entries(dids))
	if err != nil {
		return &DIDRoutes{raw: dids}, err
	}
	return &DIDRoutes{raw: dids, dids: compiled}, nil
}

// LoadRoutes reads the DID->tenant table from routes.json. A missing path yields
// an empty table (all DIDs rejected), not an error.
func LoadRoutes(path string) (*DIDRoutes, error) {
	if path == "" {
		return NewDIDRoutes(nil), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return NewDIDRoutes(nil), nil
		}
		return nil, fmt.Errorf("read routes %s: %w", path, err)
	}
	return ParseRoutes(data, path)
}

// ParseRoutes applies the loader's rules to bytes that are not on disk yet, so
// the config API refuses a table the server would refuse to start with. label
// names the source in error messages.
func ParseRoutes(data []byte, label string) (*DIDRoutes, error) {
	var rf routesFile
	if err := json.Unmarshal(data, &rf); err != nil {
		return nil, fmt.Errorf("parse routes %s: %w", label, err)
	}
	routes, err := newDIDRoutes(rf.DIDs)
	if err != nil {
		return nil, fmt.Errorf("invalid routes %s: %w", label, describeRoutesError(err, rf.DIDs))
	}
	return routes, nil
}

// describeRoutesError says what an ambiguity MEANS in a DID table.
//
// The generic message names two patterns, which is right when the reader is
// editing a dialplan. Here the two patterns are two tenants claiming the same
// numbers, and which of them receives those calls is undefined — so say that,
// with the tenant names, rather than making the operator look them up.
func describeRoutesError(err error, dids map[string]string) error {
	var ambiguous *dialplan.AmbiguousPatternsError
	if !errors.As(err, &ambiguous) {
		return err
	}
	return fmt.Errorf(
		"tenants %q and %q both claim numbers matching %q and %q, and neither claim is more "+
			"specific, so which tenant receives those calls is undefined; make one of them narrower",
		dids[ambiguous.A], dids[ambiguous.B], ambiguous.A, ambiguous.B)
}

// Count returns the number of mapped DID entries.
func (r *DIDRoutes) Count() int {
	if r == nil {
		return 0
	}
	return len(r.raw)
}

// All returns the table as written, for validation and diagnostics.
func (r *DIDRoutes) All() map[string]string {
	if r == nil {
		return nil
	}
	out := make(map[string]string, len(r.raw))
	for did, tenant := range r.raw {
		out[did] = tenant
	}
	return out
}

// TenantForDID returns the tenant a DID belongs to, or false when unmapped.
// There is no default tenant.
//
// Both E.164 forms match, and patterns work, because a DID arrives from a
// carrier rather than a keypad: which form a given trunk sends is not something
// the operator writing this file can know in advance, and owning a block of
// numbers should not mean enumerating ten thousand of them.
func (r *DIDRoutes) TenantForDID(did string) (string, bool) {
	if r == nil || did == "" {
		return "", false
	}
	return r.dids.Lookup(did)
}
