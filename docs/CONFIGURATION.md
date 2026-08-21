# Configuration Reference

All Switchboard services can be configured via environment variables or command-line flags. Flags take precedence over environment variables.

## Default Ports

| Service | Port | Protocol | Purpose |
|---------|------|----------|---------|
| Signaling | 5060 | UDP | SIP signaling |
| Signaling | 8080 | HTTP | REST API |
| RTP Manager | 9090 | gRPC | Media control |
| RTP Manager | 10000-20000 | UDP | RTP media |
| UI Server | 3000 | HTTP | Admin dashboard |
| TTS (Piper) | 8000 | HTTP | Text-to-speech |
| ASR (Whisper) | 8001 | HTTP | Speech recognition |
| Ollama | 11434 | HTTP | LLM inference |

## Signaling Server

### Network Configuration

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--port` | `PORT` | 5060 | SIP listen port (UDP) |
| `--bind` | `BIND` | 0.0.0.0 | Bind address for SIP |
| `--advertise` | `ADVERTISE` | (auto-detected) | Public IP for SIP Contact headers |
| `--api-port` | `API_PORT` | 8080 | REST API HTTP port |

### RTP Manager Connection

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--rtpmanager` | `RTPMANAGER` | localhost:9090 | Comma-separated RTP Manager addresses |

Example with multiple RTP Managers:
```bash
./switchboard-signaling --rtpmanager "rtpmanager1:9090,rtpmanager2:9090,rtpmanager3:9090"
```

### Call Supervisor

Calls that deterministic resolution cannot answer are handled by the LLM
supervisor (see the tenant routing table above for what resolves without it).

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--llm-server` | `LLM_SERVER` | http://localhost:11434 | Ollama server URL |
| `--llm-model` | `LLM_MODEL` | qwen3:8b | Model used by the call supervisor |
| `--llm-keep-alive` | `LLM_KEEP_ALIVE` | 30m | How long Ollama holds the model resident after a request (`-1` for indefinitely) |
| `--first-turn-timeout` | `FIRST_TURN_TIMEOUT` | 90s | Deadline for the **first** supervisor turn |
| `--turn-timeout` | `TURN_TIMEOUT` | 30s | Deadline for a **mid-call** supervisor turn |
| `--tts-voice` | `TTS_VOICE` | *(empty)* | Piper voice for the supervisor (empty uses the RTP manager default) |

#### Startup probe and model warm-up

At startup the signaling server checks that the LLM answers and that
`--llm-model` is actually pulled on it, then sends one small request to load the
model, logging how long that took:

```
LLM ready and warmed load_time=6.774s note=this is what the first caller would otherwise have waited inside their turn budget
```

That number is the deployment's cold cost. Nothing here fails startup — a
missing model or an unreachable server is a warning, because calls that resolve
deterministically do not need the model at all:

```
LLM server is up but does NOT have the configured model; every supervised call will fail.
Pull it (ollama pull) or fix --llm-model  available=[gemma3:4b qwen3:8b llama3.2:3b]

LLM server did not answer; supervised calls will fail until it does.
Deterministic routing is unaffected — check --llm-server
```

#### Why not a smaller model

The obvious lever for first-turn latency is a smaller model. Measured on this
hardware with `think: false` and the real prompts, it is the wrong one:

| Model | Greeting | "I need to file a claim" | Reasoning in `content` |
| --- | --- | --- | --- |
| **qwen3:8b** | 19 tok, no tool | empty content + `dial(claims)` | 0 / 4 trials |
| qwen3:4b | 1552–4027 tok | `dial(claims)` after 3236–6390 chars of monologue | **4 / 4 trials** |
| qwen3:0.6b | 10 tok, no tool | **no tool call at all** | 0 / 4 trials |

`qwen3:4b` evaluates the prompt faster (23.8s vs 44.0s cold) but generates so
much more that the turn takes **148s against the 8b's 56s** — and with
`think: false` it puts its chain of thought in `content`, which is the field the
supervisor speaks. `qwen3:0.6b` is fast and quiet but will not dial.

Use `--llm-keep-alive` and the startup warm-up for latency. Change `--llm-model`
only with the same kind of measurement behind it.

#### Why two turn budgets

They are bounded by different things. The **first** turn runs while the caller
hears ringback and may include loading a multi-gigabyte model, so its limit is
caller patience. A **mid-call** turn is a silence with an open mic after the
caller has stopped speaking, where 30s is already a long time.

A single 30s budget for both is what produced this symptom in the field: a cold
model took longer than 30s to load and answer, the turn was cancelled, and the
caller heard "the assistant is unavailable" from a perfectly healthy LLM. Warm-up
plus `--llm-keep-alive` is the fix; the larger first-turn budget is the safety
net for a genuine cold start.

If a turn does exceed its budget the log says so specifically, with the elapsed
time — distinct from the server being unreachable, which is a different problem
with a different fix.
| `--policy-config` | `POLICY_CONFIG` | resources/config/policy.json | Class-of-Service and channel-limit configuration |

The signaling server **requires** a reachable LLM server: without a supervisor
there is nothing to route calls, so it refuses to start rather than running
unsupervised. It uses Ollama's native `/api/chat` endpoint with `think: false`,
so `thinking`, `content`, and `tool_calls` come back as separate fields and
reasoning is never spoken.

### Prompts and Tenants

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--settings-path` | `SETTINGS_PATH` | resources/config | Directory containing settings.md |
| `--tenants-path` | `TENANTS_PATH` | resources/tenants | Directory containing tenant .md files |
| `--routing-path` | `ROUTING_PATH` | (same as `--tenants-path`) | Directory containing `<tenant>.routing.json` files |

A tenant's prompt is `settings.md` followed by `tenants/<name>.md`. There is no
default tenant: an unattributable call is rejected rather than supervised by a
guess.

A tenant is described by **two** files. The `.md` is judgement — identity, tone,
business facts, escalation language. The `.routing.json` is data — extensions,
DIDs, ring groups, and the names the model may dial. A tenant with only a routing
table can be **routed** but not supervised; a tenant with only a prompt can be
supervised but resolves nothing deterministically.

### Tenant Routing Table

`<tenant>.routing.json` is what deterministic resolution routes by, and the
source of the symbolic targets `dial` narrows to. Both read the same file, so a
name cannot resolve one way for the resolver and another way for the model.

```json
{
  "operator": "user/150",
  "retrieval_prefix": "*",
  "extensions": { "105": "user/105", "100": "assistant", "130": "group/claims" },
  "symbolic_targets": { "sales": "group/sales", "front-desk": "user/150" },
  "dids": { "+15558001200": "assistant", "+15558001250": "group/claims" },
  "groups": {
    "claims": {
      "strategy": "sequential",
      "members": ["user/130", "user/120"],
      "member_timeout_ms": 15000,
      "no_answer": "supervisor"
    }
  }
}
```

| Field | Meaning |
|-------|---------|
| `operator` | Fallback human: where an unknown tool name sends the caller, and the `no_answer: operator` outcome. Empty means those paths keep the call alive instead of transferring — never a hangup |
| `retrieval_prefix` | Dial prefix for picking up a parked call; the digits after it are the slot ID (`*701` → slot 701). Internal callers only |
| `extensions` | Dialed extension → destination. `user/NNN` is an endpoint, `group/NAME` a ring group, `assistant` hands off to the supervisor |
| `symbolic_targets` | Capability narrowing: the only names the model may dial. It can never express a raw number through `dial` |
| `dids` | Inbound DID → destination **within** the tenant. The DID → *tenant* step happens earlier, in `routes.json`. Matched on digits, so the leading `+` is optional |
| `groups` | Ring groups. `strategy` is `sequential` or `round-robin`; `member_timeout_ms` bounds one member's ring; `no_answer` is `supervisor`, `operator`, or `hangup` |

A missing routing file means nothing resolves deterministically for that tenant
and every call goes to the supervisor. A **malformed** one is a hard startup
error — an unparseable table would silently send every call that should have been
routed in milliseconds to a language model instead.

Both files reload through `POST /api/v1/config/reload`.

### Trunk and DID Routing

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--trunk-config` | `TRUNK_CONFIG` | resources/config/trunk_peers.json | SIP trunk peers |
| `--routes-path` | `ROUTES_PATH` | resources/config/routes.json | DID → tenant mapping |

### Policy Configuration

`policy.json` is the deterministic authorization boundary — the supervisor cannot
change it, and anything it does not grant is denied. A **missing file is not an
error**: it yields the safest posture (no external dialing anywhere, default
channel limit).

It says only what is *permitted*, never what a name *means*: `symbolic_targets`
moved to the tenant routing table, and a leftover `symbolic_targets` key here is
a hard startup error naming the file the entries belong in. Two sources for one
name is exactly the drift the move was meant to end.

```json
{
  "default_channel_limit": 10,
  "tenants": {
    "acme": {
      "channel_limit": 20,
      "allow_external_dial": false,
      "external_allowlist": [],
      "max_external_units_per_day": 0,
      "allow_caller_provided_number": false
    }
  }
}
```

| Field | Meaning |
|-------|---------|
| `default_channel_limit` | Per-tenant cap on concurrent **supervised** calls when the tenant sets none. Rejects with 486 at the limit; keeps the first-turn LLM call from queueing past SIP Timer B. Deterministically resolved calls do not consume a channel |
| `channel_limit` | Per-tenant override |
| `allow_external_dial` | Default-deny gate for any non-`user/` destination |
| `external_allowlist` | Prefix allowlist, consulted only when external dial is enabled. Empty with external enabled denies everything |
| `barred_prefixes` | Overrides the built-in barred classes (premium-rate, satellite, IRSF-heavy codes). Omit to inherit the defaults |
| `max_external_units_per_day` | Spend circuit breaker. Zero permits no external spend |
| `allow_caller_provided_number` | Gates the separate hard-gated tool that dials a raw caller-supplied number |

### Logging

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--loglevel` | `LOGLEVEL` | info | Log level: debug, info, warn, error |

### Complete Example

```bash
# Environment variables
export PORT=5060
export BIND=0.0.0.0
export ADVERTISE=192.168.1.10
export RTPMANAGER=rtpmanager1:9090,rtpmanager2:9090
export LLM_SERVER=http://localhost:11434
export LLM_MODEL=qwen3:8b
export POLICY_CONFIG=/etc/switchboard/policy.json
export TENANTS_PATH=/etc/switchboard/tenants
export LOGLEVEL=debug

./switchboard-signaling

# Or with flags
./switchboard-signaling \
  --port 5060 \
  --bind 0.0.0.0 \
  --advertise 192.168.1.10 \
  --rtpmanager rtpmanager1:9090,rtpmanager2:9090 \
  --llm-server http://localhost:11434 \
  --llm-model qwen3:8b \
  --policy-config /etc/switchboard/policy.json \
  --tenants-path /etc/switchboard/tenants \
  --routing-path /etc/switchboard/tenants \
  --loglevel debug
```

## RTP Manager

### gRPC Server

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--grpc-port` | `GRPC_PORT` | 9090 | gRPC listen port |
| `--grpc-bind` | `GRPC_BIND` | 0.0.0.0 | Bind address for gRPC |

### Media Configuration

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--advertise` | `ADVERTISE` | (auto-detected) | Public IP for SDP connection address |
| `--rtp-min` | `RTP_PORT_MIN` | 10000 | Start of RTP port range |
| `--rtp-max` | `RTP_PORT_MAX` | 20000 | End of RTP port range |
| `--audio-path` | `AUDIO_PATH` | ./audio | Base path for audio files |

### AI Service Connections

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--tts-server` | `TTS_SERVER` | http://localhost:8000 | Piper TTS server URL |
| `--asr-server` | `ASR_SERVER` | http://localhost:8001 | Whisper ASR server URL |

The RTP Manager connects to TTS and ASR services for AI agent calls. TTS converts LLM text responses into audio streamed over RTP. ASR transcribes incoming caller audio into text for the LLM.

### Port Range Planning

When running multiple RTP Managers, ensure non-overlapping port ranges:

| Instance | Port Range | Capacity |
|----------|------------|----------|
| RTP Manager 1 | 10000-13333 | ~1666 sessions |
| RTP Manager 2 | 13334-16666 | ~1666 sessions |
| RTP Manager 3 | 16667-20000 | ~1666 sessions |

Each RTP session uses 2 ports (RTP + RTCP), so capacity = (max - min) / 2.

### Logging

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--loglevel` | `LOGLEVEL` | info | Log level: debug, info, warn, error |

### Complete Example

```bash
# Environment variables
export GRPC_PORT=9090
export GRPC_BIND=0.0.0.0
export ADVERTISE=192.168.1.10
export RTP_PORT_MIN=10000
export RTP_PORT_MAX=20000
export AUDIO_PATH=/var/lib/switchboard/audio
export TTS_SERVER=http://localhost:8000
export ASR_SERVER=http://localhost:8001
export LOGLEVEL=info

./switchboard-rtpmanager

# Or with flags
./switchboard-rtpmanager \
  --grpc-port 9090 \
  --grpc-bind 0.0.0.0 \
  --advertise 192.168.1.10 \
  --rtp-min 10000 \
  --rtp-max 20000 \
  --audio-path /var/lib/switchboard/audio \
  --tts-server http://localhost:8000 \
  --asr-server http://localhost:8001 \
  --loglevel info
```

## UI Server

### HTTP Server

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--port` | `UI_PORT` | 3000 | HTTP listen port |
| `--bind` | `UI_BIND` | 0.0.0.0 | Bind address |

### Backend Configuration

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--backends` | `UI_BACKENDS` | (required) | Comma-separated backend definitions |

Backend format: `name=url` pairs, comma-separated.

```bash
# Single backend
--backends "default=http://localhost:8080"

# Multiple backends
--backends "primary=http://signaling1:8080,secondary=http://signaling2:8080"
```

### Logging

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--loglevel` | `UI_LOGLEVEL` | info | Log level: debug, info, warn, error |

### Complete Example

```bash
# Environment variables
export UI_PORT=3000
export UI_BIND=0.0.0.0
export UI_BACKENDS="dc1=http://signaling1:8080,dc2=http://signaling2:8080"
export UI_LOGLEVEL=info

./switchboard-ui

# Or with flags
./switchboard-ui \
  --port 3000 \
  --bind 0.0.0.0 \
  --backends "dc1=http://signaling1:8080,dc2=http://signaling2:8080" \
  --loglevel info
```

## AI Services

Switchboard integrates three external AI services that the call supervisor depends on. These services are not part of Switchboard itself but must be running and reachable for AI-powered call handling.

### Service Overview

| Service | Image | Default Port | Configured On |
|---------|-------|-------------|---------------|
| Piper TTS | `ghcr.io/matatonic/openedai-speech` | 8000 | RTP Manager (`TTS_SERVER`) |
| Whisper ASR | `fedirz/faster-whisper-server:latest-cpu` | 8001 | RTP Manager (`ASR_SERVER`) |
| Ollama LLM | `ollama/ollama` | 11434 | Signaling (`LLM_SERVER`) |

### How They Work Together

1. The **ASR** service (on the RTP Manager) transcribes incoming caller audio into text.
2. The **LLM** service (on the Signaling Server) generates a conversational response from the transcript.
3. The **TTS** service (on the RTP Manager) converts the LLM response text into audio streamed back over RTP.

### Running the AI Services

The Go services accept `--llm-server`, `--asr-server`, and `--tts-server` flags pointing at any reachable HTTP endpoint. Bring those services up however suits your environment — see the README for example `docker run` / `ollama serve` commands you can copy-paste — and point Switchboard at the resulting IP and port.

## Tenant and Settings Configuration

### Tenant Configuration

Tenant configuration files are stored in `resources/tenants/`. Each tenant has a Markdown prompt
and a `<tenant>.routing.json` routing table (e.g. `devtenant.md` + `devtenant.routing.json`).

The repository ships only `devtenant`, a minimal fixture for local testing. **There is no default
tenant** — a call whose domain matches none is rejected with 404, pre-answer and without any LLM
request. For a fully worked example of a realistic tenant, see
[TENANT-EXAMPLE.md](TENANT-EXAMPLE.md).

### Settings

Global settings are stored in `resources/config/settings.md`. This file contains system-wide configuration that applies across all tenants.

## Deployment Patterns

### Single Host Development

All services on one machine:

```bash
./switchboard-rtpmanager --grpc-port 9090 &
./switchboard-signaling --rtpmanager localhost:9090 &
./switchboard-ui --backends http://localhost:8080
```

### Multi-Host Production

```
                         Load Balancer
                              |
            +--------+--------+--------+
            |        |                 |
        Signaling1  Signaling2    Signaling3
            \        |                /
             \       |               /
              +------+------+-------+
                     |
            +--------+--------+
            |        |        |
         RTP1     RTP2     RTP3
```

**Signaling Servers:**
```bash
# Each signaling server
./switchboard-signaling \
  --advertise $PUBLIC_IP \
  --rtpmanager rtp1:9090,rtp2:9090,rtp3:9090
```

**RTP Managers:**
```bash
# RTP Manager 1
./switchboard-rtpmanager \
  --advertise $PUBLIC_IP \
  --rtp-min 10000 --rtp-max 13333

# RTP Manager 2
./switchboard-rtpmanager \
  --advertise $PUBLIC_IP \
  --rtp-min 13334 --rtp-max 16666

# RTP Manager 3
./switchboard-rtpmanager \
  --advertise $PUBLIC_IP \
  --rtp-min 16667 --rtp-max 20000
```

### NAT Traversal

When services are behind NAT, set `ADVERTISE` to the public IP:

```bash
# Signaling (affects SIP Contact headers)
export ADVERTISE=203.0.113.10
./switchboard-signaling

# RTP Manager (affects SDP connection address)
export ADVERTISE=203.0.113.10
./switchboard-rtpmanager
```

## Environment File

For systemd or Docker deployments, use an environment file:

```bash
# /etc/switchboard/signaling.env
PORT=5060
BIND=0.0.0.0
ADVERTISE=192.168.1.10
RTPMANAGER=localhost:9090
LLM_SERVER=http://localhost:11434
LOGLEVEL=info
```

```bash
# /etc/switchboard/rtpmanager.env
GRPC_PORT=9090
GRPC_BIND=0.0.0.0
ADVERTISE=192.168.1.10
RTP_PORT_MIN=10000
RTP_PORT_MAX=20000
AUDIO_PATH=/var/lib/switchboard/audio
TTS_SERVER=http://localhost:8000
ASR_SERVER=http://localhost:8001
LOGLEVEL=info
```

## Related Documents


---

*Last updated: March 2026*
