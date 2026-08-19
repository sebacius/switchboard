## 1. Prerequisite & branch

- [x] 1.1 Land `basic-sip-trunk` first; this change depends on its trunk recognition + DID→tenant routing
- [x] 1.2 Cut/continue the branch off main (not `llm-tool-registry` or `llm-supervisor-phase1`); create `internal/signaling/agent/`

## 2. LLM client: native /api/chat tool calling

- [x] 2.1 Add a `Client` interface in `internal/signaling/llm/` and point the implementation at Ollama native `/api/chat` (not `/v1/chat/completions`), sending `think: false` — `ChatClient` + `ChatNative` (old `/v1` kept for the dialplan until deletion)
- [x] 2.2 Model request/response with separate `thinking`, `content`, and `tool_calls` fields; only `content` is eligible for TTS
- [x] 2.3 Add `llm/tools.go`: `AgentTool`, `ToolRegistry`, `Handler`, and the `/api/chat` tool-definition converter (`AsOllamaTools`)
- [x] 2.4 Update the conversation to append tool-result messages back into history (`Continue` disposition) — implemented in the runner (`role=tool` messages + autonomous re-prompt)
- [x] 2.5 Add `llm/scripted.go`: `ScriptedClient` implementing `Client`, returning pre-programmed thinking/text/tool-call sequences

## 3. Agent package: session, context, events, nested-ctx spine

- [x] 3.1 Move `CallSession` interface + impl from `dialplan/session.go` to `agent/session.go` (+`DialError`/`ErrUserNotFound`); dialplan keeps a transitional alias shim (`agent_compat.go`) so it compiles until deletion
- [x] 3.2 Add `agent/context.go`: `CallContext{Caller, Callee, Direction, Tenant}` + `FormatForPrompt()`
- [x] 3.3 Add `agent/events.go`: `Event{Kind, Payload}` + `EventKind` enum (speech now; dtmf/signaling/media forward-compat); never-close-channel discipline documented
- [x] 3.4 Establish the three nested context scopes (`callCtx ⊃ turnCtx ⊃ playbackCtx`) in the runner skeleton
- [x] 3.5 Implement the idempotent `teardown(reason)` funnel (`sync.Once`, gated by `IsTerminated`): cancel callCtx + Hangup done; parking slot / B-leg / tenant channel / RTP releases + pre-answer-487-vs-BYE branch left as TODO hooks for their owning phases (admission/parking/B2BUA/routing)

## 4. Router & admission

- [x] 4.1 Add `agent/router.go`: direction classification (trunk-origin first, then registered directory-user via `Directory` adapter over `LocationStore`) consuming `basic-sip-trunk`; takes a plain `RouteInput` (no sipgo import)
- [x] 4.2 Tenant resolution: subdomain (leftmost label) for internal/outbound, DID→tenant for inbound; no default — unresolved → `ok=false` (group 7 maps to reject)
- [x] 4.3 Add `agent/admission.go`: deterministic preflight (`PromptSource` non-empty) + per-tenant channel semaphore with idempotent `Release`; returns decision+slot (group 7 maps reject → 4xx/486 and wires `Release` into teardown)

## 5. Tools, per-call registry & policy

- [x] 5.1 Add `agent/tools.go`: build the tool registry **per call** from `(tenant, direction)`; inbound has no external dial (affordance removed in `BuildRegistry`)
- [x] 5.2 Implement `dial`: policy-gated, runs against the resolved target via the existing adopt-and-bridge `session.Dial`. **Forward-before-answer path is `TODO(group7)`** (needs answer-deferral + B2BUA INVITE forwarding)
- [x] 5.3 `hangup` (Terminal) + `play_audio` (Continue) implemented; **`park`/`unpark` deferred to group 7** (need `parking.Service` wiring + the answer model) — `TODO(group7)`
- [x] 5.4 Add `agent/policy.go` (`tool-authorization`): zero-authority adjudication; Class of Service on dial (default-deny external, allowlist, barred classes); capability narrowing via symbolic targets + a separate gated caller-provided-number tool; per-tenant spend circuit breaker; decision logging (slog now, `TODO(cdr)` for the call record)
- [x] 5.5 Arg validation + self-correction: unknown tool → Terminal/hangup; missing/invalid arg → actionable result + Continue; refuse identical just-failed call

## 6. Runner: turns, first-turn decision, runaway breaker

- [x] 6.1 `agent/runner.go` `HandleCall`: create events channel + conversation (no-pre-answer is `routing/invite.go`'s job in group 7; the runner never answers)
- [x] 6.2 First-turn single-shot decision: speak-then-route / silent-route / content-only implemented; forward-vs-answer SIP semantics realized by the tool handlers (group 5) + answer-deferral (group 7). **Design #11's discipline completed after live testing**: `CallContext.FirstTurnDirective()` is appended AFTER the tenant prompt (position matters — a 633-line tenant prompt buried the rule), and `callRun.correctSilentRoute` gives an internal first turn that returned prose exactly ONE corrective re-prompt. The rejected prose is discarded before `speak()`, because speaking would both greet the caller and send the 200 OK, destroying the forward path irreversibly.
- [x] 6.3 `dispatchTurn` (`runTurn`): `ChatNative` under `turnCtx`; native tool-call dispatch via `ToolExecutor`; TTS `content` under `playbackCtx`
- [x] 6.4 `speechLoop` producer (ASR via `Listen`, ctx-honoring, safe-send via `sendEvent`); dispatch loop drains channel, exits on `callCtx.Done()`; `teardownWG` prevents goroutine leak
- [x] 6.5 Runaway breaker: bound consecutive autonomous turns (reset on caller input); soft cap → reactive-only; hard cap → deterministic message + teardown
- [x] 6.6 Barge-in interrupt lane stubbed (`playbackCtx`/`playbackCancel` hook + `TODO(barge-in)`); onset detection deferred to the media layer

## 7. Rewire & delete dialplan

- [x] 7.1 `routing/invite.go`: no 183/200 OK; ingress gate -> `Router.Route` (404 on unresolved) -> `Admission.Admit` (486 channel limit / 404 unloaded) -> `agent.NewSession` holding the answer SDP -> `Runner.HandleCall` with the admission release as a teardown hook
- [x] 7.2 `app/app.go`: dialplan wiring removed; builds `PromptStore`, `PolicyConfig`, `Router`, `Admission`, and the `Runner` (per-call `BuildExecutor` -> policy + registry + `CallExecutor`); LLM server is now required
- [x] 7.3 `filemanager/filemanager.go`: dialplan get/put/reload removed; `Reload` now refreshes tenant prompts via `PromptStore.ReloadSettings`
- [x] 7.4 `config/config.go` + `cmd/signaling/main.go`: `DialplanPath` dropped; added `--llm-model` (qwen3:8b), `--tts-voice`, `--policy-config`; channel limits + COS/spend live in `resources/config/policy.json` (`agent.LoadPolicyConfig`)
- [x] 7.5 `api/handlers_config.go`: `GetDialplan`/`PutDialplan` and the `/api/v1/config/dialplan` route removed
- [x] 7.6 Deleted `internal/signaling/dialplan/` (13 files) + `resources/config/dialplan.json` (and `docs/DIALPLAN.md`, which documented the deleted subsystem)
- [x] 7.7 `settings.md` rewritten (tools not text, direction decides the first move, prompt hardening / semi-public); `tenants/default.md` gained direction-aware handling + `*7XX` -> unpark and tool-based transfer rules

## 8. Smoke harness

- [x] 8.1 Added `cmd/agent-smoke/main.go`: real runner vs real Ollama, fake `CallSession` (stdin -> speech; `>>> answer|tts|forward|dial|hangup`); flags `--llm-server --model --tenant --caller --callee --direction` (+ `--settings-path --tenants-path --policy-config --verbose`)

## 9. Tests & verification

- [x] 9.1 Scripted-client runner tests — all nine scenarios, in `agent/runner_scenarios_test.go` unless noted. They drive the real runner + real `CallExecutor` + real `Policy` against `llm.ScriptedClient`:
  - silent internal forward → `TestSilentInternalForwardNeverAnswers` (tool-only first turn ⇒ `Forward`, never `Dial`, `HasAnswered()==false`, nothing spoken, `thinking` not leaked)
  - IVR answer path → `TestIVRAnswerPathAnswersThenConverses` (content ⇒ 200 OK, greeting then follow-up, never forwards)
  - forward-then-CANCEL race → `TestForwardThenCancelUnwindsCleanly` (CANCEL mid-ring ⇒ `HandleCall` returns, never answered, one teardown)
  - orphaned B-leg cleanup → `TestOrphanedBLegIsCancelledOnTeardown` (drives the **real** `sessionImpl.hangupBLeg`, asserts idempotence)
  - concurrent teardown runs once → `TestTeardownIdempotent` (5 goroutines; one `Hangup` **and** one teardown-hook run) + `TestAdmissionSlotReleasedOnceByTeardown` (slot actually returns to the pool)
  - park `Parked` until cancel → `TestParkedDispositionHoldsCallUntilCancel` (parked, answered, call held, **model not re-prompted**, teardown only on cancel)
  - runaway breaker soft/hard → `TestRunawayBreaker`, `TestRunawaySoftCapWaitsForCaller`
  - admission reject → `TestUnloadedTenantRejectedWithoutLLM` + `agent/admission_test.go` (`TestAdmitUnloadedTenantRejected`, `TestAdmitAtLimitRejected`, `TestAdmitConcurrent`)
  - inbound registry has no external dial → `TestInboundRegistryOmitsDialAndUnpark` (also proves `unpark` is internal-only and `dial` is never *advertised* inbound)
  - added beyond the list: `TestExecutorDialAfterAnswerBridges` / `TestExecutorFailedDialIsRecoverable` (forward-vs-bridge + failed dial stays `Continue`), `TestRunnerAdvertisesExecutorRegistry` (an empty advertised tool list would masquerade as a model-quality problem), `TestMisconfiguredRunnerStillReleasesResources` (a bad config fails every call, so a leaked slot there drains a tenant in seconds), and `agent/prompts_test.go` (prompt store + policy-config loading)
- [x] 9.2 `go test ./internal/signaling/...` passes (66 tests, also clean under `-race`); `go build ./...` and `go vet ./...` clean; `go mod tidy` no drift, zero new deps (the LLM client is `net/http` against Ollama)
- [x] 9.3 Smoke (think:false, qwen3:8b) — **run live against Ollama on the homelab box**. Results and the two defects it exposed:
  - **PASS** silent internal forward, short tenant prompt: `>>> forward user/105`, no TTS line, `HasAnswered()` false, `decision=allow reason="internal target"` — the pre-answer forward path works end to end against a real model.
  - **FAIL then FIXED** silent internal forward, real `resources/tenants/default.md` (633 lines): the model **greeted** instead of routing (`>>> tts: "I'm here to help!..."`). Root cause was design decision #11's self-correction retry never having been implemented — task 6.2 was checked off without it. Fixed: see 6.2 below.
  - Latency on that box: 19.5s (4-line tenant) vs 57s (633-line tenant). Well past the 30s turn deadline, which is deliberately under SIP Timer B — so a large tenant prompt is a real SLA problem on slow inference, not just a correctness one. `--turn-timeout` was added to `cmd/agent-smoke` to measure it (and the harness now widens the LLM client's HTTP timeout to match, since the 60s HTTP default otherwise fires first).
  - Also verified: an empty first-turn response (model returns no content and no tool calls) is handled correctly — the runner answers and converses rather than hanging.
  - **Not re-confirmed live after the fix**: the corrected run needs ~2 min/turn on that hardware (two LLM round-trips). The behaviour is covered by four scripted-client tests instead — see 9.1. Re-run with a small model for a fast live check: `--model qwen3:0.6b` (already on the box).
- [~] 9.4 End-to-end SIP — **partially executed; blocked then unblocked mid-test**. A real softphone call reached the supervisor (INVITE -> CreateSession -> PlayTTS -> Listen), which proved the SIP/media path, but **every `Listen` failed**: `internal/rtpmanager/asr/client.go` never sent the `model` field the OpenAI transcription API requires, so the ASR server returned 422 and the supervisor could never hear the caller. Verified against the live server (TTS-synthesised a phrase, posted it both ways): without `model` -> the 422; with `model=Systran/faster-whisper-base` -> `{"text":"I would like to speak to someone about a claim."}`. **Fixed** (see below). The remaining scenarios still need a trunk peer and two registered softphones:
    ```bash
    docker run -d --name whisper-asr -p 8001:8000 -e WHISPER__MODEL=Systran/faster-whisper-tiny fedirz/faster-whisper-server:latest-cpu
    docker run -d --name piper-tts -p 8000:8000 ghcr.io/matatonic/openedai-speech
    ollama serve & ollama pull qwen3:8b
    go run cmd/rtpmanager/main.go --asr-server http://localhost:8001 --tts-server http://localhost:8000 &
    go run cmd/signaling/main.go --llm-server http://localhost:11434 --llm-model qwen3:8b &
    ```
  Register two softphones as `102@default.<advertise>` and `105@default.<advertise>` (the From-host's leftmost label is the tenant, so it must be `default` to match `resources/tenants/default.md`).
  - **internal forward**: 102 dials 105 → `100 Trying`, `180 Ringing`, **no 183 and no 200 from Switchboard**; 105's phone rings audibly; on answer the caller gets 105's relayed `200 OK`. On no-answer the caller gets 105's own `486`/`480`, not an AI apology.
  - **inbound DID via trunk**: add the peer IP to `trunk_peers.json` and the DID→`default` row to `routes.json`; INVITE from the peer → greeting + intent routing. An unmapped DID → `603 Declined`; an unknown source IP → `403`.
  - **park/unpark `*701`**: inbound caller asks to hold → `park` → hold music, slot announced; 102 dials `*701` → `unpark` → the two are bridged. Confirm the slot frees on either party hanging up.
  - **channel-limit 486**: set `"default": {"channel_limit": 1}` in `policy.json`, place two concurrent calls → the second gets `486 Busy Here` **before** any LLM round-trip.
  - **COS deny**: with `allow_external_dial:false` (the shipped default), ask the agent to dial an outside number → no INVITE leaves the box, the deny is logged (`tool authorization decision ... decision=deny`), and the agent offers an alternative instead of dropping the call.
- [x] 9.5 `README.md` rewritten: supervisor-on-every-INVITE call path, deferred answer (forward vs engage-media table), two-layer authorization, native `/api/chat` + `think:false`, the nested-scope runner, trunk dependency, `qwen3:8b`, the `agent-smoke` recipe, and a config-file table. Roadmap/"what works" updated; dialplan references removed. Also updated `docs/CONFIGURATION.md` (supervisor + policy.json reference, dialplan flags gone) and deleted `docs/DIALPLAN.md`.

### Defects found by live testing (both fixed)

1. **ASR `model` field missing** (`internal/rtpmanager/asr/client.go`) — every transcription 422'd, so the supervisor never heard a caller. Added `Model` to the client config (default `Systran/faster-whisper-base`), threaded an `--asr-model` flag / `ASR_MODEL` env through `rtpmanager/config` -> `server` -> `cmd`, and added `asr/client_test.go` (the package had no tests). Also added a startup readiness probe (`rtpmanager/server/probe.go`) that GETs `/v1/models` on both TTS and ASR and warns loudly — a swapped port previously produced a perfectly healthy-looking banner and only failed mid-call.
2. **Internal calls greeted instead of routing** — design #11's self-correction retry was never implemented. See 6.2.

Strictly, defect 1 is in the RTP manager and outside this change's scope; it is included because it blocked 9.4 end-to-end verification.

### Group 9 status legend

`[~]` marks a task whose automatable parts were verified but whose live-service
run could not be executed in this environment. 9.3 needs `qwen3:8b` pulled
(~5 GB); 9.4 needs Whisper, Piper, a trunk peer, and two softphones. Neither is
blocked by code — the commands and expected results are recorded above.
