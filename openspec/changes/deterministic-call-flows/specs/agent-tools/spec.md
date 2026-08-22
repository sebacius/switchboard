## REMOVED Requirements

### Requirement: Native tool calling on Ollama /api/chat
**Reason**: The `llm` package and both provider clients are deleted; there is no chat transport left to speak a wire protocol over.
**Migration**: None.

### Requirement: Per-call tool registry scoped by tenant and direction
**Reason**: This was affordance removal applied to an untrusted model — an inbound caller's toolset carried no external dial at all. With no model emitting tool calls there is no affordance surface to scope.
**Migration**: The security property it delivered is preserved by different means and stated in `call-flows`: `dial_external` accepts symbolic targets only, no matched digit string can become a dial target, and every flow-produced destination is adjudicated by the tenant's Class of Service. Direction remains a trust gradient in `call-routing`.

### Requirement: Call-setup tool inventory with forward-versus-bridge dial
**Reason**: `dial`, `hangup`, `play_audio`, `park`, and `unpark` were model-facing tools.
**Migration**: The underlying primitives survive as node types in `call-flows` — `dial_user`, `dial_external`, `transfer`, `play_audio`, `tts`, `hangup` — and the forward-before-answer versus bridge-after-answer rule is carried forward there verbatim. Park and unpark are unchanged: `*7XX` retrieval still works and is evaluated before flow entry.

### Requirement: Handler disposition
**Reason**: `Continue`/`Terminal`/`Parked` were control signals for the supervisor's event loop, which no longer exists.
**Migration**: Replaced by the exit contract in `call-flows`, where each node type's exits are fixed in code and `answered`/`accepted` are terminal.

### Requirement: Argument validation and self-correction
**Reason**: Existed because a model emits malformed and repeated tool calls at runtime, and had to be told how to correct itself.
**Migration**: Superseded by load-time validation. `call-flows` requires unknown node types, unknown exits, unwired exits, dangling targets, unreachable nodes, and cycles to be rejected when the configuration loads. A deterministic graph cannot emit a malformed call at 2am, so there is nothing to self-correct.

### Requirement: OpenAI-compatible tool calling
**Reason**: The OpenAI provider is deleted along with the rest of the `llm` package.
**Migration**: None.

### Requirement: Every advertised tool call is answered
**Reason**: A constraint on replayed conversation history, which no longer exists.
**Migration**: None.

### Requirement: Reasoning is filtered out of content on every provider
**Reason**: Existed to stop model reasoning from being spoken aloud. Nothing generates reasoning.
**Migration**: None. Spoken text is now authored directly as TTS text in flow nodes.
