## Why

The LLM supervisor (`llm-pbx-supervisor`) needs to classify every call's direction — `internal`
(a registered directory user), `inbound` (from a SIP provider/trunk), or `outbound` (a directory
user dialing an external destination) — and route inbound DID calls to the right tenant. Today
Switchboard has no trunk concept at all: it only knows registered-user-to-registered-user B2BUA via
the location store. Without a trunk edge there is no "external" to classify against and no inbound DID
delivery. This change adds a **basic, self-contained SIP trunk** plus a **DID→tenant table**, built so
a later change can offload the heavy SIP edge to Kamailio without reworking the application layer.

## What Changes

- Add an `internal/signaling/trunk` package modeling one or more **static SIP trunk peers**, each
  configured as `inbound`, `outbound`, or `both`.
- **Inbound**: INVITEs whose source matches a configured trunk peer are recognized as trunk-origin
  (not a directory user). INVITEs from unknown sources that are neither registered users nor trunk
  peers are **rejected** (toll-fraud ingress protection).
- **Outbound**: a request to dial an external destination is sent to the appropriate trunk peer,
  carrying the tenant identity (From-domain = tenant, plus a tenant header).
- Add a **DID→tenant table** (`resources/config/routes.json`): inbound DIDs map to tenant names. An
  inbound DID with no mapping is **rejected** — there is no default tenant.
- Keep the trunk behind an interface so a future `kamailio-offload` change can delegate egress/ingress
  to a Kamailio proxy (send INVITE to the proxy with a tenant header; let Kamailio supply inbound
  tenant tagging) without changing callers.

## Capabilities

### New Capabilities
- `sip-trunk`: configuration and behavior of static SIP trunk peers — inbound recognition, outbound
  egress with tenant identity, and rejection of traffic from unknown sources.
- `did-routing`: the DID→tenant mapping table used to resolve the tenant for an inbound trunk call,
  including hard rejection of unmapped DIDs (no default tenant).

### Modified Capabilities
<!-- None — these are new; openspec/specs/ has no existing trunk capability. -->

## Impact

- **New code**: `internal/signaling/trunk/` (peer config, inbound source matching, outbound egress),
  DID→tenant loader (`resources/config/routes.json`).
- **Touched**: `routing/invite.go` (classify INVITE source: directory user vs trunk vs unknown→reject),
  `config/config.go` + `cmd/signaling/main.go` (trunk peer + routes config), `app/app.go` (wire trunk).
- **Dependency direction**: `llm-pbx-supervisor` depends on this change (its `call-routing` direction
  classification consumes trunk recognition; its inbound path consumes `did-routing`).
- **Not affected**: RTP Manager, media contract. No LLM involvement — this change is pure SIP plumbing
  and is independently testable with a SIP test (inbound DID delivery, outbound egress) without Ollama.
- **Future**: a `kamailio-offload` change can replace the static trunk with a Kamailio next-hop and
  header-based tenant tagging; the interface seam is introduced here.
