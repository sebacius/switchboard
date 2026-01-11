# SwitchBoard

A modular SIP IVR server built as an open-source alternative to FreeSwitch/Asterisk. Designed with separation of concerns: SIP signaling decoupled from media handling for independent scaling.

## Architecture

```
                         ┌─────────────────────┐
                         │   SIP Clients       │
                         └──────────┬──────────┘
                                    │ SIP (5060)
                         ┌──────────▼──────────┐
                         │  Signaling Server   │
                         │  (SIP + REST API)   │
                         └──────────┬──────────┘
                                    │ gRPC (9090)
              ┌─────────────────────┼─────────────────────┐
              │                     │                     │
   ┌──────────▼──────────┐ ┌───────▼────────┐ ┌─────────▼─────────┐
   │   RTP Manager #1    │ │ RTP Manager #2 │ │  RTP Manager #N   │
   │  (Media + Ports)    │ │                │ │                   │
   └──────────┬──────────┘ └───────┬────────┘ └─────────┬─────────┘
              │                    │                    │
              └────────────────────┼────────────────────┘
                                   │ RTP
                         ┌─────────▼─────────┐
                         │   SIP Clients     │
                         └───────────────────┘
```

**Two services:**
- **Signaling Server** - SIP protocol handling (INVITE, BYE, REGISTER), REST API
- **RTP Manager** - Media streaming, port allocation, SDP generation

## Quick Start

### Build

```bash
go build -o switchboard-signaling ./cmd/signaling
go build -o switchboard-rtpmanager ./cmd/rtpmanager
```

### Run

```bash
# Start RTP Manager
./switchboard-rtpmanager --grpc-port 9090

# Start Signaling Server (connects to RTP Manager)
./switchboard-signaling --rtpmanager localhost:9090
```

### Multiple RTP Managers (scaling)

```bash
# Start multiple RTP Managers on different ports
./switchboard-rtpmanager --grpc-port 9090 --rtp-min 10000 --rtp-max 15000 &
./switchboard-rtpmanager --grpc-port 9091 --rtp-min 15001 --rtp-max 20000 &

# Signaling Server load-balances across them
./switchboard-signaling --rtpmanager localhost:9090,localhost:9091
```

### Configuration

**Signaling Server:**
```bash
PORT=5060              # SIP listen port
BIND=0.0.0.0           # Bind address
ADVERTISE=192.168.1.10 # Public IP for SIP Contact header
RTPMANAGER=host:9090   # RTP Manager address(es)
LOGLEVEL=info          # debug, info, warn, error
```

**RTP Manager:**
```bash
GRPC_PORT=9090         # gRPC listen port
GRPC_BIND=0.0.0.0      # Bind address
ADVERTISE=192.168.1.10 # Public IP for SDP
RTP_PORT_MIN=10000     # RTP port range start
RTP_PORT_MAX=20000     # RTP port range end
AUDIO_PATH=/audio      # Base path for audio files
```

## Features

- Full SIP call lifecycle (REGISTER, INVITE, ACK, BYE)
- PCMU codec (G.711 u-law)
- WAV file audio streaming
- Multiple RTP Manager support with load balancing
- Session affinity (calls stay on same RTP Manager)
- REST API for monitoring
- gRPC between services (proto definitions included)

## Project Structure

```
switchboard/
├── cmd/
│   ├── signaling/           # Signaling server entry point
│   └── rtpmanager/          # RTP Manager entry point
├── services/
│   ├── signaling/           # SIP handling, REST API
│   └── rtpmanager/          # Media, RTP, SDP
├── api/proto/               # gRPC proto definitions
├── pkg/rtpmanager/v1/       # Generated gRPC code
└── internal/logger/         # Shared utilities
```

## REST API

HTTP server on port 8080:

```bash
# Health check
curl http://localhost:8080/api/v1/health

# Active sessions
curl http://localhost:8080/api/v1/sessions

# SIP registrations
curl http://localhost:8080/api/v1/registrations

# Statistics
curl http://localhost:8080/api/v1/stats
```

## Technology Stack

- **Go 1.24**
- **sipgo** - Pure Go SIP stack
- **Pion** - RTP/SDP handling
- **gRPC** - Service communication
- **g711** - PCMU codec

## License

MIT License

---

Built by Sebastian as an exploration of VoIP systems and real-time media routing.
