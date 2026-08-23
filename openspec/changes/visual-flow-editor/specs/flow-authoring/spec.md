## ADDED Requirements

### Requirement: A tenant's flow renders as a graph

The UI server SHALL serve a Flows tab that renders a selected tenant's selected flow as a node graph on a pan-and-zoom canvas. The tab SHALL offer a tenant picker and a flow picker, and SHALL be reachable at `/admin/config?tab=flows`.

#### Scenario: The reference flow renders

- **WHEN** an operator opens the Flows tab and selects tenant `devtenant` and flow `main-ivr`
- **THEN** the canvas shows one node per node in the flow, including `greeting`, `ring-sales`, `ring-engineering`, `to-operator`, `closing`, and `bye`
- **AND** each wired exit is drawn as a connection from the source node's port to the target node

#### Scenario: The tab is reachable by URL

- **WHEN** `/admin/config?tab=flows` is requested
- **THEN** the Flows tab is the active tab
- **AND** the tab's partial is fetched at `/admin/config/partials/flows`

#### Scenario: A tenant with no flows is handled

- **WHEN** a tenant with no flows file is selected
- **THEN** the tab reports that the tenant has no flows rather than rendering an empty canvas as if it did
- **AND** no error is shown

### Requirement: Every exit is a visible labelled port

Each node SHALL render one output port per exit, labelled with the exit name, plus one input port. Terminal exits SHALL NOT be rendered as ports.

#### Scenario: Exit labels are readable on the node

- **WHEN** a `dial_user` node is rendered
- **THEN** its ports are labelled `no_answer`, `busy`, `rejected`, and `unavailable`
- **AND** an operator can tell which port is which without selecting the node

#### Scenario: An ivr node shows its digit exits alongside its declared ones

- **WHEN** the `greeting` node of `devtenant`'s `main-ivr` is rendered
- **THEN** it shows ports for `timeout`, `invalid`, and `retries_exceeded`
- **AND** it shows ports for the digit exits `1` and `2`

#### Scenario: Terminal exits get no port

- **WHEN** a `dial_user` or `transfer` node is rendered
- **THEN** no port is offered for `answered` or `accepted`
- **AND** no connection can be drawn from either

### Requirement: The palette and inspector are generated from the exported schema

The palette SHALL offer exactly the node types the exported schema reports, and the inspector for a selected node SHALL generate its fields from that type's field descriptors. Neither SHALL contain a hand-written node catalogue.

#### Scenario: Dragging from the palette adds a node

- **WHEN** an operator drags a node type from the palette onto the canvas
- **THEN** a node of that type is created with the ports its schema entry declares
- **AND** it is selectable and connectable

#### Scenario: The inspector matches the selected node's type

- **WHEN** an operator selects an `ivr` node
- **THEN** the inspector shows fields for `prompt`, `timeout_ms`, `max_retries`, `terminator`, and `interruptible`
- **AND** selecting a `hangup` node instead shows a field for `cause`

### Requirement: The start node is marked and can be changed

The flow's start node SHALL be visually distinguished from every other node, and the editor SHALL provide a way to designate a different node as the start.

#### Scenario: The start node is identifiable

- **WHEN** `devtenant`'s `main-ivr` is rendered
- **THEN** `greeting` is marked as the start node
- **AND** no other node carries that mark

#### Scenario: Designating a new start

- **WHEN** an operator designates a different node as the start and saves
- **THEN** the saved flow's `start` names that node
- **AND** exactly one node is marked as the start on the canvas

### Requirement: Node positions persist in the flow file

Node positions SHALL be stored in a per-flow `_layout` key mapping node ID to an `[x, y]` pair. The key SHALL be ignored by the configuration loader, and SHALL never affect how a call is routed.

#### Scenario: A moved node stays where it was put

- **WHEN** an operator moves a node and saves, then reloads the tab
- **THEN** the node is rendered at the position it was moved to

#### Scenario: The loader is indifferent to the layout

- **WHEN** a flows file carrying `_layout` is loaded by the signaling server
- **THEN** it loads without error
- **AND** the flow validates and routes exactly as the same file without `_layout`

#### Scenario: Stale coordinates are dropped

- **WHEN** a flow is saved after a node has been removed
- **THEN** the saved `_layout` carries no entry for the removed node

#### Scenario: An unusable layout entry never breaks the flow

- **WHEN** a flows file carries a `_layout` entry that is malformed or names a node that does not exist
- **THEN** the editor treats that entry as absent and places the node by computed placement
- **AND** the flow still renders and still saves

### Requirement: A flow with no layout is placed by graph depth

When a flow has no `_layout`, or a node is missing from it, the editor SHALL compute a position from the graph: BFS depth from the start node determines the column and the order within a depth determines the row.

#### Scenario: A hand-written flow opens legibly

- **WHEN** a flow that has never been opened in the editor is rendered
- **THEN** the start node is leftmost
- **AND** each node appears in a column matching its BFS depth from the start
- **AND** placement terminates, which the flow's acyclicity guarantees

### Requirement: Saving goes through the validated write path

Saving SHALL post the whole flows file for the tenant — every flow, not only the edited one — through the existing tenant config write path. The signaling server SHALL remain the sole authority on whether a flow is valid.

#### Scenario: A valid save lands

- **WHEN** an operator saves a valid edit
- **THEN** the file is written through `PutTenantFile` for the tenant's `flows` file
- **AND** a success message is shown, with any warnings the validator returned
- **AND** the configuration drift banner re-checks

#### Scenario: An invalid save is refused with its reasons

- **WHEN** an operator deletes the connection on a `dial_user` node's `busy` exit and saves
- **THEN** the server refuses the write and nothing reaches disk
- **AND** the editor shows the returned problems against their paths, in the same form the raw JSON editor uses

#### Scenario: Unedited flows are not disturbed

- **WHEN** a tenant has several flows and an operator edits one of them and saves
- **THEN** the saved file still contains every other flow unchanged

#### Scenario: The browser hints but does not adjudicate

- **WHEN** an exit has no connection
- **THEN** the canvas highlights that port and says the server will refuse the save
- **AND** the save is still attempted, and the server's verdict is what is reported

### Requirement: Round-tripping a flow preserves what it means

Opening a flow and saving it without editing SHALL produce a file that still validates and is semantically equal to the input — the same nodes, the same types, the same entries, the same exits, and the same start. Keys the editor does not understand SHALL be preserved.

#### Scenario: The reference file survives a round trip

- **WHEN** `resources/tenants/devtenant.flows.json` is read through the editor's model and serialized back
- **THEN** the result validates against the tenant's routing table
- **AND** it is semantically equal to the input

#### Scenario: Comment keys are preserved

- **WHEN** a flows file carrying `_comment` at document scope and `_comment_nodes` at flow scope is round-tripped
- **THEN** both keys are present in the output with their original values

#### Scenario: A saved flow still traverses

- **WHEN** a flow saved through the editor is reloaded by the signaling server and simulated with the Test a Call tab
- **THEN** the traversal is the same as before the round trip

### Requirement: The editor adds no build step

The Flows tab SHALL be delivered as Go `html/template` output with its dependencies loaded as script tags, consistent with the existing UI. The repository SHALL NOT gain a bundler, a `package.json`, or a `node_modules` directory.

#### Scenario: The stack is unchanged

- **WHEN** the change is complete
- **THEN** the repository contains no `package.json` and no `node_modules`
- **AND** the graph library is loaded from a pinned CDN URL alongside the existing htmx and Tailwind tags
- **AND** `go build ./...` produces a UI binary that serves the tab with no other artifact

#### Scenario: The raw editor remains available

- **WHEN** the Flows tab cannot load its library
- **THEN** the tenant's flows file is still editable through the existing raw JSON editor
