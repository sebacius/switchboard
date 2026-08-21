## ADDED Requirements

### Requirement: DID-to-tenant table

The system SHALL load a DID→tenant mapping from `resources/config/routes.json` and resolve the tenant
for an inbound trunk call by matching the dialed DID (Request-URI or To user part) against the table.

#### Scenario: Mapped DID resolves to its tenant

- **WHEN** an inbound trunk INVITE arrives for a DID present in the table
- **THEN** the mapped tenant is resolved for the call

#### Scenario: Table is loaded at startup

- **WHEN** the signaling server starts
- **THEN** the DID→tenant table is loaded from `routes.json` and available to inbound routing

### Requirement: Unmapped DID is rejected (no default tenant)

An inbound DID with no entry in the table SHALL be rejected. There SHALL be no default tenant fallback.

#### Scenario: Unmapped DID is rejected

- **WHEN** an inbound trunk INVITE arrives for a DID not present in the table
- **THEN** the call is rejected and no tenant is assigned

#### Scenario: No default routing occurs

- **WHEN** the table contains no catch-all or default entry
- **THEN** only explicitly mapped DIDs are accepted; all others are rejected
