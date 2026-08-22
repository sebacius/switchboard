## MODIFIED Requirements

### Requirement: Deterministic preflight before the call is given resources

Before a call is given media or routing resources, the system SHALL run a deterministic
preflight: the call's tenant MUST resolve to a loaded tenant. A call whose tenant does not
resolve SHALL be rejected.

A tenant SHALL be considered loaded when it has routing configuration. There SHALL be no
prompt check, because there are no prompts.

#### Scenario: Unresolved tenant is rejected

- **WHEN** a call's tenant does not resolve to a loaded tenant configuration
- **THEN** the call is rejected before any media session is created

#### Scenario: Valid tenant passes preflight

- **WHEN** the tenant is loaded
- **THEN** preflight passes and the call proceeds to deterministic resolution

### Requirement: Per-tenant channel limit

The system SHALL enforce a per-tenant limit on concurrent calls. The limit exists as
**capacity control**: a call occupies an RTP port, a media session, and a blocked
handler goroutine for its whole life, and something must bound how many a single tenant
can hold.

This is a change of rationale, not of mechanism. The limit was previously justified by
the need to bound concurrent first-turn LLM latency inside the INVITE transaction; that
justification is gone with the model, and the limit is now founded on the physical
resources a call consumes.

Because the scarce resource is now a port rather than a model, the slot SHALL be acquired
**before** the media session is created, and SHALL be acquired for every call rather than
only for calls that reach a particular subsystem. It SHALL be released exactly once when
the call ends.

#### Scenario: Call within the limit is admitted

- **WHEN** a call arrives and its tenant has an available channel slot
- **THEN** the call acquires a slot and proceeds

#### Scenario: Call over the limit is rejected busy

- **WHEN** a call arrives and its tenant is at its channel limit
- **THEN** the call is rejected with 486 Busy Here and no media session is created

#### Scenario: Slot released on teardown

- **WHEN** a call tears down by any path, including caller abandonment
- **THEN** its tenant channel slot is released exactly once

### Requirement: Admission rejects pre-answer and bounds resource use

Preflight and channel-limit rejections SHALL occur before the INVITE is answered (4xx/486,
no 200 OK). The channel limit SHALL bound the number of RTP ports, media sessions, and
blocked handler goroutines a single tenant can hold concurrently.

#### Scenario: Rejection does not answer the call

- **WHEN** admission rejects a call
- **THEN** the rejection is a SIP failure response and no 200 OK is sent

#### Scenario: Rejection consumes no media resources

- **WHEN** a tenant is at its channel limit
- **THEN** new calls are rejected without allocating an RTP port or creating a media
  session
