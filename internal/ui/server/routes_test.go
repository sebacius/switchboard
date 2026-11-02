package server

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// adminPages is every URL the sidebar can send an operator to. A page that does
// not render standalone is the bug this whole restructure exists to fix.
var adminPages = []string{
	"/",
	"/admin/tenants",
	"/admin/deployment",
	"/admin/audio",
	"/admin/live/registrations",
	"/admin/live/dialogs",
	"/admin/live/sessions",
	"/admin/live/parked",
	"/admin/live/rtpmanagers",
	"/admin/config?tab=flows",
	"/admin/config?tab=flowtest",

	// The tenant workspace, all three tabs. Their absence from this list is
	// exactly how two of them shipped as 404s: the strip in tabsFor linked
	// URLs nothing had registered, and no test ever followed a link.
	"/admin/tenants/devtenant",
	"/admin/tenants/devtenant/flows",
	"/admin/tenants/devtenant/test",
	"/admin/tenants/devtenant/test?dialed=700",
}

// adminFragments is every URL HTMX fetches on its own.
var adminFragments = []string{
	"/admin/partials/stats",
	"/admin/partials/backends",
	"/admin/partials/rtpmanagers",
	"/admin/partials/registrations",
	"/admin/partials/dialogs",
	"/admin/partials/sessions",
	"/admin/partials/parkedcalls",
	"/admin/config/partials/tenants",
	"/admin/config/partials/globals",
	"/admin/config/partials/audio",
	"/admin/config/partials/flows",
	"/admin/config/partials/flowtest",
}

// The reload banner is deliberately absent from that list: it renders NOTHING
// when what is on disk is what is serving calls, which is the common case. A
// "did it render anything" assertion would be asserting that the deployment has
// drifted.

// Every page renders completely on a hard GET, with and without a backend
// named. Routes were never exercised before this — Server was built as a struct
// literal and handlers were called directly — and that stops being tolerable
// once a path carries a wildcard, because then routing is logic.
func TestEveryPageRendersOnAHardGet(t *testing.T) {
	ui := newTestServer(t, &fakeSignaling{flows: referenceFlows(t)})

	for _, path := range adminPages {
		for _, q := range []string{"", "server=test"} {
			target := path
			if q != "" {
				sep := "?"
				if strings.Contains(target, "?") {
					sep = "&"
				}
				target += sep + q
			}

			rec := ui.get(t, target)
			if rec.Code != http.StatusOK {
				t.Errorf("%s status = %d, want 200", target, rec.Code)
				continue
			}
			body := rec.Body.String()
			if !strings.Contains(body, "<!DOCTYPE html>") {
				t.Errorf("%s did not render a whole page", target)
			}
			if !strings.Contains(body, "</aside>") {
				t.Errorf("%s rendered without the shell", target)
			}
		}
	}
}

// A fragment renders alone, without the shell wrapped around it. Getting this
// backwards would inject a whole document into a div, on a three-second poll.
func TestEveryFragmentRendersWithoutTheShell(t *testing.T) {
	ui := newTestServer(t, &fakeSignaling{flows: referenceFlows(t)})

	for _, path := range adminFragments {
		rec := ui.get(t, path+"?server=test")
		if rec.Code != http.StatusOK {
			t.Errorf("%s status = %d, want 200", path, rec.Code)
			continue
		}
		body := rec.Body.String()
		if strings.TrimSpace(body) == "" {
			t.Errorf("%s rendered nothing", path)
		}
		if strings.Contains(body, "<!DOCTYPE html>") {
			t.Errorf("%s returned a whole page where a fragment was wanted", path)
		}
	}
}

// An unknown live monitor is a typo, not a page.
func TestAnUnknownLiveMonitorIs404(t *testing.T) {
	ui := newTestServer(t, &fakeSignaling{})
	if rec := ui.get(t, "/admin/live/nonsense"); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// internalURL finds hrefs and HTMX URLs that point back at this server.
var internalURL = regexp.MustCompile(`(?:href|hx-get|hx-post|hx-delete|hx-put)="(/[^"#][^"]*)"`)

// The regression this guards is arithmetic: every page added multiplies the
// links that can silently drop ?server= and send an operator to a different
// backend than the one they are looking at. Only the config half of the app was
// backend-aware before this, and three action endpoints already had their own
// copy of the lookup.
func TestNoRenderedLinkDropsTheSelectedBackend(t *testing.T) {
	ui := newTestServer(t, &fakeSignaling{flows: referenceFlows(t)})

	for _, path := range adminPages {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		body := ui.get(t, path+sep+"server=test").Body.String()

		for _, m := range internalURL.FindAllStringSubmatch(body, -1) {
			link := m[1]
			if exemptFromServerParam(link) {
				continue
			}
			if !strings.Contains(link, "server=") {
				t.Errorf("%s renders %q, which drops the selected backend", path, link)
			}
		}
	}
}

// exemptFromServerParam names the links that legitimately carry no backend.
//
// Nothing is exempt, and that is the point: a uniform rule is testable, while a
// growing exemption list is where the next dropped parameter hides. The
// aggregate monitor partials carry the parameter too, even though they currently
// ignore it — the selection has to survive navigation regardless of whether the
// handler at the other end happens to read it yet.
func exemptFromServerParam(link string) bool { return false }

// Every link a page renders has to go somewhere.
//
// The specific regression: the tenant workspace's tab strip linked
// /admin/tenants/{t}/flows and /test, which nothing had registered, so two of
// three tabs and all nine "Try it" links answered 404 on a perfectly healthy
// backend. Enumerating URLs by hand is what let that ship — this follows what
// the pages actually render, so the next unregistered link fails here without
// anyone remembering to add it to a list.
func TestNoRenderedLinkIs404(t *testing.T) {
	b := &routingBackend{routing: referenceRouting(t), flows: referenceFlows(t), routes: referenceRoutes(t)}
	ui := routingUI(t, b)

	checked := map[string]bool{}
	for _, path := range adminPages {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		body := ui.get(t, path+sep+"server=test").Body.String()

		for _, m := range internalURL.FindAllStringSubmatch(body, -1) {
			link := strings.ReplaceAll(m[1], "&amp;", "&")
			if checked[link] {
				continue
			}
			checked[link] = true

			// A link is a GET target. Anything the page only ever POSTs, PUTs or
			// DELETEs to answers 405 by design, which is not a broken link.
			rec := ui.get(t, link)
			if rec.Code == http.StatusNotFound {
				t.Errorf("%s links to %s, which 404s", path, link)
			}
		}
	}
	if len(checked) == 0 {
		t.Fatal("no links were extracted, so this asserts nothing")
	}
}

// hxTarget finds the element an HTMX control swaps its response into.
var hxTarget = regexp.MustCompile(`hx-target="#([^"]+)"`)

// A control that swaps into an id the page does not contain is silently inert:
// the request succeeds, the response is discarded, and nothing moves on screen.
//
// That is the second half of the JSON-editor bug — its form targeted
// #config-content, an id that exists on four other pages but not on the one
// carrying the form.
func TestEveryHXTargetExistsOnItsPage(t *testing.T) {
	b := &routingBackend{routing: referenceRouting(t), flows: referenceFlows(t), routes: referenceRoutes(t)}
	ui := routingUI(t, b)

	found := 0
	for _, path := range adminPages {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		body := ui.get(t, path+sep+"server=test").Body.String()

		for _, m := range hxTarget.FindAllStringSubmatch(body, -1) {
			found++
			if !strings.Contains(body, `id="`+m[1]+`"`) {
				t.Errorf("%s swaps into #%s, which the page does not contain", path, m[1])
			}
		}
	}
	if found == 0 {
		t.Fatal("no hx-targets were extracted, so this asserts nothing")
	}
}
