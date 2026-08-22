## MODIFIED Requirements

### Requirement: Deterministic resolution precedes flow execution

The system SHALL attempt deterministic resolution of every admitted call before executing
a flow. When the dialed destination resolves unambiguously to a single correct
destination, the system SHALL execute that resolution directly. Flow execution SHALL be
entered only when resolution yields no single destination, or when the entry mapping
names a flow.

Resolution SHALL run after `call-routing` has produced direction and tenant and after
`call-admission` has admitted the call, and before any flow node is evaluated.

Neither resolution nor flow execution SHALL make any request to a language model,
because none exists.

#### Scenario: Extension-to-extension call is forwarded directly

- **WHEN** a registered directory user dials another registered directory extension
- **THEN** the system forwards the INVITE to that extension without answering and
  without entering a flow

#### Scenario: An entry mapped to a flow enters the graph

- **WHEN** the dialed destination's entry mapping names a flow
- **THEN** flow execution begins at that flow's start node

#### Scenario: Nothing resolved and nothing mapped reaches the operator

- **WHEN** deterministic resolution produces no destination and no entry mapping matches
- **THEN** the call is directed to the tenant operator, or receives a final failure
  status when the tenant configures none

### Requirement: What counts as unambiguous

A destination SHALL be treated as unambiguously resolved only when it is one of:

- a **registered directory extension** for the call's tenant;
- a **`*7XX` call-retrieval code** naming an occupied parking slot, dialed by an
  `internal` caller;
- an **inbound DID** with a mapping in the tenant's routing table to an extension or a
  ring group;
- a **named ring group** for the call's tenant.

Anything else — an unmapped target, an unregistered extension, a `*7XX` code for an empty
slot — SHALL NOT be treated as resolved.

Call retrieval SHALL be evaluated before entry-mapping patterns, so that a pattern
cannot shadow a retrieval code.

#### Scenario: Unregistered extension is not resolved

- **WHEN** the dialed extension exists in the tenant's routing table but has no active
  registration
- **THEN** resolution does not claim the call

#### Scenario: Retrieval code for an empty slot is not resolved

- **WHEN** an internal caller dials `*701` and slot 701 holds no parked call
- **THEN** resolution does not claim the call

#### Scenario: Retrieval code from a non-internal caller is not resolved

- **WHEN** an `inbound` caller dials a `*7XX` code
- **THEN** resolution does not claim the call, preserving the existing rule that call
  retrieval is an internal-only capability

#### Scenario: A pattern cannot shadow a retrieval code

- **WHEN** a tenant's entry mapping contains a pattern that would also match `*701` and
  an internal caller dials `*701` for an occupied slot
- **THEN** the parked call is retrieved, because retrieval is evaluated first

### Requirement: Resolution stays inside the authorization boundary

Deterministic resolution SHALL NOT bypass `tool-authorization`. Every destination the
resolver dials SHALL be adjudicated by the same per-tenant policy that adjudicates every
other dial, and a denied destination SHALL NOT be dialed. Resolution SHALL NOT
grant reach that the tenant's Class of Service does not already permit.

#### Scenario: Resolved external destination is still adjudicated

- **WHEN** resolution produces an external destination for a tenant whose policy denies
  external dialing
- **THEN** the policy denies it, no INVITE leaves the system, and the deny is logged with
  the same decision logging as any other dial

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

Each group SHALL carry a per-member ring timeout. When no member answers within the
group's budget, the outcome SHALL be reported to the caller of the group — the resolver
or the flow node that targeted it — which decides what happens next. A group SHALL NOT
carry its own no-answer destination, so that a group reached from a flow has exactly one
place where its fallback is written down.

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

#### Scenario: No member answers under a flow node

- **WHEN** no member of a group targeted by a `dial_user` node answers within the budget
- **THEN** the node's no-answer exit decides what happens next

### Requirement: Per-tenant routing data is the source of routing

The system SHALL keep routing data in per-tenant structured configuration rather than in
prose. That data is the entry mapping, the extension directory, DID mappings, ring
groups and their strategies, named symbolic targets, and the tenant's flows.

A tenant's routing table and its flows SHALL be loaded and validated as one atomic unit,
so that a flow can never reference a group that the same load removed.

A tenant with no routing configuration SHALL be treated as having nothing to route. A
malformed or invalid file SHALL fail loudly at load and SHALL leave the previously loaded
configuration in force rather than replacing it with an empty one.

#### Scenario: Routing data and symbolic targets agree

- **WHEN** a tenant's routing table defines a named target
- **THEN** both direct resolution and flow-node target narrowing resolve that name to the
  same destination

#### Scenario: Malformed routing file fails loudly and changes nothing

- **WHEN** a tenant's routing or flow file cannot be parsed or fails validation
- **THEN** the load fails with an error naming the file and the problem, and the
  previously loaded configuration remains in force

#### Scenario: Flows and routing validate together

- **WHEN** a flow references a ring group that the tenant's routing table does not define
- **THEN** the load fails, because the two files are validated as one unit

### Requirement: Entry mapping supports digit patterns with computed specificity

The entry mapping SHALL support patterns, because extension ranges, DID blocks, and
outbound number plans cannot be enumerated. Patterns SHALL use a restricted digit-map
vocabulary — `X` for 0-9, `N` for 2-9, `Z` for 1-9, bracketed sets, a trailing wildcard,
and literals — and SHALL NOT be regular expressions.

When more than one pattern matches, the most specific SHALL win. Specificity SHALL be
**computed** from how narrow each position's accepted set is, and SHALL NOT be declared
as a priority number in configuration.

Two patterns that can match the same input with neither being more specific SHALL be a
load error naming both patterns, not a silent tiebreak. The restricted vocabulary exists
precisely so that specificity is well defined, which it is not for regular expressions.

A bare destination SHALL remain valid as shorthand, so simple configurations stay simple.

#### Scenario: A literal beats a pattern

- **WHEN** both an exact extension and a pattern match the dialed digits
- **THEN** the exact extension wins

#### Scenario: A narrower class beats a wider one

- **WHEN** two patterns of equal length match and one accepts a narrower set of digits at
  the position where they differ
- **THEN** the narrower one wins

#### Scenario: Genuinely ambiguous patterns are rejected at load

- **WHEN** a tenant declares two patterns that overlap with neither more specific than
  the other
- **THEN** the configuration fails to load with an error naming both patterns

#### Scenario: A bare destination still works

- **WHEN** an entry maps a literal extension directly to a user destination
- **THEN** it behaves exactly as a single-node dial flow would

### Requirement: Routing never depends on an external service

Resolution and flow execution SHALL NOT require any language model, agent, or network
egress. The signaling server SHALL start and route calls with no such service configured
or reachable.

#### Scenario: The PBX routes with nothing else running

- **WHEN** no LLM, ASR, or agent service is configured or reachable and a directory user
  dials another extension
- **THEN** the server starts normally, the call is forwarded, and the caller notices no
  difference
