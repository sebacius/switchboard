## Context

`internal/signaling/llm` is one package doing three jobs: it defines the contract the
agent runner depends on, it implements the Ollama transport, and it carries a dead
OpenAI-compatible chat path from before the supervisor existed. The contract is already
clean — `ChatClient` (`native.go:14`) has one production call site
(`agent/runner.go:347`) and one test double (`scripted.go`) — but everything behind it
assumes Ollama: `/api/chat`, `/api/tags`, `think`, `keep_alive`, and a `thinking` field
that arrives separately from `content`.

That last detail is why the repo chose Ollama's native endpoint in the first place.
`README.md:263-273` records the reasoning: `/v1/chat/completions` folds a model's
reasoning into `<think>` tags inside `content`, which is version-dependent and one bad
parse away from reading a model's scratchpad to a caller. Adding an OpenAI provider means
adopting the endpoint that decision rejected, so the leak it was avoiding has to be
handled explicitly rather than assumed away.

## Goals / Non-Goals

**Goals:**

- One parameter selects the provider, with existing deployments unchanged.
- The agent runner stays provider-agnostic — no provider knowledge above `ChatClient`.
- Reasoning cannot be spoken on either provider, including when a model folds it into content.
- OpenAI-compatible gateways work through the same path as OpenAI itself.
- No new dependency: both clients stay hand-rolled `net/http`.

**Non-Goals:**

- ASR and TTS. `internal/rtpmanager/asr` and `.../tts` have their own clients and share
  nothing with this package. Pointing them at a hosted provider is a separate change.
- Streaming. Neither provider streams here; the runner is turn-based.
- A provider registry or plugin surface. Two providers do not justify one, and a `switch`
  in one factory function is easier to read than an indirection.
- Reasoning-model support (`o1`, `o3`, `gpt-5`). They work, but their latency routinely
  exceeds the mid-call turn budget. Documented, not engineered around.

## Decisions

**0. A bare provider name selects that provider with its default model.** `--llm-model
openai` resolves to provider `openai`, endpoint `https://api.openai.com`, model `gpt-4o`;
`--llm-model ollama` resolves to `qwen3:8b` on localhost. `openai/` says the same thing.

The trap this closes is specific. `openai` has no separator, so under decision 1 alone it
falls through the back-compat rule and is read as an *Ollama model literally named
"openai"* — which fails at the probe complaining about a model nobody asked for. That is
the silently-wrong outcome the parse rule exists to prevent, reached from the other side.

The cost is accepted deliberately: the default models become part of the public interface,
so changing either constant changes the model, the latency and the per-minute cost of every
deployment using the short form. The startup banner always prints the *resolved* model,
which is what keeps the delegated choice visible.

*Alternative rejected:* erroring on a bare provider name. It is the safer default — nothing
pinned in code, no silent change on upgrade — but it turns the most natural thing to type
into a failure, and the flag default already pinned `qwen3:8b` long before this change, so
the precedent for a code-chosen model was already set.

**1. The provider is a prefix on the model identifier, not its own flag.** `--llm-model
openai/gpt-4o`. Two ways to say the same thing is how a deployment ends up with a
provider and a model that disagree, and the model id is the thing an operator actually
edits when they change providers.

The split is on the **first** separator, because a model id may contain more:
`openai/meta-llama/llama-3.1-70b` is provider `openai`, model `meta-llama/llama-3.1-70b`.
No separator at all means the default provider, which is what every deployment passes today.

*Alternative rejected:* a separate `--llm-provider`. It removes the parsing question and
creates a synchronisation one, which is worse — a wrong prefix fails at boot, a
disagreeing pair fails on the first call.

**2. An unknown prefix is a hard startup error.** The tempting fallback — "an
unrecognized prefix is part of an Ollama model name" — silently sends every call in a
deployment to Ollama under the name `openia/gpt-4o` when someone fat-fingers the
provider. Refusing to start is the cheaper failure.

The cost is real and is the one breaking change here: `hf.co/user/repo:Q4_K_M`,
`myuser/mymodel` and `library/llama3` are valid `ollama run` names that no longer parse
bare. The error message therefore contains the exact working replacement
(`ollama/hf.co/user/repo:Q4_K_M`), and a test asserts that it does — the message *is* the
migration path, so it is load-bearing rather than cosmetic.

**3. `flag.Visit` decides whether the endpoint was set explicitly.** The endpoint default
has to depend on the provider, which means distinguishing "the operator chose
localhost:11434" from "the operator left the default".

*Alternative rejected:* an empty-string default resolved later. It blanks the default out
of `--help`, where it is currently useful, and it collides with the existing "no LLM
server configured" failure whose meaning would quietly change.

*Alternative rejected:* comparing the value against the default string. An operator
running an OpenAI-compatible gateway that happens to listen on `:11434` would be silently
redirected to `api.openai.com`.

**4. The API key never enters `Config`.** It is read with `os.Getenv` at the one
construction site and stored only in the unexported `OpenAIClient.apiKey`. `Config` is
echoed by the startup banner and passed around freely; a secret that never enters it
cannot leak from it. This is why the key is not a flag either — flags are visible in `ps`
and printed in the banner.

The key is required only when the endpoint is the hosted API. A self-hosted gateway with
no auth is a normal deployment, and demanding a placeholder key would train operators to
invent one.

**5. Reasoning is filtered out of content on both providers, not just OpenAI.** The
obvious scope is the new provider, since `/v1` has no `thinking` field. But
`openspec/changes/archive/2026-08-21-llm-availability/tasks.md` records `qwen3:4b`
writing its chain of thought into `content` on 4/4 trials **with `think: false`** — an
existing, measured violation of the "Reasoning never reaches TTS" scenario on the
supposedly-safe path. One shared `stripThinkTags` closes both.

The filter fails *closed*: an unterminated `<think>` with no closer, which is what a
`max_tokens`-truncated response looks like, is treated as reasoning through to the end of
the string. Failing open there would leak exactly what the function exists to prevent.

*Alternative rejected:* trusting `think: false` on the Ollama path. It is a request, not
a guarantee, and it has been measured not holding.

**6. A real struct translation for `/v1`, not shared json tags.** It is tempting to put
both dialects' tags on `NativeMessage` and let `omitempty` sort it out. That breaks on
the first tool result: OpenAI rejects unknown message properties, so `thinking` and
`tool_name` must be *absent*, not empty. The OpenAI client therefore owns unexported
`oai*` wire structs and a `toOpenAIMessages` translation.

Two consequences of that translation are worth stating rather than discovering:
`Thinking` is dropped, so on this provider the model does not see its own prior
scratchpad (the runner keeps it only for that purpose — `runner.go:363-370`); and
`content` is emitted non-`omitempty`, because an assistant message with neither content
nor tool calls is a 400 and `runner.go:363` appends whatever came back.

**7. Tool-call correlation costs two `omitempty` fields on the shared types.**
`ToolCall.ID` and `NativeMessage.ToolCallID`. Ollama returns no tool-call ids, so both
stay empty on that path and are omitted — the Ollama request body is byte-identical to
today's, and a test asserts it.

*Alternative rejected:* a per-provider conversation representation threaded through the
runner. That pushes provider knowledge above `ChatClient`, which is precisely what the
interface exists to prevent.

**8. `ProbeProfile` instead of a provider switch in the probe.** `ProbeAndWarm` stays one
function and reads a small struct from the client: whether the model listing is
conclusive, whether warming does anything, what hint to log, and how long to allow. This
is what keeps "run `ollama pull`" from being logged at an operator whose provider has no
such notion, and what keeps a partial gateway listing from raising a false alarm about a
model that works fine.

**9. Fix the unanswered-tool-call gap now, in the runner, for both providers.**
`executeTools` (`runner.go:388-425`) returns early on the parked disposition and on turn
cancellation, leaving calls the assistant message advertised with no result. Ollama
tolerates it. OpenAI rejects the replayed history outright.

This is the change's most dangerous property: the conversation is replayed in full every
turn, so the malformed history is sent on the turn *after* the one that created it —
an unpark, typically. It cannot fail a single-turn test. The fix is provider-agnostic and
makes the Ollama history more honest too, so it belongs in the runner rather than in the
OpenAI translation.

**10. Delete the dead `/v1` code rather than reuse it.** `client.go`'s `Message`,
`ChatRequest`, `ChatResponse` and `Choice` look like a head start on the OpenAI client
and are a trap: no `tool_calls`, no `tool_call_id`, no null-content handling. Leaving a
stale implementation of `/v1/chat/completions` beside a real one is worse than having
neither.

## Risks / Trade-offs

- **Namespaced Ollama model names break.** Decision 2, accepted deliberately. Mitigated
  by an error message that names the replacement, a test on that message, and a migration
  note. No default or documented example uses one.
- **`<think>`-stripping changes existing Ollama behavior.** A model that legitimately
  emitted the literal string `<think>` in speech would now have it swallowed. No model
  does this in practice, and the alternative is a measured leak.
- **The unanswered-tool-call fix touches the runner**, the most safety-critical file in
  the change. It is additive — a synthetic result where there was previously nothing — and
  it needs a two-turn test, since a single-turn test cannot observe it.
- **Raising the HTTP timeout to exceed the first-turn budget** means a genuinely hung
  connection is now held for the budget instead of 60s. That is the correct trade: the
  budget is the number the operator configured, and holding to it is what makes the
  timeout log honest.
- **OpenAI emits parallel tool calls more readily than qwen3.** The runner already loops
  over them, but it increases the chance of hitting the unanswered-call path — another
  reason decision 9 is in scope rather than deferred.

## Migration Plan

1. No action for an existing Ollama deployment whose `--llm-model` has no separator: the
   default provider, endpoint and behavior are unchanged.
2. An operator running a namespaced Ollama model adds the `ollama/` prefix. Startup names
   the exact replacement if they do not.
3. To move to OpenAI: set `--llm-model openai/<model>` and export `OPENAI_API_KEY`. To use
   a compatible gateway, also set `--llm-server`.
4. `--llm-keep-alive` on a provider that ignores it warns and is otherwise inert; no
   config file needs editing.
5. Rollback: `git revert`. No config migration — every new setting has a default, and the
   only breaking input is rejected at startup rather than silently reinterpreted.

## Open Questions

- The per-provider default models (`qwen3:8b`, `gpt-4o`) are now part of the public
  interface: an operator writing the short form has delegated the choice to this repo, so
  bumping either constant changes the model, latency and per-minute cost of their
  deployment without their config changing. Should a future bump be treated as a breaking
  change with a migration note, or is the resolved model on the startup banner enough to
  keep it visible?
- Whether the now-dead `llm.ToolRegistry` (`llm/tools.go`, exercised only by its own
  tests — production builds tool defs through `agent.ToolDefs`) should be deleted. Out of
  scope here; flagged for a follow-up.
- Whether ASR and TTS should adopt the same `provider/model` convention when they gain
  hosted support, or whether their existing bare-URL configuration is better left alone.
