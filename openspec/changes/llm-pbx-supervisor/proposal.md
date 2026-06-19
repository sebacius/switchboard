## Why

Switchboard's call brain is a JSON dialplan plus an AI agent driven over a brittle text `ACTION:`
protocol — the parser silently swallows malformed output, and every roadmap feature wants to be a tool
the model invokes mid-call, not a dialplan keyword. Native tool calling on a small Ollama-hosted model
(qwen3:8b) is now reliable enough to retire the dialplan and the text protocol. Doing the pivot first
means later features land as tool additions. Crucially, the model is **untrusted** — caller speech
flows straight into the prompt and the supervisor can spend money via the trunk — so the design centers
on a **deterministic policy layer** that wraps a zero-authority model.

## What Changes

- **BREAKING**: Remove `internal/signaling/dialplan/` (13 files) and `resources/config/dialplan.json`.
- A single **LLM supervisor** owns every call from INVITE to teardown via **native tool calling on
  Ollama's native `/api/chat`** (not the OpenAI-compat `/v1` endpoint) with `think: false` — clean
  `thinking`/`content`/`tool_calls` separation and a latency budget the small model can meet.
- The runner is an **event loop** structured around **three nested context scopes**
  (`callCtx ⊃ turnCtx ⊃ playbackCtx`) that unify teardown, the runaway-turn breaker, and a future
  barge-in lane. Teardown is a single idempotent funnel; producers never close the events channel.
- **The LLM never answers automatically.** The first-turn decision drives SIP: a `dial` to a directory
  user or to the trunk **forwards the INVITE without answering** (relay provisional/final responses); a
  decision to speak/gather **sends 200 OK** and the supervisor owns the media. Media stays at
  Switchboard (recording, DTMF); media bypass is a future optimization.
- **Direction is a trust gradient**: `internal` = From is a registered directory user; `inbound` = from
  a trunk peer; `outbound` = directory user → external. The per-call **tool registry is scoped by
  (tenant, direction)** — an inbound caller's toolset contains no external dial at all.
- **Deterministic tool authorization**: every consequential tool call is an untrusted request a policy
  layer authorizes. `dial` carries a per-tenant Class of Service (default-deny external, allowlist,
  barred classes), symbolic targets over free-form numbers, a per-tenant spend circuit breaker, and
  decision logging. Prompt hardening is defense-in-depth, not the boundary.
- **Admission gate before the LLM**: deterministic preflight (tenant loaded, prompt non-empty) plus a
  **per-tenant channel limit**; failures reject **pre-answer** (4xx/486) without engaging the model.
  **There is no default tenant** — an unresolved/unloaded tenant is rejected.
- Add `cmd/agent-smoke/main.go` and `llm.ScriptedClient`. **Zero new third-party dependencies.**
- Scope is **call-setup tools only** (`dial`, `hangup`, `play_audio`/voicemail, `park`, `unpark`);
  mid-call tools (transfer, recording control, conference, mute) are deferred.

## Dependencies

- **Depends on `basic-sip-trunk`**: direction classification consumes trunk source recognition, and the
  inbound path consumes its DID→tenant routing (`routes.json`).

## Capabilities

### New Capabilities
- `call-supervisor`: the event-loop runner — first-turn answer/forward decision, the three nested
  context scopes, idempotent teardown (CANCEL vs BYE), the runaway-turn breaker, the call-long
  conversation, and the designed-for barge-in interrupt lane.
- `agent-tools`: native tool calling on `/api/chat`; the per-call tool registry scoped by
  (tenant, direction); the call-setup tools with forward-vs-bridge `dial`, the handler disposition
  enum (`Continue`/`Terminal`/`Parked`), ported B2BUA/park/unpark logic, and arg-validation
  self-correction.
- `call-routing`: tenant + direction resolution — directory-user vs trunk-origin, subdomain tenant
  derivation for non-DID calls, DID-derived tenant for inbound, and the `CallContext` prompt block.
  No default tenant.
- `call-admission`: deterministic preflight + per-tenant channel limit, rejecting pre-answer; protects
  the first-turn latency budget.
- `tool-authorization`: the deterministic policy layer over a zero-authority model — per-tenant Class
  of Service on `dial`, capability narrowing, spend circuit breaker, and decision logging.

### Modified Capabilities
<!-- None tracked in openspec/specs/ yet; the dialplan is removed code, not a spec capability. -->

## Impact

- **Removed**: `internal/signaling/dialplan/` (13 files), `resources/config/dialplan.json`.
- **New code**: `internal/signaling/agent/` (`runner.go`, `session.go`, `context.go`, `events.go`,
  `tools.go`, `router.go`, `policy.go`, `admission.go`); `internal/signaling/llm/` gains `tools.go` +
  `scripted.go` and a `Client` interface on the native `/api/chat` wire; `cmd/agent-smoke/main.go`.
- **Rewired**: `app/app.go`, `routing/invite.go` (defer the 200 OK; classify direction; per-call tool
  registry; admission), `filemanager/filemanager.go`, `api/handlers_config.go`, `config/config.go`,
  `cmd/signaling/main.go`, `resources/config/settings.md`, `resources/tenants/*.md`, `README.md`.
- **Registrar stays in Switchboard** (it is the directory). Build the auth/location layer behind an
  API/tooling seam so a future change can let Kamailio query Switchboard for credentials and push
  location-on-change. Direction's directory-user check uses a full-AOR location lookup (the AOR domain
  carries the tenant), so no location-store restructuring is required now.
- **Dependencies**: none added (`go mod tidy` must not drift). Model: `qwen3:8b` on Ollama.
- **Not affected**: gRPC media contract, ASR/TTS clients. Barge-in is designed-for but deferred (needs
  a speech-onset/VAD capability the media layer does not yet expose).
