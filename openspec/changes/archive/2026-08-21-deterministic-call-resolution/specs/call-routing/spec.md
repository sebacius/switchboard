## MODIFIED Requirements

### Requirement: Direction classification as a trust gradient

The system SHALL classify each call as `internal`, `inbound`, or `outbound`: `internal` when the From is
a registered directory user; `inbound` when the From is a configured trunk peer (per `basic-sip-trunk`);
`outbound` when a directory user dials a destination that is not a directory user. Direction SHALL be
usable as a trust level by `call-resolution`, `agent-tools`, and `tool-authorization`.

#### Scenario: From a directory user is internal

- **WHEN** the From AOR resolves to a registered directory user
- **THEN** the direction is `internal`

#### Scenario: From a trunk peer is inbound

- **WHEN** the INVITE source is a configured trunk peer
- **THEN** the direction is `inbound`

#### Scenario: Directory user to external is outbound

- **WHEN** a directory user dials a destination that is not a directory user
- **THEN** the direction is `outbound`

#### Scenario: Direction gates deterministic retrieval

- **WHEN** `call-resolution` considers a `*7XX` call-retrieval code
- **THEN** it uses the classified direction to permit retrieval for `internal` callers only

## ADDED Requirements

### Requirement: Routing hands off to resolution before supervision

Once direction and tenant are resolved and the call is admitted, the system SHALL pass the
call to `call-resolution` before starting the supervisor. Routing SHALL produce exactly one
of two outcomes: a deterministic destination to execute, or a hand-off to the supervisor.
Routing SHALL NOT start the supervisor directly.

#### Scenario: Resolution is consulted first

- **WHEN** a call has been classified, attributed to a tenant, and admitted
- **THEN** deterministic resolution is attempted before any supervisor runner is created

#### Scenario: Hand-off carries the full call context

- **WHEN** resolution declines to claim a call
- **THEN** the supervisor receives the same `CallContext` (caller, callee, direction,
  tenant) that resolution was given, with no re-derivation
