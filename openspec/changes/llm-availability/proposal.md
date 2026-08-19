## Why

A live call failed to engage the supervisor and the caller heard "Sorry, the
assistant is unavailable right now." The LLM was present and working. The turn
failed at **exactly 30.0s** — `defaultTurnTimeout` — while Ollama was loading a
~5GB model; `/api/ps` on that host showed the model becoming resident at the
same second the turn gave up.

So the model was cold, and loading plus prompt evaluation outran the deadline.
That matches the benchmark taken during `deterministic-call-resolution`: ~44s
cold prompt evaluation for a 3.4k-token prompt on this hardware, ~0.3s once
Ollama has the prefix cached.

The failure path itself behaved correctly — the caller got a deliberate message
rather than a dropped call. The problem is that nothing in the system prevents
the cold start, and nothing makes the cause legible:

- Nothing warms the model, so the first caller after startup pays the full load.
- No `keep_alive` is sent, so Ollama unloads after 5 idle minutes — shorter than
  the gap between calls on a quiet PBX, which means the first call of the day
  re-pays it.
- `Client.Ready()` returned `serverURL != ""` — a statement about the flags, not
  the world — and nothing called it. The RTP manager has had a real startup probe
  for TTS/ASR since the supervisor landed, for exactly this class of bug.
- One log line, "LLM unavailable", covered both "too slow" and "not there".

## What Changes

- **Model warm-up and residency.** A startup probe checks the LLM answers and
  has the configured model pulled, then issues one small request to load it,
  logging how long that took — the time a caller would otherwise have waited.
  Every chat request now carries `keep_alive` (default `30m`) so the model stays
  resident between calls.
- **A real readiness check.** `Client.Ready()` contacts `/api/tags` instead of
  inspecting its own configuration.
- **BREAKING (behaviour): the first turn gets its own budget**, default 90s,
  separate from the mid-call turn budget which stays at 30s. The two are bounded
  by different things: the first turn runs while the caller hears ringback and
  may include a model load, whereas a mid-call turn is a silence with an open
  mic. The original single 30s existed because the first turn ran inside the
  INVITE transaction against SIP Timer B; the handler now sends 180 Ringing
  before that turn, which moves the transaction to Proceeding and removes the
  constraint.
- **Both budgets are configurable** (`--turn-timeout`, `--first-turn-timeout`),
  as is residency (`--llm-keep-alive`). Only `cmd/agent-smoke` had a turn-timeout
  flag before; the server had none.
- **Timeouts and outages are logged differently**, with the elapsed time and the
  budget, so a slow model stops looking like a missing one.

## Capabilities

### New Capabilities

<!-- None: this is supervisor behaviour, not a new capability. -->

### Modified Capabilities

- `call-supervisor`: adds a requirement that the supervisor verifies and warms
  its model at startup and keeps it resident, and revises the unavailability
  requirement so the first turn may carry a larger budget than mid-call turns
  and the two failure modes are distinguishable.

## Impact

- **New code**: `internal/signaling/llm/probe.go` (`ProbeAndWarm`, `HasModel`,
  `Warm`, `listModels`) plus its tests.
- **Modified**: `internal/signaling/llm/client.go` (keep-alive, real `Ready`),
  `internal/signaling/llm/native.go` (`keep_alive` on the request),
  `internal/signaling/agent/runner.go` (`FirstTurnTimeout`, per-turn budget,
  failure classification), `internal/signaling/config/config.go` and
  `internal/signaling/app/app.go` (flags and wiring).
- **Operational**: a deployment with a large model now holds it resident. That
  is memory the deployment already sized for, but it is a change in steady-state
  footprint.
- **Dependencies**: none added.
- **Not affected**: deterministic resolution, which makes no LLM request at all —
  a registered extension still forwards with the LLM completely down.
