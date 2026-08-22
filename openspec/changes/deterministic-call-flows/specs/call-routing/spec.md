## MODIFIED Requirements

### Requirement: Direction classification as a trust gradient

The system SHALL classify each call as `internal`, `inbound`, or `outbound`: `internal` when the From is
a registered directory user; `inbound` when the From is a configured trunk peer (per `basic-sip-trunk`);
`outbound` when a directory user dials a destination that is not a directory user. Direction SHALL be
usable as a trust level by `call-resolution`, `call-flows`, and `tool-authorization`.

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

### Requirement: Routing hands off to resolution before flow execution

Once direction and tenant are resolved and the call is admitted, the system SHALL pass the
call to `call-resolution` before evaluating any flow node. Routing SHALL produce exactly one
of two outcomes: a deterministic destination to execute, or a hand-off to flow execution.
Routing SHALL NOT begin executing a flow directly.

#### Scenario: Resolution is consulted first

- **WHEN** a call has been classified, attributed to a tenant, and admitted
- **THEN** deterministic resolution is attempted before any flow node is evaluated

#### Scenario: Hand-off carries the full call context

- **WHEN** resolution declines to claim a call
- **THEN** flow execution receives the same call context (caller, callee, direction,
  tenant) that resolution was given, with no re-derivation

## REMOVED Requirements

### Requirement: Call Context block in the system prompt
**Reason**: There is no system prompt and no model to read one. The `CallContext` was formatted into a `# Call Context` markdown block and prepended to the tenant's `tenant.md`; both the formatting and the prompt files are deleted.
**Migration**: The `CallContext` value itself survives unchanged as the call identity carried through routing, resolution, admission, policy, and flow execution — it is required by "Routing hands off to resolution before flow execution" above. Only its rendering into prose is removed. The per-tenant `tenant.md` files are deleted; the wording a caller hears now lives in `tts` and `ivr` node text, specified in `call-flows`.
