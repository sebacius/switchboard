## 1. Remove the LLM supervisor

- [x] 1.1 Delete `internal/signaling/llm/` in full (15 files, both providers, probe/warm, reasoning filter, scripted client)
- [x] 1.2 Delete `agent/runner.go`, `tools.go`, `executor.go`, `prompts.go`, `handlers.go`, `disposition.go`, `events.go`
- [x] 1.3 Delete their tests: `runner_test.go`, `runner_budget_test.go`, `runner_scenarios_test.go`, `runner_toolcall_test.go`, `tools_test.go`, `prompts_test.go`, `assistant_handoff_test.go`
- [x] 1.4 Remove the ASR consumer path: `speechLoop`, `CallSession.Listen`, `sessionImpl.Listen`, `EventSpeech`. Leave the rtpmanager `Listen` RPC and `internal/rtpmanager/asr/` in place but dormant
- [x] 1.5 Strip LLM config from `config/config.go`: `LLMProvider`, `LLMModelRef`, `LLMModel`, `LLMServerURL`, `LLMKeepAlive`, `KeepAliveIgnoredWarning`, `TurnTimeout`, `FirstTurnTimeout`, their flags (`:113-122`) and env overrides (`:186-202`). Keep `TTSVoice`
- [x] 1.6 Remove `llm.New`, `llm.ProbeAndWarm`, and `NewRunner` from `app/app.go`; simplify `filemanager.MultiReloader` to the routing store alone
- [x] 1.7 Remove `llmAuthStatus` and the LLM banner lines from `cmd/signaling/main.go`
- [x] 1.8 Delete `cmd/agent-smoke/`
- [x] 1.9 Delete `resources/tenants/*.md` and `resources/config/settings.md`; remove `--settings-path` and `--tenants-path`
- [x] 1.10 Verify `go build ./...` passes and the server boots with no LLM configured or reachable

## 2. Retire the supervisor vocabulary

- [x] 2.1 Remove `RoutingTargetAssistant`, `NoAnswerSupervisor`, and `RingGroup.NoAnswer` from `routing_config.go`
- [x] 2.2 Remove `CallContext.ForAssistant`, `FormatForPrompt`, and `FirstTurnDirective` from `context.go`
- [x] 2.3 Loader errors must *name* the removed vocabulary — "'assistant' is no longer a valid destination; the LLM supervisor was removed" — not "unknown destination"
- [x] 2.4 Migrate `resources/tenants/devtenant.routing.json` (it ships both an `assistant` extension and a group `no_answer`)
- [x] 2.5 `CallResolution.Handle` returning false routes to the tenant operator, or relays 480 when none is configured

## 3. Admission as capacity control

- [x] 3.1 Delete the prompt check, `reasonNoPrompt`, and the `PromptSource` dependency from `admission.go`
- [x] 3.2 Merge `Preflight` and `Admit` into a single `Admit(cc)`; "tenant loaded" now means "has routing configuration"
- [x] 3.3 Move the call site to `invite.go:119`, before `CreateSession`, so an over-limit tenant never allocates an RTP port
- [x] 3.4 Replace the runner `TeardownHook` release with a plain `defer` in `routeCall`; keep release idempotent
- [x] 3.5 Tests: within limit admits, over limit gets 486 before any media session, slot released exactly once on every teardown path

## 4. The `dialplan` package

- [x] 4.1 Create `internal/signaling/dialplan/`; move `routing_config.go` and its tests from `agent/` as a pure rename commit (~15 files of import churn, reviewed separately)
- [x] 4.2 `flowdef.go`: `FlowDef`, `Node`, `NodeType`, the shared `PromptSpec`, and per-type entry structs decoded at load with `DisallowUnknownFields`
- [x] 4.3 `var nodeExits map[NodeType][]string` and `terminalExits` — exit names fixed in Go
- [x] 4.4 Load `<tenant>.routing.json` and `<tenant>.flows.json` as one atomic unit in a single fail-closed `Reload` pass
- [x] 4.5 Tests: a flow referencing a group the routing file does not define fails to load; a bad edit leaves the previous config in force

## 5. Digit-map matcher

- [ ] 5.1 `digitmap.go`: compile `X`, `N`, `Z`, bracketed sets, trailing `.`, and literals into a per-position class list; reject `.` anywhere but last
- [ ] 5.2 Match with exact-length semantics, or tail-absorbing when a trailing wildcard is present
- [ ] 5.3 Specificity by accepted-set cardinality (literal 1, `[147]` 3, `[2-8]` 7, `N` 8, `Z` 9, `X` 10, `.` infinite), compared as a per-position vector via dominance — never a scalar, never a declared integer
- [ ] 5.4 Pairwise language-intersection test, then dominance; two intersecting patterns with neither dominating is a load error naming both
- [ ] 5.5 Closed-set transforms `strip_digits`, `strip_suffix_digits`, `normalize: e164|digits|none`, landing in the cursor. No `${}` interpolation
- [ ] 5.6 Bare destinations stay valid as one-node-dial sugar
- [ ] 5.7 Table tests: literal beats pattern; `N` beats `Z` beats `X`; `NX` vs `XN` rejected; `[12]X` vs `[23]X` rejected; `9NXXNXXXXXX` matches a NANP number and strips correctly

## 6. Policy: side-effect-free classification

- [ ] 6.1 Split `Policy.Classify` (pure verdict) from `Policy.Consume` (the breaker); `authorizeResolved` becomes `Classify` + `Consume`
- [ ] 6.2 Hoist `externalUnits` out of the per-call `Policy` instance into a process-lifetime, per-tenant, day-bucketed counter, fixing the shipped bug where `max_external_units_per_day` resets every INVITE
- [ ] 6.3 Tests: the counter accumulates across calls and trips at the configured limit; validating N external targets consumes zero units

## 7. Typed dial outcomes

- [ ] 7.1 `DialOutcome` / `DialResult` (`Answered|NoAnswer|Busy|Rejected|Unavailable|Failed`) and `classifyDialError` over `DialError.IsBusy()`/`IsUnavailable()`/`IsTimeout()`, `ErrDialTimeout`, `ErrNoContacts`, `ErrTargetNotFound`
- [ ] 7.2 Add `CallSession.ForwardOutcome` — relays nothing; refactor `Forward` to be `ForwardOutcome` + `relayForwardFailure`
- [ ] 7.3 `b2bua.DialParallel` returns per-target outcomes: collect into a mutex-guarded slice and return a copy, changing **nothing** about winner selection or loser cancellation
- [ ] 7.4 Add `CallSession.ForwardGroupOutcome`; stop collapsing every member outcome into `ErrGroupNoAnswer`
- [ ] 7.5 Tests: a 486 from the target yields `Busy` with no status relayed upstream; existing `Forward` behaviour is unchanged for the operator fallback

## 8. SDP: carry rtpmap and negotiate telephone-event

- [ ] 8.1 `extractSDPInfo` (`routing/invite.go:326-361`) walks `mediaDesc.Attributes` for `rtpmap`/`fmtp`, joins with `MediaName.Formats`, and falls back to the static payload-type table for formats with no rtpmap
- [ ] 8.2 Add `CodecOffer` to `CreateSessionRequest`, `telephone_event_pt` to `CreateSessionResponse`, and answered offers to `UpdateSessionRemoteRequest`; additive only, no renumbering. Run `make proto`
- [ ] 8.3 Collapse the two copies of `if codec == "0"` (`session/manager.go:79-89`, `:196-207`) into one `negotiate(offer) (AnswerSpec, error)`; echo **the offerer's** telephone-event payload type, never a hardcoded 101
- [ ] 8.4 An offer without telephone-event yields an answer without it
- [ ] 8.5 `sdp.BuildAnswer` emits two formats; `builder.go:35`'s single-element `formats` is the only blocker — the rtpmap and fmtp code at `:96,113-121` already works
- [ ] 8.6 Replace `Session.Codec string` with `AudioPT` + `TelephoneEventPT`
- [ ] 8.7 Tests: a dynamic PT other than 101 is echoed; an offer without telephone-event is answered without it

## 9. DTMF detection and collection

- [ ] 9.1 RFC 4733 decoder over `pion/rtp` — end-bit and duration handling, no duplicate digits from retransmitted end packets
- [ ] 9.2 Per-session digit buffer, filled continuously from session creation so type-ahead survives between nodes
- [ ] 9.3 `CollectDigits` RPC: `PromptSpec`, `interruptible`, `max_digits`, `terminators`, first-digit / inter-digit / overall timeouts, `flush_buffer`; returns digits, a `CollectReason`, and `prompt_interrupted`
- [ ] 9.4 Server-side implementation owns the socket for the whole operation — transmits prompt frames and parses inbound RTP in one loop; drains the buffer before waiting; stops playback on the first digit when interruptible
- [ ] 9.5 Return `NO_DTMF_TRANSPORT` when the leg negotiated no telephone-event, and warn at load when a flow containing `ivr` nodes is reachable for such legs
- [ ] 9.6 Plumb through all six layers: proto → `mediaclient.Transport` → `GRPCTransport` → `Pool` (session affinity) → `CallSession` → the fake
- [ ] 9.7 SIP INFO handler for `application/dtmf-relay` feeding the same buffer; register `sip.INFO` in `app.go`
- [ ] 9.8 Tests: decode from captured packets; a digit during the prompt is not lost; dial-ahead through two menus; terminator vs max-digits vs each timeout are distinguishable

## 10. The flow engine

- [ ] 10.1 Create `internal/signaling/flow/`; `Cursor` with current node, digit buffer, per-node retry counts, hop list, and deadline
- [ ] 10.2 Nested budgets `flowCtx ⊃ nodeCtx ⊃ playCtx`, with `flowCtx` derived from `dlg.Context()`; `maxHops` backstop
- [ ] 10.3 `Engine.Handle(ctx, sess, cc) bool` keeping `CallResolution.Handle`'s exact contract and blocking on the SIP transaction goroutine
- [ ] 10.4 `*7XX` retrieval as the first branch, before entry-mapping patterns, with the internal-only guard intact
- [ ] 10.5 Media nodes `tts` and `play_audio`; answer at the top of every media node
- [ ] 10.6 `ivr` node: prompt-and-collect, digit exits, `timeout`/`invalid`/`retries_exceeded`, retries bounded inside the node
- [ ] 10.7 `dial_user` and `dial_external` nodes over `ForwardOutcome`/`ForwardGroupOutcome`; pre-answer forwards, post-answer bridges; every target through `Policy`
- [ ] 10.8 `transfer` (blind) and `hangup` nodes; `hangup` relays its cause when the call is unanswered
- [ ] 10.9 Release the cursor on every teardown path including mid-menu abandonment
- [ ] 10.10 One fake session in `flow/flowtest` — not a fourth variant

## 11. Validator

- [x] 11.1 `dialplan.Validate(tenant, table, flows, opts) []Problem` returning **all** problems with a path like `flows.main-ivr.nodes.greeting.exits.timeout`
- [x] 11.2 Schema checks: unknown node type, unknown entry field, missing required fields
- [x] 11.3 Exit checks: declared exits exist for the type; every non-terminal exit wired; terminal exits absent; every target names a real node
- [x] 11.4 `start` exists; BFS reachability; unreachable nodes are an error
- [x] 11.5 Acyclicity by three-colour DFS, reporting the actual cycle path
- [x] 11.6 Every sink node is terminal-typed
- [ ] 11.7 Target and COS checks via `Policy.Classify` — syntax, group existence, symbolic membership. **No registration check** (runtime state; would fail every boot)
- [ ] 11.8 Digit-map checks per group 5
- [ ] 11.9 E911 **warning-only**: a pattern that shadows an emergency number; a PSTN-capable tenant with no emergency route
- [ ] 11.10 `Store.Reload` fails on any Error and keeps the previous config; the config API returns `[]Problem` so the UI can say why

## 12. Tooling

- [ ] 12.1 `validate` subcommand: peek `os.Args[1]` in `cmd/signaling/main.go` before `config.Load()`, dispatch to its own `flag.NewFlagSet`. Exit 0 clean / 1 problems / 2 usage
- [ ] 12.2 `cmd/flow-smoke`: walk a flow against a fake session, feed digits on stdin, print the traversal
- [ ] 12.3 `make validate` target and a CI step over `resources/tenants/`

## 13. Call records

- [ ] 13.1 `flow.Trace` on the cursor: node, type, exit, entered-at, duration, detail per hop
- [ ] 13.2 `CDRSink` interface with one implementation — append-only JSONL under `--cdr-path`; hops into the existing `store.CDR.Metadata` blob
- [ ] 13.3 Fold `policy.logDecision` verdicts into the same record; close the `TODO(cdr)` at `policy.go:295`
- [ ] 13.4 No SQL repository, no events bus, no NATS subjects

## 14. Wire it up

- [ ] 14.1 Swap `resolution.Handle` for `flow.Engine.Handle` in `invite.go`; delete `resolver.go` and `resolution_exec.go` in the same commit
- [ ] 14.2 Construct the engine in `app.go` with the routing store, policy builder, parking service, and CDR sink
- [ ] 14.3 Ship an example `resources/tenants/devtenant.flows.json` exercising every node type

## 15. Documentation

- [ ] 15.1 `CLAUDE.md`: folder structure (`llm/` and the supervisor files are gone, `dialplan/` and `flow/` are new) and the call-flow diagram
- [ ] 15.2 `README.md`: remove the "LLM supervisor on every call" framing and the supporting-services setup
- [ ] 15.3 `docs/CONFIGURATION.md`: drop LLM flags/env, document `<tenant>.flows.json`, `--cdr-path`, and `validate`
- [ ] 15.4 `docs/TENANT-EXAMPLE.md`: a rewrite, not an edit — it is entirely about the prompt/routing/policy three-file split, and the prompt file is gone
- [ ] 15.5 Document that `answered` is terminal, so "after the bridge ends, do X" is inexpressible by design

## 16. Verification

- [ ] 16.1 `go build ./...`, `go test ./...`, `go vet ./...` clean
- [ ] 16.2 `go mod tidy` produces no diff
- [ ] 16.3 Server boots and routes with no LLM, ASR, or agent configured or reachable
- [ ] 16.4 Live: internal forward — 102 dials 105, `100 Trying` then `180 Ringing`, no 183 and no 200 from Switchboard, caller gets 105's own relayed response. Byte-identical to today
- [ ] 16.5 Live: inbound DID via trunk — INVITE from a configured peer routes to its tenant flow; unmapped DID → 603; unknown source → 403
- [ ] 16.6 Live: park/unpark `*701` — carried from archived `llm-pbx-supervisor` §9.4 and never executed (it needed a spoken conversation to park; a flow node no longer does). Park a call, retrieve from another phone, confirm the slot frees on either party hanging up
- [ ] 16.7 Live: channel-limit 486 — `channel_limit: 1`, two concurrent calls, second gets 486 **before** an RTP port is allocated
- [ ] 16.8 Live: COS deny — a `dial_external` node naming a denied target takes the `denied` exit, no INVITE leaves the box, the deny is in the call record
- [ ] 16.9 Live: menu digit selection, menu timeout, and retry exhaustion each reach the right node
- [ ] 16.10 Live: `dial_user` no-answer continues to the next node with **no 486 or 480 reaching the caller**
- [ ] 16.11 Live: caller abandons mid-menu — the cursor is released and no goroutine leaks
- [ ] 16.12 Live: a leg negotiating no telephone-event degrades via `NO_DTMF_TRANSPORT` rather than hanging
- [ ] 16.13 A config with an inter-node cycle is rejected at load with the cycle path named
- [ ] 16.14 A config with an ambiguous digit-map pair is rejected at load with both patterns named

## 17. Configuration editing surface

Deleting the prompts orphaned ~1,000 lines whose only job was editing
`settings.md` and tenant `.md` files. It still compiles, which is the hazard:
`SettingsDir` is no longer wired, so `GetSettings` would read `settings.md`
relative to the process working directory. The surface is converted to the files
that replaced the prompts rather than deleted, because a flow graph is far more
error-prone to hand-edit than a prompt was — and task 11.10 already commits the
config API to reporting validation problems.

- [x] 17.1 Remove `GetSettings`/`PutSettings` from `filemanager` and drop `SettingsDir`; `settings.md` is gone and is not coming back
- [x] 17.2 Remove `GET/PUT /api/v1/config/settings`, the UI settings page and its route, and `Client.GetSettings`/`PutSettings`
- [ ] 17.3 Convert filemanager tenant CRUD from `<tenant>.md` to `<tenant>.routing.json` + `<tenant>.flows.json`; `ListTenants` reports which files each tenant has
- [ ] 17.4 Validate on write and reject an invalid edit, returning `[]Problem` (pairs with 11.10) so a bad flow can never be saved into a running system
- [ ] 17.5 UI: edit routing and flow JSON, and render validation problems against their `flows.<id>.nodes.<node>` paths
- [ ] 17.6 Tests: a write that fails validation changes nothing on disk; a tenant with only a routing file is listed correctly

## 18. Follow-up

- [ ] 18.1 File the E911 change: hard-coded emergency route bypassing COS, un-shadowable by any pattern, dispatchable location, on-site notification. Recommend scheduling it before production traffic
