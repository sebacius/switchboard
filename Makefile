.PHONY: build run clean help proto \
	build-signaling build-rtpmanager build-ui build-all build-linux \
	run-signaling run-rtpmanager run-ui \
	test-sip test-register test-multi test-api test-deregister \
	docker-build docker-build-signaling docker-build-rtpmanager docker-build-ui \
	services-up services-down services-logs

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
	@echo "SUPPORTING SERVICES (Ollama / Whisper / Piper):"
	@echo "  make services-up      - Start LLM/ASR/TTS via docker compose"
	@echo "  make services-down    - Stop supporting services"
	@echo "  make services-logs    - Tail supporting service logs"
	@echo ""
	@echo "TESTING:"
	@echo "  make test-sip TARGET=<ip>       - Run SIPp test suite"
	@echo "  make test-sip TARGET=<ip> SCENARIO=register - Run specific test"
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
	@protoc --go_out=. --go-grpc_out=. api/proto/rtpmanager/v1/rtpmanager.proto
	@echo "Generated pkg/rtpmanager/v1/*.pb.go"

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
# Supporting services (Ollama / Whisper / Piper) via docker compose
# ============================================================================

COMPOSE_SERVICES ?= deploy/docker/docker-compose.services.yml

services-up:
	@docker compose -f $(COMPOSE_SERVICES) up -d
	@echo "Supporting services starting. ollama-init will pull the model on first run."

services-down:
	@docker compose -f $(COMPOSE_SERVICES) down

services-logs:
	@docker compose -f $(COMPOSE_SERVICES) logs -f

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
