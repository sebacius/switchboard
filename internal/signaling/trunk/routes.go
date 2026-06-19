package trunk

import (
	"encoding/json"
	"fmt"
	"os"
)

// DIDRoutes maps inbound DIDs to tenant names. There is intentionally no default
// tenant: an unmapped DID is rejected by the caller.
type DIDRoutes struct {
	dids map[string]string
}

// routesFile is the on-disk shape of routes.json.
type routesFile struct {
	DIDs map[string]string `json:"dids"`
}

// NewDIDRoutes builds a DIDRoutes from a DID->tenant map.
func NewDIDRoutes(dids map[string]string) *DIDRoutes {
	if dids == nil {
		dids = map[string]string{}
	}
	return &DIDRoutes{dids: dids}
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
	var rf routesFile
	if err := json.Unmarshal(data, &rf); err != nil {
		return nil, fmt.Errorf("parse routes %s: %w", path, err)
	}
	return NewDIDRoutes(rf.DIDs), nil
}

// Count returns the number of mapped DIDs.
func (r *DIDRoutes) Count() int { return len(r.dids) }

// TenantForDID returns the tenant mapped to the DID, or false when unmapped.
// There is no default tenant.
func (r *DIDRoutes) TenantForDID(did string) (string, bool) {
	if did == "" {
		return "", false
	}
	t, ok := r.dids[did]
	return t, ok
}
