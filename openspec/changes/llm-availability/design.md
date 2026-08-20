## Context

`deterministic-call-resolution` measured the supervisor's latency on this
hardware and recorded the finding in its tasks: ~44s cold prompt evaluation for a
3.4k-token prompt, ~0.3s once Ollama has the prefix cached, with generation
adding 4–9s. It also concluded that slimming the tenant prompt helped but did not
solve the problem, because a cold first turn still exceeded the 30s deadline.

A live call then produced exactly that failure, and the log made it look like an
outage:

```
[05:22:17] call resolution ... resolved=false kind=handoff
[05:22:47] supervisor LLM unavailable; ending the call deliberately
           error=Post "http://localhost:11434/api/chat": context deadline exceeded
```

30.0s to the second. `/api/ps` on the host showed the model resident with an
expiry consistent with a last touch at 05:22:47 — Ollama had been loading and
running it the whole time. The server was fine; the deadline was not.

## Goals / Non-Goals

**Goals:**

- A caller never pays a model load inside their turn budget.
- A misconfigured or missing model is discovered at startup, not on a call.
- "Slow" and "absent" are distinguishable in the log.
- The LLM stays optional: an unreachable model must not stop the server or stop
  deterministic routing.

**Non-Goals:**

- Streaming the model's response to cut perceived latency. Worth doing; a bigger
  change to the runner's speak path.
- Prompt caching or summarisation to shrink the per-turn prompt.
- Choosing a model *in general*. The operator picks with `--llm-model`. The
  default was re-examined here, though, because a smaller model looked like the
  obvious answer to first-turn latency: `qwen3:4b` writes its chain of thought
  into `content` under `think: false`, which the runner speaks aloud, and
  `qwen3:0.6b` will not emit a tool call. `qwen3:8b` stays the default on
  measured evidence — see the comparison in tasks.md.
- Retrying a failed turn. The runaway breaker's reasoning applies — a retry
  inside the caller's patience budget is another chance to be slow.

## Decisions

**1. Warm at startup, in the background.** The probe does two things — confirm
the model exists, then load it — and logs the load duration explicitly, because
that number *is* what the first caller would otherwise have waited. It runs in a
goroutine off `app.New`: a cold load can take minutes and the SIP stack must not
wait on it.

*Alternative rejected:* warming lazily on the first call. That is where the cost
already lands, and it is the case we are trying to remove.

**2. `keep_alive` on every request, not just the warm-up.** Ollama's default is 5
minutes. A PBX is idle most of the night, so warming at startup alone buys one
quiet spell before the next caller pays the load again. Sending it on every
request means residency is refreshed by ordinary traffic. Default `30m`,
configurable, `-1` for indefinite.

**3. Two budgets, because they are bounded by different things.** The first turn
runs while the caller hears ringback and may include a model load — the limit is
caller patience. A mid-call turn is a silence with an open mic after the caller
has stopped speaking — 30s is already generous there. One number cannot be right
for both, and the single 30s was chosen for a constraint that no longer exists:
the first turn used to run inside the INVITE transaction against SIP Timer B, and
`routing/invite.go` now sends 180 Ringing before it, which moves that transaction
to Proceeding.

`runTurn` takes the budget as a parameter rather than reading config, so the
choice is visible at each call site (`run` → first-turn, `dispatchLoop` and
`autonomousReprompt` → mid-call).

**4. `Ready()` had to start meaning something.** It returned `serverURL != ""`,
which is true for a URL pointing at nothing, and nothing called it. The
`ChatClient` interface already declared it and `ScriptedClient` already satisfied
it, so the seam existed — it just never told the truth.

**5. Warn, never fail.** This mirrors `rtpmanager/server/probe.go` deliberately,
including its tone: say what is wrong, say what it will cost, name the flag to
fix it. A missing LLM is survivable now in a way it was not before this
repository had deterministic resolution — a registered extension forwards with no
model at all — so failing startup would trade a working PBX for a strict one.

## Risks / Trade-offs

- **Resident memory becomes permanent** → the deployment already sized for the
  model; `--llm-keep-alive` can be shortened, at the cost this change exists to
  remove.
- **A 90s first turn is a long ring** → it is bounded by caller patience rather
  than a protocol timer, and the alternative is the call failing outright. A
  warmed model makes it academic; the budget is the safety net, not the plan.
- **The warm-up sends a real request** → one trivial generation per start.
- **A slow warm-up hides a slow box** → the load duration is logged precisely so
  that it does not.

## Migration Plan

1. Ship. Defaults are safe: `30m` residency, 30s mid-call, 90s first turn.
2. Watch the `LLM ready and warmed` line's `load_time` on the first restart —
   that number is the deployment's cold cost and the argument for its model
   choice.
3. If a box cannot meet 90s cold, raise `--first-turn-timeout` or move to a
   smaller model; both are flags now.
4. Rollback: `git revert`. No config migration — every new setting has a default.

## Open Questions

- Should a turn that times out be retried once against a now-warm model, or is
  that just another chance to spend the caller's patience?
- Should `Ready()` gate admission, so a tenant whose calls all need the supervisor
  rejects fast while the LLM is down instead of answering to apologise?
