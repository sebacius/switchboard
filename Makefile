.PHONY: build run clean help proto \
	build-signaling build-rtpmanager build-ui build-all build-linux \
	run-signaling run-rtpmanager run-ui \
	test-sip test-register test-multi test-api test-deregister \
	docker-build docker-build-signaling docker-build-rtpmanager docker-build-ui \
	services-up services-down services-logs \
	validate flow-smoke

# Docker image names
IMAGE_SIGNALING ?= switchboard-signaling
IMAGE_RTPMANAGER ?= switchboard-rtpmanager
IMAGE_UI ?= switchboard-ui
IMAGE_TAG ?= latest

# Build output directory
BUILD_DIR ?= build

# Test configuration
TEST_SIP_SERVER ?= localhost:5060

# Help
help:
	@echo "Switchboard Makefile"
	@echo ""
	@echo "BUILD:"
	@echo "  make build-signaling  - Build signaling server (macOS)"
	@echo "  make build-rtpmanager - Build RTP Manager (macOS)"
	@echo "  make build-ui         - Build UI server (macOS)"
	@echo "  make build-all        - Build all binaries (macOS)"
	@echo "  make build            - Build all binaries (Linux AMD64)"
	@echo "  make clean            - Clean build artifacts"
	@echo ""
	@echo "RUN:"
	@echo "  make run              - Build and run all services locally"
	@echo "  make run-signaling    - Run signaling server only"
	@echo "  make run-rtpmanager   - Run RTP Manager only"
	@echo "  make run-ui           - Run UI server only"
	@echo ""
	@echo "PROTO:"
	@echo "  make proto            - Regenerate gRPC code from proto files"
	@echo ""
	@echo "DOCKER:"
	@echo "  make docker-build           - Build all Docker images"
	@echo "  make docker-build-signaling - Build signaling Docker image"
	@echo "  make docker-build-rtpmanager- Build rtpmanager Docker image"
	@echo "  make docker-build-ui        - Build UI Docker image"
	@echo ""
	@echo "SUPPORTING SERVICES (Piper TTS / Whisper ASR; Ollama is unused):"
	@echo "  make services-up      - Start supporting services via docker compose"
	@echo "  make services-down    - Stop supporting services"
	@echo "  make services-logs    - Tail supporting service logs"
	@echo ""
	@echo "TESTING:"
	@echo "  make test-sip TARGET=<ip>       - Run SIPp test suite"
	@echo "  make test-sip TARGET=<ip> SCENARIO=register - Run specific test"
	@echo "  make test-sip TARGET=<ip> SCENARIO=trunk-reject - Ingress gate: unknown source -> 403"
	@echo "  make test-sip TARGET=<ip> SCENARIO=trunk-did - Ingress gate: trunk DID routing (needs peer config)"
	@echo "  make test-register              - Register single user (sipexer)"
	@echo "  make test-multi                 - Register multiple users"
	@echo "  make test-api                   - Check registrations via API"

# Ensure build directory exists
$(BUILD_DIR):
	@mkdir -p $(BUILD_DIR)

# Build targets (macOS)
build-signaling: $(BUILD_DIR)
	@echo "Building signaling server..."
	@go build -o $(BUILD_DIR)/switchboard-signaling ./cmd/signaling/

build-rtpmanager: $(BUILD_DIR)
	@echo "Building RTP Manager..."
	@go build -o $(BUILD_DIR)/switchboard-rtpmanager ./cmd/rtpmanager/

build-ui: $(BUILD_DIR)
	@echo "Building UI server..."
	@go build -o $(BUILD_DIR)/switchboard-ui ./cmd/ui/

build-all: build-signaling build-rtpmanager build-ui
	@echo "All binaries built in $(BUILD_DIR)/"

# Build targets (Linux)
build: build-linux

build-linux: $(BUILD_DIR)
	@echo "Building for Linux AMD64..."
	@GOOS=linux GOARCH=amd64 go build -buildvcs=false -o $(BUILD_DIR)/switchboard-signaling-linux ./cmd/signaling/
	@GOOS=linux GOARCH=amd64 go build -buildvcs=false -o $(BUILD_DIR)/switchboard-rtpmanager-linux ./cmd/rtpmanager/
	@GOOS=linux GOARCH=amd64 go build -buildvcs=false -o $(BUILD_DIR)/switchboard-ui-linux ./cmd/ui/
	@echo "Built in $(BUILD_DIR)/: switchboard-signaling-linux, switchboard-rtpmanager-linux, switchboard-ui-linux"

# Run targets
run: build-all
	@echo "Starting RTP Manager on :9090..."
	@$(BUILD_DIR)/switchboard-rtpmanager --grpc-port 9090 &
	@sleep 1
	@echo "Starting Signaling Server on :5060 (API on :8080)..."
	@$(BUILD_DIR)/switchboard-signaling --rtpmanager localhost:9090 &
	@sleep 1
	@echo "Starting UI Server on :3000..."
	@$(BUILD_DIR)/switchboard-ui --backends http://localhost:8080
	@echo "Use 'pkill switchboard' to stop"

run-signaling: build-signaling
	@$(BUILD_DIR)/switchboard-signaling --rtpmanager localhost:9090

run-rtpmanager: build-rtpmanager
	@$(BUILD_DIR)/switchboard-rtpmanager --grpc-port 9090

run-ui: build-ui
	@$(BUILD_DIR)/switchboard-ui --backends http://localhost:8080

# Proto generation
proto:
	@echo "Generating gRPC code..."
	@protoc --go_out=. --go_opt=module=github.com/sebas/switchboard \
		--go-grpc_out=. --go-grpc_opt=module=github.com/sebas/switchboard \
		api/proto/rtpmanager/v1/rtpmanager.proto
	@echo "Generated pkg/rtpmanager/v1/*.pb.go"

# Validate tenant configuration without starting the server. Runs the same
# checks the loader does, so "validate passes but the server will not start" is
# impossible; it just reports every problem instead of the first.
validate:
	@go run ./cmd/signaling validate --routing-path resources/tenants --policy-config resources/config/policy.json

# Walk a flow against a fake call. Set DIALED and optionally DIGITS:
#   make flow-smoke DIALED=700 DIGITS=2
flow-smoke:
	@go run ./cmd/flow-smoke --dialed "$(DIALED)" --digits "$(DIGITS)"

# Clean
clean:
	@rm -rf $(BUILD_DIR)
	@echo "Cleaned build artifacts"

# ============================================================================
# Docker targets
# ============================================================================

docker-build-signaling:
	@echo "Building signaling Docker image..."
	@docker build --platform linux/amd64 -f deploy/docker/Dockerfile.signaling -t $(IMAGE_SIGNALING):$(IMAGE_TAG) .

docker-build-rtpmanager:
	@echo "Building rtpmanager Docker image..."
	@docker build --platform linux/amd64 -f deploy/docker/Dockerfile.rtpmanager -t $(IMAGE_RTPMANAGER):$(IMAGE_TAG) .

docker-build-ui:
	@echo "Building ui Docker image..."
	@docker build --platform linux/amd64 -f deploy/docker/Dockerfile.ui -t $(IMAGE_UI):$(IMAGE_TAG) .

docker-build: docker-build-signaling docker-build-rtpmanager docker-build-ui
	@echo "All Docker images built"

# ============================================================================
# Supporting services via docker compose. Only Piper (TTS) is on the call path;
# Whisper is dormant and Ollama is left over from the removed LLM supervisor.
# ============================================================================

COMPOSE_SERVICES ?= deploy/docker/docker-compose.services.yml

services-up:
	@docker compose -f $(COMPOSE_SERVICES) up -d
	@echo "Supporting services starting. ollama-init will pull a model on first run; nothing consumes it."

services-down:
	@docker compose -f $(COMPOSE_SERVICES) down

services-logs:
	@docker compose -f $(COMPOSE_SERVICES) logs -f

# ============================================================================
# Homelab: the whole stack (signaling, 2x rtpmanager, ui) on a remote Docker host.
# Images are built ON the homelab from this tree over SSH; nothing is synced.
#
#   make homelab-setup     once: data dir + seed config, ufw rules, Caddy site
#   make deploy            build + (re)start everything changed
#   make deploy SVC=ui     just one service
#   make logs              follow all;  make logs SVC=signaling
#   make logs-signaling | logs-rtp | logs-ui
#   make ps | restart | down
#
# Tunables: LOGLEVEL=debug SIP_LOGLEVEL=trace (raw SIP) on deploy.
# ============================================================================

HOMELAB ?= homelab
HOMELAB_IP ?= 192.168.50.55
HOMELAB_LAN ?= 192.168.50.0/24
HOMELAB_DATA ?= /srv/switchboard
HOMELAB_CADDYFILE ?= /srv/edge/Caddyfile
HOMELAB_HOSTNAME ?= switchboard.homelab.lan
AUDIO_SRC ?= audio
SVC ?=
TAIL ?= 200

HOMELAB_COMPOSE = DOCKER_HOST=ssh://$(HOMELAB) ADVERTISE=$(HOMELAB_IP) DATA_DIR=$(HOMELAB_DATA) \
	docker compose -f deploy/homelab/compose.yml

.PHONY: deploy homelab-setup logs logs-signaling logs-rtp logs-ui ps restart down

deploy:
	@$(HOMELAB_COMPOSE) up -d --build --remove-orphans $(SVC)
	@$(HOMELAB_COMPOSE) ps

logs:
	@$(HOMELAB_COMPOSE) logs -f --tail $(TAIL) $(SVC)

logs-signaling:
	@$(HOMELAB_COMPOSE) logs -f --tail $(TAIL) signaling

logs-rtp:
	@$(HOMELAB_COMPOSE) logs -f --tail $(TAIL) rtpmanager-1 rtpmanager-2

logs-ui:
	@$(HOMELAB_COMPOSE) logs -f --tail $(TAIL) ui

ps:
	@$(HOMELAB_COMPOSE) ps

restart:
	@$(HOMELAB_COMPOSE) restart $(SVC)

down:
	@$(HOMELAB_COMPOSE) down

# Idempotent. Seeding never overwrites files already on the homelab (the UI edits them).
homelab-setup:
	@echo "==> data dir $(HOMELAB_DATA)"
	@ssh $(HOMELAB) 'sudo mkdir -p $(HOMELAB_DATA)/config $(HOMELAB_DATA)/tenants $(HOMELAB_DATA)/cdr $(HOMELAB_DATA)/audio && sudo chown -R 1000:1000 $(HOMELAB_DATA)'
	@rsync -a --ignore-existing resources/config/ $(HOMELAB):$(HOMELAB_DATA)/config/
	@rsync -a --ignore-existing resources/tenants/ $(HOMELAB):$(HOMELAB_DATA)/tenants/
	@rsync -a --ignore-existing $(AUDIO_SRC)/ $(HOMELAB):$(HOMELAB_DATA)/audio/
	@echo "==> ufw: SIP + RTP from the LAN, signaling API from Docker bridges (the UI)"
	@ssh $(HOMELAB) 'sudo ufw allow from $(HOMELAB_LAN) to any port 5060 comment "switchboard SIP" && \
		sudo ufw allow from $(HOMELAB_LAN) to any port 20000:20019 proto udp comment "switchboard RTP" && \
		sudo ufw allow from 172.16.0.0/12 to any port 8090 proto tcp comment "switchboard API (ui)"'
	@echo "==> Caddy site $(HOMELAB_HOSTNAME)"
	@ssh $(HOMELAB) 'grep -q "^$(HOMELAB_HOSTNAME) " $(HOMELAB_CADDYFILE) || { \
		sudo cp $(HOMELAB_CADDYFILE) $(HOMELAB_CADDYFILE).bak-switchboard && \
		printf "\n$(HOMELAB_HOSTNAME) {\n\treverse_proxy switchboard-ui:3000\n}\n" | sudo tee -a $(HOMELAB_CADDYFILE) >/dev/null; } && \
		docker exec caddy caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile >/dev/null && \
		docker exec caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile'
	@grep -q "$(HOMELAB_HOSTNAME)" /etc/hosts || echo "NOTE: add $(HOMELAB_HOSTNAME) to the homelab line in /etc/hosts on this laptop"

# ============================================================================
# Testing targets
# ============================================================================

# SIPp test suite - run all SIP scenarios
# Usage: make test-sip TARGET=localhost
#        make test-sip TARGET=192.168.1.100
#        make test-sip TARGET=localhost SCENARIO=register
TARGET ?= localhost
SCENARIO ?=

test-sip:
	@./test/sipp/run-tests.sh $(TARGET) $(SCENARIO)

test-register:
	@echo "Registering sebas with 3600s expiry..."
	@sipexer -register -au sebas -ex 3600 -cb $(TEST_SIP_SERVER)

test-multi:
	@echo "Registering alice, bob, and charlie..."
	@sipexer -register -fuser alice -ex 3600 -cu sip:alice@127.0.0.1:50501 $(TEST_SIP_SERVER) &
	@sipexer -register -fuser bob -ex 3600 -cu sip:bob@127.0.0.1:50502 $(TEST_SIP_SERVER) &
	@sipexer -register -fuser charlie -ex 3600 -cu sip:charlie@127.0.0.1:50503 $(TEST_SIP_SERVER) &
	@sleep 1
	@echo "Registrations submitted"

test-api:
	@echo "=== All Registrations ==="
	@curl -s http://localhost:8080/api/v1/registrations | jq . 2>/dev/null || curl -s http://localhost:8080/api/v1/registrations

test-deregister:
	@echo "Deregistering alice..."
	@sipexer -register -au alice -ex 0 -cb $(TEST_SIP_SERVER)
