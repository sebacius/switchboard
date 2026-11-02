## ADDED Requirements

### Requirement: Completed call records can be read back

The call record file SHALL be readable most-recent-first, filtered by tenant and by call, without loading the whole file.

#### Scenario: The most recent records come back first

- **WHEN** the last records are requested
- **THEN** they are returned newest first
- **AND** the whole file was not read to produce them

#### Scenario: Records can be filtered to one tenant

- **WHEN** records are requested for a tenant
- **THEN** only that tenant's records are returned

#### Scenario: An incomplete final line does not break the read

- **WHEN** the file ends with a partially written record
- **THEN** the complete records before it are still returned
- **AND** no error is reported

#### Scenario: Reading is safe while calls are being recorded

- **WHEN** records are read while the server is appending to the same file
- **THEN** the read succeeds and returns complete records only

#### Scenario: A search that matches little is bounded

- **WHEN** a filter matches nothing within the amount of the file the reader will scan
- **THEN** the reader stops and reports that its results are incomplete
- **AND** does not scan the entire file

### Requirement: The API reports call records and says when there are none to report

The signaling server SHALL expose call records over its REST API, with a bounded limit, and SHALL distinguish "recording is switched off" from "there are no records" and from "the file could not be read".

#### Scenario: Records are returned

- **WHEN** call records are requested and recording is enabled
- **THEN** the response carries the records and whether the result was truncated

#### Scenario: Recording being off is stated, not implied

- **WHEN** call records are requested on a server started without a call record path
- **THEN** the response says the source is unavailable and names the flag that enables it
- **AND** does not report an error status

#### Scenario: The limit is bounded

- **WHEN** a caller requests more records than the endpoint allows
- **THEN** the endpoint returns at most its maximum

#### Scenario: The record type is shared

- **WHEN** a record crosses the API
- **THEN** it uses the shared API types
- **AND** a record written by the server decodes into that type without loss

### Requirement: Calls are visible beside the configuration that routed them

The UI SHALL show recent calls on the dashboard and a tenant's calls inside that tenant's workspace, and SHALL show the traversal a call took.

#### Scenario: The dashboard shows recent calls

- **WHEN** the dashboard is rendered and recording is enabled
- **THEN** the most recent calls across all tenants are listed

#### Scenario: A tenant's calls are in its workspace

- **WHEN** `/admin/tenants/{name}/calls` is opened
- **THEN** only that tenant's calls are listed

#### Scenario: A call shows where it went and why

- **WHEN** an operator expands a call record
- **THEN** the path it took through the flow is shown
- **AND** each hop's node, exit and duration is shown

#### Scenario: Recording being off is explained rather than shown as emptiness

- **WHEN** the calls view is opened on a server with recording disabled
- **THEN** it says recording is disabled and names the flag
- **AND** does not render an empty table as though no calls had been made
