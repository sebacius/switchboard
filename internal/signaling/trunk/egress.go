package trunk

import (
	"github.com/emiago/sipgo/sip"
)

// DefaultTenantHeader is the SIP header used to carry tenant identity to a peer.
const DefaultTenantHeader = "X-Tenant"

// ApplyTenantIdentity stamps tenant identity onto an outbound INVITE bound for a
// trunk peer: it sets the From-URI host to the tenant and appends a tenant
// header so a downstream proxy/provider can attribute and route the call.
// headerName defaults to DefaultTenantHeader when empty.
func ApplyTenantIdentity(req *sip.Request, tenant, headerName string) {
	if req == nil || tenant == "" {
		return
	}
	if headerName == "" {
		headerName = DefaultTenantHeader
	}
	if from := req.From(); from != nil {
		from.Address.Host = tenant
	}
	req.AppendHeader(sip.NewHeader(headerName, tenant))
}
