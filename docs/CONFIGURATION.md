# Configuration Reference

All Switchboard services can be configured via environment variables or command-line flags. Flags take precedence over environment variables.

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
export LOGLEVEL=debug

./switchboard-signaling

# Or with flags
./switchboard-signaling \
  --port 5060 \
  --bind 0.0.0.0 \
  --advertise 192.168.1.10 \
  --rtpmanager rtpmanager1:9090,rtpmanager2:9090 \
  --dialplan /etc/switchboard/dialplan.json \
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
LOGLEVEL=info
```

## Related Documents

- [Getting Started](GETTING_STARTED.md) - Initial setup
- [Dialplan](DIALPLAN.md) - Dialplan configuration format
- [Development](DEVELOPMENT.md) - Development environment

---

*Last updated: January 2026*
