## Why

The admin UI is organised by file; the loader is organised by tenant. `<tenant>.routing.json` is edited in a textarea on one tab while `<tenant>.flows.json` is edited on a canvas on another, as though the two were unrelated — but `filemanager.validateCandidate` validates each against the other, because a flow must never be able to reference a ring group the other file just removed. The UI should say what the loader says.

Two consequences of that framing are worth fixing in the same pass. The sidebar is a table of contents rather than navigation: its items are `href="#overview"`, `#backends`, `#rtpmanagers` — anchors into one 856-line scrolling page — and the config page has a second, unrelated sidebar of HTMX buttons that never change the URL and that drop the tenant you were looking at when you switch tab. Nothing deep-links; nothing survives a refresh, even though the handler already supports `?tab=&tenant=&flow=` in full. And the routing table is edited as opaque text, which hides the three things an operator most needs: which entry pattern wins (specificity is *computed*, never declared), whether a DID is actually routed to this tenant by `routes.json`, and whether a symbolic target or flow is referenced by anything at all.

## What Changes

- **New**: one `layout.html` shell — header, sidebar, slots — used by every page, replacing two divergent shells that duplicate ~250 lines and seven partials whose inlined copies have already drifted from their standalone files.
- **New**: real routes. `/admin/tenants`, `/admin/tenants/{name}`, `/admin/deployment`, `/admin/audio`, `/admin/live/{registrations,dialogs,sessions,parked,rtpmanagers}`. Every sidebar entry is a URL that deep-links and survives a hard refresh. Cross-page navigation becomes real browser navigation; HTMX keeps doing what it is good at — polling, drawers, saves — *within* a page. Old `?tab=` URLs redirect rather than 404, because `tabQuery` is evidence people deep-link them.
- **New**: the tenant workspace at `/admin/tenants/{name}` with Routing, Flows, Test and Calls. Flows leave the top-level nav entirely: they are not a deployment-wide concept.
- **New**: `routingdoc.go`, a comment-preserving byte-span editor for routing tables, and `jsondoc.go`, the primitives it shares with the existing `flowdoc.go`. `devtenant.routing.json` carries `_comment_dids`-style paragraphs explaining *why* DID ownership is split across two files; `RoutingTable` has no field for them and decodes with plain `json.Unmarshal`, so any struct round-trip deletes them silently. Editing `groups` must be unable to touch `dids` even in principle.
- **New**: section-per-shape routing editors — operator and retrieval prefix as fields, extensions / DIDs / groups / symbolic targets as tables — each with an "Edit as JSON" escape hatch and a diff before save.
- **New**: a target picker reused everywhere a destination goes, including the flow canvas inspector. It knows the four namespaces and resolves them against this tenant's own files, so a dangling reference is hard to create by accident and still possible on purpose.
- **New**: `dialplan.Specificity`/`RankPatterns`/`Conflicts`, so the entry table can show computed match order and flag overlapping patterns with no winner as the operator types — using the loader's own comparison, never a JavaScript restatement of it. `CompileDigitMap`'s existing ambiguity loop is refactored onto `Conflicts`, so "ambiguous" keeps one definition.
- **New**: flow CRUD — create, rename, duplicate, delete — in the same splice-in-place style, including creating the flows file for a tenant that has none.
- **New**: a CDR reader and `GET /api/v1/cdr`, feeding a recent-calls panel on the dashboard and a Calls tab in each tenant workspace. The simulator says what *should* happen; the record says what *did*, in the same workspace.
- **Modified**: `GET /api/v1/config/tenants` gains counts, flow names and a warnings flag so the tenant list and its search render from one call — as a separate detailed listing, not a flag on the cheap one.
- **Modified**: the dashboard stops being a scroll-target dump. Configuration drift — what is on disk but not in force — moves from a banner inside the config editor to the front page, next to the reload button.
- **Modified**: the audio inventory gains format and duration columns and flags anything that is not 8 kHz mono 16-bit PCM, from data `ListAudio` already returns.
- **Unchanged**: validation. Every write still goes through the config API, which refuses an invalid one with its problems attached and nothing reaching disk. Client-side checks are a convenience layer with no authority.

No breaking changes to the signaling or RTP manager contracts. No new frontend framework, no bundler, no `package.json`.

## Capabilities

### New Capabilities

- `admin-navigation`: the single shell, the real routes, the full-page-and-fragment duality, backend selection threading, and the dashboard as an overview rather than a table of contents.
- `tenant-workspace`: the tenant list with content-aware search, and the per-tenant workspace that gathers routing, flows, test runs and calls under one URL.
- `routing-authoring`: the section-per-shape routing editors — computed specificity, DID ownership, reachability, the target picker, inline test-a-call, and the JSON escape hatch.
- `config-document-editing`: the guarantee that editing configuration through the UI preserves comments, key order and formatting, extended from flows to routing tables and generalised into shared primitives.
- `pattern-specificity`: the exported comparison that lets a UI show the order the router will actually use, and name every pair of patterns that overlap with no winner.
- `call-records`: reading the append-only call record file — tail, filter, degrade when recording is off — and surfacing it in the dashboard and the tenant workspace.
- `audio-inventory`: format and duration reporting on the audio library, flagging what will not play as recorded.

### Modified Capabilities

- `flow-authoring`: the canvas moves from `/admin/config?tab=flows` to `/admin/tenants/{name}/flows/{flow}` and its tenant combobox disappears, because the tenant is in the URL; the editor gains create, rename, duplicate and delete for whole flows, with a cross-file guard when the routing table references the flow being removed.

## Impact

**New files**
- `internal/ui/server/templates/layout.html` — the one shell
- `internal/ui/server/jsondoc.go` (+ test) — span/edit primitives shared by both document editors
- `internal/ui/server/routingdoc.go` (+ test) — the routing table's comment-preserving editor
- `internal/ui/server/static/target-picker.js` — the one destination combobox
- `internal/ui/server/handlers_tenants.go`, `handlers_live.go`, `handlers_calls.go` and their templates
- `internal/signaling/dialplan/specificity.go` (+ test)
- `internal/signaling/cdr/reader.go` (+ test)

**Modified files**
- `internal/ui/server/server.go` — the route table, `findClientStrict`, want-scoped backend fetching
- `internal/ui/server/templates.go` — per-page parse sets, page/partial registries, `LayoutData`
- `internal/ui/server/flowdoc.go` — flow CRUD
- `internal/ui/server/handlers_*.go`, every template
- `internal/ui/server/handlers_flows_test.go` — assertions re-shaped ahead of the markup moving
- `internal/signaling/api/handlers_config.go` — detailed tenant listing, CDR endpoint
- `internal/signaling/filemanager/filemanager.go` — counts and warnings
- `internal/signaling/dialplan/digitmap.go` — `CompileDigitMap` refactored onto `Conflicts`
- `api/types/v1/types.go` — tenant listing fields, call record types
- `CLAUDE.md` — the REST API section

**Not touched**: routing, admission, resolution and authorization; the RTP manager and its proto; every validation rule. The UI is a client of write paths that already exist.

**Dependencies**: none added. HTMX, Tailwind and the pinned Drawflow build, as today.
