package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/sebas/switchboard/internal/signaling/dialplan"
	"github.com/sebas/switchboard/internal/signaling/trunk"
	"github.com/sebas/switchboard/internal/ui/client"
)

// The tenant workspace, and the routing table as five tables instead of a
// textarea.
//
// routing.json holds five unrelated shapes, and three of the things an operator
// most needs to know about it are not in the file at all:
//
//   - WHICH PATTERN WINS. Specificity is computed, never declared, which is the
//     right design — a hand-maintained "priority": 20 drifts from intent — and
//     it means the order is nowhere on the page. So it is computed here, by the
//     router's own comparison, and shown as a column.
//   - WHETHER A DID EVER ARRIVES. dids says what HAPPENS to a call; routes.json
//     already said WHOSE call it is. The same number in two files answering
//     different questions is a footgun in plain text.
//   - WHAT NOTHING REACHES. A flow no entry point enters, or a ring group
//     nothing rings, is dead weight that reads exactly like live configuration.
//
// Everything here is presentation. Every write goes through the same validated
// config API the raw editor uses, and the server remains the only thing that
// decides whether a table is valid.

// tenantTab is one sub-page of the workspace.
type tenantTab struct {
	Key    string
	Label  string
	Href   string
	Active bool
}

// RoutingPageData is the routing tab.
type RoutingPageData struct {
	// Failure is a read that did not happen. Error stays for a file that parsed
	// badly, which was read fine.
	Failure *LoadFailure
	Layout  LayoutData
	Server  string
	Tenant  string
	Tabs    []tenantTab

	Operator        string
	RetrievalPrefix string

	Extensions []entryRowView
	DIDs       []entryRowView
	Groups     []groupRowView
	Symbolic   []symbolicRowView

	// Conflicts are pattern pairs that can match the same input with neither
	// more specific. A load error, surfaced before the save rather than after.
	Conflicts []dialplan.Conflict

	// RoutesUnavailable means routes.json could not be read, so no DID row can
	// be given an ownership verdict. Saying that is better than guessing.
	RoutesUnavailable bool

	// Raw carries each section's text for the Edit-as-JSON escape hatch.
	Raw map[string]string

	Success  string
	Error    string
	Problems []ConfigProblemData
	Warnings []ConfigProblemData
}

// entryRowView is one extension or DID row as the page shows it.
type entryRowView struct {
	Key         string
	Destination string
	// Rank is the position in match order, 1 first. Zero when the pattern does
	// not compile, which is itself worth seeing.
	Rank int
	// Specificity is the per-position accepted-set size, rendered as the reason
	// for the rank rather than as a number to trust on faith.
	Specificity string
	// Conflicting marks a row in an ambiguous pair.
	Conflicting bool
	// Ownership is for DIDs: "routed", "unrouted", or "pattern" — see
	// didOwnership.
	Ownership string
	// Unresolved names a destination this tenant does not define.
	Unresolved string
	// Transform describes any digit reshaping, empty when there is none.
	Transform string
}

// groupRowView is one ring group.
type groupRowView struct {
	Name            string
	Strategy        string
	Members         []string
	MemberTimeoutMs int
	// Unreached means nothing in this tenant rings this group.
	Unreached bool
	// Unresolved lists members that name nothing this tenant defines.
	Unresolved []string
}

// symbolicRowView is one symbolic target.
//
// Deliberately NOT marked "unused" when nothing internal references it. Symbolic
// targets are the tenant's externally dialable surface — they are what the
// policy narrows a caller to — so nothing referencing one from inside the
// routing table is the NORMAL case, not a finding. Greying them would tell an
// operator something false. What is worth flagging is the opposite direction: a
// symbolic target pointing at a group or a flow this tenant does not have.
type symbolicRowView struct {
	Name       string
	Target     string
	Unresolved string
}

// flowRowView is one flow in the tenant's flows file.
type flowRowView struct {
	Name  string
	Nodes int
	// ReachedBy names the entry points that lead here. Empty means nothing
	// enters this flow, which is worth seeing.
	ReachedBy []string
}

// tabsFor builds the workspace tab strip.
func (s *Server) tabsFor(l LayoutData, tenant, active string) []tenantTab {
	base := "/admin/tenants/" + tenant
	all := []tenantTab{
		{Key: "routing", Label: "Routing", Href: l.Link(base)},
		{Key: "flows", Label: "Flows", Href: l.Link(base + "/flows")},
		{Key: "test", Label: "Test a call", Href: l.Link(base + "/test")},
	}
	for i := range all {
		all[i].Active = all[i].Key == active
	}
	return all
}

// handleTenantWorkspace renders a tenant's routing table.
func (s *Server) handleTenantWorkspace(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	c := s.requireBackendPage(w, r, "tenants", "Switchboard — "+tenant, "Tenant "+tenant)
	if c == nil {
		return
	}

	data := s.routingVM(r.Context(), c, tenant)
	data.Layout = s.layoutFor(r, "tenants", "Switchboard — "+tenant, "Tenant "+tenant)
	data.Tabs = s.tabsFor(data.Layout, tenant, "routing")
	data.Layout.Nav = "tenants"
	s.renderPageOr500(w, "tenant_routing", data)
}

// handleTenantFlowsPage renders one tenant's flow canvas as a page of the
// workspace, rather than as a fragment of the config editor.
func (s *Server) handleTenantFlowsPage(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	c := s.requireBackendPage(w, r, "tenants", "Switchboard — "+tenant, "Tenant "+tenant)
	if c == nil {
		return
	}

	data := s.flowsVMScoped(r.Context(), c, tenant, r.URL.Query().Get("flow"))
	data.Layout = s.layoutFor(r, "tenants", "Switchboard — "+tenant, "Tenant "+tenant)
	data.Tabs = s.tabsFor(data.Layout, tenant, "flows")
	s.renderPageOr500(w, "tenant_flows", data)
}

// handleTenantTestPage runs the simulator against one tenant, named by the path.
//
// It reads the parameters the routing table's Try-it links already pass, so
// /admin/tenants/x/test?dialed=700 comes back as a trace rather than as a form.
func (s *Server) handleTenantTestPage(w http.ResponseWriter, r *http.Request) {
	tenant := r.PathValue("tenant")
	c := s.requireBackendPage(w, r, "tenants", "Switchboard — "+tenant, "Tenant "+tenant)
	if c == nil {
		return
	}

	data := s.flowTestVM(r.Context(), c, r.URL.Query(), tenant)
	data.Layout = s.layoutFor(r, "tenants", "Switchboard — "+tenant, "Tenant "+tenant)
	data.Tabs = s.tabsFor(data.Layout, tenant, "test")
	s.renderPageOr500(w, "tenant_test", data)
}

// routingVM reads a tenant's routing table and works out everything the file
// cannot say about itself.
func (s *Server) routingVM(ctx context.Context, c *client.Client, tenant string) RoutingPageData {
	data := RoutingPageData{Server: c.Name(), Tenant: tenant, Raw: map[string]string{}}

	content, err := c.GetTenantFile(ctx, tenant, "routing")
	if err != nil {
		data.Failure = loadFailed(c, tenant+".routing.json", err)
		return data
	}
	doc, err := parseRoutingDoc(content)
	if err != nil {
		data.Error = fmt.Sprintf("Failed to read %s.routing.json: %v", tenant, err)
		return data
	}

	data.Operator, _ = doc.Scalar(keyOperator)
	data.RetrievalPrefix, _ = doc.Scalar(keyRetrievalPrefix)
	for _, section := range []string{sectionExtensions, sectionDIDs, sectionGroups, sectionSymbolic} {
		if raw, ok := doc.SectionRaw(section); ok {
			data.Raw[section] = raw
		}
	}

	// What this tenant defines, for resolving every destination the table names.
	known := s.knownTargets(ctx, c, tenant, doc)

	data.Extensions = rankedRows(doc, sectionExtensions, known, &data.Conflicts)
	data.DIDs = rankedRows(doc, sectionDIDs, known, nil)
	s.applyDIDOwnership(ctx, c, tenant, &data)
	data.Groups = groupRows(doc, known)
	data.Symbolic = symbolicRows(doc, known)

	return data
}

// knownTargets is what a destination in this tenant can legally resolve to.
type knownTargets struct {
	groups   map[string]bool
	flows    map[string]bool
	symbolic map[string]bool
	// reached is every destination something in this tenant names, used to find
	// what nothing reaches.
	reached map[string]bool
}

// knownTargets reads both of a tenant's files, because a destination can name a
// group from one and a flow from the other. That is the same pairing the loader
// insists on, and it is why the two files are edited in one place.
func (s *Server) knownTargets(ctx context.Context, c *client.Client, tenant string, doc *routingDoc) knownTargets {
	known := knownTargets{
		groups:   map[string]bool{},
		flows:    map[string]bool{},
		symbolic: map[string]bool{},
		reached:  map[string]bool{},
	}

	groups, _ := doc.Groups()
	for _, g := range groups {
		known.groups[g.Name] = true
		for _, m := range g.Members {
			known.reached[m] = true
		}
	}
	symbolic, _ := doc.Symbolic()
	for _, sym := range symbolic {
		known.symbolic[sym.Name] = true
		known.reached[sym.Target] = true
	}
	for _, section := range []string{sectionExtensions, sectionDIDs} {
		rows, _ := doc.Entries(section)
		for _, row := range rows {
			known.reached[row.Destination] = true
		}
	}
	if op, ok := doc.Scalar(keyOperator); ok {
		known.reached[op] = true
	}

	// The flows file: what flows exist, and what their dial nodes reach.
	if flows, err := c.GetTenantFile(ctx, tenant, "flows"); err == nil && strings.TrimSpace(flows) != "" {
		for name, targets := range flowTargets(flows) {
			known.flows[name] = true
			for _, t := range targets {
				known.reached[t] = true
			}
		}
	}
	return known
}

// flowTargets reads each flow's name and the destinations its nodes dial.
//
// Decoded with a deliberately partial struct: plain json.Unmarshal ignores what
// it does not name, so this reads target out of dial_user, dial_external and
// transfer without having to know the other four node types exist.
func flowTargets(content string) map[string][]string {
	var doc struct {
		Flows map[string]struct {
			Nodes map[string]struct {
				Entry struct {
					Target string `json:"target"`
				} `json:"entry"`
			} `json:"nodes"`
		} `json:"flows"`
	}
	if err := json.Unmarshal([]byte(content), &doc); err != nil {
		return nil
	}

	out := map[string][]string{}
	for name, flow := range doc.Flows {
		var targets []string
		for _, node := range flow.Nodes {
			if node.Entry.Target != "" {
				targets = append(targets, node.Entry.Target)
			}
		}
		out[name] = targets
	}
	return out
}

// resolve reports why a destination names nothing, or "" when it is fine.
//
// A user/ destination is never questioned: a user is a REGISTRATION, not a line
// in this file, so the configuration cannot tell whether one exists and must not
// pretend otherwise.
func (k knownTargets) resolve(dest string) string {
	switch {
	case dest == "":
		return "names no destination"
	case strings.HasPrefix(dest, "user/"):
		return ""
	case strings.HasPrefix(dest, "group/"):
		if !k.groups[strings.TrimPrefix(dest, "group/")] {
			return "this tenant has no such ring group"
		}
	case strings.HasPrefix(dest, "flow/"):
		if !k.flows[strings.TrimPrefix(dest, "flow/")] {
			return "this tenant has no such flow"
		}
	default:
		if !k.symbolic[dest] {
			return "this tenant has no such symbolic target"
		}
	}
	return ""
}

// rankedRows builds an entry table in the order the router will try it.
//
// The order comes from dialplan.RankPatterns — the router's own comparison — so
// the page cannot show a different winner than a caller reaches. Conflicts come
// from dialplan.Conflicts, which reports every ambiguous pair rather than the
// first, because an operator fixing a table wants all of them in one pass.
func rankedRows(doc *routingDoc, section string, known knownTargets, conflicts *[]dialplan.Conflict) []entryRowView {
	rows, err := doc.Entries(section)
	if err != nil || len(rows) == 0 {
		return nil
	}

	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, row.Key)
	}

	rank := map[string]int{}
	spec := map[string]string{}
	if ranked, err := dialplan.RankPatterns(keys); err == nil {
		for _, r := range ranked {
			rank[r.Pattern] = r.Order
			spec[r.Pattern] = joinInts(r.Specificity)
		}
	}

	inConflict := map[string]bool{}
	if found, err := dialplan.Conflicts(keys); err == nil && len(found) > 0 {
		for _, c := range found {
			inConflict[c.A], inConflict[c.B] = true, true
		}
		if conflicts != nil {
			*conflicts = found
		}
	}

	out := make([]entryRowView, 0, len(rows))
	for _, row := range rows {
		out = append(out, entryRowView{
			Key:         row.Key,
			Destination: row.Destination,
			Rank:        rank[row.Key],
			Specificity: spec[row.Key],
			Conflicting: inConflict[row.Key],
			Unresolved:  known.resolve(row.Destination),
			Transform:   describeTransform(row.Transform),
		})
	}

	// Shown in MATCH order, not document order: this table's whole purpose is to
	// answer "which of these wins", and answering it requires seeing them in the
	// order they are tried. A pattern that does not compile has rank zero and
	// sorts first, where it is impossible to miss.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Rank < out[j].Rank })
	return out
}

// applyDIDOwnership marks each DID with whether routes.json sends it here.
//
// dids answers what HAPPENS to a call; routes.json already answered WHOSE call
// it is. A number handled here but not routed here is a row that will never
// fire, and in plain text the two files look unrelated.
//
// Three verdicts, not two. trunk.CheckRoutes — the validator that asks this same
// question on a routes.json write — deliberately abstains on non-literal DIDs:
// "deciding whether one pattern contains another is a harder problem than this
// warning is worth". routes.json legitimately holds patterns, so a pattern row
// gets "not cross-checked" rather than a verdict this codebase declines to give.
func (s *Server) applyDIDOwnership(ctx context.Context, c *client.Client, tenant string, data *RoutingPageData) {
	if len(data.DIDs) == 0 {
		return
	}

	file, err := c.GetGlobalFile(ctx, "routes")
	if err != nil {
		data.RoutesUnavailable = true
		return
	}
	routes, err := trunk.ParseRoutes([]byte(file.Content), "routes.json")
	if err != nil {
		data.RoutesUnavailable = true
		return
	}

	for i, row := range data.DIDs {
		if !isLiteralDID(row.Key) {
			data.DIDs[i].Ownership = "pattern"
			continue
		}
		if owner, ok := routes.TenantForDID(row.Key); ok && owner == tenant {
			data.DIDs[i].Ownership = "routed"
			continue
		}
		data.DIDs[i].Ownership = "unrouted"
	}
}

// isLiteralDID reports whether a key is a plain number rather than a pattern.
// The same test trunk's own cross-check uses, so the badge and the validator
// abstain on exactly the same rows.
func isLiteralDID(did string) bool {
	for _, r := range did {
		if r >= '0' && r <= '9' || r == '+' {
			continue
		}
		return false
	}
	return did != ""
}

// groupRows builds the ring group table, marking groups nothing rings.
func groupRows(doc *routingDoc, known knownTargets) []groupRowView {
	rows, err := doc.Groups()
	if err != nil {
		return nil
	}

	out := make([]groupRowView, 0, len(rows))
	for _, g := range rows {
		view := groupRowView{
			Name:            g.Name,
			Strategy:        g.Strategy,
			Members:         g.Members,
			MemberTimeoutMs: g.MemberTimeoutMs,
			Unreached:       !known.reached["group/"+g.Name],
		}
		for _, m := range g.Members {
			if why := known.resolve(m); why != "" {
				view.Unresolved = append(view.Unresolved, m)
			}
		}
		out = append(out, view)
	}
	return out
}

// symbolicRows builds the symbolic target table.
func symbolicRows(doc *routingDoc, known knownTargets) []symbolicRowView {
	rows, err := doc.Symbolic()
	if err != nil {
		return nil
	}
	out := make([]symbolicRowView, 0, len(rows))
	for _, sym := range rows {
		out = append(out, symbolicRowView{
			Name:       sym.Name,
			Target:     sym.Target,
			Unresolved: known.resolve(sym.Target),
		})
	}
	return out
}

// describeTransform renders a digit transform in words, or "" when there is none.
func describeTransform(t dialplan.Transform) string {
	if t == (dialplan.Transform{}) {
		return ""
	}
	var parts []string
	if t.StripDigits > 0 {
		parts = append(parts, fmt.Sprintf("strip %d leading", t.StripDigits))
	}
	if t.StripSuffixDigits > 0 {
		parts = append(parts, fmt.Sprintf("strip %d trailing", t.StripSuffixDigits))
	}
	if t.Normalize != "" {
		parts = append(parts, "normalize "+t.Normalize)
	}
	return strings.Join(parts, ", ")
}

// joinInts renders a specificity vector as "1·8·10".
//
// The vector rather than a score, because that is what the dialplan exports and
// for the reason it gives: a single number collapses unlike patterns onto the
// same value. Shown as the REASON for the rank, so an operator can see why one
// pattern beat another rather than being asked to trust the ordering.
func joinInts(v []int) string {
	parts := make([]string, 0, len(v))
	for _, n := range v {
		if n >= wildcardWidth {
			parts = append(parts, "∗")
			continue
		}
		parts = append(parts, fmt.Sprintf("%d", n))
	}
	return strings.Join(parts, "·")
}

// wildcardWidth is larger than any real character class, matching the dialplan's
// own stand-in for a trailing wildcard's unbounded set.
const wildcardWidth = 1 << 20

// handleRoutingSectionSave writes one section of a routing table.
//
// The splice is what makes this safe to offer: only the named section's bytes
// are replaced, so an operator editing `groups` cannot damage the paragraph
// above `dids` even if they wanted to. The write still goes through the config
// API, so the SERVER decides whether the result is valid and a refusal comes
// back with its problems attached and nothing on disk.
func (s *Server) handleRoutingSectionSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	c := s.findClient(r.URL.Query().Get("server"))
	if c == nil {
		http.Error(w, "No backend configured", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	tenant := r.PathValue("tenant")
	section := r.FormValue("section")

	data := s.routingVM(r.Context(), c, tenant)
	data.Layout = s.layoutFor(r, "tenants", "Switchboard — "+tenant, "Tenant "+tenant)
	data.Tabs = s.tabsFor(data.Layout, tenant, "routing")

	current, err := c.GetTenantFile(r.Context(), tenant, "routing")
	if err != nil {
		data.Error = fmt.Sprintf("Failed to load %s.routing.json: %v", tenant, err)
		s.renderRoutingBody(w, data)
		return
	}
	doc, err := parseRoutingDoc(current)
	if err != nil {
		data.Error = fmt.Sprintf("Failed to read %s.routing.json: %v", tenant, err)
		s.renderRoutingBody(w, data)
		return
	}

	spliced, err := doc.SpliceSection(section, normalizeContent(r.FormValue("content")))
	if err != nil {
		data.Error = err.Error()
		s.renderRoutingBody(w, data)
		return
	}

	warnings, err := c.PutTenantFile(r.Context(), tenant, "routing", spliced)
	if err != nil {
		var rejected *client.ConfigRejectedError
		if errors.As(err, &rejected) {
			data.Error = msgNotSaved
			data.Problems = problemData(rejected.Problems())
		} else {
			data.Error = fmt.Sprintf("Failed to save %s: %v", section, err)
		}
		s.renderRoutingBody(w, data)
		return
	}

	// Re-read rather than patch the model in place: the ranking, the ownership
	// badges and the reachability all change with the write, and recomputing
	// them from what actually landed is the only way the page cannot disagree
	// with the file.
	saved := s.routingVM(r.Context(), c, tenant)
	saved.Layout = data.Layout
	saved.Tabs = data.Tabs
	saved.Success = savedFileMsg(tenant, "routing")
	saved.Warnings = problemData(warnings)
	configSaved(w)
	s.renderRoutingBody(w, saved)
}

// renderRoutingBody answers a section save with just the routing table's body.
//
// The form targets #routing-body, so a whole page here would splice a second
// layout — sidebar, nav and all — inside a div on the page that submitted it.
func (s *Server) renderRoutingBody(w http.ResponseWriter, data RoutingPageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.renderPartial(w, "tenant_routing_body", data); err != nil {
		slog.Error("[UI] Failed to render routing body", "error", err, "tenant", data.Tenant)
	}
}

// handleRoutingCheck answers "would these patterns load", as they are typed.
//
// Stateless: it reads no file and writes nothing. It exists because the two
// things an operator needs while editing an entry table — the order the router
// will use, and whether any pair is ambiguous — are computed by the router's own
// comparison, and reimplementing that comparison in JavaScript would be a second
// opinion about which extension a caller reaches.
//
// A round trip per keystroke would be wasteful, hence the debounce in the page;
// but the trip itself is cheap because the dialplan is compiled into this binary
// rather than being over the network.
func (s *Server) handleRoutingCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	patterns := r.Form["pattern"]
	result := RoutingCheckData{}

	if ranked, err := dialplan.RankPatterns(patterns); err != nil {
		result.Error = err.Error()
	} else {
		for _, rank := range ranked {
			result.Ranked = append(result.Ranked, entryRowView{
				Key:         rank.Pattern,
				Rank:        rank.Order,
				Specificity: joinInts(rank.Specificity),
			})
		}
	}
	if found, err := dialplan.Conflicts(patterns); err == nil {
		result.Conflicts = found
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.renderPartial(w, "routing_check", result); err != nil {
		slog.Error("[UI] Failed to render routing check", "error", err)
	}
}

// RoutingCheckData is the answer to a live pattern check.
type RoutingCheckData struct {
	Ranked    []entryRowView
	Conflicts []dialplan.Conflict
	Error     string
}
