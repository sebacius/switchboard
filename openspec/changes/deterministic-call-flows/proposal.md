## Why

Switchboard puts an LLM supervisor in the path of every call that deterministic
resolution cannot answer in one hop. That conflated two unlike problems.

Routing needs one to three deterministic steps inside the INVITE transaction.
Conversation needs VAD, barge-in, streaming TTS, and turn-taking. Carrying both
meant the PBX owned realtime-voice complexity it is badly placed to solve —
`runner.go:558` still carries `TODO(barge-in)` against a media capability that
was never built, so the call is half-duplex and always has been.

Once routing is a flow graph, the model has no input the graph does not already
handle. Dialed digits and DTMF resolve in code; anything spoken belongs to a
conversation that lives outside the PBX. Switchboard then needs no GPU, no model,
no prompts, and no context tuning, and routes calls at zero inference cost.

Deleting `internal/signaling/llm/` — including the OpenAI provider merged in #38
one commit before this proposal — is a deliberate reversal, not neglect. The two
provider changes (`2026-08-21-openai-llm-provider`, `2026-08-21-llm-availability`)
were good work against a premise this change retires. Conversational AI returns
later as an external agent; this clears the ground for it.

## What Changes

**Removed — the model and everything that served it.**

- `internal/signaling/llm/` in full: 15 files, both providers, the probe/warm
  path, the reasoning filter, the scripted test client.
- `agent/`: `runner.go`, `tools.go`, `executor.go`, `prompts.go`, `handlers.go`,
  `disposition.go`, `events.go`, and their tests.
- The ASR consumer path: `speechLoop`, `CallSession.Listen`, `EventSpeech`.
- LLM flags, env vars, and config fields in `config/config.go`, `app/app.go`,
  `cmd/signaling/main.go`; the LLM banner lines; `cmd/agent-smoke`.
- `resources/tenants/*.md` and `resources/config/settings.md` — those were system
  prompts. IVR wording now lives in flow nodes as TTS text.
- Dead vocabulary: the `assistant` destination sentinel, `no_answer: "supervisor"`,
  `RingGroup.NoAnswer`, `CallContext.ForAssistant`, `FirstTurnDirective`.

**Added — a flow graph.**

Single-hop resolution becomes a multi-step graph of nodes with a closed type set:
`ivr`, `tts`, `play_audio`, `dial_user`, `dial_external`, `transfer`, `hangup`.
Every node has the same `{type, entry, exits}` shape; exit names are fixed in Go
so the validator rejects an unknown exit and an unwired one alike. Flows live in
a new `resources/tenants/<tenant>.flows.json`, loaded atomically with that
tenant's `.routing.json` in one fail-closed pass.

The graph is **data, never authority**: every `dial_user`, `dial_external`, and
`transfer` destination goes through `Policy.AuthorizeDial` exactly as today, and
`dial_external` accepts symbolic targets only.

**Added — digit collection.** `EventDTMF` (`agent/events.go:15`) is an enum value
with no producer anywhere, and rtpmanager discards telephone-event packets at
`session/manager.go:591`. IVR is impossible until that is closed: RFC 4733
detection in the RTP path, SIP INFO as a fallback, inter-digit timing, terminator
handling, and per-node digit buffering with type-ahead.

**Added — load-time structural guarantees.** The validator enforces that the
inter-node graph is acyclic (cycles live inside nodes bounded by counters, so
`ivr.max_retries` is a bounded self-loop contributing no edge), that every
declared exit points at a real node, that no node is unreachable, and that every
dial target parses and passes COS. This makes every flow **provably terminating**,
which no priority-ordered dialplan can offer. Shipped as a hard startup check plus
a `switchboard-signaling validate` subcommand.

**Added — digit-map entry matching.** Patterns are required; extension ranges and
DID blocks cannot be enumerated. Vocabulary is `X`/`N`/`Z`/sets/`.`/literals —
not regex. Most-specific-wins is **computed** from the cardinality of each
position's accepted set, never a declared priority integer; hand-maintained
`"priority": 20` was the actual defect in the old dialplan. Two patterns that
overlap with equal specificity are a config error at load, not a tiebreak.

**BREAKING (behavior):** a call that resolves to nothing no longer reaches an AI
receptionist; it reaches the tenant operator, or 480 if none is configured.

**BREAKING (configuration):** `<tenant>.md` prompts, `settings.md`, the
`assistant` destination, and `no_answer: "supervisor"` are all rejected at load
with errors that name the removed vocabulary.

**Modified — `Forward` stops relaying.** Today `Forward` sends 486/480 upstream
before returning (`session_answer.go:282`), so a `no_answer → next node` edge is
impossible. It gains a non-relaying typed-outcome mode; the graph decides what
the caller hears.

**Modified — admission is re-founded.** Its channel limit was justified by
first-turn LLM latency inside Timer B. That rationale is gone; the limit survives
as an honest per-tenant concurrent-call cap, now taken before `CreateSession`
because the scarce resource is an RTP port rather than a model.

**Modified — the spend breaker is fixed.** `Policy.authorizeResolved` consumes a
unit per allowed target, so load-time COS validation would drain it. Splitting
`Classify` from `Consume` also fixes the known bug that `externalUnits` is
per-`Policy`-instance while `Policy` is built per call, making "per day"
effectively per call.

**Kept:** `resolver.go`'s registered-extension and `*7XX` retrieval behavior
(now inside the engine), `router.go`, `policy.go`, `parking.go`, `session.go`,
`trunk/`, `b2bua/`, `location/`, `dialog/`, `drain/`, `api/`, `filemanager/`.
TTS is unchanged and now serves IVR prompts. The rtpmanager `Listen` RPC and
`internal/rtpmanager/asr/` are left in place but dormant — no signaling consumer
remains, and they are exactly the batch API a future voicemail change needs.

**Explicitly out of scope:** queues, callbacks, park-as-node (existing `*7XX`
retrieval keeps working), voicemail and call recording, the MCP control plane,
eBPF media offload, in-band DTMF tone detection, and any LLM or voice agent.

**Flagged, not built — E911.** A routing table containing `911` with no emergency
handling is a compliance liability, and digit maps make it worse: a reasonable
`"9."` outbound entry silently swallows `911`. Kari's Law requires direct 911
dialing without a prefix plus on-site notification; RAY BAUM'S Act requires a
dispatchable location. Emergency must bypass COS entirely and be un-configurable.
This change adds warning-only validator checks and recommends the full change as
the immediate follow-up, before production traffic.

## Capabilities

### New Capabilities
- `call-flows`: the flow graph — node vocabulary, uniform entry/exit shape,
  terminal exits, acyclicity and load-time validation, per-call flow state, and
  the traversal record written to the CDR.
- `digit-collection`: DTMF as a first-class input — RFC 4733 detection and SDP
  negotiation, SIP INFO fallback, inter-digit timing, terminators, and type-ahead
  buffering across nodes.

### Modified Capabilities
- `call-resolution`: resolution yields a flow entry rather than a single
  destination; entry mapping gains digit-map patterns with computed specificity.
- `call-routing`: no system prompt and no Call Context block; routing hands off
  to flow execution.
- `call-admission`: rationale rewritten from LLM-latency bounding to per-tenant
  capacity control; single-phase, taken before media allocation.
- `tool-authorization`: no model to distrust, so zero-authority becomes "config
  is not authority"; COS, symbolic narrowing, the spend breaker, and decision
  logging all survive and now adjudicate flow-produced destinations.
- `call-supervisor`: removed in full — every requirement describes supervisor
  mechanics that no longer exist. Each removal records where the idea went.
- `agent-tools`: removed in full — every requirement describes LLM wire protocol
  (native `/api/chat`, OpenAI tool calling, per-call tool registry, handler
  disposition, reasoning filtering). The zero-authority principle it is often
  associated with lives in `tool-authorization`, which survives.

## Impact

**Code removed:** ~2,500 lines in `internal/signaling/llm/`, ~1,400 lines of
runner/tools/prompts in `agent/`, ~2,000 lines of their tests, plus
`cmd/agent-smoke`.

**Code added:** `internal/signaling/dialplan/` (routing config moved from
`agent/`, plus flow definitions, digit maps, and the shared validator);
`internal/signaling/flow/` (engine, cursor, nodes, CDR trace); RFC 4733 decoding
and a `CollectDigits` RPC in rtpmanager; `cmd/flow-smoke`.

**gRPC contract:** additive only. A `CollectDigits` unary RPC, and `CodecOffer`
fields on `CreateSessionRequest`/`CreateSessionResponse` so the offer's `a=rtpmap`
survives — today `extractSDPInfo` (`routing/invite.go:326-361`) keeps only bare
payload-type numbers, which makes a peer's dynamic telephone-event PT unknowable.
No field renumbering, no removals. Each new RPC fans out through six layers:
proto → `mediaclient.Transport` → `GRPCTransport` → `Pool` → `CallSession` → fakes.

**Config:** new `<tenant>.flows.json`; `<tenant>.md` and `settings.md` deleted;
`policy.json` keeps `channel_limits` unchanged in name and gains a more honest
meaning. `resources/tenants/devtenant.routing.json` needs migration — it ships
both an `assistant` extension and a group `no_answer`.

**Operations:** the signaling server no longer requires a reachable LLM to boot
(`app.go:191` is a hard boot failure today) and makes no network egress for
routing. New `--cdr-path`. New `validate` subcommand wired into `make validate`
and CI.

**Docs:** `README.md`, `docs/CONFIGURATION.md`, and `docs/TENANT-EXAMPLE.md` all
document the prompt/routing/policy split and need rewriting rather than editing.
`CLAUDE.md` needs its call-flow diagram and folder structure updated.

**Dependencies:** none added. RFC 4733 decoding uses `pion/rtp` and rtpmap
parsing uses `pion/sdp/v3`, both already present; graph and digit-map algorithms
are stdlib. `go mod tidy` must produce no diff.
