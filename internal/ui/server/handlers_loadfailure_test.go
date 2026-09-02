package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/sebas/switchboard/internal/ui/config"
)

// brokenBackend answers every read with 503, which is what an operator sees
// when signaling is up but its config providers were never wired.
func brokenBackend() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "File management not configured", http.StatusServiceUnavailable)
	})
}

// A panel that could not read must not claim the configuration is absent.
//
// The two states are both "the slice is empty", so they are trivially confused,
// and confusing them tells an operator their tenants are gone when the backend
// was merely unreachable. That is the worst thing this UI can say.
func TestAFailedReadIsNotReportedAsAnEmptyConfiguration(t *testing.T) {
	ui := newTestServerWith(t, brokenBackend())

	for _, path := range []string{
		"/admin/tenants",
		"/admin/deployment",
		"/admin/audio",
		"/admin/tenants/devtenant",
		"/admin/tenants/devtenant/flows",
		"/admin/tenants/devtenant/test",
		// The legacy config page fetches its tab on load rather than rendering
		// it server-side, so its banner lives in the fragment. Asserted there.
		"/admin/config/partials/tenants",
		"/admin/config/partials/globals",
		"/admin/config/partials/audio",
		"/admin/config/partials/flows",
		"/admin/config/partials/flowtest",
	} {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		body := ui.get(t, path+sep+"server=test").Body.String()

		mustSay(t, body, "Could not load", path+": a failed read has to say so")
		mustSay(t, body, "test", path+": the operator has to know which backend failed")
		mustSay(t, body, "--backends", path+": and where that address came from")
		mustSay(t, body, "we could not look", path+": absence here is not evidence of absence")

		// The claims that would be lies.
		mustNotSay(t, body, "No tenants are configured yet",
			path+": the tenant list was never read")
		mustNotSay(t, body, "No tenant has a flow graph yet",
			path+": the tenant list was never read")
	}
}

// The mirror case, which is what makes the distinction a property rather than a
// string match: a backend that answers with nothing DOES get the empty state,
// and does not get the failure banner.
func TestAnEmptyConfigurationIsReportedAsEmpty(t *testing.T) {
	empty := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	})
	ui := newTestServerWith(t, empty)

	body := ui.get(t, "/admin/tenants?server=test").Body.String()
	mustSay(t, body, "No tenants are configured yet", "the list was read, and it is empty")
	mustNotSay(t, body, "Could not load", "nothing failed")
}

// Unit form of the same split: a read that failed sets Failure, and a file that
// merely parsed badly does not — it was read fine, and blaming the backend for
// it would be a second lie.
func TestFailureIsSetOnlyForAReadThatDidNotHappen(t *testing.T) {
	ui := newTestServerWith(t, brokenBackend())
	data := ui.tenantsVM(t.Context(), ui.clients[0])

	if data.Failure == nil {
		t.Fatal("a 503 from the backend is a load failure")
	}
	if data.Failure.Backend != "test" {
		t.Errorf("backend = %q, want test", data.Failure.Backend)
	}
	if data.Failure.Address == "" {
		t.Error("the failure has to carry the address that was tried")
	}
	if data.Failure.Missing {
		t.Error("a 503 is not a 404")
	}
	if len(data.Tenants) != 0 {
		t.Error("a failed read yields no rows")
	}

	// A malformed file is not a backend failure.
	b := &routingBackend{routing: "{ this is not json", flows: referenceFlows(t), routes: referenceRoutes(t)}
	rv := routingUI(t, b).routingVMFor(t, "devtenant")
	if rv.Failure != nil {
		t.Error("a file that parsed badly was read fine")
	}
	if rv.Error == "" {
		t.Error("but it still has to be reported")
	}
}

// A 404 means the thing does not exist, which is ordinary — a tenant with no
// flows file is not an outage, and saying so would send the operator to check a
// connection that is fine.
func TestAMissingFileIsNotAnUnreachableBackend(t *testing.T) {
	missing := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such file", http.StatusNotFound)
	})
	ui := newTestServerWith(t, missing)

	data := ui.flowsVMScoped(t.Context(), ui.clients[0], "devtenant", "")
	if data.Failure == nil {
		t.Fatal("the read did fail")
	}
	if !data.Failure.Missing {
		t.Error("a 404 is a missing file, not an unreachable backend")
	}

	body := ui.get(t, "/admin/tenants/devtenant/flows?server=test").Body.String()
	mustSay(t, body, "does not exist yet", "a missing file says so plainly")
	mustNotSay(t, body, "--backends", "nothing is wrong with the connection")
}

// newTestServerWithoutBackends is the UI started with nothing to read from,
// which is a startup argument rather than anything a page can recover from.
func newTestServerWithoutBackends(t *testing.T) *testUI {
	t.Helper()
	s, err := NewServer(&config.Config{Port: 0, BindAddr: "127.0.0.1"})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return &testUI{Server: s, handler: s.httpServer.Handler}
}

// Six of eleven sidebar entries used to answer a bare 503: plain text, no
// navigation, and no mention of the setting that would fix it.
func TestAPageWithNoBackendKeepsTheShell(t *testing.T) {
	ui := newTestServerWithoutBackends(t)

	for _, path := range []string{
		"/admin/tenants",
		"/admin/deployment",
		"/admin/audio",
		"/admin/tenants/devtenant",
		"/admin/tenants/devtenant/flows",
		"/admin/tenants/devtenant/test",
		"/admin/config",
	} {
		rec := ui.get(t, path)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s = %d, want 503", path, rec.Code)
		}
		body := rec.Body.String()
		mustSay(t, body, "<!DOCTYPE html>", path+": a page is a page")
		mustSay(t, body, "</aside>", path+": the navigation has to survive")
		mustSay(t, body, "No signaling backend is configured", path+": say what is wrong")
		mustSay(t, body, "--backends", path+": and how to fix it")
	}
}

// The pages that never short-circuit render empty tables; without the shell
// banner they are silent about why.
func TestPagesThatRenderEmptySayWhyWhenThereIsNoBackend(t *testing.T) {
	ui := newTestServerWithoutBackends(t)
	for _, path := range []string{"/", "/admin/live/registrations", "/admin/live/dialogs"} {
		body := ui.get(t, path).Body.String()
		mustSay(t, body, "No signaling backend is configured",
			path+": empty tables need an explanation")
	}
}

// A fragment is swapped into a div, so it must never carry a document. It also
// answers 200 on purpose: htmx does not swap a 5xx body, so a 503 here would be
// discarded and the panel would never change.
func TestAFragmentWithNoBackendIsNotAWholePage(t *testing.T) {
	ui := newTestServerWithoutBackends(t)

	for _, path := range []string{
		"/admin/config/partials/tenants",
		"/admin/config/partials/globals",
		"/admin/config/partials/audio",
		"/admin/config/partials/flows",
		"/admin/config/partials/flowtest",
	} {
		rec := ui.get(t, path)
		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200 — htmx discards a 5xx body", path, rec.Code)
		}
		body := rec.Body.String()
		mustNotSay(t, body, "<!DOCTYPE html>", path+": a fragment is not a document")
		mustNotSay(t, body, "</aside>", path+": a fragment must not carry the sidebar")
		mustSay(t, body, "No signaling backend is configured", path+": but it must explain itself")
	}
}
