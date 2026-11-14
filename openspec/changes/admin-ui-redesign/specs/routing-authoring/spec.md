## ADDED Requirements

### Requirement: The routing table is edited section by section

The routing tab SHALL present each shape in the routing table as its own editor — the operator and retrieval prefix as fields, and the entry mapping, DIDs, ring groups and symbolic targets as tables — rather than as one JSON textarea.

#### Scenario: Each shape has its own section

- **WHEN** the routing tab is opened for `devtenant`
- **THEN** operator and retrieval prefix appear as fields
- **AND** extensions, DIDs, groups and symbolic targets each appear as a table

#### Scenario: A ring group's members are editable

- **WHEN** an operator expands a ring group row
- **THEN** its strategy, its members and its per-member timeout are editable
- **AND** the strategy offers only the strategies the loader accepts

#### Scenario: Saving one section leaves the others alone

- **WHEN** an operator edits a ring group and saves
- **THEN** only the ring group section of the file changes on disk

### Requirement: Every section keeps a raw JSON escape hatch

Each section editor SHALL offer editing that section as raw JSON, and SHALL show what will change before the write is made.

#### Scenario: A section can be edited as text

- **WHEN** an operator chooses to edit a section as JSON
- **THEN** that section's current text is presented for editing
- **AND** saving it goes through the same validated write path as the structured editor

#### Scenario: A change is shown before it is saved

- **WHEN** an operator has edited a section and asks to save
- **THEN** the difference between what is on disk and what will be written is shown first

### Requirement: The entry table shows the order the router will use

Entry patterns SHALL be presented in computed match order with their specificity shown, and the ordering SHALL be obtained from the routing engine rather than recomputed in the browser.

#### Scenario: A narrower pattern sorts above a wider one

- **WHEN** the entry table contains both a literal and a pattern that also matches it
- **THEN** the literal is shown first
- **AND** its specificity is shown alongside it

#### Scenario: The ordering is not declared anywhere

- **WHEN** the entry table is rendered
- **THEN** no priority number appears in the file or in the table
- **AND** the order shown is derived from the patterns themselves

#### Scenario: The column does not overstate what it knows

- **WHEN** two patterns cannot match the same input
- **THEN** the table does not claim one takes precedence over the other

### Requirement: Overlapping patterns with no winner are reported while typing

When two entry patterns can match the same input and neither is more specific, the editor SHALL name both of them as the operator types, without waiting for a save.

#### Scenario: A conflict appears before saving

- **WHEN** an operator types a pattern that overlaps an existing one with neither more specific
- **THEN** both patterns are named as being in conflict
- **AND** the conflict is shown without the operator saving

#### Scenario: The conflict clears when it is resolved

- **WHEN** the operator narrows one of the two patterns
- **THEN** the conflict is no longer reported

#### Scenario: Every conflicting pair is reported

- **WHEN** a table contains more than one conflicting pair
- **THEN** all of them are named rather than only the first

### Requirement: Each DID says whether it is routed to this tenant

Every DID row SHALL report whether the deployment's DID-to-tenant table routes that number to this tenant, and SHALL abstain where the deployment table cannot answer.

#### Scenario: A routed DID is marked as routed

- **WHEN** a DID this tenant handles is routed to this tenant by the deployment table
- **THEN** its row is marked as routed here

#### Scenario: An unrouted DID is marked as unreachable

- **WHEN** a DID this tenant handles is not routed to this tenant
- **THEN** its row says so, because no call will ever arrive on it

#### Scenario: A pattern DID is not given a verdict

- **WHEN** a DID row's key is a pattern rather than a literal number
- **THEN** its row reports that it was not cross-checked
- **AND** does not claim it is either routed or unrouted

### Requirement: What nothing references is visibly unused

Symbolic targets nobody references, ring groups with no inbound reference, and flows no entry point reaches SHALL be visually distinguished from those in use.

#### Scenario: An unreferenced symbolic target is greyed

- **WHEN** a symbolic target is named by no entry, DID, group member or flow
- **THEN** its row is shown as unused

#### Scenario: A referenced group is not greyed

- **WHEN** an entry pattern resolves to a ring group
- **THEN** that group is not shown as unused

### Requirement: One destination picker is used everywhere a destination goes

A single control SHALL be used for every field that names a destination — entry rows, DID rows, ring group members, symbolic targets and the flow editor's dial target — resolving the user, group, flow and symbolic namespaces from this tenant's own configuration.

#### Scenario: The picker offers what the tenant defines

- **WHEN** an operator types part of a ring group's name into a destination field
- **THEN** that group is offered as a `group/` destination

#### Scenario: The picker offers this tenant's flows

- **WHEN** an operator opens a destination field on a tenant with flows
- **THEN** those flows are offered as `flow/` destinations

#### Scenario: The same control appears in the flow editor

- **WHEN** an operator edits a dial node's target on the flow canvas
- **THEN** the same destination control is used, offering the same namespaces

#### Scenario: A destination that does not exist is not offered

- **WHEN** an operator types a flow name the tenant does not define
- **THEN** it is not offered as a selectable option

#### Scenario: A destination that does not exist can still be written deliberately

- **WHEN** an operator explicitly chooses to use a value that resolves to nothing
- **THEN** the value is accepted into the field
- **AND** the field is marked as unresolved so the operator can see it

#### Scenario: The picker never blocks the save

- **WHEN** a field holds an unresolved destination and the operator saves
- **THEN** the save is attempted
- **AND** the server's verdict is what is reported

### Requirement: A row can be tested without leaving the table

Every entry and DID row SHALL offer running the call simulator against that row's dialed value, showing the traversal in place.

#### Scenario: Testing an extension row shows its traversal

- **WHEN** an operator runs the test on an entry row
- **THEN** the simulated traversal for that dialed value is shown alongside the table
- **AND** the operator remains on the routing tab

#### Scenario: The simulator's concurrency limit is handled

- **WHEN** the simulator refuses a run because too many are in flight
- **THEN** the drawer says so and offers to retry
- **AND** does not present the refusal as a routing failure

### Requirement: The server remains the authority on validity

Client-side checks SHALL be advisory. Every save SHALL go through the configuration write path, and a refusal SHALL be reported with the problems the server attached.

#### Scenario: A refused save reports its reasons

- **WHEN** a routing edit is refused by the server
- **THEN** the problems it returned are shown
- **AND** nothing has been written to disk

#### Scenario: Warnings do not block a save

- **WHEN** a routing edit is accepted with warnings
- **THEN** the save succeeds
- **AND** the warnings are shown with it
