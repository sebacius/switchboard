## 1. Schema export from Go

- [x] 1.1 Add `internal/signaling/dialplan/flowschema.go` with `NodeTypeSchema` and `FieldSchema` types: type name, declared exits, terminal exits, accepts-digit-exits flag, and a field descriptor list (json key, kind, required hint, prompt flag, nested fields).
- [x] 1.2 Implement `NodeSchema() []NodeTypeSchema` iterating `NodeTypes()` in sorted order, filling exits from `DeclaredExits` and `terminalExits`, and marking `ivr` as digit-accepting via the same rule `isDigitExit` encodes.
- [x] 1.3 Implement field-descriptor derivation by reflecting over the struct `entryTarget(t)` returns: json key from the tag, kind from the Go type (string, int, bool, string slice), `required` from the absence of `omitempty`, and a nested descriptor list when the field is a `PromptSpec`.
- [x] 1.4 Comment why reflection rather than a hand-written table (a second copy of the contract drifts silently) and why `required` is a form hint whose authority is `checkEntry`.
- [x] 1.5 Add `flowschema_test.go`: every type present exactly once and matching `NodeTypes()`; declared exits equal `DeclaredExits`; terminal exits satisfy `IsTerminalExit` and appear in no declared list; only `ivr` accepts digits; `prompt` is marked as a `PromptSpec` with its four nested fields; the `omitempty` heuristic holds for `tts.text` vs `tts.voice`.

## 2. Lossless flows-file model and serializer

- [x] 2.1 Add `internal/ui/server/handlers_flows.go` with an ordered key/raw-value structure that decodes a flows document while recording key order, keeping every value not being rewritten as `json.RawMessage`.
- [x] 2.2 Implement decoding one flow into an editor model: start, timeout, nodes (id, type, entry, exits), `_layout`, and the flow's unrecognized keys carried through raw.
- [x] 2.3 Implement the splice: re-emit only the edited flow object, at 2-space indent in recorded key order, leaving every other flow and every document-level key byte-identical.
- [x] 2.4 Prune `_layout` entries for nodes that no longer exist, and treat a malformed or unknown-node entry as absent on read.
- [x] 2.5 Implement BFS-depth placement for nodes with no usable layout: depth from `start` is the column, order within the depth is the row.
- [x] 2.6 Add the round-trip test over `resources/tenants/devtenant.flows.json`: decode → serialize with no edits, assert the output still validates via `dialplan.CheckFlows` against `devtenant.routing.json` and is semantically equal to the input (same start, nodes, types, entries, exits).
- [x] 2.7 Add a test asserting `_comment` at document scope and `_comment_nodes` at flow scope survive a round trip, and that an unedited sibling flow keeps its exact bytes.
- [x] 2.8 Add a test asserting a file carrying `_layout` still validates, and that layout changes alone do not change what `CheckFlows` sees.

## 3. UI server wiring

- [x] 3.1 Add `ConfigFlowsData` to `internal/ui/server/templates.go` (server, tenant list, flow list, selected flow, serialized graph, serialized schema, original file content, success/error/problems/warnings) and parse `config_flows.html` in `NewTemplates`.
- [x] 3.2 Add `RenderConfigFlows` alongside the existing render methods.
- [x] 3.3 Add `handleConfigFlowsPartial` for `GET /admin/config/partials/flows?server=&tenant=&flow=`: list tenants via `ListTenants` filtered on `HasFlows`, read the flows file via `GetTenantFile`, build the editor model, and serialize both the graph and `dialplan.NodeSchema()` into the page.
- [x] 3.4 Add `handleConfigFlowsSave` for `POST /admin/config/flows/save`: parse the form, splice the posted graph into the posted original content, call `PutTenantFile`, and render success with warnings or the refusal's problems using `applyWriteError`'s pattern; call `configSaved(w)` on success.
- [x] 3.5 Register both routes in `internal/ui/server/server.go` next to the existing config routes.
- [x] 3.6 Add `"flows"` to the `activeTab` allowlist in `handleConfigPage` and `"flow"` to `tabQuery`'s key allowlist.
- [x] 3.7 Add the "Flows" entry to the sidebar in `templates/config.html`, matching the existing tab markup.

## 4. The canvas

- [x] 4.1 Add `internal/ui/server/templates/config_flows.html` with the pinned Drawflow script and stylesheet tags, tenant and flow pickers matching the Test a Call tab's picker markup, and the canvas, palette, and inspector regions.
- [x] 4.2 Build the palette from the serialized schema — no hand-written node list — with drag-to-canvas creating a node carrying that type's ports.
- [x] 4.3 Render each node with one input port and one labelled output port per exit, omitting terminal exits entirely.
- [x] 4.4 Render `ivr` digit exits as ports alongside the declared ones, with controls to add and remove a digit exit while leaving the declared three fixed and always present.
- [x] 4.5 Mark the start node distinctly and add the control that designates a different node as start, keeping exactly one marked.
- [x] 4.6 Generate the inspector's fields for the selected node from its schema entry, including the nested prompt fields, and write edits back into the in-page graph model.
- [x] 4.7 Highlight any exit port with no connection and state that the server will refuse the save — as a hint, with the save still attempted.
- [x] 4.8 On save, serialize the graph to a single-line hidden input and post the form with htmx, targeting the config content region.
- [x] 4.9 Render the empty state for a tenant with no flows, pointing at the raw JSON editor rather than showing a blank canvas.

## 5. Verification

- [x] 5.1 `go build ./...` and `go test ./...` pass.
- [x] 5.2 Run `make run`, open `/admin/config?tab=flows`, and confirm `devtenant`'s `main-ivr` renders as a graph with labelled ports and a marked start node.
- [x] 5.3 Move a node, save, reload the tab, and confirm the position survived and the rest of the file did not change (`git diff`).
- [x] 5.4 Delete the `busy` connection on a `dial_user` node, save, and confirm the server refuses it with a readable problem against its path and nothing reaches disk.
- [x] 5.5 Reload the configuration, then run `/admin/config?tab=flowtest&tenant=devtenant&dialed=100&digits=2` and confirm the traversal is unchanged from before the round trip.
- [x] 5.6 Confirm `make validate` still passes and the repository has gained no `package.json` and no `node_modules`.
