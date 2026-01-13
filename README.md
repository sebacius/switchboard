# Switchboard

A modular B2BUA (Back-to-Back User Agent) VoIP system built from scratch in Go.
![Switchboard avatar](switchboard.png)

> **WARNING: EXPERIMENTAL PROJECT**
>
> This is a **learning project** in active development. It is **pre-alpha**, **unstable**, and **not suitable for any production use**. The architecture is still being decided. Entire subsystems may be rewritten without notice. APIs will break. Config formats will change. Here be dragons.
>
> **DO NOT USE THIS IN PRODUCTION. SERIOUSLY.**
>
> This project uses AI-assisted development (Claude Code) to rapidly prototype and iterate. Code is generated and modified across multiple fronts simultaneously. Bugs exist. Inconsistencies exist. You have been warned.

---


## Why This Exists

This project was born from curiosity: what would it look like to build a simple, modern B2BUA from scratch? Not to replace FreeSWITCH, Asterisk, or Kamailio - they have earned their place through decades of battle-testing. This is about learning, experimenting, and exploring what is possible with modern Go tooling.

The inspiration comes from the Kamailio + RTPEngine architecture - signaling and media as separate, scalable components. What if we built something with horizontal scaling and container orchestration in mind from the start?

## Architecture

Three services, loosely coupled:

```
                              ┌─────────────────────────┐
                              │      SIP Clients        │
                              └───────────┬─────────────┘
                                          │ SIP (5060)
                              ┌───────────▼─────────────┐
                              │    Signaling Server     │
                              │   (SIP B2BUA + REST)    │
                              └───────────┬─────────────┘
                                          │ gRPC (9090)
               ┌──────────────────────────┼──────────────────────────┐
               │                          │                          │
    ┌──────────▼──────────┐    ┌──────────▼──────────┐    ┌──────────▼──────────┐
    │   RTP Manager #1    │    │   RTP Manager #2    │    │   RTP Manager #N    │
    │   (Media Bridge)    │    │   (Media Bridge)    │    │   (Media Bridge)    │
    └──────────┬──────────┘    └──────────┬──────────┘    └──────────┬──────────┘
               │                          │                          │
               └──────────────────────────┼──────────────────────────┘
                                          │ RTP
                              ┌───────────▼─────────────┐
                              │      SIP Clients        │
                              └─────────────────────────┘

                              ┌─────────────────────────┐
                              │       UI Server         │
                              │  (Admin Dashboard)      │◄─── HTTP :3000
                              │  Aggregates backends    │
                              └─────────────────────────┘
```

**Signaling Server** - SIP protocol handling, B2BUA call bridging, dialplan engine, location service
- Packages: `app`, `b2bua`, `dialog`, `dialplan`, `location`, `registration`, `routing`, `transport`, `api`, `events`

**RTP Manager** - Media streaming, RTP bridging between call legs, SDP generation, port allocation
- Packages: `server`, `session`, `media`, `bridge`, `portpool`, `sdp`

**UI Server** - Admin dashboard aggregating data from multiple signaling servers
- HTMX + Tailwind CSS, multi-backend support
- Packages: `server`, `client`, `config`

## Current State

**What Actually Works (mostly)**
- SIP REGISTER with in-memory location service
- Inbound INVITE -> 183 Session Progress -> 200 OK flow
- B2BUA call bridging (A-leg to B-leg)
- RTP media bridging between sessions
- Dialplan with pattern matching and Dial action
- Basic admin dashboard with live updates
- Multiple RTP Manager load balancing with session affinity

**What Does Not Work Yet**
- Authentication (anyone can register as anyone)
- Persistent storage (everything is in-memory)
- SRTP/TLS (plaintext only)
- Most SIP edge cases (re-INVITE, UPDATE, REFER, etc.)
- Proper error handling in many places
- Tests (there are almost none)
- Documentation beyond this README

**What Might Be Completely Wrong**
- The entire B2BUA implementation
- SDP manipulation
- RTP timing and jitter handling
- Basically anything that has not been tested with real traffic

## Quick Start

### Prerequisites

- Go 1.24+
- protoc (for regenerating gRPC code)
- A SIP client for testing (Opal, Opal, sipexer, etc.)

### Build and Run

```bash
# Clone
git clone https://github.com/sebas/switchboard.git
cd switchboard

# Build all services
make build-all

# Run (starts all three services)
make run

# Or run individually:
./switchboard-rtpmanager --grpc-port 9090 &
./switchboard-signaling --rtpmanager localhost:9090 &
./switchboard-ui --backends http://localhost:8080
```

### Configuration

**Signaling Server** (env vars or flags):
```bash
PORT=5060              # SIP listen port
BIND=0.0.0.0           # Bind address
ADVERTISE=192.168.1.10 # Public IP for SIP Contact header
RTPMANAGER=host:9090   # RTP Manager address(es), comma-separated
LOGLEVEL=info          # debug, info, warn, error
```

**RTP Manager** (env vars or flags):
```bash
GRPC_PORT=9090         # gRPC listen port
GRPC_BIND=0.0.0.0      # Bind address
ADVERTISE=192.168.1.10 # Public IP for SDP
RTP_PORT_MIN=10000     # RTP port range start
RTP_PORT_MAX=20000     # RTP port range end
AUDIO_PATH=/audio      # Base path for audio files
```

**UI Server** (env vars or flags):
```bash
UI_PORT=3000           # HTTP listen port
UI_BIND=0.0.0.0        # Bind address
UI_BACKENDS=server1=http://host1:8080,server2=http://host2:8080
UI_LOGLEVEL=info       # debug, info, warn, error
```

### Scaling RTP Managers

```bash
# Start multiple RTP Managers with non-overlapping port ranges
./switchboard-rtpmanager --grpc-port 9090 --rtp-min 10000 --rtp-max 15000 &
./switchboard-rtpmanager --grpc-port 9091 --rtp-min 15001 --rtp-max 20000 &

# Signaling Server load-balances across them
./switchboard-signaling --rtpmanager localhost:9090,localhost:9091
```

## REST API

**Signaling Server** (port 8080):
```bash
curl http://localhost:8080/api/v1/health        # Health check
curl http://localhost:8080/api/v1/sessions      # Active RTP sessions
curl http://localhost:8080/api/v1/registrations # SIP registrations
curl http://localhost:8080/api/v1/dialogs       # Active SIP dialogs
curl http://localhost:8080/api/v1/stats         # Statistics
```

**UI Server** (port 3000):
- `GET /` - Admin dashboard (aggregates all backends)
- `GET /health` - UI server health

## Technology Stack

- **Go 1.24** - Because single binaries and goroutines are nice
- **[sipgo](https://github.com/emiago/sipgo)** - Pure Go SIP stack (thank you, emiago)
- **[Pion](https://github.com/pion)** - RTP/SDP handling
- **gRPC** - Service communication
- **g711** - PCMU codec
- **HTMX + Tailwind** - Dashboard UI

## Contributing

Contributions are welcome, but please understand what you are getting into:

1. **This is unstable** - Things will break. APIs will change. Your PR might become irrelevant overnight.
2. **No promises** - This is a side project for learning. Response times will vary.
3. **Discussion first** - For anything non-trivial, open an issue to discuss before submitting a PR.

That said, if you are also curious about VoIP systems and want to experiment together, pull up a chair.

## Acknowledgments

This project would not exist without:

- **[sipgo](https://github.com/emiago/sipgo)** - A clean, well-documented pure-Go SIP library
- **[Pion](https://github.com/pion)** - The entire ecosystem for WebRTC/RTP in Go
- **Kamailio** and **RTPEngine** - The architecture inspiration
- **FreeSWITCH** and **Asterisk** - For decades of teaching the world how VoIP works
- **Claude Code** - AI-assisted development that makes experimentation faster

## License

MIT License - See [LICENSE](LICENSE) for details.

---

Built by Sebastian as an exploration of VoIP systems and real-time media routing.

*If this project somehow helps you learn something, that is the whole point.*
