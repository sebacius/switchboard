# Call Flows

This document provides detailed sequence diagrams for the main call flows in Switchboard.

## SIP Registration

A SIP client registers with the Signaling Server to make itself reachable.

```
Client                  Signaling                Location
   |                        |                        |
   |-- REGISTER ----------->|                        |
   |                        |-- Store binding ------>|
   |                        |<-- OK -----------------|
   |<-- 200 OK -------------|                        |
   |                        |                        |
   |   (binding expires)    |                        |
   |                        |                        |
   |-- REGISTER ----------->|   (refresh)            |
   |                        |-- Update binding ----->|
   |                        |<-- OK -----------------|
   |<-- 200 OK -------------|                        |
```

### Registration Details

1. Client sends REGISTER with Contact header
2. Signaling validates request, extracts contacts and expires
3. Binding stored in Location service with TTL
4. 200 OK returned with current bindings
5. Client must refresh before expiration

### Wildcard Unregister

```
Client                  Signaling                Location
   |                        |                        |
   |-- REGISTER ----------->|                        |
   |   Contact: *           |                        |
   |   Expires: 0           |                        |
   |                        |-- Remove all --------->|
   |                        |<-- OK -----------------|
   |<-- 200 OK -------------|                        |
```

## Simple Call (IVR Playback)

An inbound call that plays an audio file and hangs up.

```
Client                  Signaling               RTP Manager
   |                        |                        |
   |-- INVITE (SDP) ------->|                        |
   |                        |-- CreateSession ------>|
   |                        |<-- session_id + SDP ---|
   |<-- 100 Trying ---------|                        |
   |<-- 183 Session Progress|                        |
   |   (with SDP)           |                        |
   |<-- 200 OK -------------|                        |
   |-- ACK ---------------->|                        |
   |                        |                        |
   |                        |   [execute dialplan]   |
   |                        |                        |
   |                        |-- PlayAudio ---------->|
   |<----------------------------[RTP packets]-------|
   |                        |                        |
   |                        |<-- COMPLETED ----------|
   |                        |                        |
   |<-- BYE ----------------|                        |
   |-- 200 OK ------------->|                        |
   |                        |-- DestroySession ----->|
```

### Call Setup Details

1. **INVITE arrives** - Dialog created in Initial state
2. **100 Trying** - Sent immediately
3. **CreateSession** - RTP Manager allocates ports, returns SDP
4. **183 Session Progress** - Early media possible (optional)
5. **200 OK** - Dialog transitions to WaitingACK
6. **ACK** - Dialog confirmed, dialplan execution starts

### Dialplan Execution

After ACK, the dialplan executes matched route actions:
- `play_audio` - Streams file via RTP Manager
- `hangup` - Terminates call

### Termination

1. Dialplan completes or action triggers hangup
2. Signaling sends BYE to client
3. Client responds 200 OK
4. DestroySession releases RTP resources

## Bridged Call (B2BUA)

A call bridged between two endpoints using the B2BUA.

```
Caller              Signaling              RTP Manager           Callee
   |                    |                       |                   |
   |-- INVITE --------->|                       |                   |
   |                    |-- CreateSession A --->|                   |
   |                    |<-- SDP A -------------|                   |
   |<-- 100 Trying -----|                       |                   |
   |<-- 183 SDP --------|                       |                   |
   |<-- 200 OK ---------|                       |                   |
   |-- ACK ------------>|                       |                   |
   |                    |                       |                   |
   |                    |   [dialplan: dial]    |                   |
   |                    |                       |                   |
   |                    |   [lookup target]     |                   |
   |                    |-- CreateSession B --->|                   |
   |                    |<-- SDP B -------------|                   |
   |                    |                       |                   |
   |                    |------- INVITE (SDP B) ------------------>|
   |                    |<----------------------- 180 Ringing -----|
   |                    |<----------------------- 200 OK ----------|
   |                    |------- ACK ----------------------------->|
   |                    |                       |                   |
   |                    |-- BridgeMedia ------->|                   |
   |                    |   (session A, B)      |                   |
   |                    |                       |                   |
   |<=====================================[RTP]=====================>|
   |                    |                       |                   |
   |   [caller hangs up]|                       |                   |
   |-- BYE ------------>|                       |                   |
   |<-- 200 OK ---------|                       |                   |
   |                    |-- UnbridgeMedia ----->|                   |
   |                    |------- BYE ----------------------------->|
   |                    |<----------------------- 200 OK ----------|
   |                    |-- DestroySession A -->|                   |
   |                    |-- DestroySession B -->|                   |
```

### B2BUA Details

1. **A-leg setup** - Inbound INVITE creates session A
2. **Dialplan dial action** - Triggers B2BUA dial
3. **Target resolution** - Lookup via location service or direct URI
4. **B-leg origination** - New INVITE with session B SDP
5. **Bridge creation** - BridgeMedia connects sessions
6. **Media flow** - RTP forwarded between A and B
7. **Termination** - Either party BYE triggers cleanup

### Target Resolution

The dial action supports multiple target formats:

| Format | Example | Resolution |
|--------|---------|------------|
| `user/xxx` | `user/1001` | Location service lookup |
| `sip:uri` | `sip:user@host:port` | Direct SIP URI |

## Call Cancellation

Client cancels call before answer.

```
Client                  Signaling               RTP Manager
   |                        |                        |
   |-- INVITE ------------->|                        |
   |                        |-- CreateSession ------>|
   |                        |<-- session_id + SDP ---|
   |<-- 100 Trying ---------|                        |
   |<-- 183 SDP ------------|                        |
   |                        |                        |
   |-- CANCEL ------------->|                        |
   |<-- 200 OK (CANCEL) ----|                        |
   |<-- 487 Request Term. --|                        |
   |-- ACK (487) ---------->|                        |
   |                        |-- DestroySession ----->|
```

## Remote Hangup

Callee hangs up during established call.

```
Caller              Signaling              RTP Manager           Callee
   |                    |                       |                   |
   |   [call active]    |                       |                   |
   |<====================[bridged RTP]=========================>|
   |                    |                       |                   |
   |                    |<----------------------- BYE -------------|
   |                    |------- 200 OK -------------------------->|
   |                    |                       |                   |
   |                    |-- UnbridgeMedia ----->|                   |
   |<-- BYE ------------|                       |                   |
   |-- 200 OK --------->|                       |                   |
   |                    |-- DestroySession A -->|                   |
   |                    |-- DestroySession B -->|                   |
```

## Dialog State Transitions

```
INVITE received
     |
     v
+-----------+
|  Initial  |
+-----+-----+
      | Send 100 Trying
      v
+-----------+
|   Early   |  <-- Send 183 Session Progress
+-----+-----+
      | Send 200 OK
      v
+-----------+
| WaitingACK|
+-----+-----+
      | ACK received
      v
+-----------+
| Confirmed |  <-- Call is active
+-----+-----+
      | BYE (either side)
      v
+-----------+
|Terminating|
+-----+-----+
      | Cleanup complete
      v
+-----------+
|Terminated |
+-----------+
```

### ACK Timeout

If ACK is not received within 32 seconds (per RFC 3261):

```
Client                  Signaling
   |                        |
   |-- INVITE ------------->|
   |<-- 200 OK -------------|
   |                        |
   |   [no ACK for 32s]     |
   |                        |
   |                        |-- [timeout fires]
   |                        |-- DestroySession
   |                        |-- [dialog terminated]
```

## RTP Manager Session Lifecycle

```
CreateSession(callID, remoteAddr, remotePort, codecs)
       |
       v
+-------------------------------------+
|  Allocate RTP/RTCP ports from pool  |
|  Negotiate codec (PCMU)             |
|  Generate SDP answer                |
+-------------------------------------+
       |
       v
PlayAudio(sessionID, file) or BridgeMedia(sessionA, sessionB)
       |
       v
+-------------------------------------+
|  Read WAV file or relay packets     |
|  Encode to PCMU                     |
|  Stream RTP packets (20ms frames)   |
+-------------------------------------+
       |
       v
DestroySession(sessionID)
       |
       v
+-------------------------------------+
|  Stop playback/relay                |
|  Release ports to pool              |
|  Cleanup resources                  |
+-------------------------------------+
```

## RTP Parameters

| Parameter | Value |
|-----------|-------|
| Sample Rate | 8000 Hz |
| Frame Size | 160 samples (20ms) |
| Codec | PCMU (G.711 u-law) |
| Payload Type | 0 |
| Bitrate | 64 kbit/s |

## Error Scenarios

### No Route Match

```
Client                  Signaling
   |                        |
   |-- INVITE ------------->|
   |                        |   [dialplan match fails]
   |<-- 404 Not Found ------|
```

### RTP Manager Unavailable

```
Client                  Signaling               RTP Manager
   |                        |                        X
   |-- INVITE ------------->|                        |
   |                        |-- CreateSession ------>X
   |                        |   [connection failed]  |
   |<-- 503 Service Unavail-|                        |
```

### Target Not Found

```
Caller              Signaling              Location
   |                    |                       |
   |-- INVITE --------->|                       |
   |<-- 200 OK ---------|                       |
   |-- ACK ------------>|                       |
   |                    |                       |
   |                    |   [dial user/1001]    |
   |                    |-- Lookup 1001 ------->|
   |                    |<-- NOT FOUND ---------|
   |                    |                       |
   |<-- BYE ------------|   [terminate call]    |
   |-- 200 OK --------->|                       |
```

## Related Documents

- [Architecture](ARCHITECTURE.md) - System design
- [B2BUA Design](B2BUA.md) - B2BUA implementation
- [Dialplan](DIALPLAN.md) - Route configuration
- [API Reference](API_REFERENCE.md) - gRPC protocol details

---

*Last updated: January 2026*
