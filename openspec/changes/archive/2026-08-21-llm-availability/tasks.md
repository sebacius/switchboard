## 1. Keep the model resident

- [x] 1.1 Add `keep_alive` to `nativeChatRequest` (`llm/native.go`) and thread it from the client
- [x] 1.2 Add `Config.KeepAlive` + `DefaultKeepAlive` ("30m", longer than Ollama's 5m) in `llm/client.go`
- [x] 1.3 Add `--llm-keep-alive` / `LLM_KEEP_ALIVE` and wire it through `app.go`

## 2. Verify and warm at startup

- [x] 2.1 Add `llm/probe.go` with `listModels`, `HasModel`, `Warm`, `ProbeAndWarm`, mirroring `rtpmanager/server/probe.go` in structure and posture
- [x] 2.2 Log the warm-up duration explicitly as the time a caller would otherwise have waited
- [x] 2.3 Run it in a goroutine from `app.New` so a cold load never blocks SIP startup
- [x] 2.4 Replace the `serverURL != ""` stub in `Client.Ready()` with a real `/api/tags` check

## 3. Separate turn budgets

- [x] 3.1 Add `RunnerConfig.FirstTurnTimeout` (default 90s) alongside `TurnTimeout` (30s)
- [x] 3.2 Make `runTurn` take the budget as a parameter so each call site names the one it means
- [x] 3.3 Add `--turn-timeout` and `--first-turn-timeout` (+ env) and wire both into the runner

## 4. Make the failure legible

- [x] 4.1 Split `llmUnavailable` on `context.DeadlineExceeded` vs everything else, reporting elapsed time and budget

## 5. Tests

- [x] 5.1 `llm/probe_test.go`: model present warms, model missing does not, unreachable server does not hang, `HasModel` both ways
- [x] 5.2 `Ready()` reflects the server, not the flag — including a URL pointing at nothing
- [x] 5.3 `keep_alive` is present on every chat request, and the default exceeds Ollama's 5m
- [x] 5.4 `agent/runner_budget_test.go`: the first turn gets the first-turn budget and a mid-call turn gets the shorter one; both have defaults and first >= mid
- [x] 5.5 `go build ./... && go vet ./... && go test ./internal/... -race` clean; `go mod tidy` no drift; no new dependencies

## 6. Live verification

- [x] 6.1 Start against a real Ollama and confirm the warm-up line: `LLM ready and warmed load_time=6.774s`
- [x] 6.2 Configure a model the host does not have → `LLM server is up but does NOT have the configured model ... available=[gemma3:4b qwen3:8b llama3.2:3b qwen3:0.6b]`
- [x] 6.3 Point at a dead LLM → `LLM server did not answer ... Deterministic routing is unaffected`, and the server still starts
- [x] 6.4 With the LLM unreachable, a call to a registered extension still completes: `100 → 180 → 200 → BYE → 200`, `resolved=true kind=endpoint`
- [ ] 6.5 On the reporting deployment: restart, confirm the warm-up line, replay the `230 → 600` call, then leave it idle >5 minutes and place another — the second is what `keep_alive` exists for

## 7. Model choice — measured, default unchanged

- [x] 7.0 Evaluate `qwen3:4b` as a faster default and record the result (below)

### Why the default stays qwen3:8b (measured 2026-08-20, homelab)

Prompted by "how do we make the model use qwen3:4b". The smaller model halves the
prompt-evaluation cost, which is what the 30s timeout incident was about — so it
looked like the obvious fix. It is not. Two trials per case, `think: false`, real
`settings.md` + `default.md` (3467 tokens), `/api/chat` with the `dial` and
`hangup` tools advertised:

| Model | Greeting | "I need to file a claim" | Reasoning in `content` |
| --- | --- | --- | --- |
| **qwen3:8b** | 19 tok / 82 ch, no tool | 20 tok, **empty content + `dial(claims)`** | 0 / 4 trials |
| qwen3:4b | 1552–4027 tok / 7012–18720 ch | `dial(claims)` — after 3236–6390 ch of monologue | **4 / 4 trials** |
| qwen3:0.6b | 10 tok / 35 ch, no tool | 10–18 tok, **no tool call** | 0 / 4 trials |

`qwen3:4b` **fails the `agent-tools` requirement "Reasoning never reaches TTS"**.
With `think: false` it left `thinking` empty and wrote its chain of thought into
`content` — the field the runner speaks — on every trial. A caller would have
heard *"Okay, let's tackle this. So, the user is calling from 5551234…"* read
aloud; one trial produced 18,720 characters and took 257.9s to generate.

Its prompt evaluation really is faster (23.8s vs 44.0s cold), but generation
erases it many times over: **148s total versus 56s** for the 8b on the same
prompt. Faster at reading, catastrophically slower at answering.

`qwen3:0.6b` respects `think: false` and is genuinely quick (0.1–0.2s), but did
not emit a tool call for an explicit routing request in any trial — it greets and
stops. A supervisor that cannot dial is not a supervisor.

`qwen3:8b` was correct in 4/4 trials and reproducibly picked the same symbolic
target the routing table defines. It stays the default. The archived
`llm-pbx-supervisor` chose it after a spike on exactly this question; this is
that spike re-run against the alternatives, and it holds.

Operators can still override with `--llm-model` — but a smaller Qwen3 is not the
lever for first-turn latency. Warm-up plus `keep_alive` is.

## 8. Documentation

- [x] 8.1 Document `--llm-keep-alive`, `--turn-timeout`, `--first-turn-timeout` in `docs/CONFIGURATION.md`, and the startup probe in `README.md`
