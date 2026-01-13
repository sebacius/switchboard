# Switchboard
![Switchboard avatar](switchboard.png)

> **WARNING: EXPERIMENTAL PROJECT**
> This is a **learning project** in active development. It is **pre-alpha**, **unstable**, and **not suitable for any production use**. The architecture is still being decided. Entire subsystems may be rewritten without notice. APIs will break. Config formats will change. Here be dragons.

## About
Switchboard is a VoIP platform that separates signaling and media into independently scalable components. It uses SIP for call control, RTP for media transport, and gRPC to coordinate services.

Signaling and media place very different demands on a system. Signaling is intermittent and stateful, while media is continuous, bandwidth-heavy, and sensitive to latency and I/O behavior. Treating both under the same execution and scaling model tightly couples concerns that behave differently under load.

Switchboard is built around separating these responsibilities. Signaling and media are handled by distinct components and coordinated through explicit interfaces, allowing each to scale and operate according to its own characteristics.

Go provides a practical foundation for this approach. Lightweight concurrency fits signaling workloads well, and structured interfaces such as gRPC keep communication between components explicit and predictable.

With a [stable SIP stack available in Go like sipgo](https://github.com/emiago/sipgo), and [session mangemnt libraries like Pion](https://github.com/pion) the focus shifts away from protocol implementation and toward system boundaries, resource usage, and operational behavior.

This project explores whether structuring a VoIP system around these separations leads to something that is easier to scale, easier to operate, and easier to reason about.

This project takes a different approach by explicitly separating those planes:
- **Signaling (SIP)** is lightweight, stateful, and primarily CPU-bound
- **Media (RTP)** is heavier, largely stateless per stream, and I/O-bound

By decoupling signaling and media and coordinating them through a control interface (gRPC in this case), it becomes possible to:

1. **Scale independently** — grow signaling and media at different rates  
2. **Distribute geographically** — place media closer to users while keeping signaling centralized  
3. **Isolate resources** — media load spikes don’t interfere with call setup  
4. **Keep responsibilities clear** — each component has a focused role  
5. **Deploy cleanly** — small, single-purpose services that fit container-based environments


## Architecture
```mermaid
flowchart TB
  %% Nodes
  ClientsTop["SIP Clients"]
  Signaling["Signaling Server<br/>(SIP B2BUA + REST)"]
  ClientsBottom["SIP Clients"]

  subgraph MediaPlane["Media Plane"]
    direction LR
    RTP1["RTP Manager #1<br/>(Media Bridge)"]
    RTP2["RTP Manager #2<br/>(Media Bridge)"]
    RTPN["RTP Manager #N<br/>(Media Bridge)"]
  end

  UI["UI Server<br/>(Admin Dashboard)<br/>Aggregates backends"]

  %% Edges
  ClientsTop -->|"SIP :5060"| Signaling
  Signaling -->|"gRPC :9090"| RTP1
  Signaling -->|"gRPC :9090"| RTP2
  Signaling -->|"gRPC :9090"| RTPN
  RTP1 -->|"RTP"| ClientsBottom
  RTP2 -->|"RTP"| ClientsBottom
  RTPN -->|"RTP"| ClientsBottom
  UI <-->|"HTTP :3000"| Signaling

  %% Styling
  classDef clients fill:#0b3d91,stroke:#0b3d91,color:#ffffff,stroke-width:2px;
  classDef signaling fill:#6a00ff,stroke:#6a00ff,color:#ffffff,stroke-width:2px;
  classDef media fill:#00a86b,stroke:#00a86b,color:#ffffff,stroke-width:2px;
  classDef ui fill:#ff7a00,stroke:#ff7a00,color:#ffffff,stroke-width:2px;
  classDef plane fill:#111827,stroke:#9ca3af,color:#ffffff,stroke-width:1px;

  class ClientsTop,ClientsBottom clients;
  class Signaling signaling;
  class RTP1,RTP2,RTPN media;
  class UI ui;
  class MediaPlane plane;

  %% Link styling (index-based)
  %% Order matters: these correspond to edges in the order they were declared above.
  linkStyle 0 stroke:#0b3d91,stroke-width:3px;
  linkStyle 1 stroke:#6a00ff,stroke-width:3px;
  linkStyle 2 stroke:#6a00ff,stroke-width:3px;
  linkStyle 3 stroke:#6a00ff,stroke-width:3px;
  linkStyle 4 stroke:#00a86b,stroke-width:3px;
  linkStyle 5 stroke:#00a86b,stroke-width:3px;
  linkStyle 6 stroke:#00a86b,stroke-width:3px;
  linkStyle 7 stroke:#ff7a00,stroke-width:3px,stroke-dasharray: 5 5;
````

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

## Shoulders of giants

### [Pion](https://github.com/pion)
The Pion project provides the entire foundation for RTP, SDP, and WebRTC in Go. Without Pion's clean, well-tested libraries for packet handling, SDP parsing, and media transport, building something like this would take years instead of weeks. The quality and documentation of the Pion ecosystem is exceptional.

### [sipgo](https://github.com/emiago/sipgo) & [diago](https://github.com/emiago/diago)
Emiago's sipgo library is a pure-Go SIP stack that actually makes sense. It's clean, well-documented, and handles the gnarly parts of SIP so you don't have to. The diago project, built on top of sipgo, provided invaluable patterns for B2BUA implementation, dialog management, and call handling. Many of the architectural decisions in Switchboard were informed by studying how diago approaches these problems.
**Thank you to all these projects. Switchboard is an experiment built on your foundations.**


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
