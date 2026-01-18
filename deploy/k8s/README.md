# Switchboard Kubernetes Deployment

This guide covers deploying Switchboard to a k3s cluster for development.

## Prerequisites

- Linux machine with k3s installed
- Docker (for building images)
- kubectl configured to talk to your k3s cluster

### Install k3s (if not already installed)

```bash
curl -sfL https://get.k3s.io | sh -
```

Verify installation:

```bash
sudo k3s kubectl get nodes
```

## Quick Start

From the project root directory:

```bash
# Build images, load into k3s, and deploy
make k8s-deploy
```

This single command will:
1. Build Docker images for all three services
2. Save images to tar files
3. Import images into k3s containerd
4. Apply Kubernetes manifests

## Manual Deployment Steps

If you prefer to run steps individually:

```bash
# 1. Build Docker images
make docker-build

# 2. Save images to tar files
make docker-save

# 3. Load images into k3s
make docker-load

# 4. Apply manifests
kubectl apply -k deploy/k8s/
```

## Verify Deployment

```bash
# Check status of all resources
make k8s-status

# Expected output shows 3 pods running:
# - rtpmanager-xxxxx
# - signaling-xxxxx
# - ui-xxxxx
```

## Access Services

| Service | Port | Protocol | Access Method |
|---------|------|----------|---------------|
| SIP | 5060 | UDP/TCP | Host IP (hostNetwork) |
| REST API | 8080 | HTTP | Host IP (hostNetwork) |
| gRPC | 9090 | TCP | Host IP (hostNetwork) |
| UI Dashboard | 30000 | HTTP | NodePort |

### UI Dashboard

Open in browser: `http://<node-ip>:30000`

### SIP Testing

```bash
# Register a user (replace with your k3s node IP)
sipexer -register -au alice -ex 3600 -cb <node-ip>:5060

# Check registrations via API
curl http://<node-ip>:8080/api/v1/registrations
```

## Logs

```bash
# Tail logs from all pods
make k8s-logs

# Logs from specific service
kubectl logs -n switchboard -l app.kubernetes.io/component=signaling -f
kubectl logs -n switchboard -l app.kubernetes.io/component=rtpmanager -f
kubectl logs -n switchboard -l app.kubernetes.io/component=ui -f
```

## Update After Code Changes

```bash
# Rebuild and redeploy
make k8s-deploy

# Or just restart pods (if only config changed)
make k8s-restart
```

## Cleanup

```bash
# Delete all Switchboard resources
make k8s-delete
```

## Architecture Notes

### hostNetwork Mode

The signaling and rtpmanager pods use `hostNetwork: true` because:

- **SIP protocol** requires predictable source ports for client responses
- **RTP media** needs direct host network access for UDP streaming
- Services communicate via localhost when on the same node

### Port Allocations

| Port Range | Service | Purpose |
|------------|---------|---------|
| 5060 | Signaling | SIP protocol |
| 8080 | Signaling | REST API |
| 9090 | RTP Manager | gRPC |
| 10000-10100 | RTP Manager | RTP media (dev range) |
| 30000 | UI | NodePort for dashboard |

### Audio Files

Audio files are mounted from the host at `/opt/switchboard/audio`. Create this directory and add WAV files:

```bash
sudo mkdir -p /opt/switchboard/audio
# Copy your audio files (8kHz, mono, PCMU format)
sudo cp demo-congrats.wav /opt/switchboard/audio/
```

### Dialplan Configuration

The dialplan is stored in a ConfigMap (`switchboard-dialplan`). To modify:

1. Edit `deploy/k8s/configmap.yaml`
2. Apply changes: `kubectl apply -f deploy/k8s/configmap.yaml`
3. Restart signaling: `kubectl rollout restart deployment signaling -n switchboard`

## Troubleshooting

### Pods not starting

```bash
# Check pod events
kubectl describe pod -n switchboard <pod-name>

# Check if images are loaded
sudo k3s ctr images list | grep switchboard
```

### Port conflicts

If ports are already in use on the host:

```bash
# Check what's using a port
sudo ss -tulpn | grep :5060
```

### Image pull errors

The manifests use `imagePullPolicy: Never` which requires local images. Ensure you ran:

```bash
make docker-load
```

### Verify images in k3s

```bash
sudo k3s ctr images list | grep switchboard
# Should show:
# docker.io/library/switchboard-signaling:latest
# docker.io/library/switchboard-rtpmanager:latest
# docker.io/library/switchboard-ui:latest
```
