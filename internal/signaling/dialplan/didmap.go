package dialplan

// An inbound DID is looked up twice on its way to a destination, and the two
// lookups answer different questions:
//
//	routes.json            "whose number is this?"     DID -> tenant
//	<tenant>.routing.json  "what happens to a call?"   DID -> destination
//
// They are separate because a tenant may edit its own routing file through the
// config API. If tenants declared their own DIDs, one could add another's
// number and start receiving their calls, so the number-to-tenant binding has
// to live where no tenant can write it.
//
// What they must NOT do is match differently. They did: the gate was a bare map
// lookup while the tenant table tolerated the leading '+', so a carrier
// signalling "15558001200" against a "+15558001200" route was declined at the
// door — even though the tenant's own table would have matched it perfectly.
// DIDMap is the one matcher both now use, so they cannot drift apart again.

// DIDMap resolves an inbound DID.
//
// It adds two things to a plain DigitMap, both of which exist because a DID
// arrives from a carrier rather than from a keypad:
//
//   - The leading '+' is optional on either side. Carriers are inconsistent
//     about it, and which form a given trunk sends is not something the
//     operator writing the config can be expected to know in advance.
//   - Patterns work, so a block of numbers is one line rather than ten
//     thousand.
type DIDMap struct {
	m *DigitMap
}

// CompileDIDMap compiles a DID mapping, rejecting ambiguity exactly as
// CompileDigitMap does. Two entries that can match the same number with neither
// more specific is a serious misconfiguration for DIDs in particular — it means
// two owners claim the same number — so it fails the load naming both.
func CompileDIDMap(raw map[string]Entry) (*DIDMap, error) {
	m, err := CompileDigitMap(raw)
	if err != nil {
		return nil, err
	}
	return &DIDMap{m: m}, nil
}

// Lookup resolves a DID to its mapped value.
func (d *DIDMap) Lookup(dialed string) (string, bool) {
	value, _, ok := d.LookupWithDigits(dialed)
	return value, ok
}

// LookupWithDigits also returns the dialled digits after the matching entry's
// transform.
func (d *DIDMap) LookupWithDigits(dialed string) (string, string, bool) {
	if d == nil || d.m == nil {
		return "", "", false
	}

	if value, digits, ok := d.m.LookupWithDigits(dialed); ok {
		return value, digits, true
	}
	// Try the other E.164 form before giving up.
	if alt, ok := togglePlus(dialed); ok {
		return d.m.LookupWithDigits(alt)
	}
	return "", "", false
}

// Len reports how many entries the map holds.
func (d *DIDMap) Len() int {
	if d == nil {
		return 0
	}
	return d.m.Len()
}
