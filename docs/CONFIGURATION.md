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

### Dialplan Configuration

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--dialplan` | `DIALPLAN_PATH` | dialplan.json | Path to dialplan configuration file |

### LLM Server Connection

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--llm-server` | `LLM_SERVER` | http://localhost:11434 | Ollama LLM server URL |

The signaling server connects to an Ollama instance for AI agent functionality. The LLM is used by the dialplan `ai_agent` action to generate conversational responses during calls.

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
export DIALPLAN_PATH=/etc/switchboard/dialplan.json
export LLM_SERVER=http://localhost:11434
export LOGLEVEL=debug

./switchboard-signaling

# Or with flags
./switchboard-signaling \
  --port 5060 \
  --bind 0.0.0.0 \
  --advertise 192.168.1.10 \
  --rtpmanager rtpmanager1:9090,rtpmanager2:9090 \
  --dialplan /etc/switchboard/dialplan.json \
  --llm-server http://localhost:11434 \
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

Switchboard integrates three external AI services to support the `ai_agent` dialplan action. These services are not part of Switchboard itself but must be running and reachable for AI-powered call handling.

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

Tenant configuration files are stored in `resources/tenants/` as Markdown files. Each file defines the configuration for a single tenant (e.g., `resources/tenants/default.md`).

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

- [Dialplan](DIALPLAN.md) - Dialplan configuration format

---

*Last updated: March 2026*
