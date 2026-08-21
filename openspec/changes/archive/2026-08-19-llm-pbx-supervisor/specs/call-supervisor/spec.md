## ADDED Requirements

### Requirement: LLM supervises every INVITE

The system SHALL route every INVITE — internal, inbound, and outbound — through a single LLM supervisor
runner. There SHALL be no dialplan table and no fast-path matcher that bypasses the LLM.

#### Scenario: Internal call enters the supervisor

- **WHEN** a registered directory user dials another directory user
- **THEN** the supervisor runner handles the call with no dialplan route lookup

#### Scenario: Inbound trunk call enters the supervisor

- **WHEN** an INVITE arrives from a trunk peer for a mapped DID (admitted by `call-admission`)
- **THEN** the same supervisor runner handles the call

### Requirement: LLM-driven answer — forward versus engage media

The runner SHALL NOT answer the INVITE automatically. The first-turn decision SHALL determine the SIP
response: a `dial` decision forwards the INVITE without answering (relaying provisional and final
responses); a decision to speak/gather sends 200 OK and the supervisor owns the media.

#### Scenario: Direct extension call is forwarded without answering

- **WHEN** the first turn returns `dial` to a directory user
- **THEN** the runner forwards the INVITE to that endpoint and relays its 180/200 to the caller, and
  the runner never sends its own 200 OK

#### Scenario: IVR engagement answers the call

- **WHEN** the first turn returns spoken text (greeting/gather)
- **THEN** the runner sends 200 OK, takes the media, speaks via TTS, and enters the listen/speak loop

#### Scenario: Outbound is forwarded to the trunk

- **WHEN** the first turn returns `dial` to an external destination permitted by policy
- **THEN** the runner forwards the INVITE to the trunk peer and relays responses, without answering

### Requirement: Nested context scopes

The runner SHALL structure cancellation as three nested scopes — call, turn, and playback — so that
teardown, the runaway-turn breaker, and playback interruption operate at distinct levels. Every
blocking operation within a turn (the LLM call, tool handlers, listening) SHALL honor its scope.

#### Scenario: Teardown cancels the whole call

- **WHEN** the call context is cancelled (BYE/CANCEL/timeout)
- **THEN** the in-flight LLM call and any running tool handler abort, and the runner returns

#### Scenario: A turn can be aborted without ending the call

- **WHEN** the turn scope is cancelled (per-turn deadline or runaway breaker)
- **THEN** that turn is abandoned but the call and conversation continue

### Requirement: Idempotent teardown funnel

All teardown initiators — caller CANCEL/BYE, the `hangup` tool, and context timeout — SHALL converge on
a single idempotent teardown path that runs exactly once and releases all resources (parking slot,
B-leg, tenant channel slot, RTP session). Pre-answer aborts SHALL respond 487 and cancel any B-leg;
post-answer aborts SHALL respond 200 to the BYE and tear down media. Producers SHALL NOT close the
events channel.

#### Scenario: Concurrent teardown initiators run once

- **WHEN** a caller BYE and a `hangup` tool fire concurrently
- **THEN** teardown executes exactly once and no double-free or "dialog not found" error occurs

#### Scenario: Caller abandons during forwarding

- **WHEN** the caller sends CANCEL while an outbound leg is ringing and no 200 has been sent
- **THEN** the runner responds 487 to the caller and CANCELs the outbound leg

### Requirement: Event-loop dispatch with safe producers

The runner SHALL drain a single events channel with one dispatch loop, each event becoming one turn.
Producers SHALL write using a select that also observes context cancellation so they never block after
the consumer exits.

#### Scenario: Speech event produces a turn

- **WHEN** the speech producer writes a transcribed utterance
- **THEN** the dispatch loop runs one model turn with that utterance

#### Scenario: Producer exits on cancellation

- **WHEN** the call context is cancelled while a producer is blocked trying to send an event
- **THEN** the producer observes cancellation and exits without leaking a goroutine

### Requirement: Runaway-turn breaker

The runner SHALL bound consecutive autonomous turns (a tool result re-prompting the model with no
caller input), resetting the count on any caller input. A soft cap SHALL stop autonomous re-prompting
(falling back to reactive-only); a hard cap SHALL play a deterministic message and tear down.

#### Scenario: Repeated failing tool does not loop forever

- **WHEN** the model re-issues a tool that keeps failing with no caller input
- **THEN** after the soft cap the runner stops re-prompting, and after the hard cap it ends the call
  with a deterministic message

#### Scenario: Caller input resets the budget

- **WHEN** the caller speaks after some autonomous turns
- **THEN** the consecutive-autonomous-turn count resets

### Requirement: Conversation persists for the whole call

The runner SHALL create one conversation at INVITE and retain it across every turn until teardown.
Non-terminal tool results SHALL be recorded back into the conversation.

#### Scenario: History accumulates across turns

- **WHEN** the runner processes a later utterance
- **THEN** the model request includes prior user, assistant, and tool-result messages from the call
