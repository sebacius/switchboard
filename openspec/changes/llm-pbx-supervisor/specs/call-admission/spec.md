## ADDED Requirements

### Requirement: Deterministic preflight before engaging the LLM

Before any LLM round-trip, the system SHALL run a deterministic preflight: the call's tenant MUST
resolve to a loaded tenant whose combined prompt (`settings.md` + `tenant.md`) is non-empty. A preflight
failure SHALL reject the call without engaging the model.

#### Scenario: Unloaded tenant is rejected pre-LLM

- **WHEN** a call's tenant does not resolve to a loaded, non-empty tenant configuration
- **THEN** the call is rejected and no LLM round-trip occurs

#### Scenario: Valid tenant passes preflight

- **WHEN** the tenant is loaded and its combined prompt is non-empty
- **THEN** preflight passes and the call proceeds to admission

### Requirement: Per-tenant channel limit

The system SHALL enforce a per-tenant limit on concurrent supervised calls (channels). A call that would
exceed its tenant's limit SHALL be rejected. The slot SHALL be acquired at admission and released by the
teardown funnel.

#### Scenario: Call within the limit is admitted

- **WHEN** the tenant has an available channel slot
- **THEN** the call acquires a slot and proceeds

#### Scenario: Call over the limit is rejected busy

- **WHEN** the tenant is at its channel limit
- **THEN** the call is rejected (486 Busy) and no LLM round-trip occurs

#### Scenario: Slot released on teardown

- **WHEN** a supervised call tears down
- **THEN** its tenant channel slot is released exactly once

### Requirement: Admission rejects pre-answer and protects the first-turn budget

Preflight and channel-limit rejections SHALL occur before the INVITE is answered (4xx/486, no 200 OK).
By bounding concurrency, admission SHALL keep the first-turn LLM call from queueing past the INVITE
transaction timeout under load.

#### Scenario: Rejection does not answer the call

- **WHEN** admission rejects a call
- **THEN** the rejection is a SIP failure response and no 200 OK is sent

#### Scenario: Concurrency bound protects latency

- **WHEN** the tenant is at capacity
- **THEN** new calls are rejected fast rather than queued behind in-flight first-turn LLM calls
