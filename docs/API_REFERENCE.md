# API Reference

This document covers the REST API provided by the Signaling Server and the gRPC protocol used between Signaling and RTP Manager.

## REST API

The Signaling Server exposes a REST API on port 8080 (configurable via `API_PORT`).

### Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/v1/health` | Health check |
| GET | `/api/v1/stats` | System statistics |
| GET | `/api/v1/registrations` | SIP registrations |
| GET | `/api/v1/dialogs` | Active SIP dialogs |
| GET | `/api/v1/sessions` | Active RTP sessions |
| GET | `/api/v1/rtpmanagers` | Connected RTP managers |

### Health Check

```
GET /api/v1/health
```

Returns service health status.

**Response:**
```json
{
  "status": "ok",
  "timestamp": "2026-01-15T10:30:00Z"
}
```

**Status Codes:**
- `200 OK` - Service is healthy
- `503 Service Unavailable` - Service is unhealthy

### Statistics

```
GET /api/v1/stats
```

Returns aggregate statistics.

**Response:**
```json
{
  "total_sessions": 1250,
  "active_sessions": 15,
  "total_registrations": 100,
  "active_dialogs": 10,
  "uptime_seconds": 86400
}
```

| Field | Type | Description |
|-------|------|-------------|
| `total_sessions` | int | Total sessions created since startup |
| `active_sessions` | int | Currently active RTP sessions |
| `total_registrations` | int | Total registered users |
| `active_dialogs` | int | Currently active SIP dialogs |
| `uptime_seconds` | int | Seconds since service start |

### Registrations

```
GET /api/v1/registrations
```

Returns all current SIP registrations.

**Response:**
```json
{
  "registrations": [
    {
      "aor": "sip:1001@switchboard.local",
      "contact": "sip:1001@192.168.1.100:5060",
      "expires": 3600,
      "registered_at": "2026-01-15T10:00:00Z",
      "expires_at": "2026-01-15T11:00:00Z",
      "user_agent": "OpalVoIP/3.18.8"
    }
  ]
}
```

| Field | Type | Description |
|-------|------|-------------|
| `aor` | string | Address of Record |
| `contact` | string | Contact URI (where to reach the user) |
| `expires` | int | Registration validity in seconds |
| `registered_at` | string | ISO 8601 timestamp of registration |
| `expires_at` | string | ISO 8601 timestamp when registration expires |
| `user_agent` | string | User-Agent header from REGISTER |

### Dialogs

```
GET /api/v1/dialogs
```

Returns all active SIP dialogs.

**Response:**
```json
{
  "dialogs": [
    {
      "call_id": "abc123@client.local",
      "local_tag": "tag-xyz",
      "remote_tag": "tag-abc",
      "state": "Confirmed",
      "direction": "inbound",
      "from_uri": "sip:1001@switchboard.local",
      "to_uri": "sip:1002@switchboard.local",
      "remote_addr": "192.168.1.100",
      "remote_port": 5060,
      "session_id": "sess-123",
      "created_at": "2026-01-15T10:30:00Z",
      "duration_seconds": 120
    }
  ]
}
```

| Field | Type | Description |
|-------|------|-------------|
| `call_id` | string | SIP Call-ID |
| `local_tag` | string | Local tag |
| `remote_tag` | string | Remote tag |
| `state` | string | Dialog state (Initial, Early, WaitingACK, Confirmed, Terminated) |
| `direction` | string | inbound or outbound |
| `from_uri` | string | From header URI |
| `to_uri` | string | To header URI |
| `remote_addr` | string | Remote IP address |
| `remote_port` | int | Remote port |
| `session_id` | string | Associated RTP session ID |
| `created_at` | string | ISO 8601 creation timestamp |
| `duration_seconds` | int | Call duration in seconds |

### Sessions

```
GET /api/v1/sessions
```

Returns all active RTP sessions.

**Response:**
```json
{
  "sessions": [
    {
      "session_id": "sess-123",
      "call_id": "abc123@client.local",
      "local_addr": "192.168.1.10",
      "local_port": 10000,
      "remote_addr": "192.168.1.100",
      "remote_port": 40000,
      "codec": "PCMU",
      "state": "active",
      "rtp_manager": "rtpmanager1:9090",
      "created_at": "2026-01-15T10:30:00Z"
    }
  ]
}
```

| Field | Type | Description |
|-------|------|-------------|
| `session_id` | string | Unique session identifier |
| `call_id` | string | Associated SIP Call-ID |
| `local_addr` | string | Local RTP address |
| `local_port` | int | Local RTP port |
| `remote_addr` | string | Remote RTP address |
| `remote_port` | int | Remote RTP port |
| `codec` | string | Negotiated codec (e.g., PCMU) |
| `state` | string | Session state |
| `rtp_manager` | string | RTP Manager handling this session |
| `created_at` | string | ISO 8601 creation timestamp |

### RTP Managers

```
GET /api/v1/rtpmanagers
```

Returns information about connected RTP Managers and their health status.

**Response:**
```json
{
  "rtp_managers": [
    {
      "address": "localhost:9090",
      "healthy": true,
      "active_sessions": 5,
      "available_ports": 95,
      "last_check": "2026-01-15T10:30:00Z"
    },
    {
      "address": "localhost:9091",
      "healthy": true,
      "active_sessions": 3,
      "available_ports": 97,
      "last_check": "2026-01-15T10:30:00Z"
    }
  ]
}
```

| Field | Type | Description |
|-------|------|-------------|
| `address` | string | RTP Manager gRPC address |
| `healthy` | bool | Health check status |
| `active_sessions` | int | Number of active RTP sessions |
| `available_ports` | int | Number of available RTP ports |
| `last_check` | string | ISO 8601 timestamp of last health check |

## UI Server API

The UI Server provides an HTML dashboard on port 3000 (configurable via `UI_PORT`).

### Routes

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/` | Main dashboard (sidebar navigation) |
| GET | `/health` | Health check |
| GET | `/admin/partials/stats` | HTMX partial for stats |
| GET | `/admin/partials/registrations` | HTMX partial for registrations |
| GET | `/admin/partials/dialogs` | HTMX partial for dialogs |
| GET | `/admin/partials/sessions` | HTMX partial for sessions |
| GET | `/admin/partials/rtpmanagers` | HTMX partial for RTP managers |

The HTMX partials are used for live updates without full page refresh.

### Dashboard Sections

The UI dashboard includes a sidebar with the following sections:

- **Overview** - System statistics and health summary
- **Registrations** - Active SIP registrations
- **Dialogs** - Current SIP dialogs
- **Sessions** - Active RTP sessions
- **RTP Managers** - Connected media servers with health status

## gRPC Protocol

The gRPC protocol is used between the Signaling Server and RTP Manager. The proto definition is at `api/proto/rtpmanager/v1/rtpmanager.proto`.

### Service Definition

```protobuf
service RTPManagerService {
  rpc CreateSession(CreateSessionRequest) returns (CreateSessionResponse);
  rpc DestroySession(DestroySessionRequest) returns (DestroySessionResponse);
  rpc PlayAudio(PlayAudioRequest) returns (stream PlaybackEvent);
  rpc StopAudio(StopAudioRequest) returns (StopAudioResponse);
  rpc BridgeMedia(BridgeMediaRequest) returns (BridgeMediaResponse);
  rpc UnbridgeMedia(UnbridgeMediaRequest) returns (UnbridgeMediaResponse);
  rpc UpdateSessionRemote(UpdateSessionRemoteRequest) returns (UpdateSessionRemoteResponse);
  rpc Health(HealthRequest) returns (HealthResponse);
}
```

### CreateSession

Allocates RTP ports and generates an SDP answer.

**Request:**
```protobuf
message CreateSessionRequest {
  string call_id = 1;
  string remote_addr = 2;
  int32 remote_port = 3;
  repeated string offered_codecs = 4;
}
```

**Response:**
```protobuf
message CreateSessionResponse {
  string session_id = 1;
  string local_addr = 2;
  int32 local_port = 3;
  string selected_codec = 4;
  string sdp_body = 5;
}
```

### DestroySession

Releases all resources associated with a session.

**Request:**
```protobuf
message DestroySessionRequest {
  string session_id = 1;
}
```

**Response:**
```protobuf
message DestroySessionResponse {
  bool success = 1;
}
```

### PlayAudio

Streams an audio file to the remote endpoint. Returns a stream of events.

**Request:**
```protobuf
message PlayAudioRequest {
  string session_id = 1;
  string file_path = 2;
  bool loop = 3;
}
```

**Response (stream):**
```protobuf
message PlaybackEvent {
  PlaybackEventType type = 1;
  string message = 2;
  int64 position_ms = 3;
  int64 duration_ms = 4;
}

enum PlaybackEventType {
  STARTED = 0;
  PROGRESS = 1;
  COMPLETED = 2;
  ERROR = 3;
  STOPPED = 4;
}
```

### StopAudio

Stops any currently playing audio.

**Request:**
```protobuf
message StopAudioRequest {
  string session_id = 1;
}
```

**Response:**
```protobuf
message StopAudioResponse {
  bool success = 1;
}
```

### BridgeMedia

Connects two sessions for bidirectional RTP relay.

**Request:**
```protobuf
message BridgeMediaRequest {
  string session_a_id = 1;
  string session_b_id = 2;
}
```

**Response:**
```protobuf
message BridgeMediaResponse {
  string bridge_id = 1;
  bool success = 2;
}
```

### UnbridgeMedia

Stops the RTP relay between two sessions.

**Request:**
```protobuf
message UnbridgeMediaRequest {
  string bridge_id = 1;
}
```

**Response:**
```protobuf
message UnbridgeMediaResponse {
  bool success = 1;
}
```

### UpdateSessionRemote

Updates the remote endpoint for a session (e.g., after receiving B-leg SDP).

**Request:**
```protobuf
message UpdateSessionRemoteRequest {
  string session_id = 1;
  string remote_addr = 2;
  int32 remote_port = 3;
}
```

**Response:**
```protobuf
message UpdateSessionRemoteResponse {
  bool success = 1;
}
```

### Health

Health check for the RTP Manager.

**Request:**
```protobuf
message HealthRequest {}
```

**Response:**
```protobuf
message HealthResponse {
  bool healthy = 1;
  int32 active_sessions = 2;
  int32 available_ports = 3;
}
```

## Regenerating gRPC Code

When modifying `api/proto/rtpmanager/v1/rtpmanager.proto`:

```bash
# Using Makefile
make proto

# Or manually
protoc --go_out=. --go-grpc_out=. api/proto/rtpmanager/v1/rtpmanager.proto
```

Generated files are placed in `pkg/rtpmanager/v1/`:
- `rtpmanager.pb.go` - Message types
- `rtpmanager_grpc.pb.go` - Client and server interfaces

## Error Handling

### REST API Errors

All REST endpoints return JSON error responses:

```json
{
  "error": "session not found",
  "code": "NOT_FOUND"
}
```

HTTP status codes:
- `400 Bad Request` - Invalid request format
- `404 Not Found` - Resource not found
- `500 Internal Server Error` - Server error
- `503 Service Unavailable` - Service unhealthy

### gRPC Errors

gRPC methods return standard gRPC status codes:
- `OK` - Success
- `NOT_FOUND` - Session/resource not found
- `INVALID_ARGUMENT` - Invalid request parameters
- `RESOURCE_EXHAUSTED` - No available ports
- `INTERNAL` - Internal error
- `UNAVAILABLE` - Service unavailable

## Related Documents

- [Architecture](ARCHITECTURE.md) - System design
- [Call Flows](CALL_FLOWS.md) - How API calls fit into flows
- [Configuration](CONFIGURATION.md) - Port and address configuration

---

*Last updated: January 2026*
