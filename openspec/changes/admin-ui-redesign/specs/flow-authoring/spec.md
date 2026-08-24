## MODIFIED Requirements

### Requirement: A tenant's flow renders as a graph

The UI server SHALL render a tenant's flow as a node graph on a pan-and-zoom canvas at `/admin/tenants/{tenant}/flows/{flow}`, within that tenant's workspace. The tenant and the flow SHALL be identified by the URL rather than by pickers inside the page, and the page SHALL render standalone on a hard GET.

#### Scenario: The reference flow renders

- **WHEN** an operator opens `/admin/tenants/devtenant/flows/main-ivr`
- **THEN** the canvas shows one node per node in the flow, including `greeting`, `ring-sales`, `ring-engineering`, `to-operator`, `closing`, and `bye`
- **AND** each wired exit is drawn as a connection from the source node's port to the target node

#### Scenario: The canvas is reachable and shareable by URL

- **WHEN** `/admin/tenants/devtenant/flows/main-ivr` is requested directly
- **THEN** the complete page renders with the canvas on it
- **AND** refreshing the page renders the same flow

#### Scenario: The tenant is not chosen inside the page

- **WHEN** the canvas page is rendered
- **THEN** it offers no tenant selection control
- **AND** the tenant it edits is the one named in the URL

#### Scenario: A tenant with no flows is handled

- **WHEN** the flows view is opened for a tenant with no flows file
- **THEN** it reports that the tenant has no flows and offers to create one
- **AND** does not render an empty canvas as if a flow existed
- **AND** no error is shown

#### Scenario: The graph library is loaded before the editor runs

- **WHEN** the canvas page is served
- **THEN** the graph library is loaded by the page itself rather than fetched into an existing page
- **AND** the editor script cannot run before it has arrived

## ADDED Requirements

### Requirement: A tenant's flows are listed with what reaches them

`/admin/tenants/{tenant}/flows` SHALL list the tenant's flows with each flow's node count and the entry points that reach it, and SHALL offer opening, creating, renaming, duplicating and deleting a flow.

#### Scenario: The list describes each flow

- **WHEN** the flows list is opened for a tenant with flows
- **THEN** each flow is listed with its node count
- **AND** with the entry patterns or DIDs that lead to it

#### Scenario: A flow row opens its canvas

- **WHEN** an operator opens a flow from the list
- **THEN** that flow's canvas is shown at its own URL

### Requirement: Whole flows can be created, renamed, duplicated and deleted

The editor SHALL support creating, renaming, duplicating and deleting a whole flow, writing each through the validated configuration write path and preserving everything in the document the operation did not concern.

#### Scenario: A new flow is created and loads

- **WHEN** an operator creates a flow
- **THEN** the flows file gains a flow of that name with a body the loader accepts
- **AND** every other flow in the file is unchanged

#### Scenario: The first flow creates the file

- **WHEN** an operator creates a flow for a tenant that has no flows file
- **THEN** the flows file is created containing that flow
- **AND** the tenant's routing table is unchanged

#### Scenario: A rename changes only the name

- **WHEN** an operator renames a flow
- **THEN** the flow's nodes, exits, start and any comments inside it are unchanged
- **AND** its position in the document is unchanged

#### Scenario: A duplicate is a faithful copy

- **WHEN** an operator duplicates a flow
- **THEN** the copy carries the same nodes, exits and start
- **AND** the comments inside the source flow are present in the copy

#### Scenario: Deleting the last flow leaves a valid file

- **WHEN** an operator deletes a tenant's only flow
- **THEN** the flows file remains valid and declares no flows

### Requirement: Removing a flow the routing table needs is guarded

Deleting or renaming a flow that the tenant's routing table references SHALL be reported to the operator with the referencing rows named before the write is attempted, and SHALL be refused by the server if attempted anyway.

#### Scenario: The referencing rows are named first

- **WHEN** an operator tries to delete a flow that an entry pattern, a DID or a symbolic target names
- **THEN** the editor names each referencing row before proceeding
- **AND** does not delete it without an explicit confirmation

#### Scenario: The server refuses it regardless

- **WHEN** a flows file is written with a flow removed that the tenant's routing table still references
- **THEN** the write is refused with the problem attached
- **AND** nothing reaches disk

### Requirement: The dial target uses the shared destination picker

Where the node inspector names a destination, it SHALL use the same destination control the routing editor uses, resolving the same namespaces from the same tenant.

#### Scenario: The inspector offers the tenant's own destinations

- **WHEN** an operator edits a dial node's target
- **THEN** the tenant's extensions, ring groups, flows and symbolic targets are offered
- **AND** a destination the tenant does not define is not offered as a selectable option
