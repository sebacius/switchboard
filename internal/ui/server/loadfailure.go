package server

import (
	"errors"
	"net/http"

	"github.com/sebas/switchboard/internal/ui/client"
)

// LoadFailure is a panel's inability to READ, which is a different thing from
// having read nothing.
//
// The two look identical on screen and mean opposite things. A panel that shows
// "No tenants are configured yet" when the backend was merely unreachable tells
// an operator their configuration is gone, which is the worst thing this UI can
// say and the easiest to say by accident: both states are "the slice is empty".
// So the distinction is a type rather than a convention, and every panel tests
// for it the same way.
//
// A nil *LoadFailure means the read succeeded. Emptiness after that is real.
type LoadFailure struct {
	// What was being read, named for an operator: "the tenant list", not
	// "ListTenantsDetailed".
	What string
	// Backend and Address identify WHICH signaling server failed, so the
	// operator checks the right host. With several backends configured, "could
	// not load" without a name is an instruction to go and guess.
	Backend string
	Address string
	// Detail is the underlying error, kept verbatim.
	Detail string
	// Missing is a 404: the thing does not exist, and the backend is fine. A
	// tenant with no flows file is the ordinary case, not an outage, and
	// blaming the connection for it would send the operator somewhere useless.
	Missing bool
}

// loadFailed describes a failed read, or returns nil when err is nil.
func loadFailed(c *client.Client, what string, err error) *LoadFailure {
	if err == nil {
		return nil
	}
	f := &LoadFailure{What: what, Detail: err.Error()}
	if c != nil {
		f.Backend, f.Address = c.Name(), c.BaseURL()
	}
	var httpErr *client.HTTPError
	if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusNotFound {
		f.Missing = true
	}
	return f
}
