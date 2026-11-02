package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	types "github.com/sebas/switchboard/api/types/v1"
)

// tabbedBackend is routingBackend plus the loaded-tenant listing the simulator
// reads, which is what the Test tab needs and the routing tab does not.
type tabbedBackend struct {
	*routingBackend
	loaded []types.LoadedTenant
}

func (b *tabbedBackend) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/flow/tenants", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(types.LoadedTenants{Tenants: b.loaded})
	})
	inner := b.routingBackend.handler()
	mux.HandleFunc("/", inner.ServeHTTP)
	return mux
}

func tabbedUI(t *testing.T) *testUI {
	t.Helper()
	b := &tabbedBackend{
		routingBackend: &routingBackend{
			routing: referenceRouting(t), flows: referenceFlows(t), routes: referenceRoutes(t),
		},
		loaded: []types.LoadedTenant{
			{Name: "devtenant", Flows: []string{"main-ivr"}},
			{Name: "other", Flows: []string{"other-ivr"}},
		},
	}
	return newTestServerWith(t, b.handler())
}

// The tab strip is the workspace's navigation. Two of its three entries linked
// URLs nothing had registered, so the tenant workspace shipped with a Routing
// tab and two dead ends.
func TestEveryTenantTabResolves(t *testing.T) {
	ui := tabbedUI(t)
	for _, path := range []string{
		"/admin/tenants/devtenant?server=test",
		"/admin/tenants/devtenant/flows?server=test",
		"/admin/tenants/devtenant/test?server=test",
	} {
		if rec := ui.get(t, path); rec.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200", path, rec.Code)
		}
	}
}

// Each tab marks itself, or an operator cannot tell which one they are on.
func TestEachTenantTabMarksItself(t *testing.T) {
	ui := tabbedUI(t)
	for path, want := range map[string]string{
		"/admin/tenants/devtenant?server=test":       "Routing",
		"/admin/tenants/devtenant/flows?server=test": "Flows",
		"/admin/tenants/devtenant/test?server=test":  "Test a call",
	} {
		body := ui.get(t, path).Body.String()
		// The active tab carries the accent border; exactly one may.
		if got := strings.Count(body, "border-emerald-500 text-white font-medium"); got != 1 {
			t.Errorf("%s marks %d tabs active, want 1", path, got)
		}
		mustSay(t, body, want, "the tab strip has to name the tab that is open")
	}
}

// The tenant comes from the path, so the picker that would change it is gone:
// switching tenants there swaps only the inner panel, which would leave the
// breadcrumb and the tab strip naming the tenant the operator just left.
func TestTheScopedFlowsTabOffersNoTenantPicker(t *testing.T) {
	ui := tabbedUI(t)
	body := ui.get(t, "/admin/tenants/devtenant/flows?server=test").Body.String()

	mustSay(t, body, `id="drawflow"`, "the canvas is the point of this tab")
	mustSay(t, body, "devtenant", "the page has to name the tenant it is editing")
	mustNotSay(t, body, `id="tenant-input"`, "the URL already fixes the tenant")
}

// A tenant with no flows file says so under its own name. The unscoped editor
// elects the first tenant that has flows, which is right for a picker and wrong
// for a path: it would draw another tenant's graph under this one's breadcrumb.
func TestAScopedFlowsTabNeverFallsBackToAnotherTenant(t *testing.T) {
	ui := tabbedUI(t)
	data := ui.flowsVMScoped(t.Context(), ui.clients[0], "routing-only", "")

	if data.Tenant != "routing-only" {
		t.Errorf("tenant = %q, want routing-only — a scoped page must not substitute", data.Tenant)
	}
	if !data.Scoped {
		t.Error("a page opened at a tenant's URL is scoped to it")
	}
	if len(data.Tenants) != 0 {
		t.Errorf("a scoped page offers no tenant list, got %d", len(data.Tenants))
	}
}

// The Try-it links on the routing table carry the number they were rendered
// for, and a deep link that names a number comes back as a trace rather than as
// a form to fill in again.
func TestTheTestTabRunsTheSimulationNamedInTheURL(t *testing.T) {
	ui := tabbedUI(t)
	q := url.Values{"dialed": {"700"}, "direction": {"inbound"}, "digits": {"2"}}
	data := ui.flowTestVM(t.Context(), ui.clients[0], q, "devtenant")

	if data.Tenant != "devtenant" || !data.Scoped {
		t.Errorf("tenant = %q scoped = %v, want devtenant/true", data.Tenant, data.Scoped)
	}
	if data.Dialed != "700" || data.Direction != "inbound" || data.Digits != "2" {
		t.Errorf("params not carried: %+v", data)
	}
	if !data.AutoRun {
		t.Error("a link that names a number has to run on load")
	}

	body := ui.get(t, "/admin/tenants/devtenant/test?server=test&dialed=700").Body.String()
	mustSay(t, body, `hx-trigger="load, submit"`, "AutoRun is what makes a deep link a trace")
	mustSay(t, body, `name="tenant" value="devtenant"`, "the run has to be pinned to the path's tenant")
	mustNotSay(t, body, `<option value="other"`, "the URL already fixes the tenant")
}

// The scoped simulator must not silently answer for a different tenant even
// when the query string disagrees with the path.
func TestThePathTenantWinsOverTheQueryString(t *testing.T) {
	ui := tabbedUI(t)
	q := url.Values{"tenant": {"other"}, "dialed": {"700"}}
	data := ui.flowTestVM(t.Context(), ui.clients[0], q, "devtenant")

	if data.Tenant != "devtenant" {
		t.Errorf("tenant = %q, want devtenant — the path names the tenant", data.Tenant)
	}
}

// Every "Try it" on the routing table is a real URL. Nine of them 404'd.
func TestEveryTryItLinkOnTheRoutingTableResolves(t *testing.T) {
	ui := tabbedUI(t)
	body := ui.get(t, "/admin/tenants/devtenant?server=test").Body.String()

	tried := 0
	for _, m := range internalURL.FindAllStringSubmatch(body, -1) {
		link := strings.ReplaceAll(m[1], "&amp;", "&")
		if !strings.Contains(link, "/test?") {
			continue
		}
		tried++
		if rec := ui.get(t, link); rec.Code != http.StatusOK {
			t.Errorf("Try it link %s = %d, want 200", link, rec.Code)
		}
	}
	if tried == 0 {
		t.Fatal("the routing table rendered no Try-it links, so this asserts nothing")
	}
}

// The canvas library has to be in the head, or the editor's script runs before
// the thing it needs exists. Mirrors the assertion the config page carries.
func TestTheCanvasLibraryIsInTheHeadOfTheTenantFlowsPage(t *testing.T) {
	ui := tabbedUI(t)
	body := ui.get(t, "/admin/tenants/devtenant/flows?server=test").Body.String()

	lib := strings.Index(body, "drawflow.min.js")
	canvas := strings.Index(body, `id="drawflow"`)
	if lib < 0 || canvas < 0 {
		t.Fatalf("library at %d, canvas at %d — both must render", lib, canvas)
	}
	if lib > canvas {
		t.Error("the canvas library must load before the element that uses it")
	}
}
