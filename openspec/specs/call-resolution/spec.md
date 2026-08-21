# call-resolution Specification

## Purpose
TBD - created by archiving change deterministic-call-resolution. Update Purpose after archive.
## Requirements
### Requirement: Deterministic resolution precedes the supervisor

The system SHALL attempt deterministic resolution of every admitted call before engaging
the LLM supervisor. When the dialed destination resolves unambiguously, the system SHALL
execute that resolution and SHALL NOT call the model. The supervisor SHALL be entered
only when deterministic resolution yields no destination, or when the resolved
destination is the assistant itself.

Resolution SHALL run after `call-routing` has produced direction and tenant and after
`call-admission` has admitted the call, and before the supervisor runner is started.

#### Scenario: Extension-to-extension call never reaches the model

- **WHEN** a registered directory user dials another registered directory extension
- **THEN** the system forwards the INVITE to that extension without answering, and no
  LLM request is made for the call

#### Scenario: Unresolvable destination enters the supervisor

- **WHEN** deterministic resolution produces no destination for the dialed target
- **THEN** the supervisor runner handles the call exactly as it does today

#### Scenario: A DID mapped to the assistant enters the supervisor

- **WHEN** an inbound DID resolves to the AI receptionist rather than to a person, queue,
  or extension
- **THEN** resolution hands off to the supervisor, which answers and converses

### Requirement: What counts as unambiguous

A destination SHALL be treated as unambiguously resolved only when it is one of:

- a **registered directory extension** for the call's tenant;
- a **`*7XX` call-retrieval code** naming an occupied parking slot, dialed by an
  `internal` caller;
- an **inbound DID** with a mapping in the tenant's routing table to an extension or a
  ring group;
- a **named ring group** for the call's tenant.

Anything else — an unmapped target, an unregistered extension, a `*7XX` code for an empty
slot, an intent expressed in speech — SHALL NOT be treated as resolved.

#### Scenario: Unregistered extension is not resolved

- **WHEN** the dialed extension exists in the tenant's routing table but has no active
  registration
- **THEN** resolution does not claim the call and the supervisor handles it

#### Scenario: Retrieval code for an empty slot is not resolved

- **WHEN** an internal caller dials `*701` and slot 701 holds no parked call
- **THEN** resolution does not claim the call and the supervisor handles it, so the
  caller is told the slot is empty rather than hearing silence

#### Scenario: Retrieval code from a non-internal caller is not resolved

- **WHEN** an `inbound` caller dials a `*7XX` code
- **THEN** resolution does not claim the call, preserving the existing rule that call
  retrieval is an internal-only capability

### Requirement: Resolution stays inside the authorization boundary

Deterministic resolution SHALL NOT bypass `tool-authorization`. Every destination the
resolver dials SHALL be adjudicated by the same per-tenant policy that adjudicates a
model-issued dial, and a denied destination SHALL NOT be dialed. Resolution SHALL NOT
grant reach that the tenant's Class of Service does not already permit.

#### Scenario: Resolved external destination is still adjudicated

- **WHEN** resolution produces an external destination for a tenant whose policy denies
  external dialing
- **THEN** the policy denies it, no INVITE leaves the system, and the deny is logged with
  the same decision logging as a model-issued dial

#### Scenario: Resolution cannot widen reach

- **WHEN** a routing table entry names a destination outside the tenant's allowlist
- **THEN** the dial is denied by policy, and the resolver does not treat its own table as
  authorization

### Requirement: Ring group resolution

The system SHALL resolve a named ring group to its member extensions and ring them
according to the group's configured strategy. Supported strategies SHALL be `sequential`
(members tried in configured order) and `round-robin` (starting position advances per
call across the group's members). The first member to answer SHALL win, and remaining
legs SHALL be cancelled.

Each group SHALL carry a per-member ring timeout and a no-answer outcome. When no member
answers within the group's budget, the call SHALL follow the group's configured
no-answer behavior; when that behavior is to hand off, the supervisor SHALL take the call
with the conversation intact.

#### Scenario: Sequential group rings members in order

- **WHEN** a call resolves to a sequential ring group
- **THEN** members are tried in configured order, each for the configured per-member
  timeout, until one answers

#### Scenario: Round-robin advances across calls

- **WHEN** two consecutive calls resolve to the same round-robin group
- **THEN** the second call begins at the member after the one the first call began with

#### Scenario: First answer wins

- **WHEN** a group member answers while other legs are still ringing
- **THEN** the answering leg is bridged to the caller and every other leg is cancelled

#### Scenario: No member answers

- **WHEN** no member of the group answers within the group's budget
- **THEN** the group's configured no-answer behavior runs, and if that behavior hands off
  to the supervisor, the supervisor takes the call

### Requirement: Per-tenant routing table is the source of routing data

The system SHALL keep routing data in a per-tenant structured routing file rather than in
the tenant's natural-language prompt. That data is the extension directory, DID mappings,
ring groups and their strategies, and named symbolic targets. The resolver and `tool-authorization`'s symbolic
targets SHALL read the same table, so a name the model can dial and a name the resolver
can resolve cannot drift apart.

A tenant with no routing file SHALL be treated as having an empty table: nothing resolves
deterministically and every call goes to the supervisor. A malformed routing file SHALL
fail loudly at load rather than silently yielding an empty table.

#### Scenario: Symbolic targets and resolver agree

- **WHEN** a tenant's routing table defines a named target
- **THEN** both the resolver and the model-facing symbolic-target narrowing resolve that
  name to the same destination

#### Scenario: Missing routing file degrades to supervision

- **WHEN** a loaded tenant has no routing file
- **THEN** no call for that tenant resolves deterministically and every call enters the
  supervisor

#### Scenario: Malformed routing file fails loudly

- **WHEN** a tenant's routing file cannot be parsed
- **THEN** the load fails with an error naming the file, rather than the tenant silently
  losing all deterministic routing

### Requirement: Resolution does not depend on the LLM

Deterministic resolution SHALL NOT require any LLM request. When the LLM service is
unreachable or failing, resolvable calls SHALL continue to be routed normally.

#### Scenario: PBX keeps routing during an LLM outage

- **WHEN** the LLM service is unreachable and a directory user dials another extension
- **THEN** the call is forwarded normally and the caller notices no difference

