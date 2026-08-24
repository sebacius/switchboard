## ADDED Requirements

### Requirement: A tenant's configuration lives under one URL

The UI server SHALL serve a workspace at `/admin/tenants/{name}` gathering everything about one tenant — its routing table, its flow graphs, call simulation against it, and its call records — as sub-routes of that page.

#### Scenario: The workspace sub-routes render

- **WHEN** `/admin/tenants/devtenant`, `/admin/tenants/devtenant/flows`, `/admin/tenants/devtenant/test` or `/admin/tenants/devtenant/calls` is requested directly
- **THEN** each renders a complete page with the tenant's tab strip
- **AND** the tab matching the URL is marked active

#### Scenario: Routing is the default view

- **WHEN** `/admin/tenants/devtenant` is requested
- **THEN** the routing table is shown

#### Scenario: The tenant is taken from the URL, not a picker

- **WHEN** any workspace page is rendered
- **THEN** the tenant it edits comes from the path
- **AND** no tenant selection control appears inside the page

#### Scenario: An unknown tenant is reported

- **WHEN** a workspace URL names a tenant with no configuration on disk
- **THEN** the page says so
- **AND** does not render empty tables as though the tenant existed

### Requirement: The tenant list is searchable by what a tenant contains

`/admin/tenants` SHALL list every tenant with its extension, DID and group counts, its flow names, and whether its configuration currently has validation warnings. Search SHALL match a tenant's name and its contents, and SHALL be served from a single listing request rather than by fetching each tenant's files.

#### Scenario: Searching by an extension finds its tenant

- **WHEN** an operator types an extension number that a tenant defines
- **THEN** that tenant's row remains visible

#### Scenario: Searching by a DID finds its tenant

- **WHEN** an operator types a DID a tenant handles
- **THEN** that tenant's row remains visible

#### Scenario: Searching by a flow name finds its tenant

- **WHEN** an operator types the name of a flow a tenant defines
- **THEN** that tenant's row remains visible

#### Scenario: The list costs one request

- **WHEN** `/admin/tenants` is rendered
- **THEN** the counts, flow names and warning state come from one listing call
- **AND** no per-tenant file read happens on page load

#### Scenario: A tenant with warnings is marked

- **WHEN** a tenant's configuration produces validation warnings
- **THEN** its row carries a warning badge

### Requirement: A tenant row opens its workspace

Each row SHALL navigate to that tenant's workspace when clicked, while remaining copyable and openable in a new tab, and its per-row actions SHALL NOT trigger that navigation.

#### Scenario: Clicking the row navigates

- **WHEN** an operator clicks anywhere on a tenant row that is not an action control
- **THEN** that tenant's workspace opens

#### Scenario: The name is a real link

- **WHEN** an operator middle-clicks or copies the link on a tenant's name
- **THEN** it yields that tenant's workspace URL

#### Scenario: A row action does not navigate

- **WHEN** an operator activates the delete control on a row
- **THEN** the workspace does not open
- **AND** only the delete is offered

### Requirement: The listing reports what it counts

The tenant listing response SHALL carry, for each tenant, the number of entry patterns, DIDs and ring groups it defines, the names of its flows, whether it has a flows file, and whether its configuration currently produces warnings. These SHALL be shared API types rather than an ad-hoc response shape.

#### Scenario: A routing-only tenant is described

- **WHEN** a tenant has a routing table and no flows file
- **THEN** its entry reports no flows and an empty flow name list
- **AND** its extension, DID and group counts are still reported

#### Scenario: The detailed listing is a distinct operation

- **WHEN** a caller needs only tenant names
- **THEN** it can obtain them without paying to parse and validate every tenant's files
