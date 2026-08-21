# call-admission Specification

## Purpose

The deterministic gate that runs before any LLM round-trip: tenant preflight and the per-tenant concurrent-channel limit, rejecting pre-answer so the first-turn latency budget is protected under load.
## Requirements
### Requirement: Deterministic preflight before engaging the LLM

Before any LLM round-trip, the system SHALL run a deterministic preflight: the call's tenant
MUST resolve to a loaded tenant. A call whose tenant does not resolve SHALL be rejected
without engaging the model.

The additional requirement that the tenant's combined prompt (`settings.md` + `tenant.md`)
be non-empty SHALL apply only when the call is handed to the supervisor. A call that
`call-resolution` resolves deterministically SHALL NOT be rejected for an empty or missing
tenant prompt, because it never reaches the model.

#### Scenario: Unresolved tenant is rejected pre-LLM

- **WHEN** a call's tenant does not resolve to a loaded tenant configuration
- **THEN** the call is rejected and no LLM round-trip occurs

#### Scenario: Valid tenant passes preflight

- **WHEN** the tenant is loaded
- **THEN** preflight passes and the call proceeds to deterministic resolution

#### Scenario: Empty prompt still routes a resolvable call

- **WHEN** a loaded tenant has an empty prompt but a routing table, and a directory user
  dials a registered extension
- **THEN** the call is resolved and forwarded rather than rejected

#### Scenario: Empty prompt rejects a call needing the supervisor

- **WHEN** a loaded tenant has an empty prompt and a call does not resolve deterministically
- **THEN** the call is rejected pre-answer and no LLM round-trip occurs

### Requirement: Per-tenant channel limit

The system SHALL enforce a per-tenant limit on concurrent supervised calls (channels). The
slot SHALL be acquired when a call is handed to the supervisor — not for calls that
`call-resolution` resolves deterministically — and SHALL be released by the teardown funnel.
A hand-off that would exceed its tenant's limit SHALL be rejected.

#### Scenario: Call within the limit is admitted

- **WHEN** a call is handed to the supervisor and the tenant has an available channel slot
- **THEN** the call acquires a slot and proceeds

#### Scenario: Call over the limit is rejected busy

- **WHEN** a call is handed to the supervisor and the tenant is at its channel limit
- **THEN** the call is rejected (486 Busy) and no LLM round-trip occurs

#### Scenario: Deterministic call is not charged a channel

- **WHEN** the tenant is at its channel limit and a directory user dials a registered
  extension
- **THEN** the call is resolved and forwarded without consuming a channel slot and without
  a 486

#### Scenario: Slot released on teardown

- **WHEN** a supervised call tears down
- **THEN** its tenant channel slot is released exactly once

### Requirement: Admission rejects pre-answer and protects the first-turn budget

Preflight and channel-limit rejections SHALL occur before the INVITE is answered (4xx/486, no
200 OK). By bounding the concurrency of supervised calls, admission SHALL keep the first-turn
LLM call from queueing past the INVITE transaction timeout under load. The bound SHALL apply
to supervised calls only, since deterministically resolved calls make no LLM request and
cannot contribute to that queue.

#### Scenario: Rejection does not answer the call

- **WHEN** admission rejects a call
- **THEN** the rejection is a SIP failure response and no 200 OK is sent

#### Scenario: Concurrency bound protects latency

- **WHEN** the tenant is at capacity for supervised calls
- **THEN** new calls needing the supervisor are rejected fast rather than queued behind
  in-flight first-turn LLM calls

