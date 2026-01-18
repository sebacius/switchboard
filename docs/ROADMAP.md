# Roadmap

This roadmap outlines the major capabilities and milestones planned for Switchboard.

> **Note**: This is a learning project. These are aspirations, not commitments. Priorities will shift based on what proves interesting or useful.

## Current Status

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

**What Might Be Completely Wrong**
- The entire B2BUA implementation
- SDP manipulation
- RTP timing and jitter handling
- Basically anything that has not been tested with real traffic

---

## Phase 1: Foundation

Core stability and basic functionality.

### Configuration and Lifecycle
- [ ] Configuration file support (YAML/JSON)
- [ ] Hot reload for non-critical settings (routing, logging)
- [ ] Graceful shutdown with call draining
- [ ] Readiness and liveness probes

### Logging and Observability
- [ ] Structured logging with call/dialog correlation
- [ ] Request tracing across services
- [ ] Basic metrics (calls per second, active sessions)
- [ ] Health check improvements

### Core SIP Flows
- [ ] Complete B2BUA call flows (INVITE, re-INVITE, UPDATE, BYE, CANCEL)
- [ ] Early media support (183, PRACK where applicable)
- [ ] Deterministic transaction and dialog state handling
- [ ] Proper error responses for edge cases

### Media Control
- [ ] Stable gRPC control for RTP managers (allocate, release, stats, health)
- [ ] Deterministic media cleanup on call end
- [ ] Session state synchronization

---

## Phase 2: Routing and Call Logic

Advanced call handling capabilities.

### Rule-Based Routing
- [ ] Match on source, destination, domain, headers
- [ ] Time-based routing
- [ ] Tag-based routing
- [ ] Reusable templates and macros

### Routing Actions
- [ ] Route to single target
- [ ] Fork to multiple targets (parallel dial / ring groups)
- [ ] Failover chains
- [ ] Reject with cause
- [ ] Header rewrite

### Number Handling
- [ ] E.164 normalization helpers
- [ ] Weighted routing and priority chains (LCR-style)
- [ ] From/To/PAI/RPID header manipulation

### Topology
- [ ] Optional topology hiding
- [ ] Via header management

---

## Phase 3: Security

Production-grade security features.

### Transport Security
- [ ] SIP over TLS (certificate reload, SNI)
- [ ] Optional mutual TLS for trusted peers
- [ ] SDES-SRTP support
- [ ] DTLS-SRTP for WebRTC compatibility

### Authentication
- [ ] Digest authentication for REGISTER
- [ ] Digest authentication for inbound INVITE
- [ ] IP-based ACLs
- [ ] Rate limits and basic anti-flood protections

### Protection
- [ ] Static and dynamic banning hooks
- [ ] Malformed message detection
- [ ] Resource exhaustion prevention

---

## Phase 4: Registration

Robust registration handling.

### Registration Store
- [ ] In-memory store (current)
- [ ] Redis backend option
- [ ] Binding expiration and cleanup

### NAT Handling
- [ ] NAT-aware Contact handling
- [ ] Contact ranking for multiple registrations
- [ ] Multi-contact per AOR routing

### Advanced Registration
- [ ] Keepalive strategies
- [ ] Outbound (RFC 5626) groundwork
- [ ] Registration lifecycle events for external systems

---

## Phase 5: APIs and Eventing

External integration capabilities.

### REST APIs
- [x] Health and stats endpoints
- [x] Active calls and sessions
- [ ] Call control primitives (hangup, transfer)
- [ ] Routing configuration API
- [ ] Live call inspection

### Events
- [ ] Webhooks for call lifecycle events
- [ ] SSE for live updates
- [ ] Versioned event schemas
- [ ] NATS integration for distributed events

---

## Phase 6: WebSockets and AI Hooks

Real-time integration capabilities.

### WebSocket Interface
- [ ] WebSocket connection for external controllers
- [ ] Subscription to call events
- [ ] Real-time call control actions

### AI Integration
- [ ] RTP tap/fork for recording or AI processing
- [ ] Audio stream extraction
- [ ] DTMF injection

### WebRTC
- [ ] Future WebRTC gateway considerations

---

## Phase 7: Presence and Integration

Advanced telephony features.

### SIP Presence
- [ ] SUBSCRIBE / NOTIFY handling
- [ ] BLF and dialog event packages

### External Integration
- [ ] Bridging presence to external systems
- [ ] Directory and contact integrations
- [ ] CRM webhooks

---

## Phase 8: Media Capabilities

Advanced media features.

### Recording
- [ ] Call recording (on-demand)
- [ ] Policy-driven recording
- [ ] Recording storage integration

### Transcoding
- [ ] Media transcoding (only when required)
- [ ] Explicit codec negotiation policies
- [ ] Resource limits at media layer

### DTMF
- [x] RFC 2833 DTMF detection
- [x] DTMF generation
- [ ] SIP INFO DTMF
- [ ] In-band DTMF detection

### Audio
- [ ] Tone generation
- [ ] Basic announcements
- [ ] Music on hold

---

## Phase 9: Observability and Operations

Production operations support.

### Metrics
- [ ] CPS, ASR, ACD, setup time
- [ ] SIP error rates by type
- [ ] RTP quality metrics (jitter, packet loss, bitrate)
- [ ] Prometheus export

### Tracing
- [ ] Cross-component tracing (signaling <-> media)
- [ ] Per-call debug artifacts
- [ ] Safe redaction for sensitive data

### Testing
- [ ] Load testing tooling
- [ ] Scenario replay
- [ ] Regression test suite

### Multi-Tenancy
- [ ] Optional tenant isolation
- [ ] Per-tenant quotas
- [ ] Tenant-specific routing

---

## Phase 10: High Availability and Scaling

Enterprise-grade deployment.

### State Management
- [ ] Stateless signaling with externalized state
- [ ] Redis/etcd for shared state
- [ ] Session handoff between servers

### Scaling
- [ ] Media node autoscaling strategies
- [ ] Geographic distribution (edge media, central control)
- [ ] Multiple signaling server support

### Upgrades
- [ ] Rolling upgrades
- [ ] Best-effort call preservation during updates
- [ ] Database migration tooling

---

## Contributing

This roadmap is intentionally ambitious. If any of these areas interest you, contributions are welcome. See [DEVELOPMENT.md](DEVELOPMENT.md) for how to get started.

Priorities will shift based on:
- What proves useful in practice
- What contributors are interested in
- What breaks most often

---

*Last updated: January 2026*
