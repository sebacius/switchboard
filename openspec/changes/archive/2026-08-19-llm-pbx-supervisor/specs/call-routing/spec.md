## ADDED Requirements

### Requirement: Direction classification as a trust gradient

The system SHALL classify each call as `internal`, `inbound`, or `outbound`: `internal` when the From is
a registered directory user; `inbound` when the From is a configured trunk peer (per `basic-sip-trunk`);
`outbound` when a directory user dials a destination that is not a directory user. Direction SHALL be
usable as a trust level by `agent-tools` and `tool-authorization`.

#### Scenario: From a directory user is internal

- **WHEN** the From AOR resolves to a registered directory user
- **THEN** the direction is `internal`

#### Scenario: From a trunk peer is inbound

- **WHEN** the INVITE source is a configured trunk peer
- **THEN** the direction is `inbound`

#### Scenario: Directory user to external is outbound

- **WHEN** a directory user dials a destination that is not a directory user
- **THEN** the direction is `outbound`

### Requirement: Tenant resolution without a default

For `internal`/`outbound` calls the tenant SHALL be derived from the leftmost label of the From-URI host
(e.g. `sip:102@acme.switchboard.com` → `acme`). For `inbound` calls the tenant SHALL be the DID→tenant
result from `basic-sip-trunk`. If no tenant resolves, or the resolved tenant is not loaded, the call
SHALL be rejected. There SHALL be no default tenant.

#### Scenario: Subdomain selects the tenant

- **WHEN** the From-URI host is `acme.switchboard.com` and tenant `acme` is loaded
- **THEN** tenant `acme` is selected

#### Scenario: Unresolved tenant is rejected

- **WHEN** no tenant can be resolved for the call, or the resolved tenant is not loaded
- **THEN** the call is rejected (handled by `call-admission`); no fallback tenant is used

### Requirement: Call Context block in the system prompt

The system SHALL build a `CallContext{Caller, Callee, Direction, Tenant}` and prepend a formatted
`# Call Context` block to the tenant system prompt. One universal `tenant.md` per tenant SHALL serve all
directions.

#### Scenario: Context block is prepended

- **WHEN** the runner builds the system prompt for a call
- **THEN** the prompt begins with a Call Context block containing caller, callee, direction, and tenant
