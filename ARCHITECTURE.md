# Switchboard Architecture

> **Note**: This document describes the *current* architecture as of January 2026. Everything is subject to change. This is a living document that will evolve with the project.

## Built With

This architecture is made possible by:

- **[Pion](https://github.com/pion)** - RTP, SDP, and WebRTC libraries for Go
- **[sipgo](https://github.com/emiago/sipgo)** - Pure Go SIP stack
- **[diago](https://github.com/emiago/diago)** - B2BUA patterns and reference implementation

The architectural inspiration comes from the **Kamailio + RTPEngine** pattern used in large-scale VoIP deployments.

## Design Philosophy

### Separation of Signaling and Media

The core architectural decision is separating signaling from media handling. This is inspired by the Kamailio + RTPEngine pattern:

**Traditional approach (Asterisk, FreeSWITCH):**
- Single process handles both SIP signaling and RTP media
- Scaling means scaling both concerns together
- Resource contention between CPU-bound signaling and I/O-bound media

**Switchboard approach (Kamailio/RTPEngine pattern):**
- **Signaling Server** - Handles SIP, lightweight and stateful
- **RTP Manager** - Handles media, heavyweight and I/O-bound
- **gRPC control plane** - Signaling tells media what to do

This separation enables:
1. **Independent scaling** - Add media servers without touching signaling
2. **Geographic distribution** - Media servers close to users, centralized signaling
3. **Resource isolation** - Media spikes don't affect call setup
4. **Container-friendly** - Small, focused binaries

### Why This Matters

Switchboard splits VoIP handling into distinct responsibilities:

1. **Signaling** - SIP protocol, call state, routing decisions
2. **Media** - RTP streaming, codec handling, audio bridging
3. **Presentation** - Admin visibility, monitoring

### Horizontal Scalability

Each component can scale independently:
- Multiple Signaling Servers behind a load balancer (with shared state - not yet implemented)
- Multiple RTP Managers with port range isolation
- UI Server aggregates from multiple backends

### Simplicity Over Features

The goal is not feature parity with FreeSWITCH or Asterisk. The goal is a simple, understandable codebase that does basic B2BUA functionality well.

## Component Overview

```
┌─────────────────────────────────────────────────────────────────────────┐
│                           SIP Clients                                    │
└─────────────────────────────┬───────────────────────────────────────────┘
                              │ SIP/UDP :5060
┌─────────────────────────────▼───────────────────────────────────────────┐
│                       SIGNALING SERVER                                   │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐    │
│  │    App      │  │   Dialog    │  │  Dialplan   │  │    B2BUA    │    │
│  │ Coordinator │  │   Manager   │  │   Engine    │  │   Service   │    │
│  └─────────────┘  └─────────────┘  └─────────────┘  └─────────────┘    │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐    │
│  │  Location   │  │ Registration│  │   Routing   │  │  Transport  │    │
│  │   Service   │  │   Handler   │  │  (INVITE)   │  │ (gRPC Pool) │    │
│  └─────────────┘  └─────────────┘  └─────────────┘  └─────────────┘    │
│  ┌─────────────┐  ┌─────────────┐                                       │
│  │  REST API   │  │   Events    │         HTTP :8080                    │
│  │   Server    │  │    Bus      │                                       │
│  └─────────────┘  └─────────────┘                                       │
└─────────────────────────────┬───────────────────────────────────────────┘
                              │ gRPC :9090
┌─────────────────────────────▼───────────────────────────────────────────┐
│                         RTP MANAGER                                      │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐    │
│  │   gRPC      │  │   Session   │  │    Media    │  │   Bridge    │    │
│  │   Server    │  │   Manager   │  │   Service   │  │   Manager   │    │
│  └─────────────┘  └─────────────┘  └─────────────┘  └─────────────┘    │
│  ┌─────────────┐  ┌─────────────┐                                       │
│  │  Port Pool  │  │     SDP     │                                       │
│  │  Allocator  │  │   Builder   │                                       │
│  └─────────────┘  └─────────────┘                                       │
└─────────────────────────────┬───────────────────────────────────────────┘
                              │ RTP :10000-20000
┌─────────────────────────────▼───────────────────────────────────────────┐
│                           SIP Clients                                    │
└─────────────────────────────────────────────────────────────────────────┘
```

## Signaling Server

The signaling server handles all SIP protocol interactions and call routing decisions.

### Key Packages

**`app/`** - Application coordinator (SwitchBoard struct)
- Wires together all components
- Registers SIP method handlers with sipgo
- Manages graceful shutdown

**`dialog/`** - SIP dialog state machine
- Tracks dialog state (Created, Ringing, Confirmed, Terminated)
- Handles in-dialog requests (ACK, BYE)
- Context management for cancellation

**`dialplan/`** - Call routing engine
- Pattern-based route matching (regex on destination)
- Action execution (Dial, Playback, Hangup, etc.)
- Session interface for dialplan actions

**`b2bua/`** - Back-to-Back User Agent
- Leg management (inbound A-leg, outbound B-leg)
- Call origination (INVITE, response handling, ACK)
- Bridge creation and media coordination
- Target resolution (user lookup, gateway routing)

**`location/`** - User location service
- In-memory AOR-to-contact binding storage
- Binding expiration and cleanup
- Lookup by user or full AOR

**`registration/`** - REGISTER handler
- Validates REGISTER requests
- Stores bindings in location service
- NAT handling (received/rport)

**`routing/`** - INVITE handler
- Entry point for inbound calls
- SDP extraction and media session creation
- Dialplan execution trigger

**`transport/`** - gRPC client pool
- Connection management to RTP Managers
- Load balancing (round-robin)
- Session affinity

### Call Flow: Inbound INVITE

```
1. SIP INVITE arrives at App
2. App dispatches to InviteHandler
3. InviteHandler:
   a. Creates Dialog via DialogManager
   b. Sends 100 Trying
   c. Extracts SDP, creates media session via Transport
   d. Sends 183 Session Progress with SDP
   e. Sends 200 OK
   f. Triggers dialplan execution (goroutine)
4. Dialplan executes Dial action:
   a. Session.Dial() calls CallService.DialAndBridge()
   b. B2BUA resolves target, originates B-leg
   c. On answer, creates Bridge, starts media bridging
   d. Waits for bridge termination
5. On BYE (from either leg):
   a. Bridge detects leg termination
   b. Unbridges media
   c. Hangs up other leg
   d. Dialog cleanup
```

## RTP Manager

The RTP Manager handles all media operations independent of signaling.

### Key Packages

**`server/`** - gRPC service implementation
- CreateSession, DestroySession
- PlayAudio, StopAudio
- BridgeMedia, UnbridgeMedia

**`session/`** - Session lifecycle management
- Session state tracking
- Resource cleanup on termination

**`media/`** - Audio processing
- WAV file reading
- PCMU encoding
- RTP packet timing
- Jitter buffer (receive side)

**`bridge/`** - RTP bridging
- Bidirectional packet forwarding
- Address rewriting

**`portpool/`** - Port allocation
- Thread-safe port pool
- Configurable range
- Automatic reclamation

**`sdp/`** - SDP generation
- Answer generation from offer
- Codec selection

### Media Flow: Bridged Call

```
A-leg Phone  ←→  Session A  ←→  Bridge  ←→  Session B  ←→  B-leg Phone
     │                │                           │                │
     └── RTP ────────►│                           │◄────── RTP ────┘
                      │                           │
                      └─── packets forwarded ────►│
                      │◄─── packets forwarded ────┘
```

## UI Server

A simple admin dashboard for visibility into running calls.

### Design

- **HTMX** for dynamic updates without heavy JavaScript
- **Tailwind CSS** for styling
- **Multi-backend** aggregation from multiple signaling servers
- **SSE** for real-time updates (planned)

## Data Flow

### SIP Registration

```
Client                  Signaling                Location
   │                        │                        │
   ├─ REGISTER ────────────►│                        │
   │                        ├─ Store binding ───────►│
   │                        │◄─ OK ─────────────────┤
   │◄─ 200 OK ─────────────┤                        │
```

### B2BUA Call

```
A-Phone    Signaling       RTP Manager      B-Phone
   │           │                │               │
   ├─ INVITE ─►│                │               │
   │           ├─ CreateSession ►│               │
   │           │◄─ SDP ─────────┤               │
   │◄─ 183 ────┤                │               │
   │◄─ 200 ────┤                │               │
   ├─ ACK ────►│                │               │
   │           │                │               │
   │           ├─ CreateSession ►│  (B-leg)     │
   │           ├─ INVITE ───────┼──────────────►│
   │           │◄───────────────┼─ 200 ─────────┤
   │           ├─ ACK ──────────┼──────────────►│
   │           │                │               │
   │           ├─ BridgeMedia ─►│               │
   │◄══════════╪════ RTP ══════►╪══════════════►│
   │           │                │               │
   │◄─ BYE ────┤  (A hangs up)  │               │
   ├─ 200 ────►│                │               │
   │           ├─ BYE ──────────┼──────────────►│
   │           │◄───────────────┼─ 200 ─────────┤
   │           ├─ DestroySession►│               │
```

## Key Design Decisions

### Why gRPC Between Services?

The choice of gRPC as the control protocol between Signaling and RTP Manager is central to the architecture:

**Control Plane vs Data Plane:**
- gRPC carries control messages (create session, bridge media, play audio)
- RTP carries the actual media data directly between clients and RTP Manager
- This mirrors how Kamailio uses the ng control protocol with RTPEngine

**Why gRPC specifically:**
- Strongly typed contracts (proto files) - changes are explicit and versioned
- Efficient binary serialization - low overhead for control messages
- Bidirectional streaming - used for PlayAudio status updates
- Excellent Go tooling and code generation
- Connection pooling and load balancing built-in

**Alternative considered:**
- HTTP/REST - simpler but less efficient, no streaming
- Custom TCP protocol - more work, less tooling
- Unix sockets - limits to single-host deployments

### Why In-Memory Storage?

For now, simplicity. The location service, dialog manager, and session manager all use in-memory maps. This means:
- No external dependencies to run
- State is lost on restart
- Cannot scale signaling servers horizontally (yet)

Future: Redis or etcd for shared state.

### Why No Authentication?

Not implemented yet. This is a significant security gap that makes the system completely unsuitable for production.

## Future Considerations

These are ideas, not commitments:

- **Persistent storage** - Redis for location/dialog state
- **Authentication** - Digest auth, possibly SIP over TLS
- **SRTP** - Encrypted media
- **WebRTC** - Browser-based calling
- **NATS** - Event distribution for multi-server deployments
- **Prometheus** - Metrics export
- **Re-INVITE** - Hold/resume, codec changes
- **REFER** - Call transfer
---

*This document will be updated as the architecture evolves. Last updated: January 2026*
