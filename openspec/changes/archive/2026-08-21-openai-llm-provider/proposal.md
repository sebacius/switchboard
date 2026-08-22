## Why

The call supervisor can only talk to Ollama. `internal/signaling/llm` is bound to
Ollama's native `/api/chat`, probes `/api/tags`, and sends `think: false` and
`keep_alive` on every request — parameters no other backend understands. That makes
self-hosting a model a hard requirement for running Switchboard at all, and it caps
supervisor quality at whatever a local GPU can hold. A deployment that would rather
pay for a hosted model, or that needs a stronger one than it can run, has no path
that does not involve editing the client.

Nothing about the supervisor's design requires this. `llm.ChatClient` already exists
as the single seam the agent runner talks through (`internal/signaling/llm/native.go:14`,
one call site at `internal/signaling/agent/runner.go:347`). The Ollama binding sits
behind it. A second provider is an implementation of that interface, not a redesign.

## What Changes

- **A provider is selected by a prefix on `--llm-model`**: `openai/gpt-4o`,
  `ollama/qwen3:8b`. The value splits on the **first** slash only, so a model id that
  itself contains slashes (`openai/meta-llama/llama-3.1-70b`) survives. A value with no
  slash means `ollama`, so every existing deployment is untouched.
- **A bare provider name selects that provider with a default model**: `--llm-model openai`
  resolves to `gpt-4o` on `https://api.openai.com`, `--llm-model ollama` to `qwen3:8b` on
  localhost. Without this, `openai` (which has no slash) would fall through the
  no-prefix rule and be looked up as an Ollama model literally named `openai`. The default
  models thereby become part of the public interface; the banner prints the resolved model
  so the choice is visible.
- **An unknown prefix is a startup error**, not a guess. Guessing would send an entire
  deployment's calls to the wrong endpoint over a typo (`openia/gpt-4o`).
- **BREAKING (configuration): a namespaced Ollama model name no longer parses bare.**
  `hf.co/user/repo:Q4_K_M` and `myuser/mymodel` are valid `ollama run` names today and
  now need the explicit `ollama/` prefix. The error message names the exact replacement.
  No default or documented example uses one, so the blast radius is operators running
  custom models.
- **The `openai` provider speaks `/v1/chat/completions`** and honours an explicit
  `--llm-server`, so Groq, vLLM, OpenRouter, LiteLLM and Azure-style gateways work
  through the same code path. Unset, `--llm-server` resolves to the provider's own
  default (`http://localhost:11434` for ollama, `https://api.openai.com` for openai).
- **The API key comes from `OPENAI_API_KEY` only** — never a flag, never a config
  field, never logged. Boot fails fast when the provider is `openai` against
  `api.openai.com` with no key; a self-hosted gateway with no key is allowed.
- **Reasoning is quarantined on both providers.** `/v1` has no separate `thinking`
  field, so a gateway serving a reasoning model folds its scratchpad into `content` as
  `<think>` tags or returns it as `reasoning_content`. Both are mapped to `Thinking`
  and never to `Content`.
- **BREAKING (behavior, existing path): `<think>` tags are now stripped from Ollama
  content too.** The `llm-availability` change measured `qwen3:4b` writing its chain of
  thought into `content` on 4/4 trials *with `think: false`* — a live violation of the
  "Reasoning never reaches TTS" scenario for anyone running that model. One shared
  filter closes it for both providers.
- **Fixes an unanswered-tool-call bug that OpenAI would expose.** `executeTools`
  returns early on `Parked` and on turn cancellation, leaving tool calls the assistant
  message advertised with no matching tool result. Ollama tolerates the gap; OpenAI
  rejects the replayed history with a 400 and the call dies on the *next* turn. Every
  unanswered call now gets a synthetic result.
- **Fixes a pre-existing timeout inversion.** The LLM HTTP client is built with a 60s
  cap while `--first-turn-timeout` defaults to 90s, so a first turn taking 61–90s dies
  on the transport rather than the context and reports as a generic failure.
- The dead legacy `/v1` code in `llm/client.go` (`Chat`, `ChatWithModel`,
  `Conversation`, `Say`, and their wire types) is deleted rather than left beside a
  real implementation of the same endpoint.

## Capabilities

### New Capabilities

<!-- None: this adds a provider behind an existing seam, not a new capability. -->

### Modified Capabilities

- `agent-tools`: the native tool-calling requirement is scoped to the Ollama provider
  binding and names `ChatClient` as the seam; a new requirement covers OpenAI-compatible
  tool calling (JSON-string arguments, `tool_call_id` correlation, no `think`/`keep_alive`)
  and a new requirement makes reasoning quarantine hold for content on every provider,
  not just for a separate `thinking` field.
- `call-supervisor`: a new requirement covers provider selection by model identifier —
  the parse rule, the unknown-prefix error, per-provider endpoint defaults, and
  environment-only credentials. The startup verify-and-warm requirement is revised so
  warming and residency are properties of a provider that has a resident model, and a
  hosted provider verifies availability without a billed warm-up.

## Impact

**New code**: `internal/signaling/llm/provider.go` (provider type, `ParseModelRef`,
`New` factory), `internal/signaling/llm/ollama.go`, `internal/signaling/llm/openai.go`,
`internal/signaling/llm/thinking.go`.

**Modified**: `internal/signaling/llm/native.go` (shrinks to the provider-agnostic
contract; `ToolCall.ID` and `NativeMessage.ToolCallID` added, both `omitempty` and
never emitted on the Ollama path), `internal/signaling/llm/probe.go` (`ProbeAndWarm`
takes a `Prober` and reads a `ProbeProfile`), `internal/signaling/llm/tools.go`
(`AsOllamaTools` → `AsToolDefs`), `internal/signaling/config/config.go` (`Load` returns
an error; resolves provider, model id and endpoint), `internal/signaling/app/app.go:186-203`,
`internal/signaling/agent/runner.go:413` (sets `ToolCallID`) and `:394-425` (backfills
unanswered tool calls), `cmd/signaling/main.go` (banner gains a provider row),
`cmd/agent-smoke/main.go:45,104`.

**Deleted**: `internal/signaling/llm/client.go`.

**Config**: `--llm-model` accepts `[provider/]model`; `--llm-server` default becomes
provider-dependent; `--llm-keep-alive` is ignored for `openai` and warns if explicitly
set; `OPENAI_API_KEY` is read from the environment.

**Docs**: `README.md` "Native tool calling" (currently argues against `/v1` and must
explain both bindings instead), the AI Services and Running-with-AI-Services sections,
`docs/CONFIGURATION.md` supervisor table, `CLAUDE.md` folder map.

**Dependencies**: none added. Both clients are hand-rolled `net/http`, preserving the
"no third-party LLM library" rule.

**Not affected**: deterministic resolution, routing, admission, parking, trunking and
the whole RTP/media path. ASR and TTS keep their own independent clients — pointing
them at OpenAI is a separate change.
