# Switchboard
![Switchboard avatar](switchboard.png)

> **WARNING: EXPERIMENTAL PROJECT**
> This is a **learning project** in active development. It is **pre-alpha**, **unstable**, and **not suitable for any production use**. The architecture is still being decided. Entire subsystems may be rewritten without notice. APIs will break. Config formats will change. Here be dragons.

## About
Switchboard is a VoIP platform that separates signaling and media into independently scalable components. It uses SIP for call control, RTP for media transport, and gRPC to coordinate services.

With a [stable SIP stack available in Go like sipgo](https://github.com/emiago/sipgo), and [session mangemnt libraries like Pion](https://github.com/pion) switchboard shifts the focus away from protocol implementation and toward system boundaries, resource usage, and operational behavior.

By decoupling signaling and media and coordinating them through a control interface (gRPC in this case), it becomes possible to:

1. **Scale independently** — grow signaling and media at different rates  
2. **Load Distribution** — ability to scale emdia resources
3. **Isolate resources** — load spikes don’t interfere with call setup  
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

## Switchboard Roadmap

This roadmap outlines the major capabilities and milestones planned for Switchboard.

### Foundation
- Configuration and lifecycle management (env, flags, optional config file)
- Hot reload for non-critical settings (routing, logging)
- Structured logging with call/dialog correlation
- Core B2BUA call flows (INVITE, re-INVITE, UPDATE, BYE, CANCEL)
- Early media support (183, PRACK where applicable)
- Deterministic transaction and dialog state handling
- Stable gRPC control for RTP managers (allocate, release, stats, health)
- Readiness and liveness checks and deterministic media cleanup

### Routing & Call Logic
- Rule-based routing (source, destination, domain, headers, time, tags)
- Routing actions (route, fork, failover, reject, rewrite)
- Reusable templates and macros
- Weighted routing and priority chains (LCR-style)
- E.164 normalization helpers
- Header manipulation (From, To, Contact, PAI, RPID)
- Optional topology hiding

### Security
- SIP over TLS (certificate reload, SNI)
- Optional mutual TLS for trusted peers
- SDES-SRTP support
- DTLS-SRTP for WebRTC compatibility
- Digest authentication for REGISTER and inbound traffic
- ACLs, rate limits, and basic anti-flood protections
- Static and dynamic banning hooks

### Registration
- Registration store (in-memory, optional Redis)
- NAT-aware Contact handling and ranking
- Multi-contact per AOR routing
- Expiration and keepalive strategies
- Outbound (RFC 5626) groundwork
- Registration lifecycle events for external systems

### APIs & Eventing
- REST APIs for routing and call control
- Call control primitives (hangup, transfer, header injection)
- Live call inspection and call detail views
- Webhooks and optional SSE
- Versioned call and media event schemas

### WebSockets & AI Hooks
- WebSocket interface for external controllers
- Subscription to call events
- Real-time call control actions
- RTP tap / fork for recording or AI processing
- Future WebRTC gateway considerations

### Presence & Integration
- SIP presence (SUBSCRIBE / NOTIFY)
- BLF and dialog event packages
- Bridging presence and dialog state to external systems
- Optional directory and contact integrations

### Media Capabilities
- Call recording (on-demand and policy-driven)
- Media transcoding (only when required)
- Explicit codec negotiation policies
- Resource limits enforced at the media layer
- DTMF handling (RFC2833, SIP INFO, in-band)
- Tone generation and basic announcements

### Observability & Operations
- Metrics (CPS, ASR, ACD, setup time, SIP errors)
- RTP quality metrics (jitter, packet loss, bitrate)
- Cross-component tracing (signaling ↔ media)
- Per-call debug artifacts with safe redaction
- Load testing and scenario replay tooling
- Optional multi-tenant isolation and quotas

### High Availability & Scaling
- Stateless signaling with externalized state (optional)
- Media node autoscaling strategies
- Geographic distribution (edge media, central control)
- Rolling upgrades with best-effort call preservation

## Contributing

Contributions are welcome, but please understand what you are getting into:

1. **This is unstable** - Things will break. APIs will change. Your PR might become irrelevant overnight.
2. **No promises** - This is a side project for learning. Response times will vary.
3. **Discussion first** - For anything non-trivial, open an issue to discuss before submitting a PR.

If you are also curious about VoIP systems and want to experiment together, pull up a chair and If this project somehow helps you learn something, that is the whole point.
