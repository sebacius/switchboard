package server

import (
	"net/http"
	"net/url"
	"strings"
)

// The page shell, and the one place a sidebar is described.
//
// Before this, dashboard.html and config.html each carried their own head,
// header and sidebar — about 250 duplicated lines — and the two sidebars had
// diverged into different things: the dashboard's entries were anchors into one
// long scrolling page, the config page's were HTMX buttons that never changed
// the URL. Neither was navigation. An operator could not bookmark where they
// were, and switching config tab silently discarded the tenant they were
// looking at.
//
// So the sidebar is described once, as data, and Go decides which entry is
// active. A template that ranges over a list cannot drift from itself the way
// two hand-written copies did.

// LayoutData is what the shell needs, independent of what the page shows.
//
// It is a NAMED field on each page's view model rather than an embedded struct:
// ConfigTenantsData, ConfigFlowsData and ReloadBannerData all already carry a
// Server field, and embedding would make every one of those an ambiguous
// selector.
type LayoutData struct {
	Title    string
	Subtitle string
	// Nav is the key of the sidebar entry to mark active. An empty value marks
	// nothing, which is right for a page that is not in the sidebar.
	Nav string
	// Server is the CANONICAL name of the selected backend, as resolved by
	// findClient — never the raw query value, so a typo in a bookmark cannot be
	// echoed back into every link on the page.
	Server   string
	Backends []BackendInfo
}

// MultiBackend reports whether the backend switcher is worth rendering.
func (l LayoutData) MultiBackend() bool { return len(l.Backends) > 1 }

// Items returns the sidebar with the active entry marked.
//
// Go decides which entry is active, rather than JavaScript deciding it from
// scroll position as the dashboard used to and the template ALSO deciding it as
// the config page used to. Two sources of truth for one highlight is how a
// sidebar ends up showing the wrong page.
func (l LayoutData) Items() []NavItem {
	items := sidebar()
	for i := range items {
		items[i].Active = items[i].Key == l.Nav
	}
	return items
}

// Link builds an internal URL carrying the selected backend.
//
// This is the ONLY way a template should build an internal URL, and the reason
// is arithmetic: every page added multiplies the number of links that can
// silently drop ?server= and send an operator to a different backend than the
// one they are looking at. One builder means one place to get it right and one
// place a test can check.
//
// A fragment is left alone: a URL ending in #section takes no query string
// after the fragment, and appending one would produce a link to nothing.
func (l LayoutData) Link(path string) string {
	if l.Server == "" || strings.Contains(path, "#") {
		return path
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + "server=" + url.QueryEscape(l.Server)
}

// NavItem is one sidebar entry. Icons are SVG path data rather than markup so
// the template renders them into an attribute and nothing here can inject HTML.
type NavItem struct {
	// Key is matched against LayoutData.Nav to mark the entry active.
	Key string
	// Active is set by LayoutData.Items, never by the entry's own definition.
	Active bool
	Label  string
	Href   string
	// Section, when set, starts a labelled group above this entry.
	Section string
	// Accent is the hover colour, kept from the sidebar this replaced so the
	// page looks the same as it did.
	Accent string
	Icon   []string
}

// Icon path data, named so the sidebar reads as a list of entries rather than a
// wall of SVG.
const (
	iconGrid    = "M4 6a2 2 0 012-2h2a2 2 0 012 2v2a2 2 0 01-2 2H6a2 2 0 01-2-2V6zM14 6a2 2 0 012-2h2a2 2 0 012 2v2a2 2 0 01-2 2h-2a2 2 0 01-2-2V6zM4 16a2 2 0 012-2h2a2 2 0 012 2v2a2 2 0 01-2 2H6a2 2 0 01-2-2v-2zM14 16a2 2 0 012-2h2a2 2 0 012 2v2a2 2 0 01-2 2h-2a2 2 0 01-2-2v-2z"
	iconUsers   = "M17 20h5v-2a3 3 0 00-5.356-1.857M17 20H7m10 0v-2c0-.656-.126-1.283-.356-1.857M7 20H2v-2a3 3 0 015.356-1.857M7 20v-2c0-.656.126-1.283.356-1.857m0 0a5.002 5.002 0 019.288 0M15 7a3 3 0 11-6 0 3 3 0 016 0z"
	iconChat    = "M8 12h.01M12 12h.01M16 12h.01M21 12c0 4.418-4.03 8-9 8a9.863 9.863 0 01-4.255-.949L3 20l1.395-3.72C3.512 15.042 3 13.574 3 12c0-4.418 4.03-8 9-8s9 3.582 9 8z"
	iconVideo   = "M15 10l4.553-2.276A1 1 0 0121 8.618v6.764a1 1 0 01-1.447.894L15 14M5 18h8a2 2 0 002-2V8a2 2 0 00-2-2H5a2 2 0 00-2 2v8a2 2 0 002 2z"
	iconPark    = "M5 8h14M5 8a2 2 0 110-4h14a2 2 0 110 4M5 8v10a2 2 0 002 2h10a2 2 0 002-2V8m-9 4h4"
	iconStack   = "M9 19V6l12-3v13M9 19c0 1.105-1.343 2-3 2s-3-.895-3-2 1.343-2 3-2 3 .895 3 2zm12-3c0 1.105-1.343 2-3 2s-3-.895-3-2 1.343-2 3-2 3 .895 3 2zM9 10l12-3"
	iconGearA   = "M10.325 4.317c.426-1.756 2.924-1.756 3.35 0a1.724 1.724 0 002.573 1.066c1.543-.94 3.31.826 2.37 2.37a1.724 1.724 0 001.066 2.573c1.756.426 1.756 2.924 0 3.35a1.724 1.724 0 00-1.066 2.573c.94 1.543-.826 3.31-2.37 2.37a1.724 1.724 0 00-2.573 1.066c-.426 1.756-2.924 1.756-3.35 0a1.724 1.724 0 00-2.573-1.066c-1.543.94-3.31-.826-2.37-2.37a1.724 1.724 0 00-1.066-2.573c-1.756-.426-1.756-2.924 0-3.35a1.724 1.724 0 001.066-2.573c-.94-1.543.826-3.31 2.37-2.37.996.608 2.296.07 2.572-1.065z"
	iconGearB   = "M15 12a3 3 0 11-6 0 3 3 0 016 0z"
	iconTenants = "M19 21V5a2 2 0 00-2-2H7a2 2 0 00-2 2v16m14 0h2m-2 0h-5m-9 0H3m2 0h5M9 7h1m-1 4h1m4-4h1m-1 4h1m-5 10v-5a1 1 0 011-1h2a1 1 0 011 1v5m-4 0h4"
	iconSpeaker = "M15.536 8.464a5 5 0 010 7.072m2.828-9.9a9 9 0 010 12.728M5.586 15H4a1 1 0 01-1-1v-4a1 1 0 011-1h1.586l4.707-4.707C10.923 3.663 12 4.109 12 5v14c0 .891-1.077 1.337-1.707.707L5.586 15z"
	iconFlow    = "M13 10V3L4 14h7v7l9-11h-7z"
	iconBeaker  = "M19.428 15.428a2 2 0 00-1.022-.547l-2.387-.477a6 6 0 00-3.86.517l-.318.158a6 6 0 01-3.86.517L6.05 15.21a2 2 0 00-1.806.547M8 4h8l-1 1v5.172a2 2 0 00.586 1.414l5 5c1.26 1.26.367 3.414-1.415 3.414H4.828c-1.782 0-2.674-2.154-1.414-3.414l5-5A2 2 0 009 10.172V5L8 4z"
)

// sidebar is the navigation, in order. One list, used by every page.
func sidebar() []NavItem {
	return []NavItem{
		{Key: "overview", Label: "Overview", Href: "/", Accent: "emerald", Icon: []string{iconGrid}},

		{Key: "tenants", Label: "Tenants", Href: "/admin/tenants", Section: "Configuration", Accent: "blue", Icon: []string{iconTenants}},
		{Key: "deployment", Label: "Deployment", Href: "/admin/deployment", Accent: "amber", Icon: []string{iconGearA, iconGearB}},
		{Key: "audio", Label: "Audio", Href: "/admin/audio", Accent: "purple", Icon: []string{iconSpeaker}},
		// Flows and Test a Call belong to a TENANT, not to the deployment, and
		// both leave this list when the tenant workspace exists. They stay for
		// now because dropping them first would make the canvas and the
		// simulator unreachable by clicking.
		{Key: "flows", Label: "Flows", Href: "/admin/config?tab=flows", Accent: "purple", Icon: []string{iconFlow}},
		{Key: "flowtest", Label: "Test a Call", Href: "/admin/config?tab=flowtest", Accent: "emerald", Icon: []string{iconBeaker}},

		{Key: "registrations", Label: "Registrations", Href: "/admin/live/registrations", Section: "Live", Accent: "blue", Icon: []string{iconUsers}},
		{Key: "dialogs", Label: "Dialogs", Href: "/admin/live/dialogs", Accent: "amber", Icon: []string{iconChat}},
		{Key: "sessions", Label: "Sessions", Href: "/admin/live/sessions", Accent: "emerald", Icon: []string{iconVideo}},
		{Key: "parked", Label: "Parked Calls", Href: "/admin/live/parked", Accent: "violet", Icon: []string{iconPark}},
		{Key: "rtpmanagers", Label: "RTP Managers", Href: "/admin/live/rtpmanagers", Accent: "cyan", Icon: []string{iconStack}},
	}
}

// layoutFor builds the shell's data for a request.
//
// One helper rather than each handler assembling it, because the backend name
// must be the CANONICAL one findClient resolved. A handler that copied the raw
// query value would echo a typo in a bookmark into every link on the page.
func (s *Server) layoutFor(r *http.Request, nav, title, subtitle string) LayoutData {
	l := LayoutData{
		Title:    title,
		Subtitle: subtitle,
		Nav:      nav,
		Backends: make([]BackendInfo, 0, len(s.clients)),
	}
	for _, c := range s.clients {
		l.Backends = append(l.Backends, BackendInfo{Name: c.Name(), Address: c.BaseURL()})
	}
	if c := s.findClient(r.URL.Query().Get("server")); c != nil {
		l.Server = c.Name()
	}
	return l
}

// navForTab maps a config tab name to the sidebar entry it belongs to.
//
// They are not the same vocabulary and should not be forced to be: the tab is
// "globals" because that is what the partial and its handler are called, while
// the sidebar says "Deployment" because that is what an operator calls it. A
// map keeps both honest, and keeps the sidebar's keys stable when these tabs
// become routes of their own.
func navForTab(tab string) string {
	switch tab {
	case "globals":
		return "deployment"
	default:
		return tab
	}
}
