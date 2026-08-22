## ADDED Requirements

### Requirement: The supervisor's provider is selected by the model identifier

The supervisor SHALL support more than one LLM provider, selected by an optional
`provider/` prefix on the configured model identifier. The identifier SHALL be split on
the **first** separator only, so that a model id containing separators is preserved
intact for the provider that owns it.

An identifier with no prefix SHALL mean the default provider, so a deployment configured
before this change keeps working unmodified. An unrecognized prefix SHALL be a startup
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

## MODIFIED Requirements

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
