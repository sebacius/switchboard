.PHONY: build run clean help proto \
	build-signaling build-rtpmanager build-ui build-all build-linux \
	test-register test-multi test-api test-deregister \
	run-ui \
	docker-build docker-build-signaling docker-build-rtpmanager docker-build-ui \
	docker-save docker-save-signaling docker-save-rtpmanager docker-save-ui \
	k8s-deploy k8s-delete k8s-status k8s-logs \
	k8s-deploy-signaling k8s-deploy-rtpmanager k8s-deploy-ui

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
	@echo "  make docker-save            - Save all images to tar files"
	@echo ""
	@echo "KUBERNETES (k3s):"
	@echo "  make k8s-deploy             - Deploy all to Kubernetes"
	@echo "  make k8s-deploy-signaling   - Build & deploy signaling only"
	@echo "  make k8s-deploy-rtpmanager  - Build & deploy rtpmanager only"
	@echo "  make k8s-deploy-ui          - Build & deploy UI only"
	@echo "  make k8s-delete             - Delete all Switchboard resources"
	@echo "  make k8s-status             - Show deployment status"
	@echo "  make k8s-logs               - Tail logs from all pods"
	@echo ""
	@echo "TESTING:"
	@echo "  make test-register    - Register single user"
	@echo "  make test-multi       - Register multiple users"
	@echo "  make test-api         - Check registrations via API"

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

# Save images to tar files for k3s import
docker-save: $(BUILD_DIR)
	@echo "Saving Docker images to tar files..."
	@docker save $(IMAGE_SIGNALING):$(IMAGE_TAG) -o $(BUILD_DIR)/switchboard-signaling.tar
	@docker save $(IMAGE_RTPMANAGER):$(IMAGE_TAG) -o $(BUILD_DIR)/switchboard-rtpmanager.tar
	@docker save $(IMAGE_UI):$(IMAGE_TAG) -o $(BUILD_DIR)/switchboard-ui.tar
	@echo "Saved to $(BUILD_DIR)/: switchboard-signaling.tar, switchboard-rtpmanager.tar, switchboard-ui.tar"

docker-save-signaling: $(BUILD_DIR)
	@docker save $(IMAGE_SIGNALING):$(IMAGE_TAG) -o $(BUILD_DIR)/switchboard-signaling.tar

docker-save-rtpmanager: $(BUILD_DIR)
	@docker save $(IMAGE_RTPMANAGER):$(IMAGE_TAG) -o $(BUILD_DIR)/switchboard-rtpmanager.tar

docker-save-ui: $(BUILD_DIR)
	@docker save $(IMAGE_UI):$(IMAGE_TAG) -o $(BUILD_DIR)/switchboard-ui.tar

docker: docker-build docker-save
	@echo "Docker build, save, and load complete"

# ============================================================================
# Kubernetes targets
# ============================================================================

# Full deployment: build images, load into k3s, apply manifests
# Load images into k3s containerd
k8s-load:
	@echo "Loading images into k3s..."
	@sudo k3s ctr images import $(BUILD_DIR)/switchboard-signaling.tar
	@sudo k3s ctr images import $(BUILD_DIR)/switchboard-rtpmanager.tar
	@sudo k3s ctr images import $(BUILD_DIR)/switchboard-ui.tar
	@echo "Images loaded into k3s"

k8s-deploy: k8s-load
	@echo "Deploying to Kubernetes..."
	@kubectl apply -k deploy/k8s/
	@echo ""
	@echo "Deployment complete. Run 'make k8s-status' to check status."
	@echo "UI available at: http://<node-ip>:30000"

# Delete all switchboard resources
k8s-delete:
	@echo "Deleting Switchboard resources..."
	@kubectl delete -k deploy/k8s/ --ignore-not-found
	@echo "Resources deleted"

# Show deployment status
k8s-status:
	@echo "=== Namespace ==="
	@kubectl get namespace switchboard 2>/dev/null || echo "Namespace not found"
	@echo ""
	@echo "=== Pods ==="
	@kubectl get pods -n switchboard -o wide 2>/dev/null || echo "No pods"
	@echo ""
	@echo "=== Services ==="
	@kubectl get services -n switchboard 2>/dev/null || echo "No services"
	@echo ""
	@echo "=== Deployments ==="
	@kubectl get deployments -n switchboard 2>/dev/null || echo "No deployments"

# Tail logs from all pods
k8s-logs:
	@kubectl logs -n switchboard -l app.kubernetes.io/part-of=switchboard --all-containers -f --prefix --max-log-requests=10

# Restart all deployments (useful after image updates)
k8s-restart:
	@echo "Restarting deployments..."
	@kubectl rollout restart statefulset -n switchboard
	@kubectl rollout status statefulset -n switchboard

# Individual service deployment targets
k8s-deploy-signaling: docker-build-signaling docker-save-signaling
	@echo "Loading signaling image into k3s..."
	@sudo k3s ctr images import $(BUILD_DIR)/switchboard-signaling.tar
	@echo "Restarting signaling..."
	@kubectl rollout restart statefulset/signaling -n switchboard
	@kubectl rollout status statefulset/signaling -n switchboard

k8s-deploy-rtpmanager: docker-build-rtpmanager docker-save-rtpmanager
	@echo "Loading rtpmanager image into k3s..."
	@sudo k3s ctr images import $(BUILD_DIR)/switchboard-rtpmanager.tar
	@echo "Restarting rtpmanager..."
	@kubectl rollout restart statefulset/rtpmanager -n switchboard
	@kubectl rollout status statefulset/rtpmanager -n switchboard

k8s-deploy-ui: docker-build-ui docker-save-ui
	@echo "Loading ui image into k3s..."
	@sudo k3s ctr images import $(BUILD_DIR)/switchboard-ui.tar
	@echo "Restarting ui..."
	@kubectl rollout restart statefulset/ui -n switchboard
	@kubectl rollout status statefulset/ui -n switchboard

# ============================================================================
# Testing targets
# ============================================================================

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
