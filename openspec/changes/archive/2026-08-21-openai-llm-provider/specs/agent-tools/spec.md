## MODIFIED Requirements

### Requirement: Native tool calling on Ollama /api/chat

The `ollama` provider SHALL use Ollama's native `/api/chat` endpoint with `think: false`,
sending tool definitions and parsing `tool_calls`, `content`, and `thinking` as separate
fields so reasoning is never spoken. The client SHALL expose a `Client` interface so a
scripted test double can substitute, and so a second provider can be added behind it
without the runner knowing which provider is in use. No third-party LLM library SHALL be
added for any provider.

The provider-agnostic message representation MAY carry fields that only one provider
uses. Any such field SHALL be omitted from the wire when empty, so adding a provider
SHALL NOT change the bytes another provider receives.

#### Scenario: Reasoning never reaches TTS

- **WHEN** the model returns a response with a `thinking` field
- **THEN** only `content` is eligible for TTS and `thinking` is never spoken

#### Scenario: Tool call is parsed from the response

- **WHEN** the model returns a `tool_calls` entry
- **THEN** the client returns the tool name and arguments to the runner

#### Scenario: Scripted client substitutes in tests

- **WHEN** a unit test uses the scripted client
- **THEN** it returns a pre-programmed sequence of thinking/text/tool-calls without contacting Ollama

#### Scenario: Another provider's fields do not reach Ollama

- **WHEN** a request is sent to the `ollama` provider
- **THEN** no field that exists only for another provider appears in the request body

## ADDED Requirements

### Requirement: OpenAI-compatible tool calling

The `openai` provider SHALL use the `/v1/chat/completions` endpoint, translating the
provider-agnostic conversation into that wire format rather than re-tagging it. It SHALL
NOT send `think` or `keep_alive`, which do not exist in that API and are rejected.

Tool definitions SHALL be sent in the same shape both providers accept. Tool call
arguments arrive as a JSON string and SHALL be decoded into the same parsed-map form the
Ollama provider produces, so tool handlers are provider-agnostic. A tool result SHALL be
correlated to its call by `tool_call_id`.

Arguments that are absent, null, or not valid JSON SHALL yield empty arguments rather
than failing the turn, so the existing argument-validation and self-correction path
handles them instead of the caller hearing an unavailability message.

#### Scenario: String arguments are decoded to a map

- **WHEN** the provider returns a tool call whose `arguments` is a JSON string
- **THEN** the runner receives the same parsed argument map it would receive from Ollama

#### Scenario: Malformed arguments become a self-correction, not a failed call

- **WHEN** the provider returns tool call arguments that are not valid JSON
- **THEN** the turn succeeds with empty arguments and the handler returns an actionable
  error into the conversation

#### Scenario: Tool results correlate by call id

- **WHEN** a tool result is sent back after a tool call
- **THEN** it carries the `tool_call_id` of the call it answers

#### Scenario: Ollama-only parameters are never sent

- **WHEN** a request is sent to the `openai` provider
- **THEN** the request body contains neither `think` nor `keep_alive`

### Requirement: Every advertised tool call is answered

When an assistant turn advertises tool calls, the conversation SHALL contain a result for
every one of them before the next turn is requested, including calls that were never
executed because the loop parked or the turn was canceled.

An unexecuted call SHALL be answered with a result that says so, rather than being left
unanswered. A conversation that advertises a call with no matching result is rejected by
at least one supported provider, and because the whole conversation is replayed on every
turn the rejection surfaces on a later turn rather than the one that created it.

#### Scenario: Parking answers the calls it did not execute

- **WHEN** a turn emits several tool calls and one returns the parked disposition before
  the rest have run
- **THEN** the conversation carries a result for every advertised call, and the call
  survives the next turn

#### Scenario: A canceled turn leaves no unanswered call

- **WHEN** a turn is canceled part-way through dispatching its tool calls
- **THEN** the calls that did not run are recorded as not executed

### Requirement: Reasoning is filtered out of content on every provider

Reasoning SHALL be quarantined into the thinking field regardless of how the provider
delivers it. A provider that returns reasoning in its own field SHALL have it read from
there. Reasoning that a model folds into `content` — as `<think>` tags, or in a
provider-specific reasoning field on an OpenAI-compatible response — SHALL be removed
from content before it is eligible for TTS.

Content SHALL be filtered rather than trusted: an unterminated or unmatched reasoning tag
SHALL be treated as reasoning, so a truncated response cannot leak a scratchpad to a
caller.

#### Scenario: Inline think tags are removed from spoken content

- **WHEN** a model returns content containing `<think>...</think>`
- **THEN** the enclosed text is moved to thinking and only the remainder is eligible for TTS

#### Scenario: An unterminated reasoning tag does not leak

- **WHEN** a model returns content with an opening reasoning tag and no closing tag
- **THEN** everything after the tag is treated as reasoning and nothing of it is spoken

#### Scenario: A provider reasoning field is not spoken

- **WHEN** an OpenAI-compatible response carries reasoning in its own field alongside content
- **THEN** the reasoning is reported as thinking and only content is eligible for TTS
