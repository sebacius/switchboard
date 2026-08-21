# agent-tools Specification

## Purpose

Native tool calling on Ollama /api/chat, the per-call tool registry scoped by (tenant, direction), the call-setup tool inventory with forward-versus-bridge dial, the handler disposition enum, and argument validation with model self-correction.
## Requirements
### Requirement: Native tool calling on Ollama /api/chat

The LLM client SHALL use Ollama's native `/api/chat` endpoint with `think: false`, sending tool
definitions and parsing `tool_calls`, `content`, and `thinking` as separate fields so reasoning is
never spoken. The client SHALL expose a `Client` interface so a scripted test double can substitute. No
third-party LLM library SHALL be added.

#### Scenario: Reasoning never reaches TTS

- **WHEN** the model returns a response with a `thinking` field
- **THEN** only `content` is eligible for TTS and `thinking` is never spoken

#### Scenario: Tool call is parsed from the response

- **WHEN** the model returns a `tool_calls` entry
- **THEN** the client returns the tool name and arguments to the runner

#### Scenario: Scripted client substitutes in tests

- **WHEN** a unit test uses the scripted client
- **THEN** it returns a pre-programmed sequence of thinking/text/tool-calls without contacting Ollama

### Requirement: Per-call tool registry scoped by tenant and direction

The tool set offered to the model SHALL be built per call from the resolved `(tenant, direction)`. An
inbound call SHALL NOT be offered an external-dial capability. Tools the tenant has not enabled SHALL
NOT appear in the registry.

#### Scenario: Inbound caller has no external dial

- **WHEN** the call direction is `inbound`
- **THEN** the registry offered to the model contains no tool capable of dialing an external destination

#### Scenario: Internal caller may get external dial when enabled

- **WHEN** the call is `internal` and the tenant has enabled external dialing
- **THEN** an external-capable dial tool is present, subject to `tool-authorization`

### Requirement: Call-setup tool inventory with forward-versus-bridge dial

The supervisor SHALL expose call-setup tools mapping to session/parking primitives: `dial`, `hangup`,
`play_audio`, `park`, `unpark`. `dial` SHALL forward the INVITE without answering when the call has not
been answered, and bridge media when the supervisor has already answered. Spoken text is TTS'd
implicitly; listening is the runner's job. Mid-call tools are out of scope.

#### Scenario: Dial before answer forwards

- **WHEN** the model invokes `dial` and the supervisor has not answered the caller
- **THEN** the handler forwards the INVITE and relays responses (no 200 from the supervisor)

#### Scenario: Dial after answer bridges

- **WHEN** the model invokes `dial` after the supervisor has answered and owns media
- **THEN** the handler performs the B2BUA dial and bridges media

### Requirement: Handler disposition

Tool handlers SHALL return a disposition that the dispatch loop acts on: `Continue` (keep draining
events), `Terminal` (exit and tear down), or `Parked` (the loop holds, keeping the conversation alive,
until unpark or cancellation). Handlers SHALL NOT block the dispatch loop.

#### Scenario: Park does not block the loop

- **WHEN** the model invokes `park`
- **THEN** the handler returns `Parked` immediately and the loop holds the call, releasing the slot on
  unpark or caller hangup — the handler itself does not block

#### Scenario: Hangup is terminal

- **WHEN** the model invokes `hangup`
- **THEN** the handler returns `Terminal` and the loop tears down

### Requirement: Argument validation and self-correction

Tool handlers SHALL validate arguments explicitly. An unknown tool SHALL NOT terminate the
call: the system SHALL transfer the caller deterministically to the tenant's configured
operator destination. A missing or invalid required argument SHALL return an actionable
error string into the conversation so the model can correct on the next turn. A tool call
identical to one that just failed SHALL be refused.

A tenant with no configured operator destination SHALL fall back to an actionable error
into the conversation rather than hanging up, so an unknown tool never drops a caller.

#### Scenario: Unknown tool transfers to the operator

- **WHEN** the model emits a tool name outside the call's registry
- **THEN** the caller is transferred to the tenant's operator destination and the call is
  not hung up

#### Scenario: Unknown tool with no operator configured keeps the call alive

- **WHEN** the model emits an unknown tool for a tenant with no operator destination
- **THEN** an actionable error naming the available tools is returned into the
  conversation and the call continues

#### Scenario: Missing required argument

- **WHEN** the model invokes `dial` without a target
- **THEN** the handler returns an actionable error into the conversation and the call continues

#### Scenario: Identical failing call is refused

- **WHEN** the model re-issues the same tool and arguments that just failed
- **THEN** the runner refuses the duplicate and nudges the model toward an alternative

