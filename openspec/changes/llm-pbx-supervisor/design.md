## Context

Today an INVITE flows `routing.HandleINVITE` → sends 183+SDP and **200 OK** → `executeDialplan`
(goroutine, `invite.go:233-274`) → `dialplan.NewSession()` → `executor.Execute()`, matching a route in
`dialplan.json`. The `ai_agent` action is the only decision-maker; it opens an `llm.Conversation`
(raw `net/http` to Ollama `/v1/chat/completions`, request carries only `model/messages/stream`, no
`Client` interface) and parses the model's text `ACTION:` output with `parseResponse`
(`action_ai_agent.go:356-398`), silently dropping unrecognized blocks (`:377-379`). `CallSession`
(`dialplan/session.go:19-60`, impl 62-517) is a ~16-method seam. Registrations are a single global AOR
namespace (`location/store.go`). There is no trunk, no DID table, no admission, no tool authorization.

This change replaces all of it with a single supervisor. The defining constraint surfaced in
exploration: **the model is untrusted** (caller speech → prompt; `dial` → trunk → money), so security,
cost, and correctness boundaries live in **deterministic code wrapping a zero-authority model**, not in
the prompt. It **depends on `basic-sip-trunk`** for trunk recognition and DID→tenant routing.

## Goals / Non-Goals

**Goals:**
- One supervisor runner per call, INVITE→teardown, via native tool calling.
- A runner spine that makes teardown, runaway control, and barge-in the *same* mechanism.
- Deterministic admission + tool authorization that hold even when the model is manipulated.
- LLM-driven SIP answer: forward without answering when routing; answer only to own media.
- Zero new third-party dependencies; clean `go build` / `go mod tidy`.

**Non-Goals:**
- Mid-call tools (transfer, recording control, conference, mute) — additive follow-ups.
- A fast-path matcher bypassing the LLM — rejected; forcing output discipline by direction is allowed.
- Media bypass and barge-in implementation — designed-for, deferred (need a media-layer VAD capability).
- Restructuring the location store for tenant-segmentation, or a Kamailio offload — future changes.

## Decisions

**1. Delete `dialplan/`; move `CallSession` to `internal/signaling/agent/`.** The three importers
(`app/app.go`, `routing/invite.go`, `filemanager/filemanager.go`) rewire to the agent package. The real
B2BUA dial bridge (`action_dial.go`) and unpark `BridgeMedia` (`action_unpark.go`) are **ported into
tool handlers**, not discarded.

**2. LLM supervises every INVITE — no matcher bypass.** Internal/inbound/outbound all enter the same
runner. ~200–400ms added to internal setup, accepted. Output discipline is shaped by direction (see #11),
which is not a bypass — the model still decides.

**3. Native tool calling on Ollama `/api/chat`, not `/v1/chat/completions`.** (Revises the earlier
decision to extend the `/v1` client.) The native endpoint returns `thinking`, `content`, and
`tool_calls` as separate fields, so reasoning never leaks into TTS'd text; `/v1` folds reasoning into
`<think>` tags in content, version-dependent. Since the project already accepts Ollama-specific (no
langchaingo), the portability argument for `/v1` is moot. Add a `Client` interface so `ScriptedClient`
substitutes in tests.

**4. `think: false`.** qwen3 is a reasoning model; a thinking pass is seconds and would blow the
internal-route latency budget. Thinking off is effectively forced by the SLA and also removes any
TTS-leak risk. The open spike (smoke harness): does tool-call accuracy hold with thinking off?

**5. The runner is an event loop over three nested context scopes.** This is the spine that unifies the
hard parts:

```
   callCtx        ← BYE / CANCEL / timeout → idempotent teardown funnel (whole tree)
     └─ turnCtx   ← runaway breaker / per-turn deadline → abort one turn, call lives
          └─ playbackCtx ← barge-in → cancel TTS only, turn lives
```

One dispatch loop drains a single `events` channel; producers (speech now; dtmf/signaling/media later)
write via `select { case events <- ev: case <-ctx.Done(): return }`. **The events channel is never
closed** (multiple producers); `ctx` is the only shutdown signal. *Why nested ctx:* the loop's top-level
`select` only sees cancellation *between* turns — every blocking thing *inside* a turn (the LLM call,
each tool handler, `Listen()`) must honor its scope or teardown is invisible.

**6. Idempotent teardown funnel.** Caller CANCEL/BYE, the `hangup` tool, and ctx timeout all converge on
one `teardown(reason)` guarded by `sync.Once` (gated via `CallSession.IsTerminated()`): cancel callCtx,
release parking slot if parked, CANCEL/BYE any B-leg, release the tenant channel slot, destroy the RTP
session. Pre-answer abort (no 200 sent) responds 487 to the INVITE and CANCELs the B-leg; post-answer
abort 200s the BYE and tears down media — the funnel branches on whether we answered.

**7. LLM-driven answer: forward vs. engage-media.** (Revises original decision #8.) The INVITE handler
**does not pre-answer.** The first turn decides:

```
   dial(directory user)  → forward INVITE to endpoint, relay 180/200, never answer
   dial(external)        → forward INVITE to trunk peer (basic-sip-trunk), relay, never answer
   speak / play / gather → 200 OK, supervisor owns the media, enter listen/speak loop
```

Invariant: **answering means "the AI handles this leg's media itself."** A direct extension call is a
pure forward (caller hears real ringback). The first-turn LLM call runs *inside the INVITE transaction*,
so it must complete within SIP Timer B / caller patience — which is exactly what admission (#9) protects.

**8. Direction is a trust gradient; the tool registry is scoped per call.** (Consumes `basic-sip-trunk`.)

```
   internal  = From is a registered directory user (full-AOR location lookup; domain ⇒ tenant)
   inbound   = From is a trunk peer; tenant via DID→tenant (basic-sip-trunk)
   outbound  = directory user → non-directory destination → egress via trunk
```

The per-call tool registry is built from `(tenant, direction)`. An **inbound** caller's registry has
**no external dial** — the model cannot be injected into a capability it does not hold. This is the
single strongest fraud defense and is cheap (build the registry per call, not globally).

**9. Admission gate before the LLM (`call-admission`).** Deterministic, pre-answer:

```
   resolve tenant + direction (cheap)
   preflight:  tenant loaded? settings.md + tenant.md non-empty?  → else REJECT (no LLM)
   admission:  per-tenant channel slot available?                → else 486 Busy
```

**No default tenant** — an unresolved/unloaded tenant is a hard reject. The per-tenant channel limit is
both cost control and SLA protection: it keeps the first-turn LLM call from queueing past the INVITE
transaction timeout. The slot is acquired here and released by the teardown funnel.

**10. Tool authorization: a deterministic policy over a zero-authority model (`tool-authorization`).**
Every consequential tool call is an untrusted request the policy adjudicates before execution:

- **Class of Service on `dial`** (per tenant + direction): default-deny external; allowlist of permitted
  destinations/prefixes; barred classes (premium-rate, satellite, high-risk country codes).
- **Capability narrowing**: the model emits **symbolic** targets (extension names, named forwards) a
  deterministic resolver maps to numbers; it cannot express an arbitrary `+1900…`. "Dial a
  caller-provided number" is a separate, hard-gated tool, not the default `dial`.
- **Spend circuit breaker** (per tenant): max external minutes/cost/day; trip + alert on a spike.
- **Decision logging**: every tool call + authz verdict → CDR; a denied external dial is a fraud signal.
- Prompt hardening (never reveal instructions, identity-over-voice ≠ authority) is defense-in-depth; the
  tenant prompt is treated as **semi-public** (no secrets in `tenant.md`).

**11. First-turn discipline without a fast path.** `tool_choice: required` adherence on Ollama/qwen3 is
uncertain, so silent internal routing leans on a strong direction-aware system instruction ("route,
don't greet") plus one self-correction retry if an internal first turn returns prose. The LLM still
chooses the target — output shaping, not bypass.

**12. Runaway-turn breaker.** Turns are **reactive** (caller input — human-rate-limited) or
**autonomous** (a tool result re-prompts the model — nothing rate-limits it). The breaker bounds
*consecutive autonomous* turns (reset by any caller input): soft cap → stop re-prompting, fall back to
reactive-only; hard cap → deterministic message + teardown. Tool-failure results MUST be **actionable**
("offline — offer voicemail or another extension"), and a repeat detector refuses an identical
just-failed tool call. Per-call LLM-call budget backstops total spend (complements #9).

**13. Conversation lives the whole call**, created at INVITE, no history trimming yet (revisit with
summarization when long calls accumulate context). Carried-forward pattern: non-terminal tool results
are recorded back into the conversation (`Continue` disposition).

**14. Test seams.** `llm.ScriptedClient` implements `Client` for unit tests (every runner branch);
`cmd/agent-smoke/main.go` drives the real runner against real Ollama with a fake `CallSession`
(stdin → speech events; stdout → tool dispatches + `>>>` TTS). Flags `--llm-server --model --tenant
--caller --callee --direction`.

## Risks / Trade-offs

- **Untrusted model → toll fraud / injection** → the boundary is deterministic (#8 per-call registry,
  #10 COS + capability narrowing + circuit breaker), never the prompt. Inbound external dial is off by
  affordance.
- **qwen3:8b tool-call quality with `think:false`** → handler arg-validation + self-correction + smoke
  harness tuning before merge.
- **First-turn latency inside the INVITE transaction** → bounded by per-tenant admission (#9); calls
  reject fast rather than dying in Timer B under load.
- **Park / B-leg / teardown races** → the nested-ctx spine (#5) + idempotent funnel (#6); park is a
  `Parked` disposition handled by the loop, never a blocking handler.
- **Barge-in absent** → events queue during a turn; acceptable for routing, weaker for conversation;
  the interrupt lane is designed-in (a `playbackCtx` + contentless interrupt signal) but deferred until
  the media layer exposes speech-onset/VAD.
- **Prompt leakage** → `tenant.md` is semi-public by assumption; no secrets stored there.

## Migration Plan

1. Land `basic-sip-trunk` first (prerequisite), then this change on the same branch sequence.
2. Build `agent/` (runner spine, session, tools, router, policy, admission) + the `/api/chat` client;
   port primitive bridging into handlers.
3. Rewire importers, config, API, prompts, README; defer the 200 OK in `routing/invite.go`.
4. Delete `dialplan/` + `dialplan.json` once the agent path is green.
5. Verify: `go build ./...` + `go mod tidy` (no drift) → scripted-runner unit tests (silent route,
   forward-vs-answer, teardown/CANCEL races, park disposition, runaway breaker, admission reject,
   COS deny) → `agent-smoke` against local Ollama → full SIP test (internal forward, inbound DID,
   park/unpark, per-tenant channel-limit reject, COS deny of an external dial).
6. Rollback: `git revert`. No data migrations.

## Open Questions

- Directory-user check via live registration only, or a provisioned directory independent of
  registration (lean: live registration now; provisioned directory is a future registrar change).
- Whether inbound tenant comes solely from `basic-sip-trunk`'s DID table or Kamailio later supplies it
  via header (resolve with the Kamailio-offload change).
- Spend circuit-breaker thresholds and channel-limit defaults — set during config rewire.
