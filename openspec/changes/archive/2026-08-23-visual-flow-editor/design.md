## Context

`<tenant>.flows.json` is a directed acyclic graph edited today as raw JSON in a textarea (`config_tenant_edit.html`). The rules that make a flow safe live in Go and only in Go: `dialplan.nodeExits` fixes the exit names, `terminalExits` names the outcomes that end the flow and may not be declared, `checkExits` requires every non-terminal exit to be wired, and `findCycle` rejects an inter-node cycle at load. None of that is visible while editing, so the shortest path from "is `busy` wired?" to an answer is a save and a refusal.

The write path already does the right thing. `client.PutTenantFile` → `PUT /api/v1/config/tenants/{name}?file=flows` → `filemanager.validateCandidate` parses the candidate, loads the tenant's *other* file, and runs `dialplan.CheckFlows` across both; a refusal returns `[]types.ConfigProblem` and nothing reaches disk. The gap is not validation. It is that the artifact that would make the graph legible — the graph — is the one thing the format hides.

Constraint verified before designing anything: the flows file is decoded with plain `json.Unmarshal` into `FlowSet` at every load site (`routing_config.go:529` and `:569`, `filemanager/validate.go:80`). `DisallowUnknownFields` appears exactly once in the tree, in `Node.decodeEntry`, and applies only to a node's `entry` object. Unknown keys at document, flow, and node scope are therefore ignored by the loader — which the reference file already relies on, carrying `_comment` and `_comment_nodes`.

## Goals / Non-Goals

**Goals:**

- Render a tenant's flow as a graph an operator can read at a glance: which port is `busy`, which is `no_answer`, and where each one leads.
- Keep the node catalog single-sourced in Go. Adding a node type or an entry field must surface in the editor with no front-end change.
- Persist layout without changing what the file means to the loader.
- Route every save through the existing validated write path, so the editor cannot produce a file the server would later refuse to load.
- Round-trip a flow unchanged: open it, save it without editing, and the file still validates and still means the same thing.

**Non-Goals:**

- Replacing the raw-JSON tenant editor. It stays, and remains the way to create a flows file from nothing.
- Creating or deleting whole flows, or creating a tenant, from the canvas. Node-level editing only in this change.
- Any client-side authority over validity. JavaScript may hint; Go decides.
- A build step. No npm, no bundler, no `package.json`, no `node_modules`.
- Editing the routing table graphically. The entry mapping is a digit-map problem, not a graph problem.
- Multi-user conflict detection. Last write wins, exactly as the existing editor behaves today.

## Decisions

### The Go code is the schema; the catalog is derived, not written

`dialplan.NodeSchema() []NodeTypeSchema` returns, per node type: the type name, `DeclaredExits(t)`, `terminalExits[t]`, whether the type accepts digit exits, and a field descriptor list built by reflecting over the entry struct returned by `entryTarget(t)` — json key, kind, and whether the field is a `PromptSpec`.

*Why reflection over a hand-written table:* a hand-written table is a second copy of the contract, and the failure mode is silent. Add `Terminator` to `IVREntry` and a hand-written table still renders the old form; nobody finds out until an operator cannot set a terminator. Reflection makes the entry struct the only place the field exists.

*Why not generate the catalog into JavaScript at all:* it would be a third copy with its own staleness. The handler serializes `NodeSchema()` into the page as JSON at render time, so the browser reads whatever the running binary knows.

*`required` is a hint, not a rule.* Reflection can see struct tags, not `checkEntry`'s logic. The descriptor derives `required` from the absence of `omitempty` — which is how the existing structs already distinguish `json:"text"` from `json:"voice,omitempty"` — and the schema documents it as a form hint. Go's validator stays the authority, and a field the heuristic gets wrong produces a nagging asterisk, never a rejected save or an accepted bad one.

*Terminal exits are carried but never rendered as a port.* `answered` and `accepted` end the flow; the graph cannot express what comes after, and declaring one is a load error. The schema ships them so the inspector can *say* "answered ends the call here" without offering anything to connect.

*Digit exits are data.* `ivr` accepts any of `0-9*#` as an exit, checked by `isDigitExit`, and `checkExits` requires at least one. The schema flags the type as digit-accepting; the editor lets digit exits be added and removed freely while the three declared exits (`timeout`, `invalid`, `retries_exceeded`) are fixed and always present as ports.

### Drawflow from a CDN

A single pinned `<script src="https://unpkg.com/drawflow@0.0.60/dist/drawflow.min.js">` plus its stylesheet, alongside the existing htmx and Tailwind tags.

*Alternatives:* React Flow is the better library and needs a bundler, which is the one thing this stack does not have — rejected on the stated constraint. Hand-rolled SVG with drag maths is a week of work reimplementing connection routing badly. Mermaid renders a graph but cannot edit one.

*Trade-off accepted:* the tab needs network access to unpkg, exactly as the dashboard already does for htmx and Tailwind. An air-gapped deployment loses the tab, not the ability to edit flows — the raw editor is untouched.

### Serialization lives in Go, and splices rather than rebuilds

The editor model and both directions of the conversion are Go code in `handlers_flows.go`. The browser posts the edited flow; Go produces the file text and hands it to `PutTenantFile`.

*Why not serialize in JavaScript:* the required round-trip test is a Go test. More usefully, it puts "did the editor mangle the file" under `go test` instead of under an operator's eyes.

*Splice, do not rebuild.* The document is walked with a token decoder that records the byte span of every value at three levels — document, `flows` map, and the nodes of the flow being edited. Saving then replaces **byte ranges**: the flow's `start` if it moved, its `_layout`, and the individual nodes whose content actually changed, compared semantically so a difference in spacing or key order is not mistaken for an edit. Everything else is copied, not re-encoded.

Consequences worth having: editing `main-ivr` cannot touch `after-hours` even in principle; `_comment` and `_comment_nodes` survive because they are never parsed; a node nobody touched keeps its inline entry inline and the blank line above it where its author put it; and the diff of a layout-only save is the `_layout` key and nothing else. Adding or removing a node is the one case that re-renders the node map whole — that is a structural edit the operator made on purpose, and the cases worth protecting from reformatting are the ones they did not think of as edits at all.

*Why byte spans rather than an ordered `map[string]json.RawMessage`:* re-encoding even an ordered structure normalizes whitespace, and Go marshals plain maps in sorted key order, either of which would rewrite a checked-in file the first time anyone opened the tab. A value that is never decoded is a value that cannot be reformatted. The same reasoning already lives in `normalizeContent`'s comment about CRLF.

### `_layout` at flow scope

```json
"main-ivr": { "start": "greeting", "_layout": { "greeting": [120, 40] }, "nodes": { ... } }
```

*Why this is safe:* verified above — the loader ignores unknown keys at flow scope, and the reference file already does this with `_comment`. `FlowDef` gains no field; the loader never learns `_layout` exists.

*Why not a sidecar file:* a second file that has to be moved, copied, and reviewed alongside the first, with nothing keeping them in step. A layout that travels with the flow it describes cannot desynchronize from it.

*Stale entries are pruned on save.* A node removed in the editor leaves its coordinates behind otherwise, and a later reader would wonder which node `old-greeting` was.

*No layout, or a node missing from it:* placed by BFS depth from `start` — depth is the column, order within the depth is the row. Terminating because the graph is acyclic, which the loader has already proved by the time the editor sees the file, and it produces the reading order an operator expects: the call flows left to right.

### Save posts the whole file, through the path that already exists

The POST carries the tenant, the flow name, the original file content, and the edited flow's graph. Go splices and calls `client.PutTenantFile(ctx, tenant, "flows", spliced)`, then renders success, warnings, or `[]types.ConfigProblem` with the same markup `config_tenant_edit.html` uses. `configSaved(w)` fires so the drift banner re-checks.

*Why post the original content back rather than re-reading from the server:* the editor validated its hints against the document it was given. Re-reading would splice into a file that may have changed underneath, silently discarding the other edit. Sending back what was opened makes last-write-wins explicit rather than accidental.

*Unwired exits are hinted, never enforced.* The canvas highlights a port with no connection and says the server will refuse it. If the hint and Go ever disagree, Go wins and the operator sees the refusal — a wrong hint costs a round trip, and a hint with authority would cost a flow that cannot be saved.

*Form field, not JSON body.* Matching the existing handlers' `r.ParseForm` + htmx pattern. The graph goes in a hidden input as single-line `JSON.stringify` output, which sidesteps the CRLF rewriting `normalizeContent` exists to undo.

## Risks / Trade-offs

- **Reflection's `required` heuristic drifts from `checkEntry`.** → It is a form hint and is documented as one at its definition. The worst case is a misplaced asterisk; the save is adjudicated by the validator either way.
- **Drawflow is a small, lightly maintained dependency loaded from a CDN.** → Pinned to an exact version, confined to one tab, and the raw JSON editor remains a complete alternative for every flow the canvas can edit.
- **A first save through the editor rewrites the edited flow's formatting** (indentation normalized, `_layout` added) even if nothing else changed. → Bounded to the one flow touched; other flows and every non-flow key keep their exact bytes. The round-trip test asserts semantic equality, which is the property that matters.
- **Two operators editing the same tenant overwrite each other.** → Unchanged from today's editor, and now visible: the original content travels with the form, so the behavior is a stated decision rather than an emergent one.
- **A large graph is slow or unreadable on one canvas.** → Layered placement keeps the default legible; pan and zoom come from the library. Not optimized further until a tenant has a flow big enough to complain about.
- **`_layout` is invisible to the loader, so a hand-edited file with a broken `_layout`** (a non-numeric coordinate, a node that no longer exists) still loads and calls still route. → The editor treats an unusable entry as absent and falls back to computed placement, because a layout is a convenience and must never be able to break a flow.

## Migration Plan

Additive. No file on disk changes until someone saves through the new tab, and the first such save adds one key the loader ignores. Rolling back is deleting the tab: every flows file written by the editor still loads on a binary that has never heard of `_layout`.

## Open Questions

None blocking. Two deferred by choice: creating a new flow from the canvas, and graphical editing of the routing table's entry mapping.
