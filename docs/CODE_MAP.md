# Code Map

What logic lives where in the Switchboard codebase.

---

## Entry Points

### `cmd/signaling/main.go`
- Loads config, prints banner, initializes logger
- Creates `app.SwitchBoard` instance
- Starts server, waits for shutdown signal
- Logs network interfaces for debugging

### `cmd/rtpmanager/main.go`
- Loads config, prints banner, initializes logger
- Creates RTP Manager server
- Sets up gRPC server with keepalive and logging interceptors
- Registers `RTPManagerService`, starts listening

### `cmd/ui/main.go`
- Loads config, prints banner
- Creates UI server with backend clients
- Starts HTTP server, waits for shutdown

---

## Signaling Server

### `internal/signaling/app/app.go`
**The main coordinator - ties everything together**
- `SwitchBoard` struct holds all components
- `NewServer()` - creates UA, servers, managers, media client pool, API server
- Registers SIP handlers: INVITE, BYE, ACK, CANCEL, REGISTER
- `onTerminated()` callback - cleanup when dialog ends
- `Start()` / `Close()` - lifecycle management

### `internal/signaling/config/config.go`
- `Config` struct with all signaling settings
- `Load()` - parses flags, reads env vars
- `isValidAddress()` - validates advertise address
- `getPrimaryInterfaceIP()` - auto-detects IP

---

### Dialog Management

### `internal/signaling/dialog/dialog.go`
**Single dialog entity**
- `Dialog` struct - call state, SIP session, media info
- `NewDialog()` - creates with context
- `SetState()` - state transitions with validation
- `Terminate()` - marks terminated with reason
- `Cancel()` - cancels context (stops actions)

### `internal/signaling/dialog/manager.go`
**Manages all active dialogs**
- `Manager` struct with TTL store
- `CreateFromInvite()` - new dialog from INVITE
- `Get()` / `GetByCallID()` - lookups
- `ConfirmWithACK()` - transition to confirmed state
- `Terminate()` - end dialog, trigger cleanup
- `sendBYE()` - constructs and sends BYE request
- `startACKTimeoutWatcher()` - 32s timeout per RFC 3261

### `internal/signaling/dialog/state.go`
**State machine definitions**
- `CallState` enum: Initial, Early, WaitingACK, Confirmed, Terminating, Terminated
- `TerminateReason` enum: LocalBYE, RemoteBYE, Error, Timeout, etc.
- `String()` methods for logging

### `internal/signaling/dialog/info.go`
**Dialog information struct**
- `DialogInfo` struct for API responses
- Contains call metadata without internal state

### `internal/signaling/dialog/interface.go`
**Interface definitions**
- `DialogManager` interface for dependency injection

---

### SIP Request Handlers

### `internal/signaling/routing/invite.go`
**INVITE handler - inbound call setup**
- `InviteHandler` struct
- `HandleINVITE()` - main entry point
  - Extracts SDP (client address, port, codecs)
  - Creates dialog via manager
  - Sends 100 Trying
  - Creates RTP session via media client
  - Sends 183 Session Progress + 200 OK
- `executeDialplan()` - runs after ACK, terminates when done
- `extractSDPInfo()` - parses offer SDP
- `buildContactHeader()` - constructs Contact for responses

### `internal/signaling/routing/bye.go`
**BYE handler - call termination**
- `HandleBYE()` - processes incoming BYE
- Looks up dialog, terminates it
- Sends 200 OK response

### `internal/signaling/routing/ack.go`
**ACK handler**
- `HandleACK()` - confirms dialog
- Calls `manager.ConfirmWithACK()`

### `internal/signaling/routing/cancel.go`
**CANCEL handler**
- `HandleCANCEL()` - cancels pending INVITE
- Terminates dialog if exists

### `internal/signaling/routing/register.go`
**REGISTER handler**
- `Handler` struct with location store
- `HandleRegister()` - processes REGISTER
- Validates expires, extracts contacts
- Updates location store bindings
- Handles wildcard unregister (Contact: *)
- Returns 200 OK with current bindings

---

### Dialplan Engine

### `internal/signaling/dialplan/dialplan.go`
**Route configuration and matching**
- `Dialplan` struct with atomic route pointer
- `Load()` / `LoadFromReader()` - parse JSON config
- `Match()` - find route by destination pattern
- `Reload()` - hot reload config
- Copy-on-write for lock-free reads

### `internal/signaling/dialplan/executor.go`
**Action execution engine**
- `Executor` struct
- `Execute()` - runs matched route's actions
- Sequential execution with context cancellation
- `ExecutionError` - tracks partial completion

### `internal/signaling/dialplan/route.go`
**Route definitions**
- `Route` struct with pattern, priority, actions
- Route matching logic

### `internal/signaling/dialplan/session.go`
**CallSession interface and implementation**
- Defines what actions can do:
  - `PlayAudio()`, `StopAudio()`
  - `Dial()`, `Hangup()`
  - `CallID()`, `Destination()`, `CallerID()`
- `sessionImpl` wraps dialog, media client, call service
- Variable substitution for action params

### `internal/signaling/dialplan/action.go`
**Action factory and registry**
- `Action` interface: `Execute(ctx, session) error`
- `ActionFactory` - creates actions from JSON
- `RegisterAction()` - adds action types
- Built-in registration of play_audio, dial, hangup

### `internal/signaling/dialplan/action_play_audio.go`
**play_audio action**
- `PlayAudioAction` struct
- Reads `file` param
- Calls `session.PlayAudio()`

### `internal/signaling/dialplan/action_dial.go`
**dial action**
- `DialAction` struct
- Reads `target` and `timeout` params
- Calls `session.Dial()`

### `internal/signaling/dialplan/action_hangup.go`
**hangup action**
- `HangupAction` struct
- Reads optional `reason` param
- Calls `session.Hangup()`

### `internal/signaling/dialplan/errors.go`
**Dialplan error types**
- `NoRouteError`, `ActionError`, etc.

---

### B2BUA (Call Bridging)

### `internal/signaling/b2bua/service.go`
**CallService interface definition**
- High-level B2BUA operations
- `Dial()`, `Lookup()`, `CreateOutboundLeg()`
- `AdoptInboundLeg()`, `CreateBridge()`
- `Config` struct for service settings

### `internal/signaling/b2bua/call_service.go`
**CallService implementation**
- `callService` struct
- `Dial()` - lookup + originate + wait for answer
- `Lookup()` - resolve target via resolver chain
- `CreateOutboundLeg()` - originate call
- `AdoptInboundLeg()` - wrap existing dialog as leg
- `CreateBridge()` - connect two legs

### `internal/signaling/b2bua/leg.go`
**Leg interface and implementation**
- One side of a bridged call
- `ID()`, `CallID()`, `Direction()`
- `GetState()`, `Dialog()`, `SessionID()`
- `Answer()`, `Hangup()`, `Destroy()`
- State machine: Created -> Ringing -> Answered -> Destroyed
- `SetState()` with validation
- Callback management for state changes

### `internal/signaling/b2bua/bridge.go`
**Bridge interface and implementation**
- Connects two legs for media
- `LegA()`, `LegB()`, `GetState()`
- `Start()`, `Stop()`, `WaitForTermination()`
- `Start()` - validates legs, starts media bridge via transport
- `Stop()` - stops media, optionally hangs up legs
- Monitors leg termination

### `internal/signaling/b2bua/originator.go`
**Outbound call origination**
- `Originator` struct
- `Originate()` - sends INVITE to target
  - Creates new Call-ID for B-leg
  - Builds INVITE request
  - Creates RTP session for B-leg
  - Waits for provisional/final response
- `handleProvisionalResponse()` - 180/183 handling
- `handleSuccessResponse()` - 200 OK handling
- Request/response building helpers

### `internal/signaling/b2bua/lookup.go`
**Target resolution interfaces**
- `Resolver` interface
- `LookupResult` - resolved contacts
- `LookupError` - resolution failures

### `internal/signaling/b2bua/chain_resolver.go`
**Chained resolver**
- Tries multiple resolvers in order
- First success wins

### `internal/signaling/b2bua/direct_resolver.go`
**Direct SIP URI resolver**
- Parses `sip:user@host:port` directly
- No lookup needed

### `internal/signaling/b2bua/user_resolver.go`
**User/extension resolver**
- Handles `user/1001` format
- Looks up in location store
- Returns registered contacts

### `internal/signaling/b2bua/state.go`
**State definitions**
- `LegState`: Created, Ringing, Answered, Destroyed
- `BridgeState`: Created, Active, Terminated
- `LegDirection`: Inbound, Outbound
- `TerminationCause`: Normal, Rejected, Timeout, Error

### `internal/signaling/b2bua/errors.go`
**Error types**
- `ErrTargetNotFound`
- `ErrNoContacts`
- `ErrNotImplemented`

---

### Media Client (RTP Manager Connection)

### `internal/signaling/mediaclient/transport.go`
**Transport interface**
- `CreateSession()` - allocate RTP session
- `DestroySession()` - release session
- `PlayAudio()` - stream audio file
- `StopAudio()` - stop playback
- `BridgeMedia()` / `UnbridgeMedia()` - media bridging
- `UpdateSessionRemote()` - update endpoint

### `internal/signaling/mediaclient/grpc.go`
**gRPC transport implementation**
- `GRPCTransport` struct
- Implements all Transport methods
- Converts to/from protobuf messages
- Handles streaming responses (PlayAudio)

### `internal/signaling/mediaclient/pool.go`
**Transport pool with load balancing**
- `Pool` struct with multiple transports
- `CreateSession()` - round-robin allocation
- Session affinity map
- Health checking goroutine
- `markHealthy()` / `markUnhealthy()`

---

### Location Service

### `internal/signaling/location/store.go`
**User location storage**
- `Store` struct
- `Bind()` - add/update contact binding
- `Unbind()` - remove binding
- `UnbindAll()` - remove all bindings for AOR
- `Lookup()` - get contacts for AOR
- `GetAllBindings()` - list all (for API)
- TTL-based expiration

### `internal/signaling/location/binding.go`
**Binding data structure**
- `Binding` struct: AOR, ContactURI, Expires, etc.
- `IsExpired()` check

### `internal/signaling/location/interface.go`
**Interface definitions**
- `Store` interface for dependency injection

---

### Events

### `internal/signaling/events/publisher.go`
**Event publishing**
- `Publisher` interface
- Event emission for call lifecycle

### `internal/signaling/events/types.go`
**Event type definitions**
- Call started, ended, etc.

### `internal/signaling/events/subjects.go`
**NATS subject definitions**
- Topic naming conventions

### `internal/signaling/events/nats.go`
**NATS publisher implementation**
- Publishes events to NATS

### `internal/signaling/events/builder.go`
**Event builders**
- Helpers to construct event payloads

---

### API Server

### `internal/signaling/api/server.go`
**REST API implementation**
- `Server` struct with HTTP mux
- `GET /api/v1/health` - health check
- `GET /api/v1/stats` - statistics
- `GET /api/v1/registrations` - all bindings
- `GET /api/v1/dialogs` - active dialogs
- `GET /api/v1/sessions` - RTP sessions
- `GET /api/v1/rtpmanagers` - connected RTP managers with health status
- `SessionRecorder` - tracks session info

---

### Storage

### `internal/signaling/store/ttlstore.go`
**Generic TTL-based storage**
- `TTLStore[K, V]` generic struct
- `Set()` with TTL
- `Get()`, `Delete()`
- Background cleanup goroutine
- Eviction callback support

### `internal/signaling/store/repository.go`
**Repository pattern helpers**
- Common storage patterns

---

## RTP Manager

### `internal/rtpmanager/server/server.go`
**gRPC service implementation**
- `Server` struct
- `CreateSession()` - allocates ports, generates SDP
- `DestroySession()` - cleanup
- `PlayAudio()` - starts streaming, returns event channel
- `StopAudio()` - cancels playback
- `BridgeMedia()` - connects two sessions
- `Health()` - health check

### `internal/rtpmanager/config/config.go`
- `Config` struct
- `Load()` - flags and env vars
- `getPrimaryInterfaceIP()` - auto-detection

---

### Session Management

### `internal/rtpmanager/session/manager.go`
**Session lifecycle**
- `Manager` struct
- `CreateSession()` - allocates ports, negotiates codec
- `GetSession()` - lookup by ID
- `UpdateRemoteEndpoint()` - update after B-leg SDP
- `DestroySession()` - release resources
- `PlayAudio()` / `StopAudio()` - delegates to media
- Session state tracking

---

### Media Processing

### `internal/rtpmanager/media/service.go`
**Media service interface and implementation**
- `Service` interface
- `LocalService` - in-process media handling
- `Play()` - main playback entry point
- `Stop()` - cancel playback
- Manages active playbacks map

### `internal/rtpmanager/media/interfaces.go`
**Interface definitions**
- Media-related interfaces

### `internal/rtpmanager/media/types.go`
**Type definitions**
- Codec info, playback state, etc.

### `internal/rtpmanager/media/audio.go`
**WAV file handling**
- `ReadWavFile()` - parses WAV headers
- `WavInfo` struct: sample rate, channels, bits
- Validates format (must be PCM)

### `internal/rtpmanager/media/codec.go`
**Codec management**
- `CodecManager` - registry of codecs
- `CodecInfo` struct: name, payload type, sample rate
- `Encode()` / `Decode()` methods
- PCMU implementation (G.711 u-law)
- `Resample()` - sample rate conversion

### `internal/rtpmanager/media/rtp.go`
**RTP packet construction**
- `RTPHeader` struct
- `BuildRTPPacket()` - header + payload
- `ParseRTPHeader()` - for incoming packets

### `internal/rtpmanager/media/rtp_writer.go`
**RTP packet sending**
- `RTPWriter` struct
- Binds UDP socket
- `WritePacket()` - sends to remote endpoint
- Sequence/timestamp management

### `internal/rtpmanager/media/sequence.go`
**RTP sequence numbers**
- `SequenceGenerator` - random start, increment
- `TimestampGenerator` - based on sample count

### `internal/rtpmanager/media/dtmf.go`
**DTMF definitions**
- RFC 2833 event codes
- Tone mappings (0-9, *, #, A-D)

### `internal/rtpmanager/media/dtmf_reader.go`
**DTMF detection**
- `DTMFReader` - detects DTMF in RTP stream
- Event parsing

### `internal/rtpmanager/media/dtmf_writer.go`
**DTMF generation**
- `DTMFWriter` - generates DTMF RTP events
- Duration and volume control

---

### Port Management

### `internal/rtpmanager/portpool/pool.go`
**RTP port allocation**
- `Pool` struct with available ports
- `Allocate()` - get RTP/RTCP port pair
- `Release()` - return ports to pool
- `Available()` - count free ports

---

### SDP Generation

### `internal/rtpmanager/sdp/builder.go`
**SDP answer construction**
- `Builder` struct
- `Build()` - creates SDP body
- Sets origin, connection, media lines
- Includes selected codec

---

### Media Bridging

### `internal/rtpmanager/bridge/bridge.go`
**RTP relay for B2BUA**
- `Bridge` struct
- `Start()` - creates bidirectional relay
- Binds sockets for both sessions
- Forwards packets A<->B
- `Stop()` - terminates relay
- Statistics tracking

---

## UI Server

### `internal/ui/server/server.go`
**HTTP server**
- `Server` struct
- Route registration
- `handleIndex()` - main dashboard with sidebar navigation
- `handlePartial*()` - HTMX partials for live updates
- Data aggregation from multiple signaling backends
- Dashboard sections: Overview, Registrations, Dialogs, Sessions, RTP Managers

### `internal/ui/server/templates.go`
**HTML templates**
- Template definitions
- Render functions
- HTMX integration

### `internal/ui/client/client.go`
**Backend HTTP client**
- `Client` struct
- `GetStats()`, `GetRegistrations()`
- `GetDialogs()`, `GetSessions()`
- `GetRtpManagers()` - fetches connected RTP managers
- Error handling

### `internal/ui/config/config.go`
- `Config` struct
- `Backend` struct: name, address
- `Load()` - parses backends list

---

## Shared

### `internal/banner/banner.go`
**Startup banner**
- ASCII art logo
- `Print()` - displays logo + config

### `internal/logger/logger.go`
**Logging setup**
- `InitLogger()` - configures slog
- Timestamp formatting

---

## API Types

### `api/types/v1/types.go`
**Shared API types**
- Types used between services
- Request/response structures

---

## Generated Code

### `pkg/rtpmanager/v1/rtpmanager.pb.go`
**Protobuf messages**
- Generated from `api/proto/rtpmanager/v1/rtpmanager.proto`
- All request/response message types

### `pkg/rtpmanager/v1/rtpmanager_grpc.pb.go`
**gRPC service stubs**
- `RTPManagerServiceClient` interface
- `RTPManagerServiceServer` interface
- Registration functions

---

## Related Documents

- [Architecture](ARCHITECTURE.md) - System design
- [B2BUA Design](B2BUA.md) - B2BUA implementation
- [Development](DEVELOPMENT.md) - Build and test

---

*Last updated: January 2026*
