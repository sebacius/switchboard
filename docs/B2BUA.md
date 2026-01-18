# B2BUA Design Document

## Overview

This package implements the core B2BUA (Back-to-Back User Agent) primitives for the Switchboard VoIP platform. It provides three fundamental operations:

1. **Lookup** - Resolve dial targets to SIP URIs
2. **Originate** - Create outbound call legs
3. **Bridge** - Connect two call legs for media exchange

## Architecture

```
+------------------------------------------------------------------+
|                         CallService                               |
|  +------------+  +------------+  +------------+  +------------+  |
|  |  Lookup    |  |  Dial      |  |  Bridge    |  | DialParallel| |
|  +-----+------+  +-----+------+  +-----+------+  +-----+------+  |
+---------+---------------+---------------+---------------+---------+
          |               |               |               |
          v               v               v               v
     +---------+    +-----------+   +---------+    (Future)
     |Resolver |    |Originator |   | Bridge  |
     +---------+    +-----------+   +---------+
          |               |               |
          v               v               v
     +---------+    +-----------+   +---------+
     |Location |    |   Leg     |   |   Leg   |
     | Store   |    +-----------+   | A <-> B |
     +---------+                    +---------+
```

## Core Types

### Leg

A `Leg` represents one side of a call. It wraps:
- SIP dialog state (from `dialog.Dialog`)
- RTP session (via session ID)
- Lifecycle management (state transitions, hangup)

**States:**
```
Created -> Ringing -> Answered -> Destroyed
             |
             v
         EarlyMedia -> Answered -> Destroyed
             |
             v
           Failed
```

**Two types of legs:**
- **Inbound (A-leg)**: Created from incoming INVITE via `AdoptInboundLeg()`
- **Outbound (B-leg)**: Created by dialing via `CreateOutboundLeg()` or `Dial()`

### Bridge

A `Bridge` connects two answered legs for bidirectional media exchange.

**States:**
```
Created -> Active -> Terminated
              |
              v
            Held -> Active
```

**Key behaviors:**
- Both legs must be in `Answered` state before `Start()`
- Monitors both legs; terminates when either hangs up
- Optionally hangs up remaining leg (`autoHangup`)

### Resolver

Resolvers convert dial targets to SIP URIs:

| Prefix | Resolver | Example |
|--------|----------|---------|
| `user/` | UserResolver | `user/1001` -> query LocationStore |
| `gateway/` | GatewayResolver | `gateway/carrier` -> gateway config |
| `sip:` | DirectResolver | `sip:user@host` -> passthrough |

The `ChainResolver` combines multiple resolvers, trying each in order.

## Call Flow

### Single Target Dial

```
Dialplan                    CallService                  Originator
   |                            |                            |
   |-- Dial("user/1001") ------>|                            |
   |                            |-- Lookup() --------------->|
   |                            |<-- LookupResult -----------|
   |                            |                            |
   |                            |-- Originate() ------------>|
   |                            |   (builds INVITE)          |
   |                            |   (sends INVITE)           |
   |                            |<-- 180 Ringing ------------|
   |                            |<-- 200 OK -----------------|
   |                            |   (sends ACK)              |
   |<-- Leg (Answered) ---------|                            |
   |                            |                            |
   |-- CreateBridge() --------->|                            |
   |-- Start() ---------------->|                            |
   |                            |                            |
   |   ... media flows ...      |                            |
   |                            |                            |
   |-- WaitForTermination() --->|                            |
   |   ... blocking ...         |                            |
   |                            |                            |
   |<-- BridgeInfo -------------|  (when either leg hangs up)|
```

## Ring Group Extensibility

The design supports ring groups (parallel dial) through:

### 1. LookupResult with Multiple Contacts

A ring group can be represented as a `LookupResult` with multiple contacts:

```go
ringGroup := &LookupResult{
    Type:     LookupResultTypeUser,
    Original: "sales-team",
    Contacts: []ResolvedContact{
        {URI: "sip:alice@192.168.1.10", Priority: 1.0},
        {URI: "sip:bob@192.168.1.11", Priority: 1.0},
        {URI: "sip:carol@192.168.1.12", Priority: 1.0},
    },
}
```

### 2. DialParallel Implementation (Future)

The `CallService.DialParallel()` method would:

```go
func (s *service) DialParallel(ctx context.Context, targets []*LookupResult,
    timeout time.Duration, opts ...LegOption) (Leg, error) {

    dialCtx, cancel := context.WithTimeout(ctx, timeout)
    defer cancel()

    // Channel for first answer
    winnerCh := make(chan Leg, 1)

    // Create all outbound legs in parallel
    var legs []Leg
    var wg sync.WaitGroup

    for _, target := range targets {
        for _, contact := range target.Contacts {
            wg.Add(1)
            go func(uri string) {
                defer wg.Done()

                leg, err := s.CreateOutboundLeg(dialCtx, &LookupResult{
                    Type:     target.Type,
                    Original: target.Original,
                    Contacts: []ResolvedContact{{URI: uri}},
                })
                if err != nil {
                    return
                }

                legs = append(legs, leg)

                // Wait for answer
                if err := leg.WaitForState(dialCtx, LegStateAnswered); err == nil {
                    select {
                    case winnerCh <- leg:
                    default: // Another leg won
                    }
                }
            }(contact.URI)
        }
    }

    // Wait for first answer or timeout
    select {
    case winner := <-winnerCh:
        // Cancel all other legs
        for _, leg := range legs {
            if leg.ID() != winner.ID() {
                leg.Hangup(context.Background(), TerminationCauseCancel)
            }
        }
        return winner, nil

    case <-dialCtx.Done():
        // Timeout - hangup all
        for _, leg := range legs {
            leg.Hangup(context.Background(), TerminationCauseTimeout)
        }
        return nil, ErrDialTimeout
    }
}
```

### 3. Key Design Decisions for Ring Groups

1. **Independent Call-IDs**: Each parallel INVITE gets its own Call-ID (B2BUA, not forking)
2. **Winner takes all**: First 200 OK wins, CANCEL sent to others
3. **Resource cleanup**: Media sessions created speculatively are cleaned up on cancel
4. **Early media**: Only first 183 with SDP connects to A-leg

## Integration Points

### Dialplan Session

The `CallSession.Dial()` method uses `CallService`:

```go
func (s *sessionImpl) Dial(ctx context.Context, target string, timeout time.Duration) error {
    // Create or get call service
    callSvc := s.getCallService()

    // Adopt A-leg from existing dialog
    legA, err := callSvc.AdoptInboundLeg(s.dialog, s.sessionID)
    if err != nil {
        return err
    }

    // Dial and bridge
    bridgeInfo, err := callSvc.DialAndBridge(ctx, legA, target, timeout)
    if err != nil {
        return err
    }

    // Record result
    s.bridgeInfo = bridgeInfo
    return nil
}
```

### RTP Manager Integration

The Bridge coordinates RTP via the MediaClient interface:

```go
// MediaClient interface for RTP Manager communication
type MediaClient interface {
    // Existing methods...

    // BridgeMedia connects two sessions for RTP relay
    BridgeMedia(ctx context.Context, sessionA, sessionB string) error

    // UnbridgeMedia stops RTP relay between sessions
    UnbridgeMedia(ctx context.Context, bridgeID string) error
}
```

## File Structure

```
internal/signaling/b2bua/
+-- state.go           # LegState, BridgeState, TerminationCause enums
+-- errors.go          # ErrTargetNotFound, DialError, etc.
+-- lookup.go          # LookupResult, Resolver, GatewayConfig
+-- leg.go             # Leg interface and implementation
+-- bridge.go          # Bridge interface and implementation
+-- service.go         # CallService interface
+-- call_service.go    # CallService implementation
+-- originator.go      # Originator for outbound INVITEs
+-- user_resolver.go   # UserResolver implementation
+-- direct_resolver.go # DirectResolver implementation
+-- chain_resolver.go  # ChainResolver implementation
```

## Testing Strategy

1. **Unit tests**: Mock `MediaClient` and `Resolver` to test Originator logic
2. **Integration tests**: Use in-memory location store and local transport
3. **E2E tests**: Full SIP flow with actual sipgo against test endpoint

## Future Enhancements

1. **Ring groups**: Implement `DialParallel()`
2. **Call queues**: Add queue manager with agent tracking
3. **Transfers**: Implement REFER handling
4. **Conference**: Multi-party bridge with mixing
5. **Recording**: Tap into bridge for media capture
6. **Direct media**: Re-INVITE to bypass softswitch for established calls

## Related Documents

- [Architecture](ARCHITECTURE.md) - System design
- [Call Flows](CALL_FLOWS.md) - Sequence diagrams
- [Code Map](CODE_MAP.md) - Package descriptions

---

*Last updated: January 2026*
