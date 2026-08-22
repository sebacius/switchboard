## 1. Contract additions (no behavior change, ships green against Ollama)

- [x] 1.1 Add `ID string \`json:"id,omitempty"\`` to `llm.ToolCall` and `ToolCallID string \`json:"tool_call_id,omitempty"\`` to `llm.NativeMessage` in `internal/signaling/llm/native.go` — per design.md decision #7
- [x] 1.2 Set `ToolCallID: call.ID` alongside the existing `ToolName` at `internal/signaling/agent/runner.go:413`
- [x] 1.3 Test: marshalling an Ollama tool result and an assistant tool-call message emits neither `id` nor `tool_call_id` — the wire body is byte-identical to today's
- [x] 1.4 Test in `internal/signaling/agent`: a tool result carries both `ToolName` and the originating `ToolCall.ID`

## 2. Answer every advertised tool call

- [x] 2.1 In `runner.go` `executeTools`, backfill a synthetic `{Role:"tool", ToolName, ToolCallID, Content:"not executed: ..."}` for each call not dispatched before the parked (`:419`) and canceled (`:396`) early returns — per design.md decision #9
- [x] 2.2 Test: a turn emitting two tool calls where the first parks leaves a result for both, and a second turn still succeeds (single-turn tests cannot observe this)
- [x] 2.3 Test: a turn canceled mid-dispatch records the undispatched calls as not executed

## 3. Package restructure (pure refactor, no new provider)

- [x] 3.1 Delete `internal/signaling/llm/client.go` — `Chat`, `ChatWithModel`, `Conversation`, `Say`, `TurnCount`, `Message`, `ChatRequest`, `ChatResponse`, `Choice` are dead; per design.md decision #10
- [x] 3.2a **Follow-up**: a bare provider name (`--llm-model openai`) selects that provider with
  a per-provider default model (`DefaultModel`, `DefaultOllamaModel`/`DefaultOpenAIModel`) and its
  default endpoint. Without it `openai` has no slash, falls through the no-prefix rule, and is
  looked up as an Ollama model literally named `openai` — the silently-wrong outcome the parse rule
  exists to prevent, reached from the other side. `openai/` resolves the same way. Tests pin the
  default constants, prove a bare name is not read as an Ollama model, and cover the config-level
  resolution; verified live (`--llm-model openai` → `openai` / `https://api.openai.com` / `gpt-4o`)
- [x] 3.2 New `provider.go`: `Provider` type + `ProviderOllama`/`ProviderOpenAI`, `DefaultProvider`, `DefaultOllamaServer`/`DefaultOpenAIServer`, `Providers()`, `Config`, `New(Config) (Client, error)`, `DefaultServerURL`
- [x] 3.3 New `ollama.go`: `Client` → `OllamaClient`, absorbing `ChatNative` from `native.go` and `listModels`/`HasModel`/`Warm` from `probe.go`; export `ListModels`; drop the `"llama3"` model fallback (every caller passes a model, and a wrong default is worse than an explicit failure)
- [x] 3.4 Shrink `native.go` to the provider-agnostic contract: `ChatClient`, `Prober`, `Client`, `ProbeProfile`, and the shared message/tool/result types
- [x] 3.5 `probe.go`: `ProbeAndWarm(ctx, Prober, model, log)` driven by `ProbeProfile` — per design.md decision #8
- [x] 3.6 Rename `AsOllamaTools` → `AsToolDefs` in `tools.go`
- [x] 3.7 Mechanical updates to `probe_test.go`, `native_test.go`, `app.go`, `cmd/agent-smoke/main.go`; every existing assertion must pass unmodified — that is the regression bar for this step

## 4. Reasoning filter

- [x] 4.1 New `thinking.go`: `stripThinkTags(s) (content, thinking string)` — fast path when no tag is present; orphan closing tag treated as leading reasoning; unterminated opening tag treated as reasoning to end of string (fails closed); multiple spans joined
- [x] 4.2 Apply it in the Ollama client, closing the measured `qwen3:4b` leak — per design.md decision #5
- [x] 4.3 Test `stripThinkTags` directly: no-tag identity, single span, multiple spans, orphan close, unterminated open, tags spanning newlines, tag after real content

## 5. The OpenAI provider

- [x] 5.1 New `openai.go`: `OpenAIClient`, unexported `oai*` wire structs, `ErrUnauthorized`
- [x] 5.2 `toOpenAIMessages`: drop `Thinking` and `ToolName`, emit `tool_call_id` on tool results, emit `content` non-`omitempty`, synthesize a tool-call id when one is missing — per design.md decision #6
- [x] 5.2a **Review fix**: the synthesized-id fallback originally derived the call id and the
  result id independently, so they could not match — a well-formed-looking request that is still a
  400, which is precisely what the fallback exists to prevent. Results with no id of their own are
  now correlated back to the advertised call by tool name. The original test asserted only that both
  ids were non-empty, which is what let this through; it now asserts they are equal, and a second
  test covers several un-ided calls in one turn
- [x] 5.3 `ChatNative`: POST `/v1/chat/completions`, bearer auth only when a key is present, no `think`, no `keep_alive`; map `reasoning_content`/`reasoning` and stripped `<think>` spans to `Thinking`
- [x] 5.4 `decodeToolArguments`: empty, whitespace, `null` and unparseable all yield a non-nil empty map without failing the turn, so the existing self-correction path handles it
- [x] 5.5 `ListModels` (GET `/v1/models`), `Ready`, no-op `Warm`, `ProbeProfile` with `ModelListAuthoritative: false`
- [x] 5.6 Non-200: prefer the provider's own `error.message` over the raw body; wrap `ErrUnauthorized` on 401/403

## 6. Wiring

- [x] 6.1 `config.go`: `Load() (*Config, error)`; `LLMProvider`, `LLMModelRef`, `LLMModel` (bare id), resolved `LLMServerURL`; `flag.Visit` for explicit `--llm-server` — per design.md decision #3
- [x] 6.2 `config.go`: warn when `--llm-keep-alive` is set explicitly for a provider that ignores it
- [x] 6.3 `cmd/signaling/main.go`: handle the `Load` error; banner gains an `LLM Provider` row and a credentials-present indicator that never shows the value
- [x] 6.4 `app.go:186-203`: build through `llm.New`, reading `OPENAI_API_KEY` at the call site so the key never enters `Config` — per design.md decision #4
- [x] 6.5 `app.go`: set `Timeout: cfg.FirstTurnTimeout + 30*time.Second`, fixing the 60s transport cap that currently overrides the 90s first-turn budget
- [x] 6.6 `cmd/agent-smoke/main.go`: `--model` accepts `[provider/]model`, route construction through `llm.New`

## 7. Tests

- [x] 7.1 `provider_test.go`: `ParseModelRef` table — bare, explicit prefix, nested slashes, case folding, whitespace, `openai/`, `/gpt-4o`, empty, unknown provider; assert the `hf.co/...` error contains `ollama/hf.co/`
- [x] 7.2 `provider_test.go`: endpoint defaults per provider; explicit endpoint preserved
- [x] 7.3 `provider_test.go`: key required only for the hosted endpoint (including a trailing-slash form), not for a gateway; no error string contains the key
- [x] 7.4 `openai_test.go` with a `fakeOpenAI` httptest server: request shape (no `think`, no `keep_alive`, bearer present/absent), message translation (no `thinking`, no `tool_name`, `tool_call_id` present, empty `content` present)
- [x] 7.5 `openai_test.go`: string arguments decoded; malformed/empty/`null` arguments yield empty args and no error
- [x] 7.6 `openai_test.go`: reasoning table — `reasoning_content`, inline `<think>`, unterminated, orphan close, multiple spans, both sources at once — content never carries reasoning
- [x] 7.7 `openai_test.go`: 429 surfaces the provider message, 401 satisfies `errors.Is(err, ErrUnauthorized)`, `Ready()` reflects `/v1/models`
- [x] 7.8 `probe_test.go`: the OpenAI probe issues zero chat requests, and a model missing from an advisory listing does not warn
- [x] 7.9 `config_test.go` (first in the repo): provider/endpoint resolution, `flag.Visit` explicitness, env precedence
- [x] 7.10 `go build ./... && go vet ./... && go test ./internal/... -race` clean; `golangci-lint run` clean; `go mod tidy` no drift; no new dependencies

## 8. Live verification

Run against a local Ollama (`llama3.1:8b`), against Ollama's own OpenAI-compatible
`/v1` as a stand-in gateway, and against the real `api.openai.com`.

- [x] 8.1 `--llm-model llama3.1:8b` against real Ollama — banner and boot unchanged apart from
  the new provider rows, warm-up line present, `keep_alive` still sent:
  ```
  [INFO] LLM supervisor configured provider=ollama server=http://localhost:11434 model=llama3.1:8b keep_alive=30m
  [INFO] LLM ready and warmed load_time=849ms note=this is what the first caller would otherwise have waited inside their turn budget
  ```
  `ollama/llama3.1:8b` resolves identically. A missing model still warns with the Ollama hint:
  `LLM server is up but does NOT have the configured model ... Pull it (ollama pull) or fix --llm-model available=[llama3.1:8b]`
- [~] 8.2 `--llm-model openai/gpt-4o` against **the real api.openai.com** — **partially executed:
  no paid key available.** With a deliberately invalid key the probe reached OpenAI and the 401
  was classified correctly rather than reported as an unreachable server, which is the branch
  that could only be wrong against the real API:
  ```
  [INFO] LLM supervisor configured provider=openai server=https://api.openai.com model=gpt-4o
  [WARN] LLM server rejected our credentials; every supervised call will fail. Check OPENAI_API_KEY.
         Deterministic routing is unaffected error=... (status 401) from https://api.openai.com/v1/models
  ```
  No warm-up line, no `keep_alive`, no `ollama pull` text. The key appears **zero** times in the
  log. The working path is covered by 8.3/8.4 against a real `/v1` server.
- [x] 8.3 Two-turn tool-calling history against a live `/v1` server — the case that cannot fail on
  turn one. Server-issued id round-trips, JSON-string arguments decode, and the replayed history
  is accepted:
  ```
  turn 1: content="" tool_calls=1
    call: id="call_tszxveds" name="lookup_extension" args=map[name:Dana]
  turn 2 OK: content="Dana's extension is 105. Is there anything else I can help you with?"
  ```
  Kept as `internal/signaling/llm/live_manual_test.go` behind a `manual` build tag:
  `LLM_LIVE_SERVER=... LLM_LIVE_MODEL=... go test -tags manual -run LiveTwoTurn ./internal/signaling/llm/ -v`
- [x] 8.4 `--llm-model openai/llama3.1:8b --llm-server http://localhost:11434` with **no key** —
  boots unauthenticated and probes clean:
  ```
  LLM Provider     : openai
  LLM Server       : http://localhost:11434
  LLM Auth         : none (unauthenticated)
  [INFO] LLM ready note=hosted provider: no model load to absorb, so no warm-up was sent
  ```
  A real supervised turn through this path dispatched a tool call end to end
  (`>>> dial user/110 (post-answer bridge)`), proving the /v1 tool round trip through the runner.
- [x] 8.5 `--llm-model openai/gpt-4o` with no key — exits 1 naming the variable. **Correction to
  the original wording:** it fails just *after* the banner, not before, because the credential
  rule lives in the one place that owns it (`newOpenAIClient`) rather than being duplicated into
  config. That ordering is better, not worse — the banner shows `LLM Auth: none (unauthenticated)`
  immediately above the error, so the resolved state and the reason are adjacent. `ps` shows no
  secret; there is no flag that could carry one.
- [x] 8.6 `--llm-model anthropic/claude-3` — exits 2:
  `unknown provider "anthropic" (valid: ollama, openai). If ... write it as "ollama/anthropic/claude-3"`.
  `--llm-model hf.co/user/repo:Q4_K_M` — exits 2 with the `ollama/hf.co/user/repo:Q4_K_M` hint.
- [x] 8.7 Reasoning never reaches TTS on either provider — see §10.

## 9. Documentation

- [x] 9.1 `README.md` "Native tool calling" — currently argues against `/v1`; rewrite to cover both bindings and how reasoning is quarantined on each, keeping the no-third-party-library statement
- [x] 9.2 `README.md` AI Services / Running with AI Services — OpenAI example with the key as an env var; note that reasoning models are a poor fit for the turn budget
- [x] 9.3 `docs/CONFIGURATION.md` supervisor table — `[provider/]model`, per-provider endpoint default, `OPENAI_API_KEY` row, `--llm-keep-alive` marked Ollama-only
- [x] 9.4 `CLAUDE.md` folder map — `llm/` is no longer "Ollama native /api/chat client"

## 10. Reasoning quarantine — measured, both providers

Verified against `qwen3:4b`, the model the `llm-availability` change measured writing its chain
of thought into `content` on 4/4 trials with `think: false`. Three prompts written to provoke
reasoning ("think carefully, then…", "work out step by step, then…"), asserting no `<think>`
marker survives into spoken content.

- [x] 10.1 **openai provider over `/v1`** — the endpoint with no separate reasoning field, where
  the leak this repo originally avoided actually lives. The model reasoned on all three prompts
  and none of it reached the spoken half:
  ```
  prompt 1
    SPOKEN:   "Hello, this is Acme Corp, and I can assist you today."
    THINKING: "We are a phone receptionist. We must greet the caller who is calling Acme Corp in one sentence.  The greeting should be professional and friendly. ..."
  prompt 2
    SPOKEN:   "No, 17 multiplied by 23 equals 391, which is less than 400."
    THINKING: "Okay, the user wants me to act as a phone receptionist who speaks directly to the caller. I need to work through whether 17 times 23 is more than 400 step by st..."
  ```
- [x] 10.2 **ollama provider over `/api/chat`** — same model, same prompts, reasoning separated
  and never spoken:
  ```
  prompt 2
    SPOKEN:   "Hello, 17 multiplied by 23 equals 391, which is less than 400, so it's not more than 400."
    THINKING: "Okay, the user wants me to act as a phone receptionist and work out whether 17 times 23 is more than 400. I need to do this step by step and then tell the calle..."
  ```
- [x] 10.3 Kept as `internal/signaling/llm/leak_manual_test.go` behind the `manual` build tag, so
  it never runs in CI and never needs a network:
  `LLM_LIVE_SERVER=... LLM_LIVE_MODEL=qwen3:4b LLM_LIVE_PROVIDER=openai go test -tags manual -run LiveReasoning ./internal/signaling/llm/ -v`
