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
