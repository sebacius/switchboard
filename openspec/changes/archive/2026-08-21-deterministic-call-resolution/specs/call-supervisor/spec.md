## MODIFIED Requirements

### Requirement: LLM supervises every INVITE

The system SHALL route every INVITE that is not resolved deterministically by
`call-resolution` — internal, inbound, and outbound alike — through a single LLM
supervisor runner. There SHALL be no dialplan table. Deterministic resolution ahead of
the supervisor SHALL be limited to destinations with exactly one correct answer as
defined by `call-resolution`, SHALL remain subject to `tool-authorization`, and SHALL NOT
be extensible into a general-purpose matcher: a call that needs judgement about intent,
wording, or business context SHALL reach the model.

Once the supervisor takes a call, it owns that call from that point through teardown.

#### Scenario: Internal call to a resolvable extension does not enter the supervisor

- **WHEN** a registered directory user dials another registered directory extension
- **THEN** `call-resolution` forwards the call and the supervisor runner is never started
  for it

#### Scenario: Internal call that does not resolve enters the supervisor

- **WHEN** a registered directory user dials a target that `call-resolution` does not
  resolve
- **THEN** the supervisor runner handles the call with no dialplan route lookup

#### Scenario: Inbound trunk call to the assistant enters the supervisor

- **WHEN** an INVITE arrives from a trunk peer for a mapped DID (admitted by
  `call-admission`) whose mapping is the AI receptionist
- **THEN** the same supervisor runner handles the call

#### Scenario: Intent-bearing speech is never resolved deterministically

- **WHEN** a caller states what they need in words rather than dialing a target
- **THEN** the supervisor decides, and no deterministic matcher interprets the utterance

## ADDED Requirements

### Requirement: Supervisor unavailability does not end a resolvable call

A failure to reach or complete an LLM request SHALL NOT, by itself, terminate a call that
did not require the model. When the LLM is unavailable, deterministically resolved calls
SHALL continue to be routed, and a call that does require the supervisor SHALL be told
the assistant is unavailable rather than being dropped without explanation.

#### Scenario: LLM outage does not stop extension dialing

- **WHEN** the LLM service is unreachable and a directory user dials another registered
  extension
- **THEN** the call is forwarded normally, because no LLM request was needed

#### Scenario: LLM outage on a supervised call degrades gracefully

- **WHEN** the LLM service is unreachable and a call requires the supervisor
- **THEN** the caller is told the assistant is unavailable and the call is ended
  deliberately, rather than the runner returning an error that drops the call silently
