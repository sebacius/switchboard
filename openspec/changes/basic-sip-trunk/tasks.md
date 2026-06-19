## 1. Trunk package

- [x] 1.1 Create `internal/signaling/trunk/` with a `Peer` type (host/IP + role `inbound|outbound|both`) and a `Trunk` interface (ingress match + egress send)
- [x] 1.2 Implement the static-peer backend of the `Trunk` interface
- [x] 1.3 Implement inbound source matching: given an INVITE source, return whether it matches a trunk peer
- [x] 1.4 Implement outbound egress: send an INVITE to an egress-capable peer with From-domain = tenant and a tenant header

## 2. DID routing

- [x] 2.1 Add `resources/config/routes.json` with the `{ "dids": { ... } }` shape (no default entry)
- [x] 2.2 Implement a DID→tenant loader and lookup; unmapped DID returns "no tenant" (caller rejects)

## 3. Config & wiring

- [x] 3.1 Add trunk peer config + routes.json path to `config/config.go` (flags + env)
- [x] 3.2 Wire trunk peer config and DID loader in `cmd/signaling/main.go` and `app/app.go`
- [x] 3.3 In `routing/invite.go`, classify INVITE source: registered user vs trunk peer vs neither → reject neither

## 4. Verification

- [x] 4.1 Unit tests: source classification (user / trunk / unknown→reject), DID lookup (mapped / unmapped→reject), egress tenant tagging
- [x] 4.2 `go build ./...` clean, `go mod tidy` no drift
- [x] 4.3 SIP test (no Ollama): ingress gate verified end-to-end with sipp against the real binary — unknown source → 403, trunk + unmapped DID → 603, trunk + mapped DID → accepted with tenant=acme_support. Scenarios: `test/sipp/scenarios/uac_{expect_403,trunk_unmapped,trunk_mapped}.xml`, run via `make test-sip TARGET=<ip> SCENARIO=trunk-reject|trunk-did`. (Mapped-DID full 200 OK answer + outbound egress need the full stack / supervisor wiring; egress identity is covered by `TestApplyTenantIdentity`.)
