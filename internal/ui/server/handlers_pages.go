package server

import (
	"log/slog"
	"net/http"
)

// Full pages for the routes the sidebar names.
//
// Every entry in the sidebar is a URL that renders a complete page on a hard
// GET and survives a refresh. That was the whole complaint about what this
// replaced: the dashboard's sidebar was anchors into one long scrolling page and
// the config page's was HTMX buttons that never changed the URL, so nothing
// could be bookmarked and switching tab discarded the tenant you were on.
//
// Each page here is PAIRED with a fragment route that already existed, and both
// are built from one view-model function. Two thin handlers, one data path, one
// template definition.
//
// Deliberately NOT done by sniffing HX-Request and returning either a page or a
// fragment from one URL: a response body that varies by request header breaks
// curl and caching, history-restore does not carry the header uniformly, and it
// would collapse the poll URL and the page URL into one — so a single
// mis-detection injects a whole document into a div, on a three-second loop.

// livePageOf maps a live route to the page template and the sidebar entry it
// marks, so the five monitors are one table rather than five near-copies.
var livePages = map[string]struct {
	page     string
	nav      string
	title    string
	subtitle string
}{
	"registrations": {"live_registrations", "registrations", "Registrations", "SIP registrations"},
	"dialogs":       {"live_dialogs", "dialogs", "Dialogs", "Active SIP dialogs"},
	"sessions":      {"live_sessions", "sessions", "Sessions", "Active RTP sessions"},
	"parked":        {"live_parked", "parked", "Parked Calls", "Calls on hold in parking slots"},
	"rtpmanagers":   {"live_rtpmanagers", "rtpmanagers", "RTP Managers", "Media nodes and drain state"},
}

// handleLivePage renders one of the read-only monitors as its own page.
//
// The tables still aggregate every configured backend and label each row with
// the one it came from, which is what they did when they were sections of the
// dashboard. The selected backend rides along because the ACTIONS on these
// pages — drain, cancel drain, force unpark — are addressed to one backend, and
// because losing the selection on navigation is the regression this whole change
// is trying not to introduce.
func (s *Server) handleLivePage(w http.ResponseWriter, r *http.Request) {
	which := r.PathValue("which")
	spec, ok := livePages[which]
	if !ok {
		http.NotFound(w, r)
		return
	}

	data := s.buildTemplateData(r.Context())
	data.Layout = s.layoutFor(r, spec.nav, "Switchboard — "+spec.title, spec.subtitle)
	s.renderPageOr500(w, spec.page, data)
}

// handleTenantsPage renders the tenant list at its own URL.
func (s *Server) handleTenantsPage(w http.ResponseWriter, r *http.Request) {
	c := s.findClient(r.URL.Query().Get("server"))
	if c == nil {
		http.Error(w, "No backend configured", http.StatusServiceUnavailable)
		return
	}
	data := s.tenantsVM(r.Context(), c)
	data.Layout = s.layoutFor(r, "tenants", "Switchboard — Tenants", "Tenant configuration")
	s.renderPageOr500(w, "tenants", data)
}

// handleDeploymentPage renders the deployment-wide files at their own URL.
func (s *Server) handleDeploymentPage(w http.ResponseWriter, r *http.Request) {
	c := s.findClient(r.URL.Query().Get("server"))
	if c == nil {
		http.Error(w, "No backend configured", http.StatusServiceUnavailable)
		return
	}
	data := s.globalsVM(r.Context(), c)
	data.Layout = s.layoutFor(r, "deployment", "Switchboard — Deployment", "Deployment-wide configuration")
	s.renderPageOr500(w, "deployment", data)
}

// handleAudioPage renders the audio library at its own URL.
func (s *Server) handleAudioPage(w http.ResponseWriter, r *http.Request) {
	c := s.findClient(r.URL.Query().Get("server"))
	if c == nil {
		http.Error(w, "No backend configured", http.StatusServiceUnavailable)
		return
	}
	data := s.audioVM(r.Context(), c)
	data.Layout = s.layoutFor(r, "audio", "Switchboard — Audio", "Audio library")
	s.renderPageOr500(w, "audio", data)
}

// renderPageOr500 renders a page, or reports that it could not.
//
// A page failing to render is a 500, unlike a fragment: a fragment that fails
// leaves the operator looking at what was already on screen, while a page that
// fails would be a blank window with no explanation.
func (s *Server) renderPageOr500(w http.ResponseWriter, page string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.renderPage(w, page, data); err != nil {
		slog.Error("[UI] Failed to render page", "page", page, "error", err)
		http.Error(w, "Failed to render template", http.StatusInternalServerError)
	}
}

// tabRedirects are the config tabs that became routes of their own.
//
// A redirect rather than a 404. tabQuery exists precisely because these URLs are
// deep-linked — it forwards tenant, dialed, digits, direction, name, file and
// flow through the page — so someone has them bookmarked, and answering 404
// would punish them for having used the feature as designed.
var tabRedirects = map[string]string{
	"tenants": "/admin/tenants",
	"globals": "/admin/deployment",
	"audio":   "/admin/audio",
}
