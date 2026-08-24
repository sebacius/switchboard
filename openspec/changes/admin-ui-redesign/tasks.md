Each numbered group is one commit and leaves the UI working. Groups 1, 2, 5 and
6 are invisible to users by design: every risky change is preceded by the commit
that makes it verifiable.

## 1. Re-shape the tests before any markup moves

- [x] 1.1 Extract every user-facing string the tests assert on ("No tenant has a flow graph yet.", "defines no flows", "Nothing was saved.", "Saved, but worth a look:", the saved-file message) into named constants in `internal/ui/server`, referenced by both the view models and `handlers_flows_test.go`. **As built:** `messages.go`. The two Go-owned messages are wired through the handlers now; the four template-owned ones are named there and consumed by the tests, and the templates adopt them in group 3 — referencing a Go constant from a template means editing the template, which task 1.7 forbids this commit.
- [x] 1.2 Extract each flows handler's view-model construction into `s.flowsVM(ctx, r) (ConfigFlowsData, error)` so a test can assert on data rather than markup.
- [x] 1.3 Replace the six structural assertions (`id="drawflow"`, `id="flow-palette"`, `id="flow-inspector"`, `id="flow-inspector-panel"`, `id="tenant-input"`, `wireTenantPicker`, `const TENANTS`) with view-model assertions, keeping exactly one render smoke test per page. **As built:** five dropped; `id="drawflow"` kept as the page anchor; `id="tenant-input"` kept deliberately with a comment saying why — the property is that the tenant picker sits outside the has-a-flow branch, and the picker is a JS-driven input with no form field, so no view-model assertion can prove it rendered. The bug it guards is a page you cannot steer out of.
- [x] 1.4 Keep and strengthen the wire-contract assertions (`name="tenant"`, `name="flow"`, `name="file"`) — they are the form contract, not markup.
- [x] 1.5 Add `newTestServer(t, clients...)` that builds the real `http.ServeMux` via `NewServer`, and drive the flows tests through `httptest` requests rather than direct method calls.
- [x] 1.6 Wrap the remaining `strings.Contains` checks in named helpers with failure messages that say what was expected and why. Add no HTML-parsing dependency.
- [x] 1.7 Confirm `go test ./internal/ui/...` is green and `git diff --stat` shows zero lines changed under `templates/`.

## 2. Shared JSON document primitives

- [x] 2.1 Create `internal/ui/server/jsondoc.go` and move `span`, `edit`, `objectKeys`, `objectSpans`, `skipToValue`, `encodeKey`, `indentBefore`, `applyEdits`, `detectIndent`, `renderValue`, `compact` and `quote` out of `flowdoc.go` unchanged.
- [x] 2.2 Carry over the reasoning: comment the file with why a value that is never decoded is a value that cannot be reformatted, and why byte spans beat an ordered map.
- [x] 2.3 Add `removeKey`, handling all four separator cases in one place — not-first swallows the preceding comma, first-with-successor runs to the next key's indent, only-key leaves `{}`. **Also added** `objectBraces`: both structural edits originally assumed the closing brace was a span's last byte, which is true for a span from `objectSpans` and false for a whole-document span carrying the file's trailing newline. Caught by the only-key test; pinned by `TestStructuralEditsFindTheBraceRatherThanAssumingTheLastByte`.
- [x] 2.4 Add `insertKey`, which **appends after the last key and never prepends**, with a comment explaining that a comment key precedes the key it describes, so prepending can separate them.
- [x] 2.5 Add a key-token guard used by rename: assert the bytes at a key's recorded start really are that key and return an error rather than splicing blindly. **As built:** shipped as `renameKey`, which carries the guard, rather than as a bare guard both editors would have to remember to call.
- [x] 2.6 Add `jsondoc_test.go` covering the three new primitives, including insert into an empty object and delete of first/middle/last/only.
- [x] 2.7 Confirm `flowdoc_test.go` compiles and passes **with no modification** — that is this group's proof.

## 3. One layout

- [x] 3.1 Add `internal/ui/server/templates/layout.html` with head, header, sidebar and `<main>`, calling `{{template "content" .}}` and offering `{{block "head_extra" .}}` and `{{block "scripts" .}}`.
- [x] 3.2 Add `LayoutData` (title, active nav item, server, backends, forwarded query) and attach it to view models as a **named field**, not embedded — `Server` already collides in several of them.
- [x] 3.3 Add `LayoutData.Link(path)`, which appends the selected backend and the allowlisted params, and make it the only way a template builds an internal URL.
- [x] 3.4 Replace the 19 standalone parses with per-page sets: `template.New("layout.html").ParseFS(fs, layout, page, partials...)`, plus `pages`/`partials` maps and `renderPage`/`renderPartial`.
- [x] 3.5 Wrap each shared partial file's body in a single `{{define "<name>"}}`, and switch its render wrapper to `ExecuteTemplate` — a body that is only a `define` renders empty under `Execute`.
- [x] 3.6 Delete the seven `{{define "…-content"}}` copies in `dashboard.html` and render the partial definitions instead. Confirm `gap-6 p-6 w-12` now appears once.
- [x] 3.7 Collapse the success / error / problems markup duplicated across four config templates into one shared definition.
- [x] 3.8 Convert `dashboard.html` and `config.html` to `{{define "content"}}` pages over the layout; move the drain modal script into the rtpmanagers page's `scripts` block, keeping `pauseRtpManagersPolling`, `resumeRtpManagersPolling`, the container id and the `htmx.process()` call verbatim.
- [x] 3.9 Add a test asserting the header and sidebar markup is identical between the dashboard and a config page. **As built:** identical *modulo the active marker*, which is the one thing that legitimately varies by page — asserting them byte-equal would assert the opposite of what is wanted. Plus a test that every page marks exactly one entry, which caught a real bug: the config tab is named `globals` and the sidebar key `deployment`, so that page marked nothing active. Fixed with `navForTab`.
- [x] 3.10 Keep the Flows sidebar entry for now. Flows belong to a tenant and the entry leaves in group 9, but dropping it before the tenant workspace exists would make the canvas unreachable by clicking.

## 4. Real routes

- [x] 4.1 Add page routes for `/admin/tenants`, `/admin/deployment`, `/admin/audio` and `/admin/live/{registrations,dialogs,sessions,parked,rtpmanagers}`, each rendering the layout around the same partial definition its poll returns. **As built:** the five monitors also LEFT the dashboard in this commit rather than in group 10. Keeping them on both would have re-created the duplicated-markup problem group 3 just removed. The dashboard keeps Overview and Backends; group 10 adds the drift panel, tenants-at-a-glance and recent calls.
- [x] 4.8 Fix the page parse sets to pull in each embedded partial's own includes. A page embedding `config_tenants` was missing `config-flash`, so the page failed at execute time while the fragment rendered fine — found by the route test.
- [x] 4.2 Keep every existing `/admin/partials/*` and `/admin/config/partials/*` handler, and build both the page and the fragment from one view-model function per view.
- [x] 4.3 Rebuild the sidebar as plain `<a href>` links through `Layout.Link`. Do not use `hx-boost`; cross-page navigation is a browser navigation.
- [x] 4.4 Redirect old `/admin/config?tab=…` URLs to their new equivalents rather than 404ing them — `tabQuery`'s existence is evidence people deep-link them.
- [x] 4.5 Collapse the three duplicated inline client-lookup loops (`server.go:473`, `:523`, `:582`) into `findClientStrict`, keeping their 404-on-unknown behaviour; leave `findClient`'s documented fallback for reads.
- [x] 4.6 Add a table-driven test asserting every route answers 200 on a hard GET, with and without `?server=`.
- [x] 4.7 Add a test that scans every rendered page for internal URLs and fails if any omits the backend parameter.

## 5. Fetch only what the page renders

- [x] 5.1 Split `fetchBackendData` into independent per-resource fetchers, with health always fetched because it gates the rest.
- [x] 5.2 Add a `want` struct to `buildTemplateData` so each page and partial requests only its own slice.
- [x] 5.3 Run the wanted fetchers concurrently within each backend; they are independent GETs and the current sequencing buys nothing.
- [x] 5.4 Add a call-counting test against a fake backend asserting the dialogs poll makes two calls per backend, not seven.

## 6. Specificity in the dialplan

- [x] 6.1 Add `internal/signaling/dialplan/specificity.go` with `(*Pattern).Specificity() []int` returning the per-position cardinality vector.
- [x] 6.2 Add `RankPatterns(raws []string) ([]Rank, error)`, ordering by cardinality vector lexicographically ascending and tie-breaking on the raw string for stability.
- [x] 6.3 Add `Conflicts(raws []string) ([]Conflict, error)` reporting every pair that overlaps with neither dominating.
- [x] 6.4 Refactor `CompileDigitMap` to use `Conflicts` and return `*AmbiguousPatternsError` from the first result, so "ambiguous" has one definition.
- [x] 6.5 Comment why no scalar score is exported, citing the package's own argument that a weighted scalar collapses unlike patterns onto the same number.
- [x] 6.6 Add tests: ranking agrees with `Dominates` for every comparable pair; all conflicts are reported, not just the first; existing digitmap tests pass unmodified.

## 7. The routing document editor

- [x] 7.1 Add `internal/ui/server/routingdoc.go` with `parseRoutingDoc` over the shared primitives, and a header comment explaining that `RoutingTable` has no field for `_comment*` and decodes without `DisallowUnknownFields`, so a struct round-trip deletes the prose silently.
- [x] 7.2 Add readers returning each section in document order — operator, retrieval prefix, extensions, DIDs, groups, symbolic targets — so tables are stable across saves.
- [x] 7.3 Add scalar writers for `operator` and `retrieval_prefix`, inserting at the root tail when absent and deleting the key when set empty.
- [x] 7.4 Add entry writers for `extensions` and `dids` that re-emit the bare string when there is no transform and the object form otherwise, mirroring `Entry.MarshalJSON`.
- [x] 7.5 Add group and symbolic-target writers, plus rename and delete for each section, rename rewriting the key token span alone. **As built:** one generic `setKey`/`RenameKey`/`DeleteKey` over any map section, with thin typed wrappers that only decide how a value renders. Ten near-identical methods would have been ten chances to get separators wrong.
- [x] 7.11 Add `ReferencesToFlow`, which names every place the routing table points at a flow. Needed by group 9's delete guard, and it belongs here where the document is already parsed.
- [x] 7.6 Add `ensureSection`, appending a missing section at the root tail; make every mutator `parse → single edit → applyEdits → return`, re-parsing rather than chaining offsets.
- [x] 7.7 Add `SectionRaw` and `SpliceSection` as the single implementation behind the per-section raw JSON escape hatch.
- [x] 7.8 Add `routingdoc_test.go` against the real `resources/tenants/devtenant.routing.json`: `_comment`, `_comment_extensions` and `_comment_dids` byte-identical after every mutator; key order preserved after a middle edit; a groups edit's line diff entirely inside the groups span; four-space fixture stays four-space.
- [x] 7.9 Add the bare-vs-object round-trip test: a written entry must parse to a `dialplan.Entry` whose re-marshalling is byte-identical to the input's.
- [x] 7.10 Assert each mutator's output loads through `dialplan.ValidateTable` and `CheckFlows`, and confirm `make validate` still passes.

## 8. The routing tab

- [ ] 8.1 Add the tenant workspace shell at `/admin/tenants/{name}` with its Routing / Flows / Test / Calls tab strip as real links.
- [ ] 8.2 Build the tenant list page with counts, flow names, warning badge, clickable rows that are also real links, and client-side search over name, extensions, DIDs and flow names.
- [ ] 8.3 Add `ListTenantsDetailed` to the file manager and the signaling API — counts from parsing each tenant's files, warnings from `ParseTenant` plus `CheckFlows` — and switch `handleConfigTenantList` to marshal the shared type instead of an ad-hoc map.
- [ ] 8.4 Add the extended fields to `api/types/v1/types.go` and a marshal-round-trip test pinning the mirror to what the server actually emits.
- [ ] 8.5 Build the routing tab's section editors: operator and retrieval prefix as fields; extensions, DIDs, groups (expandable, with strategy, members and member timeout) and symbolic targets as tables.
- [ ] 8.6 Render the computed specificity column from `RankPatterns`, sorted by match order, with a tooltip stating the order is partial — genuine for overlapping patterns, cosmetic for disjoint ones.
- [ ] 8.7 Add the debounced `POST /admin/tenants/{name}/routing/check` endpoint running `RankPatterns` and `Conflicts` in-process, returning the conflicting pairs as markup. It reads no file and writes nothing.
- [ ] 8.8 Add the three-state DID badge using `trunk.ParseRoutes` and `TenantForDID` on the content of `routes.json`: routed here, not routed here, or — for a pattern key — not cross-checked, because `CheckRoutes` deliberately abstains there.
- [ ] 8.9 Grey out symbolic targets nobody references, groups with no inbound reference, and flows no entry point reaches.
- [ ] 8.10 Wire the per-section raw JSON escape hatch with a diff shown before save, and render the 422's problems on refusal and warnings on success.
- [ ] 8.11 Keep the deployment page's `--allow-global-config-writes` gating: read-only with the explanation when the flag is off.

## 9. The destination picker, and flow CRUD

- [ ] 9.1 Write the guard test first: delete a flow the routing table references, PUT it, and assert 422 with nothing written. If that direction is not covered, fix it in `filemanager` rather than papering over it in the UI.
- [ ] 9.2 Add `//go:embed static/*` and an `/admin/static/` route, and add `static/target-picker.js` — a `role="combobox"` with filtering, arrow keys, Enter, Escape and blur-commit.
- [ ] 9.3 Add the picker's Go partial, emitting both the field and a hidden `<template id="target-picker-template">` so the markup has one owner even where JS builds the DOM.
- [ ] 9.4 Serve the tenant's destination catalog once per page as `<script type="application/json" id="target-catalog">`, built from the routing document and the flow names the handler already holds.
- [ ] 9.5 Implement the three states: resolved, free (amber, offered only as an explicit "use anyway" row that arrow keys cannot reach), and invalid. Never block a save.
- [ ] 9.6 Make `mount(root)` idempotent and call it on load, on `htmx:afterSwap`, and at the end of the flow inspector's render; have the inspector clone the template rather than hand-write markup.
- [ ] 9.7 Consume the picker in extension rows, DID rows, group members, symbolic targets and the inspector's dial target.
- [ ] 9.8 Add `CreateFlow`, `DeleteFlow`, `RenameFlow` and `DuplicateFlow` to `flowdoc.go` over the shared primitives — rename rewriting the key token alone, duplicate copying the source span verbatim including comments, delete leaving `{"flows":{}}` when it was the last.
- [ ] 9.9 Handle the tenant with no flows file: creation is a PUT of a minimal document, since `GetTenantFile` returns empty rather than an error.
- [ ] 9.10 Add the flows list page with node counts, the entry points that reach each flow, and open / duplicate / rename / delete; move the canvas to `/admin/tenants/{name}/flows/{flow}` and remove its tenant combobox.
- [ ] 9.11 Add `referencesToFlow` and the confirm dialog that names every referencing row before a delete or rename, with an explicit override that still defers to the server's verdict.
- [ ] 9.12 Define drawflow's tags in the canvas page's `head_extra` block, keeping the existing in-page fallback, and add tests for the CRUD operations over the real fixture.

## 10. Test-a-call, call records and audio

- [ ] 10.1 Add the inline test drawer on every extension and DID row, prefilling and running the simulator without leaving the routing tab.
- [ ] 10.2 Handle the simulator's 429 at four concurrent runs as a "try again" rather than presenting it as a routing failure.
- [ ] 10.3 Add `internal/signaling/cdr/reader.go` with `Reader.Tail`, reading backwards in fixed chunks newest-first, skipping unparsable lines silently, and capping bytes scanned with a `truncated` flag.
- [ ] 10.4 Comment why skipping a torn final line is correct and why it is also what makes concurrent reading safe without locking.
- [ ] 10.5 Add reader tests over synthesized JSONL: newest-first ordering, tenant filter, a torn final line, and the scan cap reporting truncation.
- [ ] 10.6 Add `GET /api/v1/cdr?tenant=&call_id=&limit=&before=` with a bounded limit, answering 200 with `source: "unavailable"` and the flag name when `--cdr-path` is unset.
- [ ] 10.7 Add `CallRecord`, `CallHop` and `CallDecision` to `api/types/v1/types.go` with a marshal-round-trip test against `cdr.Record`; add the client method.
- [ ] 10.8 Add the dashboard's recent-calls panel and the tenant workspace's Calls tab, with expandable rows showing the path and each hop's node, exit and duration, and one calm line when recording is off.
- [ ] 10.9 Rework the dashboard into the health strip, the configuration-drift panel with the reload action, tenants at a glance, and recent calls.
- [ ] 10.10 Add format and duration columns to the audio page, flagging anything not 8 kHz mono 16-bit PCM, and leaving format blank rather than accusing a file the player could not probe.
- [ ] 10.11 Update `CLAUDE.md`'s REST API and UI Server sections with the new routes and endpoints.

## 11. Verification

- [ ] 11.1 `go build ./...` and `go test ./...` pass.
- [ ] 11.2 `make validate` passes on `resources/tenants/devtenant.*`, and `make flow-smoke DIALED=700 DIGITS=2` gives the same traversal as before.
- [ ] 11.3 Hard-GET and refresh every route; confirm deep links survive and `?server=` persists with two backends configured.
- [ ] 11.4 Edit one routing section, save, and confirm `git diff resources/tenants/devtenant.routing.json` moves only that section's lines with every `_comment_*` paragraph intact.
- [ ] 11.5 Make a routing write invalid and confirm the problems render against their paths with nothing reaching disk.
- [ ] 11.6 Start signaling without `--allow-global-config-writes` and confirm the deployment page is read-only with the explanation; start without `--cdr-path` and confirm the Calls tab names the flag rather than showing an empty table.
- [ ] 11.7 Open a drain modal and confirm the RTP manager table stops refreshing under it and resumes afterwards.
- [ ] 11.8 Confirm the repository has gained no `package.json` and no `node_modules`.
