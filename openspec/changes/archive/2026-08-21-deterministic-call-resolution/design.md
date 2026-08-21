## Context

The archived `llm-pbx-supervisor` change (2026-08-19) committed to decision #2, *"LLM
supervises every INVITE — no matcher bypass"*, with the trade-off recorded as "~200–400ms
added to internal setup, accepted". Live smoke testing in that same change measured
something two orders of magnitude worse and recorded it in its `tasks.md` §9.3:

| Observation (§9.3) | Value |
| --- | --- |
| First turn, 4-line tenant prompt | 19.5s |
| First turn, real 633-line `default.md` | **57s** |
| Turn deadline (deliberately under SIP Timer B) | 30s |
| Internal ext-to-ext call with the real prompt | **greeted instead of routing** |
| `correctSilentRoute` fix, round-trips per turn | 2 (~2 min/turn) |
| `correctSilentRoute` re-confirmed live | **no** — too slow to test |

The estimate was off by ~140×, and the correctness failure it produced is the exact
outcome the pre-answer forward path exists to prevent: a colleague dials an extension and
an AI greets them.

The cause is structural, not a tuning problem. An 8B model is being asked to decide
something with exactly one correct answer — dial ext 110 → ring ext 110 — inside the
INVITE transaction, while holding a business knowledge base as its system prompt. The
knowledge base is itself part of the problem: `resources/tenants/default.md` carries
structured routing data as prose (§5.1 staff directory, §6.1 intent→department table, §8.2
ring groups, §9.1 extension numbering plan) while `resources/config/policy.json` defines
only 7 `symbolic_targets`, so the model infers the rest from a markdown table at inference
time, every turn.

Current path (`internal/signaling/routing/invite.go:94-236`):

```
INVITE → ingress gate → Router.Route → Admission.Admit → dialog/media setup
       → 180 Ringing → Runner.HandleCall → first LLM turn decides everything
```

This change inserts deterministic resolution between admission and the runner, so calls
with one correct answer never reach the model. It is a correction of decision #2, not of
the guardrail architecture: the model stays untrusted and zero-authority, and
`Policy.AuthorizeDial` still adjudicates every dial the resolver issues.

## Goals / Non-Goals

**Goals:**

- Calls with exactly one correct answer never make an LLM request.
- Routing data lives in structured config that both the resolver and the model-facing
  symbolic targets read, so they cannot drift apart.
- Ring groups and queues resolve deterministically, with real ring strategies.
- An LLM outage degrades to "AI receptionist unavailable" instead of stopping the PBX.
- A hallucinated tool name never drops a caller.
- Zero new third-party dependencies; `go mod tidy` does not drift.

**Non-Goals:**

- Reinstating `dialplan.json` or any general-purpose pattern matcher. The resolvable set is
  closed and enumerated in `call-resolution`; anything requiring judgement about intent,
  wording, or business context still reaches the model.
- Weakening the authorization boundary. The per-call registry scoped by (tenant,
  direction), the inbound no-external-dial affordance, default-deny COS, symbolic-target
  narrowing, and the spend breaker are all untouched.
- Mid-call tools (transfer, recording control, conference, mute) — still deferred.
- Barge-in / VAD — still deferred; unrelated to this change.
- A durable call record. The `TODO(cdr)` at `policy.go:271` remains open.

## Decisions

**1. The resolver runs between admission preflight and the runner, and admission splits.**

New order in `routing/invite.go`:

```
   ingress gate
   Router.Route                     → direction + tenant (unchanged)
   Admission preflight              → tenant loaded?            → reject 404
   dialog + media session + 180 Ringing
   Resolver.Resolve(cc)             → Destination | HandOff
     ├─ Destination → Policy.AuthorizeDial → forward / ring group     (no LLM, no slot)
     └─ HandOff     → prompt non-empty? → channel slot? → Runner.HandleCall
```

The channel slot and the prompt-non-empty check move from admission-for-every-call to
admission-at-hand-off. *Why:* `call-admission` already describes the limit as bounding
"concurrent **supervised** calls" and justifies it as protecting the first-turn LLM
latency budget. Leaving the slot where it is would mean a tenant at its LLM channel limit
gets 486 Busy on a plain extension dial that costs nothing — a capacity regression
introduced by a latency optimisation. It also means a tenant with a routing table but an
empty prompt can still route extensions.

*Alternative rejected:* short-circuit inside `Runner.HandleCall`'s first turn. It reads as
a smaller diff but still constructs the runner, assembles the prompt, and takes the slot —
paying most of the cost this change exists to remove — and it leaves the "supervisor owns
every call" invariant ambiguous.

**2. The resolvable set is closed, and that is the anti-drift guard.**

`call-resolution` enumerates exactly four resolvable shapes: registered directory
extension, `*7XX` retrieval code from an `internal` caller, inbound DID with a mapping,
and named ring group. The closure is the design: a resolver that can be extended with
"just one more pattern" becomes the dialplan again. Anything expressed in speech is the
model's job by construction, because the resolver only ever sees the dialed target, never
an utterance.

**3. Per-tenant routing file; `symbolic_targets` moves out of `policy.json`.**

`resources/tenants/<tenant>.routing.json`, loaded by a `RoutingStore` alongside
`agent.PromptStore` (`agent/prompts.go`) and reloaded by the same `filemanager` path:

```jsonc
{
  "operator": "user/150",                       // unknown-tool fallback, group overflow
  "retrieval_prefix": "*7",
  "extensions":       { "110": "user/110", "150": "user/150" },
  "symbolic_targets": { "claims": "group/claims", "front-desk": "user/150" },
  "dids":   { "+15558001200": "assistant", "+15558001250": "group/claims" },
  "groups": {
    "claims": {
      "strategy": "sequential",                 // sequential | round-robin
      "members": ["user/130", "user/120", "user/110"],
      "member_timeout_ms": 15000,
      "no_answer": "supervisor"                 // supervisor | operator | hangup
    }
  }
}
```

`policy.json` keeps only authorization: `channel_limit`, `allow_external_dial`,
`external_allowlist`, `barred_prefixes`, `max_external_units_per_day`,
`allow_caller_provided_number`. Its `symbolic_targets` key is **removed**, and
`agent.LoadPolicyConfig` SHALL fail loudly if it is still present, naming the tenant and
telling the operator where the entries now live.

*Why a hard error over a merge:* two sources for one name is exactly the drift
`call-resolution` forbids. A deprecation-and-merge path would let a stale `policy.json`
entry silently win over the routing file for the lifetime of a deployment.

*Why per-tenant files over one shared directory file:* tenant data already lives per tenant
(`resources/tenants/<name>.md`), tenants are edited independently through the existing
config UI/API, and a shared file makes every tenant edit a whole-file rewrite.

Note the two DID layers do not conflict: `resources/config/routes.json` (from
`basic-sip-trunk`) maps DID→**tenant**; the tenant routing file maps DID→**destination**
within that tenant.

**4. Ring groups need `DialParallel` finally implemented.**

`b2bua.CallService.DialParallel` (`call_service.go:241`) has been `ErrNotImplemented` since
it was written, under a `// --- Ring Group Support (Future) ---` heading. Round-robin and
"first answer wins" both need it. Sequential is a loop over `Dial` with a per-member
timeout; round-robin fans out from a rotating start position.

Round-robin's cursor is per (tenant, group), held in memory in the resolver. *Trade-off:*
with multiple signaling servers the cursor is per process, so distribution is fair per
server rather than globally. Sharing it needs the durable store that does not exist yet
(the unimplemented `store.CDRRepository` seam); noted, not solved here.

**5. `correctSilentRoute` is deleted, not tuned** (`agent/runner.go:342`, `374-410`).

It exists solely to catch an internal first turn that returned prose instead of a dial. With
internal routing resolved before the model is consulted, that path no longer exists. Tuning
it would preserve a 2-round-trip turn to patch a decision the model should not be making.
The scripted tests covering it are removed with it; the behaviour they protected is now
covered by resolver tests.

**6. Unknown tool → deterministic operator transfer** (`agent/tools.go:143-147`).

Today an unregistered tool name returns `DispositionTerminal` — one hallucinated token drops
a customer's call. It becomes a transfer to the tenant's `operator`. A tenant with no
`operator` configured falls back to the actionable-error-and-continue path used for bad
arguments, so the floor is "never hang up on the caller". The runaway-turn breaker still
bounds a model that keeps emitting garbage.

**7. The tenant prompt keeps judgement, loses data.**

Deleted from `resources/tenants/default.md`: §2.5 "Call Handling by Direction" (direction is
a struct field already computed in `agent/router.go` — restating it as prose for the model
to re-derive is the same category error as making it route ext 110), §5.1 staff directory,
§6.1 intent→department table, §8.2 ring group members, §9.1/§9.2 extension plan. Retained:
identity and tone, business facts, hours, scenario handling, escalation language,
authentication rules — the things that genuinely need a language model. Target well under
200 lines, down from 633.

The intent→department table (§6.1) is the interesting case: the *keywords* are judgement and
stay in the prompt, but the *destinations* they name become `symbolic_targets` entries, so
the model emits `claims` and the resolver owns what `claims` means.

**8. LLM failure stops being fatal.**

`runner.go:322` returns an error when `ChatNative` fails, which kills the call — so today
Ollama being down stops internal extension dialing. After decision #1 a resolvable call
never enters the runner, so the outage is invisible to it. For a call that does need the
supervisor, a chat failure plays a deterministic "the assistant is unavailable" message and
tears down deliberately instead of dropping the caller with an error in the log.

**9. The authorization boundary is unchanged, and the resolver is inside it.**

The resolver produces a destination and hands it to the *same* `Policy.AuthorizeDial` the
model-issued `dial` goes through, with the same decision logging. Its routing table is data,
not authority: a table entry naming a destination outside the tenant's allowlist is denied
exactly as a model-issued one would be. Practically this is invisible for the common cases
(`user/...` targets are internal and always allowed), and it is what keeps "the resolver is
a performance optimisation, not a new trust path" true.

## Risks / Trade-offs

- **The resolver grows into a dialplan** → the resolvable set is closed and enumerated in
  `call-resolution`; extending it requires a spec change, which is the point of friction.
- **Routing data and prompt drift apart** (a tenant edits `.md`, forgets the routing file)
  → one source for both the resolver and `symbolic_targets`, plus a hard load error on the
  removed `policy.json` key. Residual risk: prose in the `.md` that contradicts the table
  is still possible; task group for prompt slimming removes the current instances.
- **Deterministic calls are no longer channel-limited** → this is deliberate (decision #1),
  but it removes an incidental cap on concurrent internal calls. Capacity is still bounded
  by RTP port range and registrations; if a per-tenant total-call cap is wanted it is a
  separate concern from the LLM channel limit.
- **Round-robin fairness is per process** → acceptable now (see decision #4); revisit with
  a durable/shared store.
- **`DialParallel` is new, untested code on the media path** → covered by unit tests plus
  the end-to-end SIP verification group, and sequential strategy (a loop over existing
  `Dial`) is the lower-risk default for the shipped tenant.
- **Config migration breaks a running deployment** → the removed `policy.json` key fails
  loudly at startup with a message naming the destination file, rather than silently
  routing nothing.
- **Less LLM exposure means less LLM testing** → the smoke harness (`cmd/agent-smoke`)
  still drives supervised paths; the verification group keeps the supervised scenarios
  (inbound DID to the assistant, COS deny) in the loop.

## Migration Plan

1. Add the routing-file schema, `RoutingStore` loader, and its `filemanager` reload path.
   Author `resources/tenants/default.routing.json` and `devtenant.routing.json` from the
   data currently in the `.md` files and `policy.json`.
2. Remove `symbolic_targets` from `policy.json`; make `LoadPolicyConfig` error if present.
3. Add the resolver and rewire `routing/invite.go` (decision #1), including the admission
   split. Resolution off by default is *not* offered — a half-enabled resolver would leave
   both paths live and untested.
4. Implement `DialParallel` and the ring-group strategies.
5. Delete `correctSilentRoute`; change the unknown-tool disposition; change the LLM-failure
   posture.
6. Slim the tenant prompts; update `README.md`, `docs/CONFIGURATION.md`, and `CLAUDE.md`
   (which still lists `internal/signaling/dialplan/` and `resources/config/dialplan.json`,
   and still shows the `183 → 200 OK` call flow the supervisor removed).
7. Verify: unit tests → `agent-smoke` → the end-to-end SIP group, including the four
   scenarios the archived change left at `[~]`.
8. Rollback: `git revert`. The only stateful artefacts are config files; keeping the old
   `policy.json` alongside the revert restores the previous behaviour. No data migration.

## Open Questions

- Should a ring group be dialable by the model as a symbolic target (`claims` →
  `group/claims`), or only by the resolver? Leaning yes — it is the same authorization path
  and it lets the supervisor hand an inbound caller to a queue after triage.
- `no_answer: "supervisor"` on an unanswered group: the caller has heard ringback for the
  full group budget by then. Does the supervisor answer and apologise, or does the group
  fall through to voicemail — which does not exist as a tool yet?
- Does the `*7XX` retrieval prefix belong per tenant (as designed here) or global? Per
  tenant is more flexible but means two tenants can disagree about what `*7` means on a
  shared registrar.
