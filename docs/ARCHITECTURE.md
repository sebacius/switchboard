# Architecture

> **Note**: This document describes the architecture as of March 2026. This is a living document that evolves with the project.

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

### Component Responsibilities

Switchboard splits VoIP handling into distinct responsibilities:

1. **Signaling** - SIP protocol, call state, routing decisions, LLM conversation context
2. **Media** - RTP streaming, codec handling, audio bridging, TTS synthesis, ASR transcription
3. **AI Services** - LLM inference (Ollama), text-to-speech (Piper/openedai-speech), speech-to-text (Whisper/faster-whisper)
4. **Presentation** - Admin visibility, monitoring

### Horizontal Scalability

Each component can scale independently:

- Multiple Signaling Servers behind a load balancer (with shared state - not yet implemented)
- Multiple RTP Managers with port range isolation
- UI Server aggregates from multiple backends

### Simplicity Over Features

The goal is not feature parity with FreeSWITCH or Asterisk. The goal is a simple, understandable codebase that does basic B2BUA functionality well.

## Component Overview

```
+-------------------------------------------------------------------------+
|                           SIP Clients                                    |
+-----------------------------+-------------------------------------------+
                              | SIP/UDP :5060
+-----------------------------v-------------------------------------------+
|                       SIGNALING SERVER                                   |
|  +-------------+  +-------------+  +-------------+  +-------------+    |
|  |    App      |  |   Dialog    |  |  Dialplan   |  |    B2BUA    |    |
|  | Coordinator |  |   Manager   |  |   Engine    |  |   Service   |    |
|  +-------------+  +-------------+  +-------------+  +-------------+    |
|  +-------------+  +-------------+  +-------------+  +-------------+    |
|  |  Location   |  |   Routing   |  | MediaClient |  |  REST API   |    |
|  |   Service   |  |  Handlers   |  | (gRPC Pool) |  |   Server    |    |
|  +-------------+  +-------------+  +-------------+  +-------------+    |
|  +-------------+  +-------------+                                       |
|  |   Events    |  | LLM Client  |  HTTP :8080                          |
|  |    Bus      |  |  (Ollama)   |                                       |
|  +-------------+  +-------------+                                       |
+--------+--------------------+-------------------------------------------+
         |                    | gRPC :9090
         | HTTP               |
         v                    v
+----------------+   +-----------------------------------------------+
|    OLLAMA      |   |                 RTP MANAGER                     |
|  LLM Server   |   |  +-------------+  +-------------+              |
|  (llama3, etc) |   |  |   gRPC      |  |   Session   |              |
+----------------+   |  |   Server    |  |   Manager   |              |
                     |  +-------------+  +-------------+              |
                     |  +-------------+  +-------------+              |
                     |  |    Media    |  |   Bridge    |              |
                     |  |   Service   |  |   Manager   |              |
                     |  +-------------+  +-------------+              |
                     |  +-------------+  +-------------+              |
                     |  |  Port Pool  |  |     SDP     |              |
                     |  |  Allocator  |  |   Builder   |              |
                     |  +-------------+  +-------------+              |
                     +--------+----+-----------+-----------------------+
                              |    |           |
                              |    | HTTP      | HTTP
                              |    v           v
                              |  +-----------+ +------------------+
                              |  |   PIPER   | | WHISPER          |
                              |  |  TTS      | | (faster-whisper) |
                              |  |  Server   | | ASR Server       |
                              |  +-----------+ +------------------+
                              |
                              | RTP :10000-20000
+-----------------------------v-------------------------------------------+
|                           SIP Clients                                    |
+-------------------------------------------------------------------------+
```

## Signaling Server

The signaling server handles all SIP protocol interactions and call routing decisions.

### Key Packages

| Package | Location | Purpose |
|---------|----------|---------|
| `app` | `internal/signaling/app/` | Application coordinator (SwitchBoard struct) |
| `dialog` | `internal/signaling/dialog/` | SIP dialog state machine |
| `dialplan` | `internal/signaling/dialplan/` | Call routing engine |
| `b2bua` | `internal/signaling/b2bua/` | Back-to-Back User Agent |
| `location` | `internal/signaling/location/` | User location service |
| `routing` | `internal/signaling/routing/` | SIP request handlers (INVITE, BYE, ACK, CANCEL, REGISTER) |
| `mediaclient` | `internal/signaling/mediaclient/` | gRPC client pool to RTP Manager |
| `api` | `internal/signaling/api/` | REST API server |
| `events` | `internal/signaling/events/` | Event publishing (NATS) |

### Request Flow

1. SIP request arrives at the sipgo server
2. App coordinator dispatches to appropriate handler in `routing/`
3. Handler interacts with dialog manager, location service, and dialplan
4. Media operations delegated to RTP Manager via `mediaclient/`
5. Responses sent back through sipgo

## RTP Manager

The RTP Manager handles all media operations independent of signaling.

### Key Packages

| Package | Location | Purpose |
|---------|----------|---------|
| `server` | `internal/rtpmanager/server/` | gRPC service implementation |
| `session` | `internal/rtpmanager/session/` | Session lifecycle management |
| `media` | `internal/rtpmanager/media/` | Audio processing, RTP streaming |
| `bridge` | `internal/rtpmanager/bridge/` | RTP relay for call bridging |
| `portpool` | `internal/rtpmanager/portpool/` | Port allocation |
| `sdp` | `internal/rtpmanager/sdp/` | SDP generation |

### Media Flow

```
A-leg Phone  <->  Session A  <->  Bridge  <->  Session B  <->  B-leg Phone
     |                |                           |                |
     +-- RTP -------->|                           |<------ RTP ----+
                      |                           |
                      +--- packets forwarded ---->|
                      |<--- packets forwarded ----+
```

## UI Server

A simple admin dashboard for visibility into running calls.

### Key Packages

| Package | Location | Purpose |
|---------|----------|---------|
| `server` | `internal/ui/server/` | HTTP server and route handlers |
| `client` | `internal/ui/client/` | HTTP client for signaling API |
| `config` | `internal/ui/config/` | Configuration |

### Design

- **HTMX** for dynamic updates without heavy JavaScript
- **Tailwind CSS** for styling
- **Multi-backend** aggregation from multiple signaling servers
- **SSE** for real-time updates (planned)

## AI Agent Architecture

The `ai_agent` dialplan action makes LLM-driven voice conversations a first-class routing mechanism. Rather than being a bolt-on integration, the AI agent sits alongside `play_audio`, `dial`, `park`, and `hangup` as a core action in the dialplan engine.

### Service Responsibilities

The AI agent spans three layers, each with a clear role:

| Layer | Service | Responsibility |
|-------|---------|----------------|
| Context | Signaling Server | Holds conversation state, LLM client, conversation history, action execution |
| Media I/O | RTP Manager | TTS synthesis (via Piper), ASR transcription (via Whisper), RTP streaming |
| Inference | External (Ollama, Piper, Whisper) | Stateless AI services, no call awareness |

The signaling server owns the conversation loop and decides what happens next. The RTP Manager is the media workhorse -- it converts text to speech and speech to text but has no knowledge of conversation context. The external AI services (Ollama, Piper, Whisper) are stateless HTTP servers that process individual requests.

### Two Operating Modes

**Conversational mode** (default): Multi-turn dialogue. After the greeting, the system enters a loop: listen for caller speech (ASR), send transcript to the LLM, speak the LLM response (TTS), repeat. The loop continues until the LLM emits an ACTION block (transfer, hangup, park) or max turns is reached.

**Routing mode**: Single-shot decision. After the greeting, the LLM receives a single prompt and responds with a message and an action. There is no listen step -- the LLM decides based on tenant instructions alone. This is useful for after-hours routing where every call should be handled the same way.

### Two-Layer Prompt System

The LLM system prompt is assembled from two sources:

1. **Settings** (`resources/config/settings.md`): Loaded once at startup by the action factory. Defines the action contract (available actions, response format, rules). This is the same for all tenants and all calls.

2. **Tenant config** (`resources/tenants/<name>.md`): Loaded per-call based on the `config` parameter in the dialplan. Provides business context, personality, and specific instructions for the AI agent. The `${domain}` variable in the config field enables dynamic tenant resolution from the SIP domain of the incoming call.

This separation means the action contract is stable and cached, while tenant-specific behavior can vary per call without any code changes.

### LLM Action Execution

The LLM can emit ACTION blocks in its responses. The signaling server parses these and executes them against the call session:

| Action | Effect | Required Params |
|--------|--------|-----------------|
| `transfer` | Dial an extension via B2BUA | `extension` |
| `hangup` | Terminate the call | (none) |
| `park` | Place caller in a parking slot with MOH | `slot` (optional) |

Actions are validated before execution. Unknown action names are silently ignored. In conversational mode, a failed transfer informs the caller and continues the conversation rather than ending the call.

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

**Alternatives considered:**
- HTTP/REST - simpler but less efficient, no streaming
- Custom TCP protocol - more work, less tooling
- Unix sockets - limits to single-host deployments

### Why In-Memory Storage?

For now, simplicity. The location service, dialog manager, and session manager all use in-memory maps. This means:

- No external dependencies to run
- State is lost on restart
- Cannot scale signaling servers horizontally (yet)

Future: Redis or etcd for shared state.

### Why AI Agent as a First-Class Dialplan Action?

The AI voice agent is implemented as an `ai_agent` action registered in the same ActionRegistry as `play_audio`, `dial`, and `hangup`. This was a deliberate choice over a separate subsystem or external webhook.

**Why a dialplan action:**
- Consistent with existing routing model -- no new concepts for operators to learn
- Can be combined with other actions in a route (e.g., play greeting, then AI agent)
- Same lifecycle management (context cancellation, session cleanup)
- Configuration lives in dialplan.json alongside all other routing

**Alternatives considered:**
- External webhook/API -- adds latency, requires separate deployment, loses session context
- Separate AI service -- adds complexity without clear benefit since signaling already has the call context

### Why Context Lives in Signaling, Not RTP Manager?

The conversation history, LLM client, and action execution all live in the signaling server. The RTP Manager only handles media I/O (TTS and ASR).

**Rationale:**
- Signaling already manages call state, dialog lifecycle, and action execution
- Conversation context is a routing concern, not a media concern
- Keeps RTP Manager stateless with respect to AI -- it just converts between text and audio
- Allows a single signaling server to coordinate conversations across multiple RTP Managers

### Why a Two-Layer Prompt System?

The system prompt is split into settings (action contract) and tenant config (business context).

**Rationale:**
- Settings define the stable interface (action format, rules) and are loaded once at startup
- Tenant config changes per deployment or per call, without modifying the action contract
- The `${domain}` variable allows multi-tenant deployments where the SIP domain determines which tenant config to load
- Operators can customize AI behavior by editing markdown files, with no code changes or restarts needed for tenant configs

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

## Related Documents

- [Call Flows](CALL_FLOWS.md) - Detailed sequence diagrams
- [B2BUA Design](B2BUA.md) - B2BUA implementation details
- [Code Map](CODE_MAP.md) - Detailed package descriptions

---

*Last updated: March 2026*
