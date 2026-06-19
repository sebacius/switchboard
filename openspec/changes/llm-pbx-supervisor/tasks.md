## 1. Prerequisite & branch

- [x] 1.1 Land `basic-sip-trunk` first; this change depends on its trunk recognition + DID→tenant routing
- [x] 1.2 Cut/continue the branch off main (not `llm-tool-registry` or `llm-supervisor-phase1`); create `internal/signaling/agent/`

## 2. LLM client: native /api/chat tool calling

- [x] 2.1 Add a `Client` interface in `internal/signaling/llm/` and point the implementation at Ollama native `/api/chat` (not `/v1/chat/completions`), sending `think: false` — `ChatClient` + `ChatNative` (old `/v1` kept for the dialplan until deletion)
- [x] 2.2 Model request/response with separate `thinking`, `content`, and `tool_calls` fields; only `content` is eligible for TTS
- [x] 2.3 Add `llm/tools.go`: `AgentTool`, `ToolRegistry`, `Handler`, and the `/api/chat` tool-definition converter (`AsOllamaTools`)
- [ ] 2.4 Update the conversation to append tool-result messages back into history (`Continue` disposition) — deferred to the runner phase (NativeMessage supports `role=tool`/`tool_name`)
- [x] 2.5 Add `llm/scripted.go`: `ScriptedClient` implementing `Client`, returning pre-programmed thinking/text/tool-call sequences

## 3. Agent package: session, context, events, nested-ctx spine

- [x] 3.1 Move `CallSession` interface + impl from `dialplan/session.go` to `agent/session.go` (+`DialError`/`ErrUserNotFound`); dialplan keeps a transitional alias shim (`agent_compat.go`) so it compiles until deletion
- [x] 3.2 Add `agent/context.go`: `CallContext{Caller, Callee, Direction, Tenant}` + `FormatForPrompt()`
- [x] 3.3 Add `agent/events.go`: `Event{Kind, Payload}` + `EventKind` enum (speech now; dtmf/signaling/media forward-compat); never-close-channel discipline documented
- [ ] 3.4 Establish the three nested context scopes (`callCtx ⊃ turnCtx ⊃ playbackCtx`) in the runner skeleton
- [ ] 3.5 Implement the idempotent `teardown(reason)` funnel (`sync.Once`, gated by `IsTerminated`): cancel callCtx, release parking slot, CANCEL/BYE B-leg, release tenant channel, destroy RTP; pre-answer 487 vs post-answer BYE branch

## 4. Router & admission

- [ ] 4.1 Add `agent/router.go`: direction classification (directory-user via full-AOR lookup / trunk-origin / external) consuming `basic-sip-trunk`
- [ ] 4.2 Tenant resolution: subdomain for internal/outbound, DID→tenant for inbound; no default — unresolved/unloaded → reject
- [ ] 4.3 Add `agent/admission.go`: deterministic preflight (tenant loaded, prompt non-empty) + per-tenant channel semaphore; reject pre-answer (4xx/486); acquire slot, release via teardown

## 5. Tools, per-call registry & policy

- [ ] 5.1 Add `agent/tools.go`: build the tool registry **per call** from `(tenant, direction)`; inbound has no external dial
- [ ] 5.2 Implement `dial` with forward-vs-bridge behavior (port B2BUA dial from `action_dial.go`); forward before answer, bridge after
- [ ] 5.3 Implement `hangup` (Terminal), `play_audio` (Continue), `park` (Parked — no blocking), `unpark` (port `BridgeMedia` from `action_unpark.go`)
- [ ] 5.4 Add `agent/policy.go` (`tool-authorization`): zero-authority adjudication; Class of Service on dial (default-deny external, allowlist, barred classes); capability narrowing via symbolic targets + a separate gated caller-provided-number tool; per-tenant spend circuit breaker; decision logging to the call record
- [ ] 5.5 Arg validation + self-correction: unknown tool → hangup; missing/invalid arg → actionable error into conversation; refuse identical just-failed call

## 6. Runner: turns, first-turn decision, runaway breaker

- [ ] 6.1 `agent/runner.go` `HandleCall`: create events channel + conversation; do NOT pre-answer
- [ ] 6.2 First-turn single-shot decision: tool-only `dial` → forward (no answer); text → 200 OK + media + listen/speak; both → answer then act
- [ ] 6.3 `dispatchTurn`: `/api/chat` call under `turnCtx`; native tool-call dispatch; TTS `content` under `playbackCtx`
- [ ] 6.4 `speechLoop` producer (ASR via `Listen`, ctx-honoring, safe-send); dispatch loop drains channel, exits on `callCtx.Done()`
- [ ] 6.5 Runaway breaker: bound consecutive autonomous turns (reset on caller input); soft cap → reactive-only; hard cap → deterministic message + teardown; per-call LLM-call budget backstop
- [ ] 6.6 Leave the barge-in interrupt lane stubbed (a `playbackCtx`-cancelling signal) for a future media-VAD producer; do not implement onset detection now

## 7. Rewire & delete dialplan

- [ ] 7.1 `routing/invite.go`: defer the 200 OK; classify direction; run admission; per-call tool registry; hand to `agent.Runner.HandleCall` at the old `executeDialplan` hook
- [ ] 7.2 `app/app.go`: remove dialplan wiring; construct runner, router, admission, policy; inject into invite handler
- [ ] 7.3 `filemanager/filemanager.go`: drop dialplan reload (reload tenant prompts / routes as applicable)
- [ ] 7.4 `config/config.go` + `cmd/signaling/main.go`: drop `DialplanPath`; add model (qwen3:8b), channel limits, COS/spend config, native LLM endpoint
- [ ] 7.5 `api/handlers_config.go`: stop serving `dialplan.json`
- [ ] 7.6 Delete `internal/signaling/dialplan/` (13 files) + `resources/config/dialplan.json`
- [ ] 7.7 Update `resources/config/settings.md` (native tool calling, semi-public prompt rules) and `resources/tenants/*.md` (direction-aware, `*7XX` → unpark, no secrets)

## 8. Smoke harness

- [ ] 8.1 Add `cmd/agent-smoke/main.go`: real runner vs real Ollama with a fake `CallSession` (stdin → speech; stdout → tool dispatches + `>>>` TTS); flags `--llm-server --model --tenant --caller --callee --direction`

## 9. Tests & verification

- [ ] 9.1 Scripted-client runner tests: silent internal forward (first-turn tool-only), IVR answer path, forward-then-CANCEL race + orphaned B-leg cleanup, concurrent teardown runs once, park `Parked` disposition until cancel, runaway breaker soft/hard caps, admission reject (unloaded tenant + channel limit), COS deny of external dial, inbound registry has no external dial
- [ ] 9.2 `go test ./internal/signaling/...` passes; `go build ./...` clean; `go mod tidy` no drift (zero new deps)
- [ ] 9.3 Smoke (think:false, qwen3:8b): `--direction internal --caller 102 --callee 105` → immediate `>>> dial user/105`, no text; inbound DID → greeting + intent routing; "goodbye" → `>>> hangup`; verify no reasoning leaks into TTS
- [ ] 9.4 End-to-end SIP (Ollama+Whisper+Piper+softphones): internal forward (no answer, real ringback), inbound DID via trunk, park/unpark `*701`, per-tenant channel-limit 486, COS deny of an external dial
- [ ] 9.5 Update `README.md` (supervisor model, native Ollama, trunk dependency, removed dialplan)
