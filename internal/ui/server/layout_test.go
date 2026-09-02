package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The whole point of the extraction: one sidebar, one header, both pages.
func TestTheShellIsIdenticalBetweenPages(t *testing.T) {
	ui := newTestServer(t, &fakeSignaling{flows: referenceFlows(t)})

	dash := ui.get(t, "/").Body.String()
	cfg := ui.get(t, "/admin/config").Body.String()

	for _, page := range []struct{ name, body string }{{"dashboard", dash}, {"config", cfg}} {
		if !strings.Contains(page.body, "<!DOCTYPE html>") {
			t.Fatalf("%s did not render a page:\n%.400s", page.name, page.body)
		}
	}

	// The sidebars must be the same navigation, differing ONLY in which entry
	// is marked active — which is the one thing that legitimately varies by
	// page. Comparing them verbatim would assert the opposite of what is wanted.
	sidebar := func(body string) string {
		i := strings.Index(body, "<aside")
		j := strings.Index(body, "</aside>")
		if i < 0 || j < 0 {
			t.Fatalf("no sidebar in:\n%.600s", body)
		}
		return strings.ReplaceAll(body[i:j+len("</aside>")], "nav-item active", "nav-item")
	}
	if sidebar(dash) != sidebar(cfg) {
		t.Error("the two pages render different navigation")
	}
	// ...and each does mark exactly one entry, from the server rather than from
	// scroll position.
	for _, page := range []struct{ name, body string }{{"dashboard", dash}, {"config", cfg}} {
		if got := strings.Count(page.body, "nav-item active"); got != 1 {
			t.Errorf("%s marks %d nav entries active, want 1", page.name, got)
		}
	}

	header := func(body string) string {
		i := strings.Index(body, "<header")
		j := strings.Index(body, "</header>")
		return body[i : j+len("</header>")]
	}
	// Headers differ only in their actions block, which is each page's own.
	for _, want := range []string{"Switchboard", "fixed top-0"} {
		if !strings.Contains(header(dash), want) || !strings.Contains(header(cfg), want) {
			t.Errorf("both headers should contain %q", want)
		}
	}
}

// Every page must mark exactly one sidebar entry. A page that marks none looks
// to an operator like it is nowhere, and this is easy to get wrong because the
// config tab names and the sidebar keys are deliberately different vocabularies
// — "globals" is a handler name, "Deployment" is what an operator calls it.
func TestEveryPageMarksExactlyOneNavEntry(t *testing.T) {
	ui := newTestServer(t, &fakeSignaling{flows: referenceFlows(t)})
	// adminPages rather than a second copy of it: the duplicate drifted, and a
	// page missing from both lists is a page no test ever visits.
	for _, path := range adminPages {
		body := ui.get(t, path).Body.String()
		if got := strings.Count(body, "nav-item active"); got != 1 {
			t.Errorf("%s marks %d nav entries active, want 1", path, got)
		}
	}
}

// The tabs that became routes redirect rather than 404. tabQuery exists because
// these URLs get deep-linked, so someone has them bookmarked.
func TestOldTabURLsRedirectRatherThan404(t *testing.T) {
	ui := newTestServer(t, &fakeSignaling{})
	for tab, want := range map[string]string{
		"tenants": "/admin/tenants",
		"globals": "/admin/deployment",
		"audio":   "/admin/audio",
	} {
		rec := ui.get(t, "/admin/config?tab="+tab)
		if rec.Code != http.StatusFound {
			t.Errorf("?tab=%s status = %d, want 302", tab, rec.Code)
			continue
		}
		if got := rec.Header().Get("Location"); got != want {
			t.Errorf("?tab=%s redirected to %q, want %q", tab, got, want)
		}
	}
}

// The selected backend must survive the redirect, or following a bookmark
// silently switches which server you are editing.
func TestARedirectKeepsTheSelectedBackend(t *testing.T) {
	ui := newTestServer(t, &fakeSignaling{})
	rec := ui.get(t, "/admin/config?tab=tenants&server=test")
	if got := rec.Header().Get("Location"); !strings.Contains(got, "server=test") {
		t.Errorf("Location = %q, which drops the selected backend", got)
	}
}

// The seven partials had drifted between their standalone file and the copy the
// dashboard inlined. There is now one definition, so the page's first paint and
// the poll cannot disagree.
func TestFirstPaintMatchesThePoll(t *testing.T) {
	ui := newTestServer(t, &fakeSignaling{flows: referenceFlows(t)})
	dash := ui.get(t, "/").Body.String()

	// The stat tiles were w-12 h-12 / p-6 in stats.html and w-11 h-11 / p-5 in
	// the copy the dashboard inlined, so the page showed one layout on load and
	// another three seconds later. Only one may survive.
	if strings.Contains(dash, "w-11 h-11") {
		t.Error("the drifted copy of the stats partial is still in the dashboard")
	}
	if !strings.Contains(dash, "w-12 h-12") {
		t.Error("the dashboard's first paint does not use the shared stats definition")
	}

	// Whatever the page painted, the poll must produce the same thing.
	poll := ui.get(t, "/admin/partials/stats").Body.String()
	if strings.TrimSpace(poll) == "" {
		t.Fatal("the stats poll rendered nothing — a define-only file needs ExecuteTemplate")
	}
	if !strings.Contains(dash, strings.TrimSpace(poll)) {
		t.Errorf("the first paint and the poll are not the same markup\n--- poll ---\n%s", poll)
	}
}

// Every live partial must still render on its own, or a poll swaps in emptiness.
func TestEveryLivePartialStillRenders(t *testing.T) {
	ui := newTestServer(t, &fakeSignaling{})
	for _, path := range []string{
		"/admin/partials/stats", "/admin/partials/backends", "/admin/partials/rtpmanagers",
		"/admin/partials/registrations", "/admin/partials/dialogs",
		"/admin/partials/sessions", "/admin/partials/parkedcalls",
	} {
		rec := ui.get(t, path)
		if rec.Code != http.StatusOK {
			t.Errorf("%s status = %d", path, rec.Code)
		}
		if strings.TrimSpace(rec.Body.String()) == "" {
			t.Errorf("%s rendered nothing", path)
		}
	}
}

// The drain modal freezes the table under it. Nothing else tests this, and it
// depends on names the extraction moved.
func TestTheDrainPauseContractSurvives(t *testing.T) {
	ui := newTestServer(t, &fakeSignaling{})
	// It lives with the table it freezes, which is now its own page.
	dash := ui.get(t, "/admin/live/rtpmanagers").Body.String()
	for _, want := range []string{
		`id="rtpmanagers-container"`, `id="drain-modal-container"`,
		"pauseRtpManagersPolling", "resumeRtpManagersPolling", "htmx.process",
	} {
		if !strings.Contains(dash, want) {
			t.Errorf("the drain pause contract lost %q", want)
		}
	}
}

// The canvas library must be in the head of a page that carries the canvas, so
// the editor script can never run before it.
func TestTheCanvasLibraryIsInTheHead(t *testing.T) {
	ui := newTestServer(t, &fakeSignaling{flows: referenceFlows(t)})
	body := ui.get(t, "/admin/config?tab=flows").Body.String()

	head := body[:strings.Index(body, "</head>")]
	if !strings.Contains(head, "drawflow") {
		t.Error("drawflow is not in the head of the config page")
	}
	_ = httptest.NewRequest
}

// The flash region was spelled out in four templates, which is four places a
// refusal could come to read differently. It is now one definition, so this
// checks each consumer actually got it — a partial parsed without the shared
// file fails at execute time, not at boot.
func TestEveryConfigEditorRendersTheSharedFlash(t *testing.T) {
	ui := newTestServer(t, &fakeSignaling{flows: referenceFlows(t)})

	problems := []ConfigProblemData{{Path: "somewhere", Message: "a reason"}}
	warnings := []ConfigProblemData{{Path: "elsewhere", Message: "worth a look"}}

	cases := []struct {
		name   string
		render func() error
	}{
		{"config_tenants", func() error {
			return ui.templates.RenderConfigTenants(io.Discard, ConfigTenantsData{
				Error: "broke", Problems: problems, Warnings: warnings})
		}},
		{"config_tenant_edit", func() error {
			return ui.templates.RenderConfigTenantEdit(io.Discard, ConfigTenantEditData{
				Error: "broke", Problems: problems, Warnings: warnings})
		}},
		{"config_global_edit", func() error {
			return ui.templates.RenderConfigGlobalEdit(io.Discard, ConfigGlobalEditData{
				Error: "broke", Problems: problems, Warnings: warnings})
		}},
		{"config_flows", func() error {
			return ui.templates.RenderConfigFlows(io.Discard, ConfigFlowsData{
				Error: "broke", Problems: problems, Warnings: warnings})
		}},
	}
	for _, c := range cases {
		if err := c.render(); err != nil {
			t.Errorf("%s cannot render the shared flash: %v", c.name, err)
		}
	}

	// And it says the right thing for each outcome: problems blocked the write,
	// warnings rode along with a success. Opposite meanings, same markup.
	var buf strings.Builder
	if err := ui.templates.RenderConfigTenantEdit(&buf, ConfigTenantEditData{
		Problems: problems, Warnings: warnings}); err != nil {
		t.Fatalf("render: %v", err)
	}
	mustSay(t, buf.String(), msgProblemsHeading, "problems blocked the write")
	mustSay(t, buf.String(), msgWarningsHeading, "warnings did not")
	mustSay(t, buf.String(), "a reason", "the problem's message must appear")
	mustSay(t, buf.String(), "somewhere", "a problem is only actionable against its path")
}
