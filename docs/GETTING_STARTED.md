# Getting Started

This guide covers installing, building, and running Switchboard for the first time.

## Prerequisites

### Required

- **Go 1.24+** - [Download Go](https://go.dev/dl/)
- **protoc** - Protocol Buffers compiler (for regenerating gRPC code)

### Optional

- **A SIP client** for testing - Opal, Opal, sipexer, Opal, etc.
- **make** - For using Makefile targets
- **Docker** - Required for running TTS and ASR services locally
- **Ollama** - Required for AI agent functionality ([Install Ollama](https://ollama.com/download))

### Installing protoc

**macOS:**
```bash
brew install protobuf
```

**Ubuntu/Debian:**
```bash
apt install -y protobuf-compiler
```

**From source:**
```bash
# Download from https://github.com/protocolbuffers/protobuf/releases
```

Install Go plugins for protoc:
```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
```

## Quick Start

### Clone and Build

```bash
# Clone the repository
git clone https://github.com/sebas/switchboard.git
cd switchboard

# Build all services
make build-all

# Or build individually
go build -o switchboard-signaling ./cmd/signaling
go build -o switchboard-rtpmanager ./cmd/rtpmanager
go build -o switchboard-ui ./cmd/ui
```

### Start AI Services (Optional)

If you want to use the AI agent functionality, start the AI services before running Switchboard:

```bash
# Start TTS (Piper) and ASR (Whisper) via Docker
make services-start

# Start Ollama (runs natively)
ollama serve
```

The AI services must be running before calls to the AI agent extension (600) will work. Switchboard's core SIP functionality works without them.

### Run All Services

The simplest way to run Switchboard is with the Makefile:

```bash
make run
```

This starts all three services with default configuration.

### Run Services Individually

For more control, run each service separately:

```bash
# Terminal 1: Start RTP Manager
./switchboard-rtpmanager --grpc-port 9090

# Terminal 2: Start Signaling Server
./switchboard-signaling --rtpmanager localhost:9090

# Terminal 3: Start UI Server
./switchboard-ui --backends http://localhost:8080
```

### Verify Services Are Running

```bash
# Check signaling server health
curl http://localhost:8080/api/v1/health

# Check UI server
open http://localhost:3000
```

## Default Ports

| Service | Port | Protocol | Purpose |
|---------|------|----------|---------|
| Signaling | 5060 | UDP | SIP signaling |
| Signaling | 8080 | HTTP | REST API |
| RTP Manager | 9090 | gRPC | Media control |
| RTP Manager | 10000-20000 | UDP | RTP media |
| UI Server | 3000 | HTTP | Admin dashboard |
| TTS (Piper) | 8000 | HTTP | Text-to-speech |
| ASR (Whisper) | 8001 | HTTP | Speech recognition |
| Ollama | 11434 | HTTP | LLM inference |

## Testing with a SIP Client

### Register a User

Configure your SIP client to register with:
- **Server**: localhost:5060
- **Username**: Any (e.g., 1001)
- **Password**: (none - auth not implemented)

### Make a Test Call

1. Register two SIP clients (e.g., 1001 and 1002)
2. From 1001, dial 1002
3. The call should ring on 1002
4. Answer on 1002
5. Verify audio flows both ways

### Test with sipexer

```bash
# Register user 1001
sipexer -register -timeout 5 -user 1001 udp:localhost:5060

# Send INVITE
sipexer -invite -user 1001 sip:1002@localhost:5060
```

### Testing the AI Agent

To test the AI agent, ensure all three AI services are running (TTS, ASR, and Ollama), then call extension 600 from a registered SIP client:

1. Start the AI services (`make services-start` and `ollama serve`)
2. Register a SIP client (e.g., user 1001) against `localhost:5060`
3. Dial extension **600**
4. Speak into the microphone -- the AI agent will transcribe your speech, generate a response via the LLM, and play it back as synthesized audio

If the AI agent does not respond, check that all three services are healthy:

```bash
# Verify TTS is running
curl http://localhost:8000/health

# Verify ASR is running
curl http://localhost:8001/health

# Verify Ollama is running
curl http://localhost:11434/api/tags
```

## Scaling RTP Managers

You can run multiple RTP Managers for load balancing:

```bash
# Start first RTP Manager with port range 10000-15000
./switchboard-rtpmanager --grpc-port 9090 --rtp-min 10000 --rtp-max 15000 &

# Start second RTP Manager with port range 15001-20000
./switchboard-rtpmanager --grpc-port 9091 --rtp-min 15001 --rtp-max 20000 &

# Start Signaling Server pointing to both
./switchboard-signaling --rtpmanager localhost:9090,localhost:9091
```

The signaling server load-balances across RTP Managers using round-robin with session affinity.

## Build for Deployment

Build Linux binaries for server deployment:

```bash
# Build all for Linux
GOOS=linux GOARCH=amd64 go build -o switchboard-signaling-linux ./cmd/signaling
GOOS=linux GOARCH=amd64 go build -o switchboard-rtpmanager-linux ./cmd/rtpmanager
GOOS=linux GOARCH=amd64 go build -o switchboard-ui-linux ./cmd/ui

# Or use the Makefile
make build
```

## Systemd Deployment

Systemd service files are provided in `deploy/systemd/`. See the [deployment README](../deploy/systemd/README.md) for installation instructions.

### Quick Systemd Setup

```bash
# Copy service files
sudo cp deploy/systemd/*.service /etc/systemd/system/

# Copy binaries
sudo cp switchboard-* /usr/local/bin/

# Enable and start
sudo systemctl daemon-reload
sudo systemctl enable switchboard.target
sudo systemctl start switchboard.target
```

### Viewing Logs

```bash
# Follow logs for a specific service
journalctl -u switchboard-signaling -f

# Follow logs for all switchboard services
journalctl -u 'switchboard-*' -f

# View recent logs (last 100 lines)
journalctl -u switchboard-signaling -n 100
```

## Troubleshooting

### Service Won't Start

Check the logs:
```bash
journalctl -u switchboard-signaling -n 50
```

Common issues:
- Port already in use - another service on 5060, 8080, or 9090
- RTP Manager not reachable - check `--rtpmanager` flag

### No Audio

Verify RTP ports are open:
```bash
# Check if RTP Manager allocated ports
curl http://localhost:8080/api/v1/sessions
```

Common issues:
- Firewall blocking UDP 10000-20000
- NAT traversal issues - set `ADVERTISE` to public IP

### SIP Clients Can't Register

Verify signaling server is listening:
```bash
# Check UDP 5060
netstat -an | grep 5060
```

## Next Steps

- [Configuration Reference](CONFIGURATION.md) - All configuration options
- [Dialplan Guide](DIALPLAN.md) - Configure call routing
- [API Reference](API_REFERENCE.md) - REST API documentation
- [Development Guide](DEVELOPMENT.md) - Contributing and testing

---

*Last updated: March 2026*
