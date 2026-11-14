## Why

A tenant's flow graph is today edited as raw JSON in a textarea. The exit contract that makes flows provably terminating — every non-terminal exit wired, no terminal exit declared, no inter-node cycle — is invisible in that view, so the fastest way to learn that `busy` is unwired is to save and read the refusal. An operator reasoning about "where does a caller go when nobody answers" has to hold a thirty-node adjacency map in their head, and the one artifact that would answer it at a glance — the graph — is exactly what the format hides.

A graphical editor makes the graph the thing being edited. It does not make JavaScript the authority on what a valid flow is: Go still decides, through the same validated write path, so the editor can be wrong about a hint without ever being able to write a flow the server would refuse to load.

## What Changes

- **New**: `dialplan.NodeSchema()` exports the node-type catalog — every type, its declared exits, its terminal exits, and a field descriptor per entry-struct field — derived by reflection over the existing entry structs. Adding a node type in Go makes it appear in the editor with no front-end change; the catalog is never hand-written in JavaScript.
- **New**: a "Flows" tab in the UI server's config page rendering a tenant's flow as a drag-and-drop graph (Drawflow, MIT, from unpkg — no npm, no bundler, no build step, consistent with the existing htmx + Tailwind-from-CDN stack).
- **New**: node palette, per-node labelled output ports (one per exit, digit exits included), a marked start node with a way to designate a different one, and a schema-generated inspector form for the selected node.
- **New**: per-flow `_layout` key storing node positions. Safe because only `Node.entry` decodes with `DisallowUnknownFields`; the flow object itself is decoded by plain `json.Unmarshal`, which already carries `_comment` and `_comment_nodes` in the reference file. A flow with no `_layout` is placed by BFS depth from start — column by depth, row by order within the layer — which terminates because the graph is guaranteed acyclic.
- **New**: two UI routes, `GET /admin/config/partials/flows` and `POST /admin/config/flows/save`.
- **New**: a Go serialize path (flows file → editor model → flows file) with a round-trip test over `resources/tenants/devtenant.flows.json`, so "the editor did not silently change my file" is a compile-time-adjacent guarantee rather than a hope.
- **Unchanged**: validation. Saving posts the whole flows file through the existing `client.PutTenantFile`, which the signaling server validates and either refuses with `[]types.ConfigProblem` attached — nothing reaching disk — or accepts with warnings. Unwired exits are hinted in the browser as a courtesy before the round trip; the server remains the authority.

No breaking changes. The existing raw-JSON tenant editor stays exactly as it is.

## Capabilities

### New Capabilities

- `flow-schema`: the machine-readable node-type catalog exported from `internal/signaling/dialplan` — node types, declared exits, terminal exits, and per-type entry field descriptors — so a UI can render a palette and a property form without restating the exit contract.
- `flow-authoring`: the graphical flow editor in the UI server — graph rendering, layout persistence, node and connection editing, and the validated save that routes through the signaling server's config API.

### Modified Capabilities

None. No existing spec's requirements change: routing, admission, resolution and authorization all see the same files, validated by the same code, produced by a different editor.

## Impact

**New files**
- `internal/signaling/dialplan/flowschema.go` — schema export
- `internal/signaling/dialplan/flowschema_test.go` — schema and round-trip tests
- `internal/ui/server/handlers_flows.go` — partial + save handler, flows-file model and serializer
- `internal/ui/server/templates/config_flows.html` — canvas, palette, inspector

**Modified files**
- `internal/ui/server/server.go` — two routes
- `internal/ui/server/templates.go` — render method and template data types
- `internal/ui/server/templates/config.html` — "Flows" sidebar tab
- `internal/ui/server/handlers_config.go` — `"flows"` in the `activeTab` allowlist, `flow` in `tabQuery`'s key allowlist

**Dependencies**: one new `<script>` tag (Drawflow, MIT, unpkg). No `package.json`, no `node_modules`, no build step.

**Not touched**: the signaling server, its config API, `filemanager`, and every validation rule. The editor is a client of a write path that already exists.
