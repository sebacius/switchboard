# call-supervisor Specification

## Purpose

The per-call event-loop runner: the first-turn answer-versus-forward decision, the three nested cancellation scopes (call, turn, playback), the idempotent teardown funnel, safe event producers, the runaway-turn breaker, and the call-long conversation.
## Requirements
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

### Requirement: Supervisor unavailability does not end a resolvable call

A failure to reach or complete an LLM request SHALL NOT, by itself, terminate a
call that did not require the model. When the LLM is unavailable,
deterministically resolved calls SHALL continue to be routed, and a call that
does require the supervisor SHALL be told the assistant is unavailable rather
than being dropped without explanation.

The first turn SHALL be bounded by its own budget, which MAY be larger than the
budget for mid-call turns: it runs while the caller hears ringback and may
include loading the model, whereas a mid-call turn is a silence the caller is
waiting through. Both budgets SHALL be configurable.

A turn that exceeded its budget and a server that could not be reached SHALL be
logged distinguishably, with the elapsed time and the budget, so that a slow
model is not reported as a missing one.

#### Scenario: LLM outage does not stop extension dialing

- **WHEN** the LLM service is unreachable and a directory user dials another
  registered extension
- **THEN** the call is forwarded normally, because no LLM request was needed

#### Scenario: LLM outage on a supervised call degrades gracefully

- **WHEN** the LLM service is unreachable and a call requires the supervisor
- **THEN** the caller is told the assistant is unavailable and the call is ended
  deliberately, rather than the runner returning an error that drops the call
  silently

#### Scenario: The first turn gets more room than a mid-call turn

- **WHEN** a call runs a first turn and then a mid-call turn
- **THEN** the first turn is bounded by the first-turn budget and the mid-call
  turn by the shorter mid-call budget

#### Scenario: A slow model is not reported as a missing one

- **WHEN** a turn exceeds its budget while the LLM server is reachable
- **THEN** the log says the model did not answer within the budget and reports the
  elapsed time, distinctly from the unreachable-server case

### Requirement: The supervisor verifies and warms its model at startup

At startup the system SHALL check that the configured LLM server answers and that
the configured model is available on it, and SHALL log the outcome. When both
hold, it SHALL issue one request to load the model, and SHALL log how long that
took.

Neither check SHALL block startup or fail it: a deployment whose LLM is
unreachable SHALL still start and SHALL still route every call
`call-resolution` resolves deterministically.

The system SHALL ask the LLM server to keep the model resident for longer than
the gap it expects between calls, so a caller does not pay a model load inside
their turn budget.

#### Scenario: Model present is loaded before the first call

- **WHEN** the server starts and the configured model is available
- **THEN** the model is loaded and the load time is logged

#### Scenario: Missing model is reported at startup

- **WHEN** the LLM server answers but does not have the configured model
- **THEN** a warning names the model, lists what the server does have, and startup
  continues

#### Scenario: Unreachable LLM does not stop the server

- **WHEN** the LLM server does not answer at startup
- **THEN** a warning is logged, the server starts, and a call to a registered
  extension is still forwarded

#### Scenario: Readiness reflects the server

- **WHEN** readiness is checked against a configured URL that nothing is serving
- **THEN** the client reports not ready, rather than reporting ready because a URL
  was configured

