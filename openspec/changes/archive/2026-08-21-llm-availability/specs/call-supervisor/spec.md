## ADDED Requirements

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

## MODIFIED Requirements

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
