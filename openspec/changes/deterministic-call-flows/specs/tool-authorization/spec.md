## MODIFIED Requirements

### Requirement: Configuration has zero authority; destinations are authorized deterministically

Every consequential destination the system is asked to dial SHALL be treated as an
untrusted request that a deterministic policy layer authorizes before execution. A denied
destination SHALL NOT be dialed.

The untrusted input is no longer a model's tool call but a **configuration file**: an
entry mapping, a flow node, or a ring group member. The principle is unchanged and its
statement is narrowed to fit — *config is not authority*. Anyone able to edit a routing
or flow file SHALL NOT thereby be able to grant themselves reach the tenant's Class of
Service does not already permit.

Authorization SHALL be a property of the tenant's policy alone, and SHALL NOT be
derivable from the file that named the destination.

#### Scenario: Denied destination does not dial

- **WHEN** the policy layer denies a destination
- **THEN** no INVITE leaves the system and the caller is routed by the denied outcome
  rather than being connected

#### Scenario: Editing a flow cannot grant authority

- **WHEN** a flow file is edited to name a destination the tenant's Class of Service
  denies
- **THEN** the policy layer's decision is unchanged and the dial is still denied

#### Scenario: Every dial path is adjudicated alike

- **WHEN** a destination is produced by direct resolution, by a flow node, or as an
  individual ring group member
- **THEN** all three are adjudicated by the same policy with the same verdict

### Requirement: Class of Service on dial

External dialing SHALL be governed by a per-tenant, per-direction Class of Service: external dial is
default-deny; permitted destinations are an allowlist of numbers/prefixes; barred classes (premium-rate,
satellite, high-risk country codes) SHALL be blocked.

Class of Service SHALL be evaluable **without side effects**, so that configuration can
be checked against it at load time without consuming the tenant's spend budget.

#### Scenario: External dial denied by default

- **WHEN** a tenant has not enabled external dialing and a dial to an external destination is attempted
- **THEN** it is denied

#### Scenario: Barred class is blocked even when external is enabled

- **WHEN** external dialing is enabled but the destination is in a barred class (e.g. premium-rate)
- **THEN** the dial is denied

#### Scenario: Allowlisted destination is permitted

- **WHEN** external dialing is enabled and the destination matches the tenant allowlist
- **THEN** the dial is authorized

#### Scenario: Load-time checking does not consume budget

- **WHEN** every external destination in a tenant's configuration is checked at load
- **THEN** the verdicts are produced without consuming any of the tenant's spend budget

### Requirement: Capability narrowing of dial targets

Configuration SHALL name external destinations symbolically (named forwards) that a deterministic
resolver maps to numbers; a flow SHALL NOT be able to express an arbitrary external number through
`dial_external`. Dialing a caller-provided number SHALL require a separate, explicitly gated path.

A digit string matched by an entry-mapping pattern SHALL NOT be usable as a dial target.
Pattern matching selects *what to run*; it SHALL NOT supply *where to dial*, because the
moment matched digits can become a destination, symbolic narrowing is bypassed.

#### Scenario: Symbolic target resolves deterministically

- **WHEN** a flow node dials a named forward
- **THEN** the resolver maps it to the configured number; the configuration never supplied the raw number

#### Scenario: Arbitrary number requires the gated path

- **WHEN** a caller-provided number must be dialed
- **THEN** it is only possible via the separate gated path, subject to Class of Service

#### Scenario: Matched digits cannot become a destination

- **WHEN** an entry-mapping pattern matches a dialed external number and routes to a flow
- **THEN** no node can dial those digits; only symbolic targets are dialable

### Requirement: Per-tenant spend circuit breaker

The system SHALL enforce per-tenant spend/rate limits on external calls (e.g. max external minutes or
cost per day) and SHALL trip and alert when a limit is exceeded.

The counter SHALL be scoped to the tenant over the configured period and SHALL persist
across calls for the life of the process. A counter scoped to a single call does not
satisfy this requirement, because a per-call counter can never reach a per-day limit.

#### Scenario: Spend spike trips the breaker

- **WHEN** a tenant's external call spend exceeds its configured limit
- **THEN** further external dials are blocked and an alert is raised

#### Scenario: The counter accumulates across calls

- **WHEN** a tenant places external calls across many separate calls within the period
- **THEN** each one advances the same counter, and the limit is reached as configured

### Requirement: Decision logging

Every authorization verdict SHALL be logged to the call record so denied actions are
auditable as potential fraud signals. The record SHALL identify the call, the destination
attempted, and the verdict, and SHALL be durable rather than existing only as process log
output.

#### Scenario: Denied dial is recorded

- **WHEN** an external dial is denied by policy
- **THEN** the call record contains the attempted destination and the deny verdict

#### Scenario: Verdicts join the call's traversal record

- **WHEN** a dial inside a flow is denied
- **THEN** the deny appears in the same durable call record as the flow traversal, so the
  path and the verdict are readable together
