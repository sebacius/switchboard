## Context

Two archived changes built the system this one dismantles. `llm-pbx-supervisor`
(2026-08-19) deleted the JSON dialplan and put an untrusted model behind a
deterministic policy layer on every call. `deterministic-call-resolution`
(2026-08-21) then walked half of that back, putting a resolver in front of the
model after live testing showed a 633-line prompt greeting callers instead of
routing them and a 57s first turn against a 30s deadline.

That second change proved the point without finishing it. Once a resolver
handles extensions, DIDs, retrieval codes, and ring groups deterministically, the
model's remaining job is the *multi-step* cases: menus, conditional dialing,
fallbacks. Those are not conversation. They are a graph, and a graph is
cheaper, faster, auditable, and cannot greet a caller when it was asked to route
one.

What the model was genuinely better at — understanding intent from speech — was
never actually working. `runner.go:558` still carries `TODO(barge-in)` against a
media capability that was never built, so every supervised call was half-duplex.

Current state this change starts from:

- `Resolver.Resolve` (`resolver.go:134`) is a pure function returning one of four
  destination kinds. `CallResolution.Handle` (`resolution_exec.go:68`) executes it
  and returns `bool` — "I own this call and it's finished."
- `EventDTMF` (`agent/events.go:15`) is an enum value with **no producer
  anywhere**. rtpmanager drops telephone-event packets at
  `session/manager.go:591`.
- There is **no pattern matching anywhere**. Every lookup is an exact map hit.
- `llm.New` (`app.go:191`) is a hard boot failure, so a developer needs Ollama
  running to start the process.

## Goals / Non-Goals

**Goals:**

- Route every call with no model, no agent, and no network egress.
- Express multi-step routing — menus, prompts, transfers, conditional dialing —
  as validated data.
- Make every flow provably terminating at load time, not at runtime.
- Make DTMF a real input, since it currently does not exist.
- Preserve the authorization boundary exactly: default-deny external, symbolic
  narrowing, spend breaker, per-tenant COS.
- Add no third-party dependency.

**Non-Goals:**

- Voicemail, call recording, and any storage lifecycle. Cut deliberately — see
  Decision 2.
- Queues, callbacks, park-as-node, the MCP control plane, eBPF media offload.
- Conversational AI in any form. It returns later as an external agent.
- In-band DTMF tone detection.
- E911. Flagged as the immediate follow-up; see Risks.

## Decisions

### 1. Delete the LLM rather than disable it

Leaving the `llm` package behind a flag would keep 2,500 lines, two provider
clients, a probe/warm path, and a reasoning filter alive with no caller — and
would keep `llm.New`'s hard boot failure one config mistake away. It also leaves
the `assistant` destination sentinel and `no_answer: "supervisor"` as live
branches the flow engine would have to carry.

Deleting the OpenAI provider merged in #38, one commit before this proposal, is
the uncomfortable part and is deliberate. The provider work was correct against
its premise; the premise is what changed.

### 2. Cut voicemail and recording from this change

Originally in scope, cut after exploration showed the cost was structural rather
than incremental.

There is no persistent per-session RTP socket. Four subsystems bind the session's
one local port and are mutually exclusive: file playback
(`media/service.go:155`), TTS playback (`:251`), capture
(`session/manager.go:528`, which force-stops playback and sleeps 100ms to take
the socket), and bridge relay (`bridge/bridge.go:140,148`). Recording has to
coexist with playback, so it forces a per-session RTP demux with sink fan-out —
a rewrite of `media/`, `session/`, and `bridge/`, and by far the largest and
riskiest item in the change.

Cutting recording collapses the requirement. The only remaining place inbound RTP
must be read while audio is transmitting is prompt-and-collect, and that is one
operation, not a general capability. See Decision 6.

Consequence: nothing transcribes anything, so the ASR consumer path goes. The
rtpmanager `Listen` RPC and `internal/rtpmanager/asr/` are left **in place and
dormant** — they have no signaling dependency, cost nothing, and
`asr.Client.Transcribe(ctx, []byte) (string, error)` is already exactly the batch
API a future voicemail change needs.

### 3. Fixed exit names in Go, and terminal exits are not declarable

The alternative — free-form exit names — makes "unknown exit" undetectable, so a
typo (`"no-answer"` for `"no_answer"`) becomes a silently dead branch discovered
during an outage.

Fixing the names in `var nodeExits map[NodeType][]string` makes three checks
possible at load: the exit exists for that type, every non-terminal exit is
wired, and every target names a real node. Requiring *every* non-terminal exit
rather than defaulting means "what happens on busy" is always written down.

Terminal exits (`answered`, `accepted`) must be **absent** from config. The
alternative, allowing `"answered": ""`, needs an empty-string special case in
every check. Making it an error keeps "every declared exit points at a real node"
uniform.

This means "after the bridge ends, do X" is inexpressible. That is correct — the
flow's job ends when the call is connected — and is documented rather than worked
around.

### 4. Acyclic between nodes, bounded counters inside them

A cycle-free inter-node graph plus per-node bounded repetition makes every flow
provably terminating by construction. No priority-ordered dialplan offers this:
Asterisk's `Goto` can loop forever and you find out in production.

An `ivr` node's `max_retries` is a bounded self-loop handled *inside* the node,
contributing no graph edge. So the common re-prompt case works without weakening
the guarantee.

**Known cost:** "press 9 to return to the main menu" is a cycle, and it is the
single most-requested IVR feature. Under a strict DAG the only expression is
duplicating the menu node. Holding the line in v1 as specified, but implementing
a `maxHops` runtime backstop anyway — so relaxing later to counter-bounded
back-edges is a validator change, not an executor change. This is the likeliest
spec revision within a month.

### 5. Computed specificity, never a declared priority

The old dialplan's defect was not that it matched patterns; it was
hand-maintained `"priority": 20` integers that drifted from intent and had to be
renumbered to insert a rule.

Specificity is computed from the **cardinality of each position's accepted set**
— literal 1, `[147]` 3, `[2-8]` 7, `N` 8, `Z` 9, `X` 10, trailing wildcard
infinite. This is principled rather than assigned: `N` automatically beats `Z`
because 8 < 9, and sets of any size work without a rule.

Comparison is a **per-position vector, not a weighted scalar** — a scalar invites
collisions between unlike patterns. *P dominates Q* iff `card(P[i]) <= card(Q[i])`
everywhere and strictly less somewhere.

Ambiguity is a load error, not a tiebreak. `NX` and `XN` both match `22` with
neither dominating; there is no defensible winner, so the config is wrong. This
is exactly why the vocabulary is restricted — specificity is well-defined for
digit maps and undefined for regular expressions.

Implementation is a compiled pattern list with a linear scan. Not a trie: tables
are tens of entries, matching happens once per call, and a trie makes specificity
scoring and pairwise ambiguity detection materially harder for no gain.

### 6. One socket-owning `CollectDigits` RPC instead of an RTP demux

With recording cut, prompt-and-collect is the only operation needing simultaneous
transmit and receive. It becomes a single unary RPC that owns the session's socket
for the whole operation, transmitting prompt frames and parsing inbound RTP in the
same loop.

Prompt and collection must be **one RPC**, not two calls. Split, a digit arriving
between "prompt finished" and "collection started" is lost — and inter-digit
timing measured across gRPC round trips to a possibly-remote rtpmanager is wrong.
Keep the clock next to the socket.

This works because an `ivr` node always runs pre-bridge on the A-leg, so it never
contends with bridge relay. The four existing binders stay; they simply never
overlap.

**Type-ahead is not optional.** Digits buffer continuously and a collection drains
the buffer before waiting. Without it a caller who knows the menu loses digits
between nodes — the most common IVR complaint there is.

Consequence accepted: DTMF on a *bridged* leg stays invisible, because
`bridge.go:184,241` relay raw bytes without parsing. Nothing in scope needs it —
blind transfer only, no attended transfer, no mid-call feature codes.

### 7. Fix SDP before DTMF, and echo the offerer's payload type

`extractSDPInfo` (`routing/invite.go:326-361`) captures only bare payload-type
numbers from `MediaName.Formats` and discards the offer's `a=rtpmap` attributes.
So a peer offering telephone-event on PT 96 or 100 — common on real ATAs — cannot
be identified at all. Assuming 101 would work in a lab and fail in the field.

Carry rtpmap through as an additive `CodecOffer` field, negotiate by matching the
encoding name, and answer with the payload type **the offerer proposed**. If the
offer has no telephone-event, the answer must not invent one; that leg has no DTMF
transport and the `ivr` node degrades by a declared exit rather than appearing to
hang.

`sdp/builder.go` already has `"101": "telephone-event/8000"` in its rtpmap table
(`:96`) and already emits `fmtp 101 0-15` (`:113-121`). Both are unreachable
because `builder.go:35` hardcodes `formats` to a single element. The answer side
is nearly free once negotiation carries two formats.

### 8. `Forward` must stop relaying — the load-bearing session change

Today `Forward` on failure calls `relayForwardFailure` (`session_answer.go:282`),
which **sends 486/480 upstream to the caller** and then returns a typed
`*agent.DialError`. A `no_answer → next node` edge is therefore impossible: the
caller already got a final response.

Add `ForwardOutcome`/`ForwardGroupOutcome` returning a typed `DialOutcome`
(`Answered|NoAnswer|Busy|Rejected|Unavailable|Failed`) and relaying nothing. The
graph decides what the caller hears; a flow ending without relaying gets a 480 on
its behalf.

The raw information already exists — `DialError.IsBusy()`/`IsUnavailable()`/
`IsTimeout()`, `ErrDialTimeout`, `ErrNoContacts`, `ErrTargetNotFound` — it is
simply discarded above `sessionImpl`. Keep `Forward` itself as `ForwardOutcome` +
relay for the operator fallback.

`ForwardGroup` must also stop collapsing every member outcome into
`ErrGroupNoAnswer`. `DialParallel` (`b2bua/call_service.go:252`) already collects
per-target outcomes and discards the losers. **Collect into a mutex-guarded slice
and return a copy; change nothing about winner selection or loser cancellation** —
that race window is the most delicate code in the repo.

### 9. Answer only for media; no 183, no early media

A flow that speaks before dialing must either answer or use early media. This
codebase has never sent a 183.

Rule: *a dial before any media node forwards; once any media node runs the call is
answered and later dials bridge.* This falls out of invariants that already exist
— `HasAnswered()` (`session_answer.go:58`), `Dial` errors if unanswered
(`session.go:370`), `Forward` errors if answered (`session_answer.go:116`).

183 + early media was rejected: PRACK/100rel interop, carriers that drop early
media, and ringback conflicts, for a small payoff. Consequence: a one-node dial
flow behaves byte-identically to today, and a menu-first flow answers before the
greeting.

### 10. Split `Policy.Classify` from `Policy.Consume`

`authorizeResolved` (`policy.go:243`) consumes a spend unit on every *allowed*
external target. Validating N external destinations at load would burn N units of
the tenant's daily budget — the validator would be a denial-of-service against
the thing it validates.

Splitting the pure verdict from the breaker fixes that, and is also the natural
moment to fix a known shipped bug: `externalUnits` lives on a `Policy` instance
and `Policy` is built per call (`app.go:280`), so `max_external_units_per_day`
resets on every INVITE and has never enforced a daily limit. Hoist it to a
process-lifetime, per-tenant, day-bucketed counter.

### 11. Do not check registration at load

The requirement "every `dial_*` target resolves and passes COS at load" is
tempting to read as including registration. It must not.

`Resolver.isRegistered` (`resolver.go:234`) reads runtime location bindings. There
is no static user list in this system. A load-time registration check would fail
every boot, before any phone has registered. Load-time validation covers syntax,
group existence, and COS; an unregistered extension is a runtime `unavailable`
exit.

### 12. Package split to avoid an import cycle

`flow` needs `agent.CallSession` and `agent.Policy`; the routing table needs to
carry flow types. Putting the engine in `agent` creates a cycle.

`internal/signaling/dialplan/` holds pure data and validation with no dependency
on `agent`. `internal/signaling/flow/` holds the executor and imports both.
`agent` imports `dialplan`. `routing/invite.go` imports `flow`.

Reusing the name `dialplan` — deleted by `llm-pbx-supervisor` — is deliberate. The
arc is that the dialplan returns, as a provably-terminating graph rather than a
priority-ordered table.

### 13. Two files per tenant, loaded as one atomic unit

Flows live in `<tenant>.flows.json`, separate from `<tenant>.routing.json`.

The risk of a second file is a window where flows reference a group the other file
just removed. Designed out rather than accepted: both files for a tenant are read
and validated **together in one `Reload` pass**, preserving today's fail-closed
behaviour (`routing_config.go:241` — a bad edit keeps the old cache in force).

### 14. `Engine.Handle` keeps `CallResolution.Handle`'s signature

Same shape, same `bool` contract, so `invite.go` changes by one line. `true` still
means "I own this call and it's finished"; `false` now means only "no entry
mapping" → operator → 480.

It **must keep blocking on the SIP transaction goroutine** (`invite.go:229`).
sipgo calls `tx.Terminate()` when the handler returns, so responses sent from a
spawned goroutine are silently swallowed — a defect found by live testing during
`deterministic-call-resolution` and documented at `invite.go:219-228`. Every node
is a synchronous step; a dial reaching `answered` blocks for the life of the
bridge and then returns.

### 15. Admission becomes capacity control, taken earlier

The channel limit's rationale — bound concurrent first-turn LLM latency inside
Timer B — evaporates. It survives as an honest per-tenant concurrent-call cap,
which is what `policy.json`'s `channel_limits` already says, so there is **no
config migration**.

Merge the two phases into one `Admit` sited before `CreateSession`. Today the slot
is taken after the media session exists (`invite.go:246`) because the scarce thing
was the model; now it is the RTP port, so a tenant over its limit must be rejected
before burning one. The prompt check and `PromptSource` go with the prompts.

### 16. CDR: the minimum that answers "why did this caller end up here"

`store.CDR` (24 fields, including a `Metadata` JSON blob) and `CDRRepository`
already exist with **zero implementations and zero callers**;
`events.CallEndedEvent` and `SubjectCDRRaw` exist and the whole `events` package
is imported by nothing. It is easy to over-build here.

Build only: `flow.Trace` on the cursor → one structured log line + a `CDRSink`
interface with exactly one implementation (append-only JSONL under `--cdr-path`),
hops serialized into the existing `Metadata` blob. Fold `policy.logDecision`'s
verdicts into the same record, closing the `TODO(cdr)` at `policy.go:295`. No SQL
repository, no events bus, no NATS subjects.

## Risks / Trade-offs

**DTMF is three transports and this change ships one.** RFC 4733 requires the peer
to offer telephone-event. Real deployments also use SIP INFO (no handler exists;
`app.go:392-396` registers only REGISTER/INVITE/BYE/ACK/CANCEL) and in-band tones
(needs a hand-written Goertzel detector under the zero-dependency rule). Shipping
4733 + SIP INFO, with an honest degraded exit when neither is available. In-band
is a separate change. *Mitigation:* the `NO_DTMF_TRANSPORT` outcome and a load
warning when a flow with `ivr` nodes is reachable.

**The `DialParallel` signature change touches the most delicate code in the
repo.** `b2bua` is ~2,500 lines of leg state machines and teardown races, and the
change sits in the window between winner selection and loser cancellation.
*Mitigation:* collect outcomes into a mutex-guarded slice, return a copy, and
change nothing about selection or cancellation.

**Blocking the transaction goroutine, now for minutes.** Already true for bridges,
but a menu-navigation phase is new. The 200 OK must go out before the first long
media node or a retransmitting UAC times out. *Mitigation:* answer at the top of
every media node and assert `HasAnswered()` before collecting digits. N concurrent
calls means N blocked goroutines, which is now the channel limit's job to bound —
another reason Decision 15 keeps it.

**Config migration is a real break with a bad default error.** Every
`"600": "assistant"` and every `no_answer: "supervisor"` becomes invalid, and
`devtenant.routing.json` ships one of each. *Mitigation:* the loader must say
*"'assistant' is no longer a valid destination; the LLM supervisor was removed"*,
not *"unknown destination"*.

**Test debt.** ~2,000 lines of tests are deleted and `resolution_exec_test.go` is
rewritten. *Mitigation:* build **one** fake session in `flow/flowtest`, not a
fourth variant, and replace `cmd/agent-smoke` with `cmd/flow-smoke` — the harness
shape already exists and it is by far the fastest loop for authoring graphs.

**Zero new dependencies is achievable, with one temptation.** RFC 4733 decoding is
~40 lines over `pion/rtp`; rtpmap parsing uses `pion/sdp/v3`; graph and digit-map
algorithms are stdlib. The temptation is a JSON-schema library for validation —
refuse it. `DisallowUnknownFields` plus hand-rolled checks give strictly better
operator errors, which is the entire point.

**E911 — this change makes the exposure worse.** Nothing special-cases `911`. A
tenant with the documented-safe `AllowExternalDial: false` cannot dial it at all;
neither can one with external enabled and an empty allowlist. Digit maps add a new
failure: a reasonable `"9."` outbound entry silently swallows `911`. Kari's Law
requires direct 911 dialing without a prefix plus on-site notification; RAY BAUM'S
Act requires a dispatchable location; emergency must bypass COS entirely and be
un-configurable. **Not built here.** Two warning-only validator checks are
included as the cheapest possible guardrail, and the full change is recommended as
the immediate follow-up, before this system carries production traffic.

## Migration Plan

1. Remove the LLM first. The intermediate state is a working single-hop
   deterministic PBX: resolver, forward, ring groups, retrieval — everything today
   does minus the fallback, which becomes operator-or-480. Safe to leave the
   branch here.
2. Move `routing_config.go` into `dialplan` as a pure rename commit, so the flow
   additions review separately from ~15 files of import churn.
3. Land typed dial outcomes and the policy split before the engine, since the
   engine depends on both.
4. Land SDP rtpmap and `CollectDigits` before the `ivr` node.
5. Swap `resolution.Handle` for `flow.Engine.Handle` last, and delete
   `resolver.go`/`resolution_exec.go` in the same commit.

Config migration for operators: delete `<tenant>.md` and `settings.md`; remove any
`assistant` extension and any group `no_answer`; add `<tenant>.flows.json` for any
tenant that needs more than one hop. `switchboard-signaling validate` reports every
problem at once rather than failing on the first.

## Open Questions

- **Should `ivr` support a `#`-terminated variable-length collect for
  extension-dialing inside a menu ("dial an extension at any time")?** The
  `CollectDigits` contract supports it; whether an `ivr` node exposes it, or a
  separate `collect` node type does, is unresolved. Deferred until a real config
  wants it.
- **Round-robin cursor durability.** The cursor is per-process and in-memory today
  (`resolution_exec.go:254`), so it resets on restart and diverges across
  replicas. Unchanged by this change, but flows make groups more prominent.
- **Does `transfer` need REFER, or is re-INVITE bridging enough?** v1 ships blind
  transfer as a dial-and-bridge. True SIP REFER with NOTIFY progress is a
  different feature and is not specified here.
