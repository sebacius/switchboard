# Switchboard

A modular B2BUA (Back-to-Back User Agent) VoIP system built in Go, exploring the separation of signaling and media.

![Switchboard avatar](switchboard.png)

> **WARNING: EXPERIMENTAL PROJECT**
>
> This is a **learning project** in active development. It is **pre-alpha**, **unstable**, and **not suitable for any production use**. The architecture is still being decided. Entire subsystems may be rewritten without notice. APIs will break. Config formats will change. Here be dragons.
>
> **DO NOT USE THIS IN PRODUCTION. SERIOUSLY.**
>
> This project uses AI-assisted development (Claude Code) to rapidly prototype and iterate. Code is generated and modified across multiple fronts simultaneously. Bugs exist. Inconsistencies exist. You have been warned.

---

## Standing on the Shoulders of Giants

Before anything else, this project exists because of the incredible work done by others:

### [Pion](https://github.com/pion)
The Pion project provides the entire foundation for RTP, SDP, and WebRTC in Go. Without Pion's clean, well-tested libraries for packet handling, SDP parsing, and media transport, building something like this would take years instead of weeks. The quality and documentation of the Pion ecosystem is exceptional.

### [sipgo](https://github.com/emiago/sipgo) & [diago](https://github.com/emiago/diago)
Emiago's sipgo library is a pure-Go SIP stack that actually makes sense. It's clean, well-documented, and handles the gnarly parts of SIP so you don't have to. The diago project, built on top of sipgo, provided invaluable patterns for B2BUA implementation, dialog management, and call handling. Many of the architectural decisions in Switchboard were informed by studying how diago approaches these problems.

### Kamailio & RTPEngine
The architectural inspiration for separating signaling and media comes directly from the Kamailio + RTPEngine pattern that has proven itself in large-scale VoIP deployments. This battle-tested approach showed that decoupling these concerns enables true horizontal scaling.

**Thank you to all these projects. Switchboard is an experiment built on your foundations.**

---

## Why This Exists

This project was born from a specific curiosity: **what if we built a VoIP system with the Kamailio/RTPEngine separation pattern from day one, but in Go?**

The traditional approach in systems like Asterisk or FreeSWITCH is to handle signaling and media in the same process. This works well, but scaling becomes challenging - you're scaling both concerns together even when they have different resource profiles.

The Kamailio + RTPEngine architecture separates these concerns:
- **Signaling** (SIP) is lightweight, stateful, and CPU-bound
- **Media** (RTP) is heavyweight, mostly stateless per-stream, and I/O-bound

By separating them and using a control protocol (in our case, gRPC) to manage media servers, you get:

1. **Independent scaling** - Add more media servers without touching signaling, or vice versa
2. **Geographic distribution** - Media servers close to users, signaling centralized
3. **Resource isolation** - A media processing spike doesn't affect call setup
4. **Simpler deployments** - Each component has a focused responsibility
5. **Container-friendly** - Small, single-purpose binaries that fit the Kubernetes model

This is an experiment to see if this architecture, combined with Go's concurrency model and modern tooling, can produce something that's both scalable and understandable.

**I couldn't explore this without the work done on Pion, sipgo, and diago. They made it possible to prototype these ideas in weeks rather than years.**

---

## Architecture

Three services, loosely coupled via gRPC:

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

## Systemd Deployment

Systemd service files are provided in `deploy/systemd/`. See the [deployment README](deploy/systemd/README.md) for installation instructions.

### Viewing Logs

When running under systemd, logs go to the journal:

```bash
# Follow logs for a specific service
journalctl -u switchboard-signaling -f

# Follow logs for all switchboard services
journalctl -u 'switchboard-*' -f

# View recent logs (last 100 lines)
journalctl -u switchboard-signaling -n 100

# View logs since last boot
journalctl -u switchboard-signaling -b

# View logs from a specific time
journalctl -u switchboard-signaling --since "10 minutes ago"

# View logs with priority filtering
journalctl -u switchboard-signaling -p err  # Only errors
```

### Service Management

```bash
# Start all services
systemctl start switchboard.target

# Stop all services
systemctl stop switchboard.target

# Restart all services
systemctl restart switchboard.target

# Check status
systemctl status switchboard-signaling
systemctl status switchboard-rtpmanager
systemctl status switchboard-ui
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

- **Go 1.24** - Single binaries, goroutines, and a great standard library
- **[sipgo](https://github.com/emiago/sipgo)** - Pure Go SIP stack
- **[diago](https://github.com/emiago/diago)** - B2BUA patterns and inspiration
- **[Pion](https://github.com/pion)** - RTP, SDP, and WebRTC ecosystem
- **gRPC** - Service communication between signaling and media
- **g711** - PCMU/PCMA codec support
- **HTMX + Tailwind** - Dashboard UI

## Contributing

Contributions are welcome, but please understand what you are getting into:

1. **This is unstable** - Things will break. APIs will change. Your PR might become irrelevant overnight.
2. **No promises** - This is a side project for learning. Response times will vary.
3. **Discussion first** - For anything non-trivial, open an issue to discuss before submitting a PR.

That said, if you are also curious about VoIP systems and want to experiment together, pull up a chair.

## License

MIT License - See [LICENSE](LICENSE) for details.

---

Built by Sebastian as an exploration of VoIP systems, scalable architecture, and real-time media routing.

*If this project somehow helps you learn something, that is the whole point.*
