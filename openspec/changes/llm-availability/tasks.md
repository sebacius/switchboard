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

## 7. Documentation

- [x] 7.1 Document `--llm-keep-alive`, `--turn-timeout`, `--first-turn-timeout` in `docs/CONFIGURATION.md`, and the startup probe in `README.md`
