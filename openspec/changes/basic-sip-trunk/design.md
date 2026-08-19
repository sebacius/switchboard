## Context

Switchboard is a B2BUA + registrar: today every call is registered-user-to-registered-user, resolved
through the global location store (`location/store.go`), with target resolvers for `sip:`/`sips:`/
`user/` and a `CallService` B2BUA. There is no notion of a trunk peer, no "external" destination, and
no DID table. The `llm-pbx-supervisor` change needs all three to classify direction and deliver
inbound DID calls. The deliberate stance (decided in exploration): build a *basic* trunk in
Switchboard now, with seams so Kamailio can take over the heavy SIP edge later — Switchboard should not
try to be a full SIP router when better tools exist for that.

## Goals / Non-Goals

**Goals:**
- Recognize inbound INVITEs that originate from a configured SIP trunk peer.
- Send outbound external calls to a trunk peer, carrying tenant identity.
- Map inbound DIDs to tenants; reject unmapped DIDs.
- Reject INVITEs from sources that are neither registered users nor trunk peers.
- Leave a clean interface so a future change can delegate to Kamailio.

**Non-Goals:**
- Provider registration/authentication flows beyond a static IP/host peer (can be added later).
- Full SIP routing logic, least-cost routing, failover — that is Kamailio's job in the target topology.
- Any LLM/supervisor behavior — that lives entirely in `llm-pbx-supervisor`.
- Media bypass or RTP changes.

## Decisions

**1. Static trunk peers, configured per deployment.** A trunk peer is identified by source host/IP and
a direction role (`inbound` | `outbound` | `both`). Inbound matching is by source address; outbound
selection picks a peer whose role permits egress. *Alternative:* dynamic registration to a provider —
deferred; static IP trunks cover the common case and are simpler to secure.

**2. Source classification happens at INVITE ingress.** `routing/invite.go` classifies the INVITE
source into one of three buckets before anything else:

```
   INVITE source is a registered directory user  → internal-origin
   INVITE source matches a trunk peer            → trunk-origin (inbound)
   neither                                        → REJECT (403/603)
```

This is the predicate `llm-pbx-supervisor`'s direction classifier consumes. *Alternative:* trust any
source and let the supervisor decide — rejected; unauthenticated ingress is the toll-fraud front door.

**3. DID→tenant table in `routes.json`, no default.** Inbound trunk calls resolve tenant by matching
the dialed DID (Request-URI / To user) against the table. An unmapped DID is **rejected**, not routed
to a fallback — there is no default tenant (a firm decision from the supervisor exploration). *Format:*

```json
{ "dids": { "+15551234567": "acme_support", "+15559876543": "globex" } }
```

**4. Outbound carries tenant identity to the peer.** Egress INVITEs set From-domain to the tenant and
add a tenant header, so a downstream proxy (or the provider) can attribute and route the call. This is
exactly the shape a future Kamailio next-hop expects. *Alternative:* anonymous egress — rejected;
loses tenant attribution and the Kamailio seam.

**5. Trunk behind an interface (the Kamailio seam).** Ingress recognition and egress are expressed
through a small interface so a `kamailio-offload` change can swap the static implementation for a
Kamailio-proxy delegate (egress = forward to proxy with tenant header; ingress tenant = read a header
Kamailio set) without touching `routing/` or the supervisor.

## Risks / Trade-offs

- **Toll-fraud ingress** — accepting INVITEs from anywhere is the classic fraud entry → mitigated by
  decision #2 (reject non-peer, non-user sources) and by `llm-pbx-supervisor`'s outbound COS.
- **Spoofed source address on a static IP trunk** — a static-IP peer trusts the source IP; if the
  network path is untrusted, spoofing is possible → note for deployment (TLS/IP ACLs at the edge);
  the Kamailio-offload future hardens this.
- **DID table drift** — unmapped DID rejects rather than guesses; a missing mapping is a hard failure,
  surfaced as a rejected call rather than a silently mis-tenanted one (intentional, per "no default").

## Migration Plan

1. Land on the same branch sequence ahead of `llm-pbx-supervisor` (this change is its prerequisite).
2. Add the `trunk` package + config + DID loader; wire source classification into `routing/invite.go`.
3. Verify with a SIP test (no Ollama needed): inbound DID delivered to the right tenant context;
   outbound INVITE reaches the peer with tenant identity; unknown-source INVITE rejected; unmapped DID
   rejected.
4. Rollback: `git revert`. No data migrations.

## Open Questions

- Whether outbound peer selection needs per-tenant peer assignment now, or one shared egress peer is
  enough for the first cut (lean: one shared egress peer, per-tenant selection later).
- Exact tenant header name shared with the future Kamailio contract (resolve when that change lands).
