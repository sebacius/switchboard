## REMOVED Requirements

### Requirement: LLM supervises every INVITE
**Reason**: The LLM is removed from the call path entirely. Routing needs one to three deterministic steps inside the INVITE transaction; conversation needs VAD, barge-in, streaming TTS, and turn-taking. Conflating them made the PBX own realtime-voice complexity it is badly placed to solve.
**Migration**: Calls that resolve in one hop are handled by `call-resolution` as before. Everything else is handled by `call-flows`, which expresses the routing decisions the supervisor was making — menus, prompts, conditional dialing — as a validated graph. Conversational handling returns later as an external agent reached like any other destination.

### Requirement: LLM-driven answer — forward versus engage media
**Reason**: There is no model to decide when to answer.
**Migration**: Preserved as a structural rule in `call-flows`: a dial reached before any media node forwards without answering, and once any node plays media the call is answered. The observable behaviour for a direct extension call is unchanged.

### Requirement: Nested context scopes
**Reason**: The runner that owned `callCtx ⊃ turnCtx ⊃ playbackCtx` is deleted.
**Migration**: The spine survives in `call-flows` as flow ⊃ node ⊃ playback. It was the right shape and is carried forward deliberately.

### Requirement: Idempotent teardown funnel
**Reason**: Attached to the supervisor runner's lifecycle.
**Migration**: `call-flows` requires that per-call flow state is released when the call ends by any path, including a caller who abandons mid-menu, which is the property this requirement existed to guarantee.

### Requirement: Event-loop dispatch with safe producers
**Reason**: The event loop existed to serialise asynchronous speech events from an ASR producer. Flow execution is synchronous and step-driven, so there is no loop and no producer goroutine to make safe.
**Migration**: None needed. DTMF, the only remaining caller input, is consumed synchronously by the node that asked for it — see `digit-collection`.

### Requirement: Runaway-turn breaker
**Reason**: Bounded consecutive autonomous model turns. With no model there are no autonomous turns.
**Migration**: Superseded by a stronger structural guarantee: `call-flows` requires the inter-node graph to be acyclic with repetition bounded by per-node counters, making every flow provably terminating rather than merely bounded at runtime.

### Requirement: Conversation persists for the whole call
**Reason**: There is no conversation.
**Migration**: The per-call state that persists is the flow cursor — current node, buffered digits, retry counts, traversal — specified in `call-flows`.

### Requirement: Supervisor unavailability does not end a resolvable call
**Reason**: The failure mode this protected against cannot occur; there is no supervisor to be unavailable.
**Migration**: Strengthened into an unconditional property: `call-flows` requires that routing works with no model, agent, or network egress reachable at all. The signaling server no longer requires a reachable LLM to boot.

### Requirement: The supervisor verifies and warms its model at startup
**Reason**: No model to probe, warm, or keep resident.
**Migration**: None. The startup work that remains is configuration validation, specified in `call-flows` and exposed as a `validate` subcommand.

### Requirement: The supervisor's provider is selected by the model identifier
**Reason**: Both providers are deleted, including the OpenAI provider added one commit before this change. This is a deliberate reversal of a recent decision, not an oversight.
**Migration**: None. Provider selection, credentials, endpoint defaults, and the reasoning filter all disappear with the `llm` package.
