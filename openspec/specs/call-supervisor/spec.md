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
the configured model is available on it, and SHALL log the outcome. When the provider has
a resident model that a request would otherwise load, it SHALL issue one request to load
the model, and SHALL log how long that took.

Warming and residency are properties of a provider that loads a model on demand. A
provider that serves models it already holds SHALL NOT be sent a warm-up request, which
would buy nothing and may be billed.

Whether a model's absence from the server's listing is conclusive SHALL be a property of
the provider. Where it is conclusive, absence SHALL be reported as a warning naming what
the server does have. Where the listing is advisory or partial, absence SHALL be reported
without implying the call will fail.

Neither check SHALL block startup or fail it: a deployment whose LLM is
unreachable SHALL still start and SHALL still route every call
`call-resolution` resolves deterministically.

The system SHALL ask a provider that holds a model resident to keep it resident for
longer than the gap it expects between calls, so a caller does not pay a model load
inside their turn budget.

Guidance logged when a check fails SHALL be correct for the provider in use, so that an
operator is not told to take an action their provider has no notion of. A rejection of
the system's credentials SHALL be reported distinguishably from a server that did not
answer.

The transport SHALL NOT impose a deadline shorter than the largest configured turn
budget, so that a slow turn is bounded by the budget an operator configured rather than
by a limit they cannot see.

#### Scenario: Model present is loaded before the first call

- **WHEN** the server starts and the configured model is available on a provider that
  loads models on demand
- **THEN** the model is loaded and the load time is logged

#### Scenario: A hosted provider is verified but not warmed

- **WHEN** the server starts against a provider that does not load models on demand
- **THEN** reachability is logged and no warm-up request is sent

#### Scenario: Missing model is reported at startup

- **WHEN** the LLM server answers but does not have the configured model, on a provider
  whose model listing is conclusive
- **THEN** a warning names the model, lists what the server does have, and startup
  continues

#### Scenario: An advisory listing does not raise a false alarm

- **WHEN** the configured model is absent from the listing of a provider whose listing is
  partial or gateway-dependent
- **THEN** the outcome is logged without claiming the model is unavailable, and startup
  continues

#### Scenario: Unreachable LLM does not stop the server

- **WHEN** the LLM server does not answer at startup
- **THEN** a warning is logged, the server starts, and a call to a registered
  extension is still forwarded

#### Scenario: Rejected credentials are reported as such

- **WHEN** the LLM server answers but rejects the system's credentials
- **THEN** the warning names the credential to check, distinctly from an unreachable server

#### Scenario: Readiness reflects the server

- **WHEN** readiness is checked against a configured URL that nothing is serving
- **THEN** the client reports not ready, rather than reporting ready because a URL
  was configured

#### Scenario: The transport does not cut a turn short of its budget

- **WHEN** a turn is allowed a budget larger than any transport-level deadline would permit
- **THEN** the turn is bounded by the configured budget, not by the transport

### Requirement: The supervisor's provider is selected by the model identifier

The supervisor SHALL support more than one LLM provider, selected by an optional
`provider/` prefix on the configured model identifier. The identifier SHALL be split on
the **first** separator only, so that a model id containing separators is preserved
intact for the provider that owns it.

An identifier with no prefix SHALL mean the default provider, so a deployment configured
before this change keeps working unmodified. An unrecognised prefix SHALL be a startup
error naming the valid providers, rather than being interpreted as part of a model name:
sending an entire deployment's calls to the wrong endpoint over a typo is worse than
refusing to start.

An identifier that names a provider and no model SHALL select that provider with a default
model defined for it, resolving to a complete and usable configuration. It SHALL NOT be
interpreted as a model name belonging to the default provider, which would send the
deployment looking for a model nobody asked for. The resolved model SHALL be reported at
startup, so that a choice made on the operator's behalf is visible rather than implicit.

Each provider SHALL have its own default endpoint, used when no endpoint is configured
explicitly. An explicitly configured endpoint SHALL always be honoured, so that any
server implementing a supported provider's API can be used.

Credentials SHALL be read from the environment only. They SHALL NOT be accepted as a
command-line flag, SHALL NOT be stored in the configuration structure, and SHALL NOT
appear in the startup banner, in logs, or in any error message. Startup SHALL fail when a
provider requires credentials for the endpoint in use and none are available.

A parameter that is meaningful only to one provider SHALL be ignored by the others, and
SHALL warn when it was set explicitly for a provider that ignores it.

#### Scenario: An unprefixed model keeps working

- **WHEN** the supervisor is configured with a model identifier that has no provider prefix
- **THEN** the default provider is used and the identifier is the model id unchanged

#### Scenario: A prefixed model selects its provider

- **WHEN** the supervisor is configured with a `provider/model` identifier
- **THEN** that provider is used and the remainder, including any further separators, is
  the model id

#### Scenario: A provider named with no model resolves to that provider's default

- **WHEN** the model identifier names a supported provider and no model
- **THEN** that provider is selected with its default model and its default endpoint, and
  the resolved model is reported at startup

#### Scenario: A provider name is not mistaken for a model name

- **WHEN** the model identifier is exactly a supported provider's name
- **THEN** it selects that provider, rather than being looked up as a model belonging to
  the default provider

#### Scenario: An unknown provider stops startup

- **WHEN** the model identifier carries a prefix that names no supported provider
- **THEN** startup fails with an error naming the valid providers and how to write the
  identifier if the prefix was part of a model name

#### Scenario: The endpoint defaults per provider

- **WHEN** no endpoint is configured
- **THEN** the selected provider's own default endpoint is used

#### Scenario: An explicit endpoint is honoured

- **WHEN** an endpoint is configured explicitly
- **THEN** it is used as given, so a compatible third-party server can serve the provider

#### Scenario: Credentials never reach a log or the banner

- **WHEN** the supervisor starts with a provider that uses credentials
- **THEN** the banner and logs report whether credentials are present but never their value

#### Scenario: A missing required credential stops startup

- **WHEN** the selected provider requires credentials for the configured endpoint and none
  are set in the environment
- **THEN** startup fails with an error naming the environment variable to set
