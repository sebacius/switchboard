# sip-trunk Specification

## Purpose

Static SIP trunk peers: configuration and direction roles, INVITE ingress source classification (directory user vs trunk peer vs reject), outbound egress carrying tenant identity, all behind an interface so a future Kamailio-backed implementation can substitute without changing routing callers.

## Requirements

### Requirement: Configurable static SIP trunk peers

The system SHALL support one or more configured SIP trunk peers, each identified by a source host/IP
and a direction role of `inbound`, `outbound`, or `both`.

#### Scenario: Trunk peer is configured

- **WHEN** the signaling server starts with a trunk peer defined in config
- **THEN** that peer is registered with its host/IP and direction role

#### Scenario: Peer with no egress role is not used for outbound

- **WHEN** an outbound call needs egress and a peer is role `inbound`
- **THEN** that peer is not selected for the outbound INVITE

### Requirement: Inbound INVITE source classification

On INVITE ingress the system SHALL classify the source as a registered directory user, a configured
trunk peer, or neither. An INVITE whose source is neither a registered user nor a configured trunk
peer SHALL be rejected.

#### Scenario: INVITE from a trunk peer is recognized as inbound

- **WHEN** an INVITE arrives whose source matches a configured trunk peer
- **THEN** it is classified as trunk-origin (inbound)

#### Scenario: INVITE from an unknown source is rejected

- **WHEN** an INVITE arrives from a source that is neither a registered user nor a trunk peer
- **THEN** the system rejects it (e.g. 403/603) and does not route it

### Requirement: Outbound egress carries tenant identity

When sending an outbound call to a trunk peer, the system SHALL set the From-domain to the tenant and
include a tenant header so the downstream peer can attribute and route the call.

#### Scenario: Outbound INVITE is tagged with tenant

- **WHEN** the system sends an external call to a trunk peer on behalf of tenant `acme`
- **THEN** the egress INVITE's From-domain is the tenant and a tenant header identifies `acme`

### Requirement: Trunk operations are behind an interface

The trunk's ingress recognition and egress SHALL be expressed through an interface so a future
implementation can delegate to an external proxy (Kamailio) without changing routing callers.

#### Scenario: Static implementation is swappable

- **WHEN** the trunk interface is implemented by the static-peer backend
- **THEN** routing code depends only on the interface, allowing a future Kamailio-backed implementation
  to substitute without changes to `routing/`
