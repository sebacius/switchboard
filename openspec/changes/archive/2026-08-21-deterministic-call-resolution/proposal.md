## Why

The archived `llm-pbx-supervisor` change decided (#2) that the **LLM supervises every
INVITE — no matcher bypass**. Its own live smoke testing (that change's `tasks.md`
§9.3) produced the evidence against it:

- With the real 633-line `resources/tenants/default.md`, qwen3:8b **greeted instead of
  routing** on an internal extension-to-extension call — precisely the outcome the
  pre-answer forward path exists to avoid.
- First-turn latency measured **57s with the 633-line tenant prompt vs 19.5s with a
  4-line one**, against a 30s turn deadline deliberately set under SIP Timer B.
- The fix — `correctSilentRoute`, a one-shot corrective re-prompt — doubles the
  round-trips to roughly 2 minutes per turn and was **never re-confirmed live**,
  because it was too slow to test on that hardware.

An 8B model is being asked to decide something that has exactly one correct answer
(dial ext 110 → ring ext 110), inside the INVITE transaction, while holding a business
knowledge base as its system prompt. Every symptom above follows from that. This change
puts a deterministic resolver in front of the supervisor so calls with one correct
answer never reach the model.

This **revises decision #2; it does not reverse the architecture.** The resolver sits
inside the same authorization boundary, and `Policy.AuthorizeDial` still adjudicates
every dial. The model remains untrusted and zero-authority.

## What Changes

- **Deterministic resolver in front of the runner.** Not a return of `dialplan.json`.
  The same policy layer, consulted first: if the dialed target resolves unambiguously —
  registered directory extension, `*7XX` unpark code, DID mapped to a destination, or a
  tenant ring group — the resolver executes it and the model is never called. The
  supervisor is entered only when deterministic resolution yields no answer, or when the
  resolved destination *is* the assistant (a DID that routes to the AI receptionist).
- **Ring groups and queues resolve deterministically.** Sequential and round-robin
  strategies with no-answer/overflow behavior. `b2bua.DialParallel`
  (`call_service.go:241`) is currently `ErrNotImplemented` and must be implemented.
- **BREAKING (config): routing data moves out of the tenant prompt.**
  `resources/tenants/default.md` is 633 lines and carries structured routing data as
  prose — §5.1 staff directory (12 people), §6.1 intent→department table, §8.2 ring
  groups, §9.1 extension numbering plan — while `resources/config/policy.json` defines
  only **7** `symbolic_targets`. That data moves into a new per-tenant routing file
  (`resources/tenants/<tenant>.routing.json`) that both the resolver and
  `symbolic_targets` read. `tenant.md` keeps behavior, tone, business facts, and
  escalation language — the things that actually need a language model — targeting well
  under 200 lines.
- **BREAKING (behavior): an unknown tool no longer hangs up.**
  `internal/signaling/agent/tools.go:143-147` returns `DispositionTerminal` when the
  model emits a tool outside its registry, so one hallucinated tool name drops a
  customer's call. It becomes a deterministic transfer to the tenant's operator.
- **`correctSilentRoute` is deleted, not tuned** (`internal/signaling/agent/runner.go:342`,
  `374-410`). With internal routing resolved deterministically, the prose-instead-of-route
  failure it patches cannot occur on the path that mattered.
- **`resources/tenants/default.md` §2.5 "Call Handling by Direction" is deleted.**
  Direction is a struct field already computed deterministically in `agent/router.go`;
  restating it as prose for the model to re-derive is the same category error.
- **LLM-outage failure mode is restored.** Today `runner.go:322` returns an error when
  `ChatNative` fails, killing the call — so Ollama being down stops internal extension
  dialing entirely. After this change an LLM outage degrades to "AI receptionist
  unavailable" while the PBX keeps routing.

## Capabilities

### New Capabilities

- `call-resolution`: deterministic resolution ahead of the supervisor — what counts as
  unambiguous, precedence versus the supervisor, ring-group strategies and their
  no-answer behavior, resolution remaining inside the authorization boundary, and the
  degradation path when the LLM is unavailable.

### Modified Capabilities

- `call-supervisor`: the requirement "LLM supervises every INVITE" is revised. The
  sentence "There SHALL be no dialplan table and no fast-path matcher that bypasses the
  LLM" keeps its "no dialplan table" half; the matcher clause is replaced by the
  deterministic-resolution rule. The runner's behavior when the LLM is unavailable is
  also specified.
- `call-routing`: resolution now produces either a deterministic destination or a
  hand-off to the supervisor.
- `agent-tools`: "Argument validation and self-correction" currently requires an unknown
  tool to cause a hangup; it becomes a deterministic operator transfer.
- `call-admission`: preflight currently requires a non-empty tenant *prompt* and the
  channel slot is acquired for every admitted call. Since the channel limit exists to bound
  concurrent *supervised* calls, leaving it as-is would mean a tenant at its LLM limit gets
  486 Busy on a plain extension dial, and a tenant with a routing table but an empty prompt
  could not route at all. The prompt check and the slot move to the hand-off to the
  supervisor.

## Impact

- **New code**: a resolver in `internal/signaling/agent/` consulted from
  `internal/signaling/routing/invite.go` (between `Router.Route`/`Admission.Admit` and
  `Runner.HandleCall`); a per-tenant routing-file loader alongside
  `agent/prompts.go`'s `PromptStore`; a ring-group strategy engine.
- **Modified**: `internal/signaling/b2bua/call_service.go` (`DialParallel`),
  `internal/signaling/agent/runner.go` (delete `correctSilentRoute`; LLM-failure
  posture), `internal/signaling/agent/tools.go` (unknown-tool disposition),
  `internal/signaling/agent/policyconfig.go` (`symbolic_targets` sourced from the
  routing file), `internal/signaling/app/app.go` and `config/config.go` (wiring +
  flag/env for the routing path), `internal/signaling/filemanager/filemanager.go`
  (reload).
- **Config**: new `resources/tenants/<tenant>.routing.json`; `resources/tenants/*.md`
  slimmed; `resources/config/policy.json` unchanged in shape.
- **Docs**: `CLAUDE.md` still lists `internal/signaling/dialplan/` and
  `resources/config/dialplan.json` in its folder structure, and its call-flow diagram
  still shows the `183 → 200 OK` sequence the supervisor deliberately removed; both are
  corrected here. `README.md` and `docs/CONFIGURATION.md` gain the resolver and the
  routing file.
- **Depends on** the archived `llm-pbx-supervisor` and `basic-sip-trunk` changes
  (2026-08-19) — this modifies specs they promoted into `openspec/specs/`.
- **Dependencies**: none added. Zero new third-party dependencies; `go mod tidy` must
  not drift.
- **Unchanged — the authorization boundary is not weakened**: per-call tool registry
  scoped by (tenant, direction), inbound calls get no external-dial affordance,
  default-deny Class of Service, symbolic-target capability narrowing, and the
  per-tenant spend breaker all remain exactly as shipped.
