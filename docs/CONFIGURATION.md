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

## Signaling Server

### Network Configuration

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--port` | `PORT` | 5060 | SIP listen port (UDP) |
| `--bind` | `BIND` | 0.0.0.0 | Bind address for SIP and the REST API |
| `--advertise` | `ADVERTISE` | (auto-detected) | Public IP for SIP Contact headers |
| `--api-port` | `API_PORT` | 8080 | REST API HTTP port |

### RTP Manager Connection

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--rtpmanager` | `RTPMANAGER_ADDRS` | localhost:9090 | Comma-separated RTP Manager addresses |

Example with multiple RTP Managers:
```bash
./switchboard-signaling --rtpmanager "rtpmanager1:9090,rtpmanager2:9090,rtpmanager3:9090"
```

### Speech

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--tts-voice` | `TTS_VOICE` | alloy | Default voice for flow prompts that do not name one |

Routing needs no external service. The voice is used by `tts` nodes and `ivr`
prompts; a flow that plays recorded audio files needs no TTS at all.

### Call Records

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--cdr-path` | `CDR_PATH` | *(empty)* | Append-only JSONL call record file. Empty disables recording |

Each record carries the traversal — which nodes, in order, with the exit each
produced and the time spent there — plus the authorization verdicts. Without the
path, "why did this caller end up with the operator" has no answer.

### Tenant Configuration

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--tenants-path` | `TENANTS_PATH` | resources/tenants | Directory containing per-tenant configuration |
| `--routing-path` | `ROUTING_PATH` | (same as `--tenants-path`) | Directory containing `<tenant>.routing.json` and `<tenant>.flows.json` |
| `--policy-config` | `POLICY_CONFIG` | resources/config/policy.json | Class of Service and channel limits |

A tenant is described by **two** files, loaded and validated as one unit — which
is what makes cross-file checks meaningful, since a flow may dial a ring group
the other file defines. There is **no default tenant**.

#### Validating without starting the server

```bash
switchboard-signaling validate --routing-path resources/tenants

# Or, with the repo defaults for every path:
make validate
```

It also takes `--policy-config` (default `resources/config/policy.json`),
`--routes-path` (default `resources/config/routes.json`) and `--quiet`. Class of
Service is checked against the policy file and DIDs are cross-checked against
`routes.json`, so pointing it at the wrong file weakens the check rather than
skipping it loudly.

It runs the same checks the loader does — so "validate passes but the server will
not start" cannot happen — and reports every problem rather than the first, each
with a path such as `flows.main.nodes.greeting.exits.timeout`. Exit 0 is clean,
1 means problems, 2 is a usage error.

### Tenant Routing Table

`<tenant>.routing.json` is what routing runs on, and the source of the symbolic
targets external dialing narrows to. One file, so a name cannot mean two things.

```json
{
  "operator": "user/150",
  "retrieval_prefix": "*",
  "extensions": {
    "105": "user/105",
    "100": "flow/main-ivr",
    "130": "group/claims",
    "2XX": "user/150"
  },
  "symbolic_targets": { "sales": "group/sales", "front-desk": "user/150" },
  "dids": { "+15558001200": "flow/main-ivr" },
  "groups": {
    "claims": {
      "strategy": "sequential",
      "members": ["user/130", "user/120"],
      "member_timeout_ms": 15000
    }
  }
}
```

| Field | Meaning |
|-------|---------|
| `operator` | Fallback human: where a call goes when nothing else claims it. Empty means such a call is declined with 480, never hung up on |
| `retrieval_prefix` | Dial prefix for picking up a parked call (`*701` → slot 701). Internal callers only, and evaluated **before** the entry mapping so no pattern can shadow it |
| `extensions` | Entry mapping. Keys are literals or digit-map patterns; values are `user/NNN`, `group/NAME`, or `flow/NAME` |
| `symbolic_targets` | Capability narrowing: the only names a flow may dial externally. A flow can never express a raw number |
| `dids` | Inbound DID → destination **within** the tenant. The DID → *tenant* step happens earlier, in `routes.json`. Both `+1555…` and `1555…` match |
| `groups` | Ring groups. `strategy` is `sequential` or `round-robin`; `member_timeout_ms` bounds one member's ring. A group carries **no** no-answer destination — that belongs to whatever rang it, written down in one place |

#### Entry patterns

A digit-map vocabulary, deliberately not regular expressions:

| Symbol | Matches |
|--------|---------|
| `X` | 0-9 |
| `N` | 2-9 |
| `Z` | 1-9 |
| `[2-8]`, `[147]` | the listed digits or range |
| `.` | one or more further digits; **trailing only** |
| literals | `0-9`, `*`, `#`, `+` |

The most specific match wins, **computed** from how narrow each position's
accepted set is rather than declared as a priority number:

```
literal 1  ·  [147] 3  ·  [2-8] 7  ·  N 8  ·  Z 9  ·  X 10  ·  "." unbounded
```

Comparison is per-position, so two patterns that overlap with neither strictly
narrower (`NX` and `XN` both match `22`) are a **load error** naming both, not a
silent tiebreak. The restricted vocabulary exists precisely so specificity is
well defined, which it is not for regular expressions.

### Tenant Flows

`<tenant>.flows.json` holds the graphs. Optional — a tenant may route entirely by
direct mapping.

```json
{
  "flows": {
    "main-ivr": {
      "start": "greeting",
      "timeout_ms": 300000,
      "nodes": {
        "greeting": {
          "type": "ivr",
          "entry": {
            "prompt": { "text": "Press 1 for sales, 2 for claims." },
            "timeout_ms": 5000, "max_retries": 2,
            "terminator": "#", "interruptible": true
          },
          "exits": {
            "1": "ring-sales", "2": "ring-claims",
            "timeout": "operator", "invalid": "operator",
            "retries_exceeded": "operator"
          }
        },
        "ring-claims": {
          "type": "dial_user",
          "entry": { "target": "group/claims", "timeout_ms": 20000 },
          "exits": { "no_answer": "operator", "busy": "operator",
                     "rejected": "operator", "unavailable": "operator" }
        },
        "operator": {
          "type": "dial_user",
          "entry": { "target": "user/150", "timeout_ms": 25000 },
          "exits": { "no_answer": "bye", "busy": "bye",
                     "rejected": "bye", "unavailable": "bye" }
        },
        "bye": { "type": "hangup", "entry": { "cause": "normal_clearing" } }
      }
    }
  }
}
```

Node types: `ivr`, `tts`, `play_audio`, `dial_user`, `dial_external`,
`transfer`, `hangup`.

Rules the loader enforces:

- **Every non-terminal exit must be wired.** No defaults, so what a caller hears
  when the line is busy is always written down.
- **Terminal exits must be absent.** `answered` and `accepted` end the flow; the
  graph has nothing to say about what follows a connected call.
- **The inter-node graph must be acyclic**, with repetition confined to
  `ivr.max_retries`. Every flow therefore provably terminates.
- **No unreachable nodes**, and every dial target must resolve and pass Class of
  Service — at load, not at 2am.
- **Unknown entry fields are rejected**, so `timout_ms` fails at startup rather
  than silently defaulting a five-second wait to zero.

`ivr.max_retries` bounds re-prompting inside the node. With retries configured an
exhausted node takes `retries_exceeded`; with `max_retries: 0` the first mistake
takes `timeout` or `invalid` directly.

A malformed routing file is a hard startup error, and a failed reload leaves the
previously loaded configuration in force — a bad edit must never strip a live
tenant's routing. Both files reload through `POST /api/v1/config/reload`, and a
write through the config API is validated first: an invalid flow is refused with
the problems attached and never reaches disk. A finding of severity `warning`
does not block the write — it would make the editor stricter than the loader —
and comes back with the successful response instead.

### Editing configuration over the API

| Route | What it addresses | Activation |
|-------|-------------------|------------|
| `GET\|POST /api/v1/config/tenants` | list, create | `reload` |
| `GET\|PUT\|DELETE /api/v1/config/tenants/{name}?file=routing\|flows` | one tenant file | `reload` |
| `GET /api/v1/config/files` | the deployment-wide files | — |
| `GET\|PUT /api/v1/config/files/{policy\|routes\|trunk_peers}` | one of them | **`restart`** |
| `GET /api/v1/config/status` | what on disk is not yet in force | — |
| `GET /api/v1/config/audio` | audio the flows name, joined with what the player has | — |
| `POST /api/v1/config/reload` | activate tenant files | — |

The deployment-wide files are addressed by NAME against a closed allowlist, never
by path. All three are read once at startup — the policy overrides, the DID table
and the peer store are captured by value at construction — so **a reload does not
activate them**; the reload response says as much rather than answering "ok".

Writing them requires `--allow-global-config-writes` (default off). `policy.json`
decides which tenant may dial out and how much, and the API has no
authentication, so making the authorization boundary editable is an operator's
decision. Reading is always allowed.

### Walking a flow without placing a call

| Route | Purpose |
|-------|---------|
| `GET /api/v1/flow/tenants` | the tenants currently loaded, with their flow names |
| `POST /api/v1/flow/simulate` | walk one fake call and return the traversal |

These read the **loaded** configuration, while every `/config` route reads disk —
which is why they live under a different prefix. `make flow-smoke` and the UI's
Test a Call tab both go through the same code (`internal/signaling/flow/flowsim`).

A simulation is inert: it builds its own engine with no parking service and no
resolver, so `*NNN` retrieval returns before it can touch a real parked call, and
a ledger-free policy, so an authorized external destination costs nothing against
the tenant's daily cap. Nothing is dialed and no RTP is allocated.

### Trunk and DID Routing

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--trunk-config` | `TRUNK_CONFIG` | resources/config/trunk_peers.json | SIP trunk peers |
| `--routes-path` | `ROUTES_PATH` | resources/config/routes.json | DID → tenant mapping |

An inbound DID is looked up **twice**, and the two lookups answer different
questions:

| Step | File | Question | Who may edit it |
|------|------|----------|-----------------|
| 1 | `routes.json` | **Whose** number is this? DID → tenant | operator only, global |
| 2 | `<tenant>.routing.json` → `dids` | **What happens** to a call to it? DID → destination | the tenant |

They are separate files for a reason worth keeping. A tenant can edit its own
routing table through the config API, so if tenants declared their own DIDs, one
could add another tenant's number and start receiving their calls. The
number-to-tenant binding lives where no tenant can write it.

```json
// routes.json — step 1: the number belongs to devtenant
{ "dids": { "+15558001200": "devtenant", "+1555800XXXX": "devtenant" } }

// devtenant.routing.json — step 2: what devtenant does with it
"dids": {
  "+15558001200": "flow/main-ivr",
  "+15558001201": "group/engineering"
}
```

A call to `+15558001200` from a trunk peer is attributed to `devtenant` by step
1, then enters the main menu by step 2. A call to `+15558009999` is still
*devtenant's* call — the block in step 1 says so — but matches nothing in step 2,
so it reaches the tenant operator.

Both steps use the same matcher, so they cannot disagree about whether a number
matches:

- **Either E.164 form works.** A carrier signaling `15558001200` finds a route
  written `+15558001200`. Which form a given trunk sends is not something the
  operator writing this file can know in advance.
- **Patterns work**, so owning a block is one line rather than ten thousand.
- **The most specific claim wins**, so carving one number out of a block and
  handing it to a different tenant works as expected.
- **Two claims that overlap with neither more specific fail at startup**, naming
  both tenants. Whose calls those are would otherwise be undefined.

`switchboard-signaling validate` cross-checks the two files: a DID routing to a
tenant that has no routing file is an **error** (such a call is attributed to a
tenant that does not exist and rejected with 404), and a tenant handling a
literal DID that `routes.json` does not send it is a **warning** — it will never
receive a call on that number. Tenants whose DID keys are patterns are not
cross-checked, because deciding whether one pattern contains another is a harder
problem than the warning is worth.

### Policy Configuration

`policy.json` is the deterministic authorization boundary — configuration cannot
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
| `default_channel_limit` | Per-tenant cap on concurrent calls when the tenant sets none. Rejects with 486 at the limit. This is capacity control: every call holds an RTP port, a media session and a goroutine for its life, so the slot is taken **before** the media session is created |
| `channel_limit` | Per-tenant override |
| `allow_external_dial` | Default-deny gate for any non-`user/` destination |
| `external_allowlist` | Prefix allowlist, consulted only when external dial is enabled. Empty with external enabled denies everything |
| `barred_prefixes` | Overrides the built-in barred classes (premium-rate, satellite, IRSF-heavy codes). Omit to inherit the defaults |
| `max_external_units_per_day` | Spend circuit breaker. Zero permits no external spend |
| `allow_caller_provided_number` | Gates dialing a raw, un-narrowed number. **Nothing reaches this path today** — it gated a supervisor tool that no longer exists, and no flow node can express a raw number. Leave it false |

### Logging

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--loglevel` | `LOGLEVEL` | debug | Log level: debug, info, warn, error |

### Complete Example

```bash
# Environment variables
export PORT=5060
export BIND=0.0.0.0
export ADVERTISE=192.168.1.10
export RTPMANAGER_ADDRS=rtpmanager1:9090,rtpmanager2:9090
export CDR_PATH=/var/log/switchboard/cdr.jsonl
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
  --cdr-path /var/log/switchboard/cdr.jsonl \
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
| `--bind` | `BIND` | 0.0.0.0 | Bind address for gRPC |

### Media Configuration

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--advertise` | `ADVERTISE` | (auto-detected) | Public IP for SDP connection address |
| `--rtp-port-min` | `RTP_PORT_MIN` | 10000 | Start of RTP port range |
| `--rtp-port-max` | `RTP_PORT_MAX` | 20000 | End of RTP port range |
| `--audio-path` | `AUDIO_PATH` | ./audio | Base path for audio files |

A `play_audio` node's file name is resolved against `--audio-path`. A name may
name a subdirectory (`acme/welcome.wav`) but cannot escape the base path; an
absolute path in a flow is used as written.

> **Upgrading:** this path was previously parsed and then ignored, so file names
> resolved against the RTP manager's *working directory*. A deployment that
> relied on that must set `AUDIO_PATH` to that directory, or move the files.

### Speech Service Connections

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--tts-server` | `TTS_SERVER` | http://localhost:8000 | TTS server URL (Piper, or any OpenAI-compatible `/v1/audio/speech`) |
| `--asr-server` | `ASR_SERVER` | http://localhost:8001 | ASR server URL — **currently unused** |
| `--asr-model` | `ASR_MODEL` | Systran/faster-whisper-base | Model id sent to the ASR server; required by that API |

TTS synthesizes `tts` node text and `ivr` prompts into audio streamed over RTP.
A flow that only plays recorded files, dials, transfers and hangs up needs no
TTS at all.

The ASR client is dormant — nothing transcribes anything today. It is kept
because it is exactly the batch API a future voicemail feature needs.

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
| `--loglevel` | `LOGLEVEL` | debug | Log level: debug, info, warn, error |

### Complete Example

```bash
# Environment variables
export GRPC_PORT=9090
export BIND=0.0.0.0
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
  --bind 0.0.0.0 \
  --advertise 192.168.1.10 \
  --rtp-port-min 10000 \
  --rtp-port-max 20000 \
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
| `--backends` | `UI_BACKENDS` | http://localhost:8080 | Comma-separated backend definitions |

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

## Speech Services

Switchboard needs no external service to route calls. One optional service adds
voice.

### Service Overview

| Service | Image | Default Port | Configured On |
|---------|-------|-------------|---------------|
| Piper TTS | `ghcr.io/matatonic/openedai-speech` | 8000 | RTP Manager (`TTS_SERVER`) |
| Whisper ASR | `fedirz/faster-whisper-server:latest-cpu` | 8001 | RTP Manager (`ASR_SERVER`) — unused today |

### Running TTS

```bash
docker run -d --name piper-tts -p 8000:8000 ghcr.io/matatonic/openedai-speech
./switchboard-rtpmanager --tts-server http://localhost:8000
```

Audio comes back as WAV, is resampled to 8kHz, encoded to PCMU and streamed as
20ms RTP frames.

## Tenant Configuration

Tenant configuration lives in `resources/tenants/`. Each tenant has a routing
table and, optionally, a flow file — `devtenant.routing.json` and
`devtenant.flows.json`. The two are loaded and validated together.

The repository ships only `devtenant`, a minimal fixture for local testing.
**There is no default tenant** — a call whose domain matches none is rejected
with 404, pre-answer. For a worked example see [TENANT-EXAMPLE.md](TENANT-EXAMPLE.md).

Both files are editable through the config API and reload without a restart. A
write that would not load is refused with the problems attached, so a broken
flow cannot be saved into a running system.

## Deployment Patterns

### Single Host Development

All services on one machine:

```bash
# make build-all writes these into build/
./build/switchboard-rtpmanager --grpc-port 9090 &
./build/switchboard-signaling --rtpmanager localhost:9090 &
./build/switchboard-ui --backends http://localhost:8080
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
  --rtp-port-min 10000 --rtp-port-max 13333

# RTP Manager 2
./switchboard-rtpmanager \
  --advertise $PUBLIC_IP \
  --rtp-port-min 13334 --rtp-port-max 16666

# RTP Manager 3
./switchboard-rtpmanager \
  --advertise $PUBLIC_IP \
  --rtp-port-min 16667 --rtp-port-max 20000
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
RTPMANAGER_ADDRS=localhost:9090
LOGLEVEL=info
```

```bash
# /etc/switchboard/rtpmanager.env
GRPC_PORT=9090
BIND=0.0.0.0
ADVERTISE=192.168.1.10
RTP_PORT_MIN=10000
RTP_PORT_MAX=20000
AUDIO_PATH=/var/lib/switchboard/audio
TTS_SERVER=http://localhost:8000
ASR_SERVER=http://localhost:8001
LOGLEVEL=info
```

## Related Documents

| Document | Description |
|----------|-------------|
| [Tenant example](TENANT-EXAMPLE.md) | A worked tenant: routing table, flows, policy, and the two DID lookups |
| [Project README](../README.md) | Architecture, call path, and what does and does not work yet |

---

*Last updated: August 2026*
