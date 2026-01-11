.PHONY: build run clean help proto \
	build-signaling build-rtpmanager build-ui build-all build-linux \
	deploy deploy-signaling deploy-rtpmanager \
	test-register test-multi test-api test-deregister \
	run-ui

# Configuration
UTM_VM_IP ?= 192.168.50.181
UTM_VM_USER ?= root
TEST_SIP_SERVER ?= 192.168.50.181:5060

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
	@echo "DEPLOY (UTM VM):"
	@echo "  make deploy           - Build and deploy all services to UTM VM"
	@echo "  make ssh              - SSH into UTM VM"
	@echo ""
	@echo "TESTING:"
	@echo "  make test-register    - Register single user"
	@echo "  make test-multi       - Register multiple users"
	@echo "  make test-api         - Check registrations via API"

# Build targets (macOS)
build-signaling:
	@echo "Building signaling server..."
	@go build -o switchboard-signaling ./cmd/signaling/

build-rtpmanager:
	@echo "Building RTP Manager..."
	@go build -o switchboard-rtpmanager ./cmd/rtpmanager/

build-ui:
	@echo "Building UI server..."
	@go build -o switchboard-ui ./cmd/ui/

build-all: build-signaling build-rtpmanager build-ui
	@echo "All binaries built"

# Build targets (Linux)
build: build-linux

build-linux:
	@echo "Building for Linux AMD64..."
	@GOOS=linux GOARCH=amd64 go build -o switchboard-signaling-linux ./cmd/signaling/
	@GOOS=linux GOARCH=amd64 go build -o switchboard-rtpmanager-linux ./cmd/rtpmanager/
	@GOOS=linux GOARCH=amd64 go build -o switchboard-ui-linux ./cmd/ui/
	@echo "Built: switchboard-signaling-linux, switchboard-rtpmanager-linux, switchboard-ui-linux"

# Run targets
run: build-all
	@echo "Starting RTP Manager on :9090..."
	@./switchboard-rtpmanager --grpc-port 9090 &
	@sleep 1
	@echo "Starting Signaling Server on :5060 (API on :8080)..."
	@./switchboard-signaling --rtpmanager localhost:9090 &
	@sleep 1
	@echo "Starting UI Server on :3000..."
	@./switchboard-ui --backends http://localhost:8080
	@echo "Use 'pkill switchboard' to stop"

run-signaling: build-signaling
	@./switchboard-signaling --rtpmanager localhost:9090

run-rtpmanager: build-rtpmanager
	@./switchboard-rtpmanager --grpc-port 9090

run-ui: build-ui
	@./switchboard-ui --backends http://localhost:8080

# Proto generation
proto:
	@echo "Generating gRPC code..."
	@protoc --go_out=. --go-grpc_out=. api/proto/rtpmanager/v1/rtpmanager.proto
	@echo "Generated pkg/rtpmanager/v1/*.pb.go"

# Clean
clean:
	@rm -f switchboard-signaling switchboard-rtpmanager switchboard-ui
	@rm -f switchboard-signaling-linux switchboard-rtpmanager-linux switchboard-ui-linux
	@echo "Cleaned build artifacts"

# UTM Deployment
deploy: build-linux
	@echo "Deploying to UTM VM ($(UTM_VM_IP))..."
	@ssh $(UTM_VM_USER)@$(UTM_VM_IP) 'mkdir -p /opt/switchboard'
	@ssh $(UTM_VM_USER)@$(UTM_VM_IP) 'pkill switchboard || true'
	@scp switchboard-signaling-linux $(UTM_VM_USER)@$(UTM_VM_IP):/opt/switchboard/switchboard-signaling
	@scp switchboard-rtpmanager-linux $(UTM_VM_USER)@$(UTM_VM_IP):/opt/switchboard/switchboard-rtpmanager
	@scp switchboard-ui-linux $(UTM_VM_USER)@$(UTM_VM_IP):/opt/switchboard/switchboard-ui
	@echo "Deployed to /opt/switchboard/"

ssh:
	@ssh $(UTM_VM_USER)@$(UTM_VM_IP)

# Testing targets
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
