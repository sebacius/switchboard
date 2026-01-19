# Deployment Guide

This document covers deploying Switchboard using Docker containers and Kubernetes (k3s).

## Prerequisites

- Docker (for building images)
- k3s (for Kubernetes deployment) or any Kubernetes cluster
- kubectl configured for your cluster
- Go 1.24+ (for local builds only)

## Quick Deployment

```bash
# Build Docker images and deploy to k3s in one command
make docker-build docker-save k8s-deploy
```

## Docker Images

### Building Images

Build all three service images:

```bash
make docker-build
```

Or build individually:

```bash
make docker-build-signaling
make docker-build-rtpmanager
make docker-build-ui
```

### Image Details

| Image | Exposed Ports | Description |
|-------|---------------|-------------|
| `switchboard-signaling:latest` | 5060/udp, 5060/tcp, 8080/tcp | SIP signaling + REST API |
| `switchboard-rtpmanager:latest` | 9090/tcp, 10000-10100/udp | gRPC + RTP media ports |
| `switchboard-ui:latest` | 3000/tcp | Admin dashboard |

All images:
- Use Alpine Linux 3.19 for minimal footprint
- Run as non-root user (`switchboard`, UID 1000)
- Include ca-certificates and tzdata
- Are statically compiled (CGO_ENABLED=0)

### Running with Docker

```bash
# Create a network
docker network create switchboard

# Run RTP Manager
docker run -d --name rtpmanager \
  --network switchboard \
  -p 9090:9090 \
  -p 10000-10100:10000-10100/udp \
  -v /path/to/audio:/app/audio \
  switchboard-rtpmanager:latest

# Run Signaling Server
docker run -d --name signaling \
  --network switchboard \
  -p 5060:5060/udp \
  -p 5060:5060/tcp \
  -p 8080:8080 \
  -v /path/to/dialplan.json:/app/config/dialplan.json \
  -e RTPMANAGER_ADDRS=rtpmanager:9090 \
  switchboard-signaling:latest

# Run UI Server
docker run -d --name ui \
  --network switchboard \
  -p 3000:3000 \
  -e UI_BACKENDS=local=http://signaling:8080 \
  switchboard-ui:latest
```

### Environment Variables

**Signaling Server:**
| Variable | Default | Description |
|----------|---------|-------------|
| `LOGLEVEL` | `info` | Log level (debug, info, warn, error) |
| `PORT` | `5060` | SIP listen port |
| `BIND` | `0.0.0.0` | Bind address |
| `DIALPLAN_PATH` | `/app/config/dialplan.json` | Dialplan configuration file |
| `RTPMANAGER_ADDRS` | - | Comma-separated RTP Manager addresses |
| `DATABASE_URL` | - | PostgreSQL connection string |
| `REDIS_ADDR` | - | Redis address (host:port) |
| `NATS_URL` | - | NATS connection URL |

**RTP Manager:**
| Variable | Default | Description |
|----------|---------|-------------|
| `LOGLEVEL` | `info` | Log level |
| `GRPC_PORT` | `9090` | gRPC listen port |
| `GRPC_BIND` | `0.0.0.0` | Bind address |
| `RTP_PORT_MIN` | `10000` | Minimum RTP port |
| `RTP_PORT_MAX` | `10100` | Maximum RTP port |
| `AUDIO_PATH` | `/app/audio` | Audio files directory |

**UI Server:**
| Variable | Default | Description |
|----------|---------|-------------|
| `UI_LOGLEVEL` | `info` | Log level |
| `UI_PORT` | `3000` | HTTP listen port |
| `UI_BIND` | `0.0.0.0` | Bind address |
| `UI_BACKENDS` | - | Backend servers (format: `name=url,name2=url2`) |

## Kubernetes Deployment

### Architecture

The Kubernetes deployment uses `hostNetwork: true` for all services because:
- **SIP** requires predictable ports for response routing
- **RTP** needs direct UDP access for media streaming
- **UI** needs to reach signaling on localhost (in this configuration)

```
┌─────────────────────────────────────────────────────────────┐
│                    k3s Node (hostNetwork)                    │
├─────────────────────────────────────────────────────────────┤
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐          │
│  │  Signaling  │  │ RTP Manager │  │     UI      │          │
│  │   :5060     │  │   :9090     │  │   :3000     │          │
│  │   :8080     │  │ :10000-100  │  │             │          │
│  └──────┬──────┘  └──────┬──────┘  └──────┬──────┘          │
│         │                │                │                  │
│         └────────────────┴────────────────┘                  │
│                     localhost                                │
└─────────────────────────────────────────────────────────────┘
```

### Manifests Overview

```
deploy/k8s/
├── kustomization.yaml  # Orchestrates all resources
├── namespace.yaml      # switchboard namespace
├── configmap.yaml      # Dialplan configuration
# Infrastructure
├── postgres.yaml       # PostgreSQL database
├── redis.yaml          # Redis cache
├── nats.yaml           # NATS messaging
# Application
├── signaling.yaml      # Signaling Deployment + Service
├── rtpmanager.yaml     # RTP Manager Deployment + Service
└── ui.yaml             # UI Deployment + Service
```

### Infrastructure Components

The deployment includes three infrastructure services:

#### PostgreSQL

**Purpose**: Persistent storage for accounts, users, CDRs, and dialplan configuration.

| Setting | Value |
|---------|-------|
| Image | `postgres:16-alpine` |
| Storage | 5Gi PVC (local-path) |
| Port | 5432 |
| Database | `switchboard` |
| Schema | `switchboard` |

**Connection string:**
```
postgres://switchboard:switchboard-dev-password@postgres:5432/switchboard
```

**Pre-created tables:**
- `switchboard.accounts` - Multi-tenant organizations
- `switchboard.users` - SIP users and extensions
- `switchboard.registrations` - SIP registration backup
- `switchboard.dialplan_routes` - Call routing rules
- `switchboard.cdrs` - Call Detail Records

#### Redis

**Purpose**: Fast caching for SIP registrations and call state.

| Setting | Value |
|---------|-------|
| Image | `redis:7-alpine` |
| Memory limit | 128MB |
| Eviction | `allkeys-lru` |
| Persistence | Disabled (dev) |
| Port | 6379 |

**Connection:**
```
redis:6379
```

**Data structures used:**
- `reg:{aor}` - HASH for SIP registrations
- `call:{call_id}` - HASH for active call state
- `ratelimit:{type}:{id}` - Rate limiting counters

#### NATS

**Purpose**: Event streaming for call events and inter-service messaging.

| Setting | Value |
|---------|-------|
| Image | `nats:2.10-alpine` |
| JetStream | Enabled |
| Memory store | 256MB |
| File store | 1GB |
| Client port | 4222 |
| Monitor port | 8222 |

**Connection:**
```
nats://nats:4222
```

**Subject hierarchy:**
```
switchboard.calls.{call_id}.{event}     # Call lifecycle events
switchboard.registrations.{aor}.{event} # Registration events
switchboard.sessions.{id}.{event}       # RTP session events
switchboard.cdr.raw                     # CDR stream (JetStream)
```

### Infrastructure Resource Limits

| Service | CPU Request | CPU Limit | Memory Request | Memory Limit |
|---------|-------------|-----------|----------------|--------------|
| PostgreSQL | 100m | 500m | 256Mi | 512Mi |
| Redis | 50m | 200m | 64Mi | 256Mi |
| NATS | 50m | 500m | 64Mi | 512Mi |

### Deploying to k3s

**Step 1: Build and save Docker images**

```bash
make docker-build
make docker-save
```

**Step 2: Load images into k3s**

```bash
make k8s-load
```

This loads the tar files into k3s containerd. Images are tagged with `imagePullPolicy: Never` in the manifests.

**Step 3: Deploy**

```bash
kubectl apply -k deploy/k8s/
```

Or use the all-in-one command:

```bash
make k8s-deploy
```

### Verifying Deployment

Check status:

```bash
make k8s-status
```

Expected output:
```
=== Pods ===
NAME                          READY   STATUS    RESTARTS   AGE
postgres-0                    1/1     Running   0          1m
redis-0                       1/1     Running   0          1m
nats-0                        1/1     Running   0          1m
rtpmanager-0                  1/1     Running   0          1m
rtpmanager-1                  1/1     Running   0          1m
signaling-0                   1/1     Running   0          1m
ui-0                          1/1     Running   0          1m
```

View logs:

```bash
make k8s-logs
```

### Accessing Services

| Service | Access |
|---------|--------|
| UI Dashboard | `http://<node-ip>:3000` |
| SIP | `<node-ip>:5060` (UDP/TCP) |
| REST API | `http://<node-ip>:8080/api/v1/` |

### Updating Deployment

After making changes:

```bash
# Rebuild images
make docker-build docker-save k8s-load

# Restart deployments to pick up new images
make k8s-restart
```

### Deleting Deployment

```bash
make k8s-delete
```

### Resource Limits

Default resource allocations:

| Service | CPU Request | CPU Limit | Memory Request | Memory Limit |
|---------|-------------|-----------|----------------|--------------|
| Signaling | 100m | 500m | 64Mi | 256Mi |
| RTP Manager | 100m | 500m | 64Mi | 256Mi |
| UI | 50m | 200m | 32Mi | 128Mi |

Adjust in the respective YAML files under `spec.template.spec.containers[0].resources`.

### Health Checks

All services have liveness and readiness probes configured:

**Application:**
- **Signaling**: HTTP GET `/api/v1/health` on port 8080
- **RTP Manager**: TCP socket on port 9090
- **UI**: HTTP GET `/health` on port 3000

**Infrastructure:**
- **PostgreSQL**: `pg_isready -U switchboard`
- **Redis**: `redis-cli ping`
- **NATS**: HTTP GET `/healthz` on port 8222

### Scaling RTP Managers

The RTP Manager runs as a StatefulSet, allowing multiple replicas on a single node with unique port ranges:

| Pod | gRPC Port | RTP Ports |
|-----|-----------|-----------|
| rtpmanager-0 | 9090 | 10000-10099 |
| rtpmanager-1 | 9091 | 10100-10199 |
| rtpmanager-2 | 9092 | 10200-10299 |

**Adjust replica count:**
```yaml
# In deploy/k8s/rtpmanager.yaml
spec:
  replicas: 2  # Change to desired count
```

**Update signaling to use all RTP Managers:**
```yaml
# In deploy/k8s/signaling.yaml
- name: RTPMANAGER_ADDRS
  value: "localhost:9090,localhost:9091,localhost:9092"
```

The signaling server's transport pool handles round-robin allocation with session affinity.

**For multi-node production:**
- Run one RTP Manager per node (all use port 9090)
- Configure signaling with node IPs: `192.168.1.100:9090,192.168.1.101:9090`

### Audio Files

The RTP Manager expects audio files at `/app/audio`. In Kubernetes, this is configured as a hostPath volume:

```yaml
volumes:
  - name: audio
    hostPath:
      path: /opt/switchboard/audio
      type: DirectoryOrCreate
```

Place your WAV files (8kHz, mono, PCMU) in `/opt/switchboard/audio` on the k3s node.

### Dialplan Configuration

The dialplan is embedded in a ConfigMap (`deploy/k8s/configmap.yaml`). To update:

1. Edit `deploy/k8s/configmap.yaml`
2. Apply changes: `kubectl apply -k deploy/k8s/`
3. Restart signaling: `kubectl rollout restart deployment signaling -n switchboard`

## Production Considerations

### Not Covered Yet

This deployment is designed for development and testing. For production:

- **TLS/SRTP**: Not configured
- **Persistent Storage**: Uses hostPath (not suitable for multi-node)
- **Ingress**: No ingress controller configured
- **Secrets Management**: Credentials not externalized
- **High Availability**: Single replicas only
- **Monitoring**: No Prometheus/Grafana integration

### Network Requirements

Ensure these ports are accessible:

| Port | Protocol | Service |
|------|----------|---------|
| 5060 | UDP/TCP | SIP signaling |
| 8080 | TCP | REST API |
| 9090 | TCP | gRPC (internal) |
| 3000 | TCP | UI dashboard |
| 10000-10100 | UDP | RTP media |

### Troubleshooting

**Pods not starting:**
```bash
kubectl describe pod -n switchboard <pod-name>
kubectl logs -n switchboard <pod-name>
```

**Image not found:**
```bash
# Verify images are loaded
sudo k3s ctr images list | grep switchboard
```

**Selector immutable error:**
```bash
# Delete and recreate the deployment
kubectl delete deployment <name> -n switchboard
kubectl apply -k deploy/k8s/
```

**Services not reachable:**
```bash
# Check hostNetwork is working
kubectl exec -n switchboard <pod> -- netstat -tlnp
```

## Makefile Reference

| Target | Description |
|--------|-------------|
| `make docker-build` | Build all Docker images |
| `make docker-save` | Save images to tar files |
| `make k8s-load` | Load images into k3s |
| `make k8s-deploy` | Full deploy (load + apply) |
| `make k8s-delete` | Remove all resources |
| `make k8s-status` | Show deployment status |
| `make k8s-logs` | Tail logs from all pods |
| `make k8s-restart` | Restart all deployments |
