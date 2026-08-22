## 1. Per-tenant routing file

- [x] 1.1 Define the routing-file schema in a new `internal/signaling/agent/routing_config.go`: `operator`, `retrieval_prefix`, `extensions`, `symbolic_targets`, `dids`, `groups` (strategy, members, member_timeout_ms, no_answer) — per design.md decision #3
- [x] 1.2 Add a `RoutingStore` alongside `agent.PromptStore` (`agent/prompts.go`) that loads `resources/tenants/<tenant>.routing.json` per tenant; a missing file yields an empty table, a malformed file fails loudly naming the file
- [x] 1.3 Add `--routing-path` flag / `ROUTING_PATH` env to `internal/signaling/config/config.go` and `cmd/signaling/main.go` (defaulting alongside `--tenants-path`)
- [x] 1.4 Wire `RoutingStore` reload into `internal/signaling/filemanager/filemanager.go` so `/api/v1/config/reload` refreshes routing tables with prompts
- [x] 1.5 Author `resources/tenants/default.routing.json` from the data currently in `default.md` §5.1, §6.1, §8.2, §9.1, §9.2 and `policy.json`'s `symbolic_targets`
- [x] 1.6 Author `resources/tenants/devtenant.routing.json` from `devtenant.md`'s extension table

## 2. Authorization config migration

- [x] 2.1 Source `symbolic_targets` from the routing table instead of `policy.json` in `internal/signaling/agent/policyconfig.go` and `policy.go`'s `resolveSymbol`
- [x] 2.2 Make `agent.LoadPolicyConfig` fail loudly when a tenant block still carries `symbolic_targets`, naming the tenant and the routing file it belongs in (design.md #3 — no silent merge)
- [x] 2.3 Remove `symbolic_targets` from `resources/config/policy.json` and update its inline `_comment_symbolic` documentation

## 3. Resolver and call-path rewiring

- [x] 3.1 Add `internal/signaling/agent/resolver.go`: `Resolve(cc CallContext) (Destination, bool)` over the tenant routing table, covering exactly the four resolvable shapes in the `call-resolution` spec — registered directory extension, `*7XX` from an `internal` caller, mapped inbound DID, named ring group
- [x] 3.2 Route every resolved destination through `Policy.AuthorizeDial` with the same decision logging as a model-issued dial (spec: "Resolution stays inside the authorization boundary")
- [x] 3.3 Split admission in `internal/signaling/agent/admission.go`: tenant-loaded preflight stays before resolution; the prompt-non-empty check and the channel-slot acquisition move to the supervisor hand-off
- [x] 3.4 Rewire `internal/signaling/routing/invite.go` to the new order — preflight → dialog/media/180 → `Resolver.Resolve` → destination or hand-off — keeping the existing slot-release-on-every-failure-path discipline
- [x] 3.5 Execute a resolved single destination as a pre-answer forward (the existing `session.Forward` path), so the caller still hears real ringback and no 200 OK is sent by Switchboard
- [x] 3.6 Execute a resolved `*7XX` retrieval through the existing `parking.Service.Unpark` + bridge path; an empty slot declines resolution and falls through to the supervisor

## 4. Ring group engine

- [x] 4.1 Implement `b2bua.CallService.DialParallel` (`internal/signaling/b2bua/call_service.go:241`, currently `ErrNotImplemented`): originate to all targets, first answer wins, cancel the rest
- [x] 4.2 Implement the `sequential` strategy: members in configured order, each for `member_timeout_ms`
- [x] 4.3 Implement the `round-robin` strategy with a per-(tenant, group) in-memory cursor that advances per call
- [x] 4.4 Implement `no_answer` outcomes — `supervisor` (hand off with context intact), `operator`, `hangup` — and the group ring budget
- [x] 4.5 Ensure group legs are canceled and released through the existing teardown funnel on caller CANCEL/BYE mid-ring

## 5. Runner changes

- [x] 5.1 Delete `correctSilentRoute` and `silentRouteCorrection` (`internal/signaling/agent/runner.go:342`, `374-410`) and the first-turn call site — deleted, not tuned (design.md #5)
- [x] 5.2 Change the `ChatNative` failure posture at `runner.go:322`: play a deterministic "assistant unavailable" message and tear down deliberately instead of returning an error that drops the call
- [x] 5.3 Remove the scripted tests that exist only to cover `correctSilentRoute`; keep every other runner scenario test green

## 6. Unknown-tool disposition

- [x] 6.1 Replace the `DispositionTerminal` unknown-tool branch (`internal/signaling/agent/tools.go:143-147`) with a deterministic transfer to the tenant's configured `operator`
- [x] 6.2 Fall back to an actionable error-and-continue when the tenant has no `operator` configured, so an unknown tool never hangs up on a caller

## 7. Tenant prompt slimming

- [x] 7.1 Delete `resources/tenants/default.md` §2.5 "Call Handling by Direction" — direction is a struct field already computed in `agent/router.go`
- [x] 7.2 Delete the routing data now held in the routing file: §5.1 staff directory, §6.1 destination columns (keep the intent keywords), §8.2 ring group members, §9.1/§9.2 extension tables
- [x] 7.3 Confirm `default.md` is well under 200 lines and that what remains is judgement — identity, tone, business facts, hours, scenarios, escalation language, authentication rules
- [x] 7.4 Update `resources/config/settings.md` where it instructs the model about direction-driven first moves that the resolver now owns
- [x] 7.5 Re-measure first-turn latency with `cmd/agent-smoke --turn-timeout` on the slimmed prompt and record it against the §9.3 baselines (19.5s / 57s) — **measured on homelab (qwen3:8b, Ollama at `homelab:11434`); see "First-turn latency, measured" below**

## 8. Tests

- [x] 8.1 Resolver unit tests for each resolvable shape and each non-resolvable case in the `call-resolution` spec, including `*7XX` from a non-internal caller and an unregistered extension
- [x] 8.2 Test that a resolved destination outside the tenant allowlist is denied by `Policy.AuthorizeDial` and no INVITE leaves the system
- [x] 8.3 Ring group tests: sequential order, round-robin cursor advancing across calls, first-answer-wins canceling the losers, and each `no_answer` outcome
- [x] 8.4 Admission-split tests: a tenant at its channel limit still routes a resolvable extension call (no 486, no slot), while a hand-off at the limit still gets 486
- [x] 8.5 Test that a tenant with an empty prompt but a populated routing table routes extensions, and that the same tenant rejects a call needing the supervisor
- [x] 8.6 Test the unknown-tool operator transfer and the no-operator fallback
- [x] 8.7 Test that a `ChatNative` failure does not drop a supervised call, and that a resolvable call succeeds with the LLM client failing every request
- [x] 8.8 `go build ./...`, `go vet ./...`, `go test ./internal/... -race` clean; `go mod tidy` produces no drift and no new third-party dependency

## 9. End-to-end SIP verification

Carries forward the four scenarios the archived `llm-pbx-supervisor` change left at `[~]`
(its §9.4). Needs Whisper, Piper, Ollama, a trunk peer, and two registered softphones
(`102@default.<advertise>` and `105@default.<advertise>`).

- [x] 9.1 **Internal forward, re-run**: 102 dials 105 → `100 Trying`, `180 Ringing`, no 183 and no 200 from Switchboard, 105 rings, caller gets 105's relayed `200 OK`. This path was verified in the archived change but changes here, so it is re-run — and it must now complete with Ollama stopped
- [x] 9.2 **Inbound DID via trunk**: peer IP in `trunk_peers.json`, DID→`default` in `routes.json`, DID→destination in the tenant routing file. A DID mapped to `assistant` greets and triages; a DID mapped to a group rings the group; an unmapped DID → `603 Declined`; unknown source IP → `403`
- [ ] 9.3 **Park/unpark `*701`**: inbound caller parked → hold music and slot announced; 102 dials `*701` → resolved deterministically (no LLM) → the two are bridged; slot frees when either party hangs up
- [x] 9.4 **Channel-limit 486**: with `"default": {"channel_limit": 1}`, two concurrent calls needing the supervisor → the second gets `486 Busy Here` before any LLM round-trip; two concurrent *resolvable* extension calls both succeed
- [~] 9.5 **COS deny**: with `allow_external_dial:false`, ask the agent to dial an outside number → no INVITE leaves the box, `tool authorization decision ... decision=deny` is logged, and the agent offers an alternative instead of dropping the call
- [x] 9.6 **Ring group live**: an inbound DID mapped to a sequential group rings members in order; a round-robin group starts at a different member on the second call

Not re-run: the sipp ingress gate, which was verified in the archived change and is
untouched here.

## 10. Documentation

- [x] 10.1 Update `CLAUDE.md`: its folder-structure section still lists `internal/signaling/dialplan/` and `resources/config/dialplan.json` (both deleted in the archived change), and its call-flow diagram still shows the `183 → 200 OK` sequence the supervisor deliberately removed
- [x] 10.2 Add the resolver, the per-tenant routing file, and the deterministic-vs-supervised call path to `CLAUDE.md`'s architecture overview and folder structure
- [x] 10.3 Update `README.md`: the call path is now resolve-then-supervise; document the routing file and the LLM-outage degradation
- [x] 10.4 Update `docs/CONFIGURATION.md` with the routing-file reference, the `--routing-path` flag, and the `policy.json` `symbolic_targets` removal and its startup error

## Group 9 results — executed live (2026-08-19)

Test bed: signaling + rtpmanager on this host (192.168.50.241), AI services on
`homelab`, endpoints driven with `sipp`. Two or more endpoints registered per
scenario; the host itself configured as the trunk peer for the inbound tests.

**9.1 internal forward — PASS, twice.** `102` dials `110`:
`INVITE → 100 Trying → 180 Ringing → 200 OK → ACK → BYE → 200`. No 183, and the
200 is `110`'s own answer relayed. Server side: `call resolution ... resolved=true
kind=endpoint reason=registered endpoint user/110`, then
`tool authorization decision tool=resolve_dial ... decision=allow`, then
`Forwarding (pre-answer)` → `Forward bridged`. **Zero LLM requests for the call.**
Re-run with `--llm-server http://127.0.0.1:1` (nothing listening): identical
result. Extension dialing survives a total LLM outage, which is the change's
central claim.

**9.2 inbound DID via trunk — PASS.** Unmapped DID `+15559999999` →
`603 Declined - unmapped DID`. DID `+15558001200` mapped to `assistant` →
`resolved=false kind=handoff reason=routing table maps this target to the
assistant`, supervisor answers with 200 and speaks. DID `+15558001250` mapped to
a group → rings the group (see 9.6). An INVITE from a source that is neither a
registered user nor a trunk peer → `403 Forbidden - unknown source` (observed when
a registration lapsed mid-test).

**9.4 channel limit — PASS, both halves.** With `channel_limit: 1` and the single
channel held by a supervised call: a second supervised call got
`486 Busy Here - tenant at channel limit` before any LLM round-trip, while a
**resolvable extension call placed at the same moment got 200 OK and was
forwarded**. That second half is the regression the admission split exists to
prevent — before it, a tenant at its LLM limit would have received 486 on a plain
extension dial that costs nothing.

**9.6 ring group — PASS, both strategies.** Sequential (`claims`: 130 → 120 →
110): round 1 rang `user/130` for the full 15s member timeout, round 2 skipped
`user/120` (`Ring group member unreachable ... no registrations found`), round 3
reached `user/110`, which answered and bridged — order honoured, unreachable
member skipped rather than failing the group. Every member was adjudicated
individually first (`tool authorization decision tool=resolve_group_member` ×3).
Round-robin, four consecutive calls to the same group:

```
call 1 → user/920, user/921, user/922
call 2 → user/921, user/922, user/920
call 3 → user/922, user/920, user/921
call 4 → user/920, user/921, user/922      (cursor wrapped)
```

**9.5 COS deny — PARTIAL.** The resolution half is verified live: an extension
mapped in the tenant's own routing table to `+18005551212`, dialed internally
with `allow_external_dial:false`, produced
`resolved=false ... destination +18005551212 is not registered` and no INVITE
left the box. The half in the task as written — the *model* asking to dial an
outside number, being denied, and offering an alternative — needs a spoken
conversation and was not executed (see below).

**9.3 park/unpark — NOT EXECUTED.** The `*NNN` path requires an occupied parking
slot, and a slot only becomes occupied when the supervisor decides to park a
caller. That decision needs a two-way spoken conversation, which `sipp` cannot
drive (it sends no speech for ASR to transcribe). Needs a human on a softphone.

### Defect found by live testing: no final response could ever reach a caller

**This is pre-existing and outside this change's scope, but it blocked every
group 9 scenario, so it is fixed here.**

`sipgo`'s server calls `tx.Terminate()` as soon as the request handler returns
(`server.go` `handleRequest`: *"Must be called to prevent any transaction
leaks"*). `HandleINVITE` handed the call to a goroutine and returned immediately,
so the INVITE server transaction was destroyed while the call was still being
set up. `ServerTx.Respond` then spins an already-finished FSM and **returns nil
without writing anything** — so the response was logged as sent and never left
the box.

Observed directly: `[Dialog] Sent 200 OK` in the log, `sipp` receiving only
`100` and `180` and then timing out. It affected every final response — the
supervisor's own 200, a forward's relayed 200, and a late 486 — while pre-answer
rejections sent from inside the handler (404, 403, 603) worked, because the
handler had not returned yet.

Fix: run the call on the handler's goroutine instead of spawning one
(`routing/invite.go`). This is the intended usage — sipgo's transaction layer
already dispatches every request on its own goroutine
(`transaction_layer.go`: `go txl.handleRequest(msg)`). The `go` predates this
change; it came in with the archived `llm-pbx-supervisor`, whose §9.4 recorded
the internal-forward expectations without being able to run them.

## First-turn latency, measured (2026-08-19, homelab, qwen3:8b)

The archived §9.3 reported single samples of 19.5s (4-line tenant) and 57s
(633-line tenant) and concluded prompt length was the cause. Re-measuring exposed
a confound: **Ollama caches the prompt prefix**, so a repeated call with an
unchanged tenant prompt evaluates it in ~0.3s. Runs through `agent-smoke` were
correspondingly all over the place — 11s, 29s, 56s, in prompt-size-INVERTED
order. Forcing a cache miss per run (unique Call Context) makes it stable and
linear:

| Prompt | Tokens | Cold prompt eval | Cold total | Warm total |
| --- | --- | --- | --- | --- |
| settings.md + slim `default.md` (182 lines) | 3473 | 44.1s | 53.2–54.2s | 6.8–10.4s |
| settings.md + `devtenant.md` (35 lines) | 1605 | 19.7s | 24.1–24.7s | 5.0–5.2s |

Prompt evaluation is ~12.7ms/token (~79 tok/s) on this box and scales linearly;
generation adds 4–9s. `settings.md` alone accounts for roughly 1100 of those
tokens, a fixed ~14s cold floor every tenant pays.

Two conclusions worth carrying forward:

1. **Slimming worked but does not solve the latency problem.** A cold first turn
   for the slimmed `default` tenant is still ~54s, well past the 30s turn
   deadline and SIP Timer B. Prompt size was never going to get an 8B model on
   this hardware inside the INVITE transaction — which is the argument for
   deterministic resolution, not against it. Resolution removes the deadline
   entirely for the calls that have one right answer.
2. **The §9.3 latency figures were partly measuring cache state**, not only
   prompt length. The correctness half of that evidence — the model greeting
   instead of routing — is unaffected and remains the stronger reason for this
   change.

## Status of the unchecked tasks

The AI services are on `homelab` (192.168.50.55), and the ports differ from the
recipe the archived change recorded:

| Service | Endpoint | Note |
| --- | --- | --- |
| Ollama | `http://homelab:11434` | `qwen3:8b` and `qwen3:0.6b` present |
| ASR (Whisper) | `http://homelab:9000` | `Systran/faster-whisper-base` — **not :8001** |
| TTS (Piper/openedai-speech) | `http://homelab:8001` | serves `tts-1` — **not :8000** |

The archived recipe says ASR on 8001 and TTS on 8000; here they are 9000 and 8001,
with an unrelated service answering on 8000. This is exactly the swapped-port
failure the archived change added `rtpmanager/server/probe.go` for.

Task 7.5 is now measured (above). Group 9 still needs a running signaling +
rtpmanager pair, a configured trunk peer, and two registered softphones.

Everything they depend on is in place: the code builds and vets clean, the whole
suite passes under `-race`, `go mod tidy` shows no drift, and the shipped
`policy.json` and both tenant routing tables are loaded and validated by tests
(`TestShippedRoutingTablesLoad`, `TestShippedPolicyConfigLoads`,
`TestShippedSymbolicTargetsResolve`).

The commands and expected results are recorded in each task above; running them
is a matter of bringing the services up.
