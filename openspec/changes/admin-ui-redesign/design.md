## Context

The UI server is 3,978 lines of Go and 3,243 lines of templates, and the shape it has grown into now works against the system it administers.

`internal/ui/server/templates.go` parses **19 independent `*template.Template` values**, one per file, with no base layout and no shared func map. So `dashboard.html` (856 lines) and `config.html` (194) each carry their own `<head>`, header and `<aside class="fixed left-0 top-14 bottom-0 w-56">` — and `dashboard.html` additionally inlines `{{define}}` **copies** of all seven partials for first paint. Those copies have already drifted: `stats.html` renders `gap-6 p-6 w-12`, `dashboard.html:583` renders `gap-4 p-5 w-11`, so the dashboard shows one layout on load and a different one three seconds later.

Navigation is anchors. The dashboard's sidebar items are `href="#overview"`, `#backends`, `#rtpmanagers`; the config page's are `<button hx-get="/admin/config/partials/{tab}" hx-target="#config-content">` which never change the URL and which **drop `.TabQuery`**, so switching tab discards the tenant. The server is not the limitation — `handleConfigPage` allowlists `tab` and `tabQuery` forwards `tenant, dialed, digits, direction, name, file, flow` — the sidebar simply never uses it.

Meanwhile the write path is already good, and this change must not disturb it. `client.PutTenantFile` → `PUT /api/v1/config/tenants/{name}?file=` → `filemanager.validateCandidate`, which validates a routing candidate against the tenant's **existing flows** and a flows candidate against its **existing routing table** (`validate.go:52-89`). A refusal is HTTP 422 carrying `[]ConfigProblem` with dotted paths and nothing reaches disk; a success carries warnings rather than blocking on them. The write is atomic (temp file, `Sync`, `Rename`) because a reload can fire at any moment.

Three constraints were verified before designing anything:

- **Comments are load-bearing and unprotected.** `RoutingTable` (`routing_config.go:83`) has no field for `_comment*` and decodes with plain `json.Unmarshal` — no `DisallowUnknownFields`. `devtenant.routing.json` carries three paragraphs, including the one explaining why DID ownership is split across two files. A struct round-trip deletes all of it silently. `flowdoc.go`'s header already makes this argument for flows; it applies harder here.
- **The UI server already links the dialplan.** `internal/ui/server` imports `internal/signaling/dialplan` (`handlers_flows.go:11`) for `NodeSchema()`. Ranking and conflict detection therefore need no HTTP round trip and no JavaScript restatement.
- **`api/types/v1` is a decorative mirror.** No signaling handler imports it; `handleConfigTenantList` builds an ad-hoc `[]map[string]interface{}` (`handlers_config.go:127`). Adding fields to the mirror alone would change nothing.

## Goals / Non-Goals

**Goals:**

- One shell and one sidebar, where every entry is a URL that deep-links and survives a hard refresh.
- A tenant's routing table, flow graphs, test runs and call records reachable from one place, because the loader already treats the first two as one unit.
- Make visible the three things the JSON cannot say: which entry pattern wins, whether a DID is actually routed here, and what nothing references.
- Extend `flowdoc.go`'s guarantee — an edit cannot damage what it did not touch — to routing tables, and generalise it rather than copying it.
- Keep Go the authority on validity. The browser may hint; the server decides.
- Land in commits that each leave the UI working, with the verifiable change preceding the risky one.

**Non-Goals:**

- Serving audio bytes. Adding a player needs a gRPC method (`PlayAudio` streams RTP, not file contents) or an HTTP surface on a service that is deliberately gRPC-only. Format and duration columns come from data `ListAudio` already returns; playback, upload and delete are a later change.
- Making `trunk.CheckRoutes` run on a tenant routing write. It runs today only on a `routes.json` write, so the DID-ownership finding is invisible when editing a tenant. The UI surfaces it as a badge; changing when the *server* emits it is a signaling behaviour change and belongs elsewhere.
- A caching layer in front of the backends.
- Multi-user conflict detection. Last write wins, as today, and now visibly so.
- A frontend framework, a bundler, or an HTML-parsing test dependency.
- Editing `policy.json` structurally. It stays a raw editor behind `--allow-global-config-writes`.

## Decisions

### One layout, one parse set per page, duality by entry point

`layout.html` holds head, header, sidebar and `<main>`, and calls `{{template "content" .}}`. Each page defines `content`. Optional slots — `{{block "head_extra" .}}`, `{{block "scripts" .}}` — use `block` so a page may omit them, while `content` stays a bare `{{template}}` so a page that forgets it fails at startup rather than rendering an empty shell.

Each page gets its **own** parse set: `template.New("layout.html").ParseFS(fs, "templates/layout.html", pageFile, partialsItUses...)`. Then one set serves both needs, because `ExecuteTemplate` picks the entry point:

- full page → `ExecuteTemplate(w, "layout.html", vm)`
- fragment → `ExecuteTemplate(w, "content", vm)`

*Why not one global ParseFS set:* every `{{define}}` name becomes global, so a collision is silent last-wins, and one bad template breaks all 19 pages at startup instead of one.

*Collapsing the seven drifted copies:* each partial file's body is wrapped in a single `{{define "dialogs"}}…{{end}}`. The live page renders `{{template "dialogs" .}}` for first paint and the poll handler renders the same definition. One definition, two call sites; the seven copies in `dashboard.html` are deleted. The cost is that seven render wrappers move from `Execute` to `ExecuteTemplate` — a template whose body is only a `define` renders empty under `Execute`.

*Layout data is a named field, not an embedded struct.* `Layout LayoutData` on each view model. Embedding collides: `ConfigTenantsData`, `ConfigFlowsData` and `ReloadBannerData` all already carry `Server string`.

*Migration is per-page.* `renderPage`/`renderPartial` back onto `pages`/`partials` maps; the 26 existing one-line wrappers become forwarders and are deleted opportunistically, so no commit has to rewrite all of them.

### Cross-page navigation is real navigation; HTMX swaps within a page

Plain `<a href>` for every sidebar entry and every tenant sub-tab. **No `hx-boost`.** HTMX keeps the polls, the drawers, the save results and the in-table refreshes.

This is not merely stylistic. Drawflow is pinned in `config.html`'s `<head>` today with a comment saying it is there rather than in the tab's partial "so the editor's script can never run before the library it needs has arrived". A page-level `head_extra` block preserves that for a hard GET — but a *fragment* swapped into a page whose head lacks the library would not. Making cross-page navigation a real navigation eliminates that entire class of failure, and it is also what "deep-links and survives a refresh" means. Putting Drawflow in the global layout instead would re-couple exactly what that comment warns against, for one page's benefit.

*Consequence:* no `hx-push-url` in v1. The tenant tab strip is four links to four routes, each a few milliseconds.

### Paired routes, not `HX-Request` sniffing

`GET /admin/live/dialogs` renders the page; `GET /admin/partials/dialogs` renders the fragment; both call one `dialogsVM(ctx, r)`. The "two code paths" worry is answered at the view-model layer, where all the logic is; the two render calls are one line each.

*Why not sniff the header:* a response body that varies by request header breaks curl and caching, and history-restore and boosted requests do not carry it uniformly. Worse, sniffing would collapse the poll URL and the page URL into one — so a single mis-detection injects `<html>` into a `<div>`, on a three-second loop.

### Backend selection gets one choke point

`func (l LayoutData) Link(path string) string` appends the selected server and the allowlisted params, and becomes the **only** way a template builds an internal URL. Every new route otherwise multiplies the places `?server=` can be dropped, and only the config half of the app is server-aware today.

The three duplicated six-line inline lookup loops (`server.go:473`, `:523`, `:582`) collapse into one `findClientStrict` — same 404-on-unknown policy, which is right for an action endpoint — while `findClient` keeps its documented fallback for reads. A fourth copy is otherwise inevitable.

The runtime poll-pause contract moves **verbatim**: `id="rtpmanagers-container"`, `pauseRtpManagersPolling`/`resumeRtpManagersPolling`, the `removeAttribute`/`setAttribute` + `htmx.process()` pair, and the `htmx:afterSwap` listener. It is the only thing stopping the RTP table refreshing under an open drain modal.

### Fetch what the page renders — which makes the split cheaper, not dearer

`fetchBackendData` makes **seven sequential HTTP calls per backend on every partial poll**, regardless of which partial asked. The intuition that splitting monitors onto separate pages multiplies that is backwards: today a single dialogs poll already pays for all seven.

A `want` struct scopes each fetch to its own slice, and the wanted fetchers run **concurrently within** a backend — they are independent GETs and the current sequencing buys nothing. The dialogs poll drops from 7 sequential calls per backend to 2. Total call volume falls even as page count rises.

*Why not a cache:* a TTL at or near the 3 s poll period is a coin flip between no benefit and one-period staleness, and it would create invalidation duties at drain, cancel-drain and unpark that are read-after-write correct today only because nothing is cached. The `want` struct *deletes* work.

This must land in the same commit as the live-page split, or there is a window where five open tabs each pay seven calls.

### Span-splice, generalised

`flowdoc.go`'s primitives are already document-generic; only its flow-shaped parts are not. Extract to `jsondoc.go`: `span`, `edit`, `objectKeys`, `objectSpans`, `skipToValue`, `encodeKey`, `indentBefore`, `applyEdits`, `detectIndent`, `renderValue`, `compact`, `quote`. A pure move — the proof is that `flowdoc_test.go` compiles and passes **unmodified**.

Three primitives are added there, so both documents share one implementation and one set of tests:

- `removeKey` — the delete span with all separator handling in one place: not-first swallows the preceding comma; first-with-successor runs to the next key's indent; only-key leaves `{}`. Deleting a flow, an extension and a group are then the same tested code rather than three chances to get commas wrong.
- `insertKey` — **appends after the last key; never prepends.** That is the whole answer to the comment hazard: in these files a comment key *precedes* the key it describes, so inserting before an existing key can land between a comment and its subject. Appending can only land after the last value and before `}`.
- A key-token guard for rename: assert the bytes at `keyStarts[old]` really are `quote(old)` and error rather than splice blindly. `encodeKey` recovers a key's start only for keys written without escapes; today that holds, and the guard makes it a checked assumption. `flowdoc` benefits too.

`routingDoc` then exposes readers in document order (so tables are stable across saves) and writers that each return the whole new document. Two rules matter more than the surface:

- **Every mutator is `parse → single edit → applyEdits → return`.** Offsets are never chained across edits; `ensureSection` appends `"groups": {}` and then re-parses. Re-parsing costs microseconds and removes the stale-offset bug class entirely. Batch mutation is therefore unsupported in v1, and the UI edits one row per request anyway.
- **`SectionRaw` + `SpliceSection` *is* the "Edit as JSON" escape hatch**, for every section, implemented as one splice with operator-supplied text. There is deliberately no second write path — a JSON tab implemented separately is a second thing that can be wrong.

`Entry`'s bare-string-vs-object duality is preserved by re-emitting the bare form when there is no transform and the object form otherwise. That is a second implementation of `Entry.MarshalJSON`, which exists for exactly this reason, so it is pinned by a round-trip test: `SetEntry`'s output must parse to a `dialplan.Entry` whose `json.Marshal` is byte-identical to the input's. `RenameEntry` rewrites the **key token span only**, so a pattern edit cannot re-sort the table under the operator.

### Specificity belongs in the dialplan, and is a vector

`digitmap.go` already exports `CompilePattern`, `Dominates` and `Overlaps`, and the UI server already links the package. What is missing is the per-position cardinality vector. New `specificity.go`:

- `(*Pattern).Specificity() []int`
- `RankPatterns(raws []string) ([]Rank, error)` — sorted by cardinality vector lexicographically ascending, tie-broken by raw string for stability
- `Conflicts(raws []string) ([]Conflict, error)` — every pair where `Overlaps && !Dominates` either way

*Why not a scalar score:* `digitmap.go`'s own header argues that a weighted scalar "collapses unlike patterns onto the same number and invites collisions", which is why comparison is a per-position vector in the first place. Exporting a score would contradict the package's stated design.

*Why in `dialplan` rather than an O(n²) sort in the UI:* the UI-side sort produces the same partial order while relocating the *definition* of match order into a template. That is the same reason the flow canvas takes its palette from `NodeSchema()` rather than a JS catalog. `Conflicts` is literally the loop already inside `CompileDigitMap` (`digitmap.go:470-482`); lifting it and refactoring `CompileDigitMap` to call it preserves behaviour, keeps one definition of "ambiguous", and gives the UI *all* conflicts rather than only the first.

*The column is honest about being partial.* `Dominates` is a partial order. For overlapping patterns the vector order is genuine match order; for disjoint patterns relative row position is arbitrary and cosmetic; where two overlap with neither dominating, that is exactly a `Conflict` and the row goes red rather than pretending to rank them. The tooltip says so.

*Live checking* is a debounced `hx-post` to a stateless UI-server endpoint that runs `RankPatterns` + `Conflicts` in-process and returns a few hundred bytes of HTML. It reads no file and writes nothing. "No JS reimplementation" plus "live as you type" admits no other answer, and here the round trip is cheap precisely because the dialplan is already linked in.

### DID ownership has three states, because the validator abstains

`trunk.CheckRoutes` already answers "is this number routed to this tenant", and `trunk.ParseRoutes` + `TenantForDID` handle patterns and both E.164 forms. Reuse them; the precedent is explicit at `validate.go:137-139` — "the cross-check itself lives in the trunk package, so this command and the config API cannot disagree about what a bad DID table looks like." A third opinion in the UI would be the thing that finally disagrees.

But `CheckRoutes` **deliberately abstains on non-literal DIDs**: "deciding whether one pattern contains another is a harder problem than this warning is worth". And `routes.json` legitimately holds patterns — `"+1555800XXXX": "devtenant"`. So the badge is *routed here* / *not routed here* / *pattern, not cross-checked*. The UI must not invent a verdict the validator refuses to give.

### The target picker: one owner for the markup, even where JS builds the DOM

A Go partial plus a JS module served as a static asset (`//go:embed static/*`, an `/admin/static/` route). Not inline in the layout: inline script inside a swapped fragment re-executes on every swap, and the flow inspector needs the same code. Not a native `<datalist>`: it cannot render namespace badges or dangling-state styling and behaves differently per browser.

Option data is **server-rendered once per page** into `<script type="application/json" id="target-catalog">`. The workspace handler already holds the routing document and the flow names, so this costs no request, is automatically correct under `?server=`, and cannot disagree with the table being edited the way a separate endpoint could. The flows tab already ships `const TENANTS` and the node schema this way.

The partial also emits a hidden `<template id="target-picker-template">`, and the Drawflow inspector **clones it** rather than hand-writing markup — so the markup has exactly one owner even where the DOM is constructed in JavaScript. `mount(root)` is idempotent and DOM-driven, called from `DOMContentLoaded`, an `htmx:afterSwap` listener, and the end of the inspector's render.

Three states, not two: **resolved** (in the catalog), **free** (plausible namespace, not defined — amber, and offered only as an explicit *"Use anyway"* row that arrow keys cannot reach), and **invalid** (empty or an unknown namespace — red). Dangling must never hard-block: a flow that does not exist yet is a legitimate intermediate state, and flow CRUD requires authoring in both orders. The picker's job is to make the 422 rare, not to replace it.

### Flow CRUD, and where the cross-file check lives

`CreateFlow`, `DeleteFlow`, `RenameFlow`, `DuplicateFlow` on `*flowsDoc`, all splicing. Rename rewrites the key token alone, so comments and formatting inside the flow are *provably* untouched; duplicate copies the source span verbatim, comments included, which is the point. Deleting the last flow leaves `{"flows":{}}` — a valid file meaning "no flows" — rather than deleting the file, since the API has no flows-delete and adding one is out of scope.

Creating the first flow for a tenant with no flows file needs no special case in the UI: `GetTenantFile` returns `("", nil)` for a missing flows file, so creation is a PUT of a minimal document, and the server's validate-on-write enforces that the tenant already has a routing table.

The cross-file guard is layered, and the layering matters. **The server is authority**: PUT-ing flows with a referenced flow removed should fail `CheckFlows` against the existing routing table with nothing reaching disk. That direction is *verified empirically first* — a test that deletes a referenced flow through the API and expects 422 is the first thing written; if it turns out not to be covered, the fix belongs in `filemanager`, not in a UI check papering over it. The UI check is advisory: it names the referencing rows (`extensions."700"`, `dids."+15558001200"`) before the round trip, refuses by default, and offers an explicit "delete anyway" that attempts the save and reports the server's verdict.

### Call records: tail backwards, degrade like audio

`Reader.Tail` reads backwards from EOF in 64 KiB chunks, newest-first, stopping at the limit or the file start, so a large file is never loaded. **Unparsable lines are skipped silently** — a torn final line is an expected property of an append-only file written without fsync per record, and skipping is also what makes reading concurrently with `JSONLSink` safe with no locking. Total bytes scanned are capped and `truncated` reported, so a filter matching nothing does not walk a 2 GB file. That cap is the honest small answer; an index is out of scope for "if cheap to expose".

Recording is **off by default** (`--cdr-path ""`). The endpoint answers HTTP 200 with `source: "unavailable"` and a reason naming the flag — the exact shape `GET /api/v1/config/audio` already uses. Not 404, not 503: the UI would then have to tell "broken" from "off", and the house style is already set for precisely this situation.

### The tenant listing's cost class

Counts require parsing every tenant's two files; `has_warnings` requires running validation per tenant. `ListTenants` today only `Stat`s directory entries. So this is a **separate `ListTenantsDetailed`**, not a `?detail=1` flag — an endpoint that *sometimes* does forty times the work eventually gets called from a poll by accident.

`handleConfigTenantList` also switches to marshalling the shared type rather than an ad-hoc map. Adding fields to a mirror nothing imports would otherwise change nothing, and a marshal-round-trip test converts that weakness into a test failure — for the CDR types too.

### Re-shape the tests before moving the markup

Eight tests assert `strings.Contains` on ids (`id="drawflow"`, `id="flow-palette"`, `wireTenantPicker`, `const TENANTS`) and on user-facing strings. The instinct is to update them in the commit that moves the markup. That is wrong for one reason: when a template rewrite and its assertions change together, a legitimate rename and a silently accepted regression look identical in review. That is this change's most likely undetected failure.

So the assertions are re-shaped **first, with templates untouched**, and they get different fates by kind. Wire contract (`name="tenant"`, `name="flow"`, `name="file"`) is kept and strengthened — it *is* the form contract. Behaviour-as-text is kept but the strings move to named constants shared by view model and test, so wording changes in one place while the test keeps meaning "this state is reported". Structural ids are replaced by view-model assertions, with one render smoke test per page.

No HTML parser: `go.mod` has neither `x/net/html` nor goquery, and this does not justify a dependency. And route-level tests are added — today `Server` is built by struct literal and handlers are called directly, so routes are never exercised, while `GET /admin/tenants/{name}/flows/{flow}` makes path parsing the new logic and the likeliest bug site. Go 1.24 gives stdlib method-and-wildcard patterns, so no router dependency either.

## Risks / Trade-offs

- **`?server=` regresses across the new routes.** Only the config half is server-aware today and three endpoints already duplicate the lookup. → `Layout.Link` as the sole URL builder in templates, a regex test over every rendered body asserting no internal link drops the param, and the three loops consolidated now.
- **A span-splice bug corrupts a live routing file.** New code on files whose prose, if mangled, takes the explanation of the two-step DID model with it. → re-parse rather than chain offsets; real checked-in fixtures; every mutator's output run through a `dialplan` load *and* a line-diff assertion; `make validate` per commit.
- **Drawflow load order regresses when the canvas moves route.** The existing comment says this has been hit before, and layout extraction is exactly the change that reintroduces it. → `head_extra` on the canvas page, the no-`hx-boost` rule, and the in-page fallback stays.
- **The poll-pause contract breaks silently** — nothing tests it, and it depends on ids that move. → moved verbatim, both function names and the container id kept, presence asserted.
- **The tenant list becomes expensive and then gets polled.** → a separate detailed listing rather than a flag on the cheap one; `has_warnings` computed lazily per row if it proves costly.
- **The specificity column implies a total order that does not exist.** → label it relative, say so in the tooltip, and let `Conflicts` turn genuinely incomparable overlapping pairs red rather than ranking them.
- **First-save reformatting.** A section edited through the UI comes back at the document's own indent, which may differ in whitespace from what its author typed inside that section. → bounded to the edited section; every other key and every comment keeps its exact bytes, which is the property the tests assert.
- **Commit 1 is pure test churn with no user-visible change.** A diff that appears to do nothing. → accepted deliberately; it is what makes the eight following commits reviewable.

## Migration Plan

Additive and incremental. No file on disk changes format until someone saves through a section editor, and that save is a splice bounded to one section. The signaling API gains two read surfaces (detailed tenant listing, call records) and one export (`specificity.go`); nothing existing changes shape except `handleConfigTenantList`, which starts marshalling the shared type it already agreed with on the wire.

Old `/admin/config?tab=…` URLs **redirect** rather than 404. `tabQuery`'s existence is evidence people deep-link them.

Rollback is per commit: each leaves the UI working, and commits 1, 2, 5 and 6 are invisible to users by design, so every risky change is preceded by the commit that makes it verifiable. Reverting the whole change leaves every configuration file still loading, because nothing the editors write is outside what the loader already accepts.

## Open Questions

- **Does deleting a referenced flow actually fail validation today?** `validateCandidate` checks a flows candidate against the existing routing table, which should catch it. Verified by a test written before the flow-CRUD UI; if it does not hold, the fix belongs in `filemanager`.
- **Is `has_warnings` cheap enough to compute eagerly per tenant?** Measured on the detailed listing; the fallback is a lazy per-row `hx-get`, not dropping the badge.
- **Deferred by choice:** serving audio bytes (needs a gRPC method or an HTTP surface on a gRPC-only service); making `CheckRoutes` run on tenant routing writes; graphical editing of `policy.json`.
