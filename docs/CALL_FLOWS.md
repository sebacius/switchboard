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

## AI Agent Call Flow

The `ai_agent` dialplan action enables LLM-driven voice conversations. The signaling server owns the conversation loop and context, while the RTP Manager handles media I/O (TTS and ASR) by delegating to external AI services.

### Conversational Mode

Multi-turn dialogue: greeting, then a listen-think-speak loop until the LLM emits an action or max turns is reached.

```
Caller              Signaling              RTP Manager         Ollama    Piper    Whisper
   |                    |                       |                 |        |         |
   |-- INVITE --------->|                       |                 |        |         |
   |                    |-- CreateSession ----->|                 |        |         |
   |                    |<-- session_id + SDP --|                 |        |         |
   |<-- 200 OK ---------|                       |                 |        |         |
   |-- ACK ------------>|                       |                 |        |         |
   |                    |                       |                 |        |         |
   |                    |   [load system prompt: settings.md + tenant.md]  |         |
   |                    |                       |                 |        |         |
   |                    |   --- greeting TTS --------------------------->|         |
   |                    |                       |<-- audio -------|--------|         |
   |<---------------------------------[RTP]----|                 |        |         |
   |                    |                       |                 |        |         |
   |                    |   [conversation loop begins]            |        |         |
   |                    |                       |                 |        |         |
   |----------------------------[RTP]---------->|   (caller speaks)        |         |
   |                    |                       |-- ASR ---------|---------|-------->|
   |                    |                       |<-- transcript --|---------|---------|
   |                    |<-- user text ---------|                 |        |         |
   |                    |                       |                 |        |         |
   |                    |-- LLM request --------|---------------->|        |         |
   |                    |<-- LLM response ------|-----------------|        |         |
   |                    |                       |                 |        |         |
   |                    |   [parse response: spoken text + action?]        |         |
   |                    |                       |                 |        |         |
   |                    |-- speak TTS --------->|-----------------|------->|         |
   |                    |                       |<-- audio -------|--------|         |
   |<---------------------------------[RTP]----|                 |        |         |
   |                    |                       |                 |        |         |
   |                    |   [repeat until ACTION or max turns]    |        |         |
   |                    |                       |                 |        |         |
   |                    |   [ACTION: transfer extension 100]      |        |         |
   |                    |-- Dial user/100 ----->|                 |        |         |
   |                    |   (B2BUA flow)        |                 |        |         |
```

### Conversational Mode Details

1. **Call setup** -- Standard INVITE/200 OK/ACK, same as any dialplan action
2. **System prompt assembly** -- Settings (action contract from `settings.md`, cached at startup) combined with tenant config (loaded per-call from `resources/tenants/<config>.md`)
3. **Greeting** -- Spoken via TTS before the loop starts. Configurable in dialplan params, defaults to "Hello, how can I help you today?"
4. **Listen** -- RTP Manager captures caller audio, sends to Whisper for ASR, returns transcript to signaling
5. **Think** -- Signaling sends transcript to Ollama with full conversation history, receives response
6. **Speak** -- Response text sent to RTP Manager, which calls Piper for TTS synthesis, then streams audio as RTP
7. **Action** -- If the LLM response contains an ACTION block, signaling parses and executes it (transfer, hangup, park)
8. **Loop termination** -- Conversation ends when an action executes successfully, max turns is reached, or the caller hangs up

### Routing Mode

Single-shot decision: greeting, then one LLM call to determine what action to take. No caller input is captured.

```
Caller              Signaling              RTP Manager         Ollama    Piper
   |                    |                       |                 |        |
   |-- INVITE --------->|                       |                 |        |
   |                    |-- CreateSession ----->|                 |        |
   |                    |<-- session_id + SDP --|                 |        |
   |<-- 200 OK ---------|                       |                 |        |
   |-- ACK ------------>|                       |                 |        |
   |                    |                       |                 |        |
   |                    |   [load system prompt: settings.md + tenant.md]  |
   |                    |                       |                 |        |
   |                    |   --- greeting TTS --------------------------->|
   |                    |                       |<-- audio -------|--------|
   |<---------------------------------[RTP]----|                 |        |
   |                    |                       |                 |        |
   |                    |-- LLM routing req ----|---------------->|        |
   |                    |<-- LLM response ------|-----------------|        |
   |                    |                       |                 |        |
   |                    |   [parse response: spoken text + action]|        |
   |                    |                       |                 |        |
   |                    |-- speak TTS --------->|-----------------|------->|
   |                    |                       |<-- audio -------|--------|
   |<---------------------------------[RTP]----|                 |        |
   |                    |                       |                 |        |
   |                    |   [execute ACTION]    |                 |        |
   |                    |                       |                 |        |
   |<-- BYE ------------|   (if hangup)         |                 |        |
   |-- 200 OK --------->|                       |                 |        |
   |                    |-- DestroySession ---->|                 |        |
```

### Routing Mode Details

1. **Call setup** -- Identical to conversational mode
2. **Greeting** -- Spoken via TTS. Defaults to "Thank you for calling." in routing mode
3. **LLM decision** -- Single prompt sent to Ollama: "The caller has connected and heard the greeting. Based on your instructions, respond with what to say and what action to take."
4. **Response** -- LLM returns spoken text and an ACTION block based on tenant instructions
5. **Execution** -- Action is validated and executed. If no action is present, defaults to hangup
6. **No ASR** -- Whisper is not involved in routing mode since there is no caller input

### AI Agent Error Handling

| Scenario | Behavior |
|----------|----------|
| LLM unavailable at call start | Action returns error, call terminated |
| LLM error mid-conversation | "I'm having trouble thinking right now" spoken, turn continues |
| ASR returns empty transcript | "I didn't catch that" spoken, turn retried |
| Transfer fails | Caller informed, conversation continues (conversational mode only) |
| Invalid action from LLM | Action ignored, conversation continues |
| Max turns reached | Goodbye message spoken, call ends naturally |

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

*Last updated: March 2026*
