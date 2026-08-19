# SIPp functional tests

Driven by `run-tests.sh` (or `make test-sip TARGET=<ip> [SCENARIO=<name>]`). Requires `sipp` installed
and a running signaling server. `TARGET` is the signaling host IP (SIP on :5060).

```
make test-sip TARGET=192.168.50.181              # all (register, calls, parking, trunk-reject)
make test-sip TARGET=192.168.50.181 SCENARIO=register
make test-sip TARGET=192.168.50.181 SCENARIO=calls
make test-sip TARGET=192.168.50.181 SCENARIO=parking
make test-sip TARGET=192.168.50.181 SCENARIO=trunk-reject
make test-sip TARGET=192.168.50.181 SCENARIO=trunk-did
```

Logs land in `results/<timestamp>/`.

## Ingress gate tests (`basic-sip-trunk`)

The signaling server now classifies every INVITE's source: a registered directory user or a configured
trunk peer is allowed; any other source is rejected. These tests exercise that gate.

- **`trunk-reject`** — an unregistered, non-peer source INVITE expects **403**. Runs with the default
  config (this host is not a configured trunk peer) and is included in `all`. No setup needed.
- **`trunk-did`** — simulates an inbound trunk call: an unmapped DID expects **603**, a mapped DID
  (`+15551234567`, from `routes.json`) is accepted. **Prerequisite:** add this host's IP to
  `resources/config/trunk_peers.json` as a peer and **restart signaling** (config loads at startup),
  otherwise both sub-tests see 403 (unknown source). Not included in `all`.

Note: `calls` assumes the callers are registered first (the `all` flow registers before calling). Run
standalone `calls` without a prior `register` and the gate will reject the unregistered callers with 403.
