# Development Guide

This guide covers building, testing, and contributing to Switchboard.

## Prerequisites

- **Go 1.24+** - [Download Go](https://go.dev/dl/)
- **protoc** - Protocol Buffers compiler
- **make** - For Makefile targets
- **git** - Version control

### Installing Go Tooling

```bash
# Install protoc plugins
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

# Verify installation
protoc --version
protoc-gen-go --version
protoc-gen-go-grpc --version
```

## Project Structure

```
switchboard/
+-- cmd/                    # Entry points
|   +-- signaling/          # Signaling server main
|   +-- rtpmanager/         # RTP Manager main
|   +-- ui/                 # UI server main
|
+-- internal/               # Private packages
|   +-- signaling/          # Signaling server packages
|   +-- rtpmanager/         # RTP Manager packages
|   +-- ui/                 # UI server packages
|   +-- banner/             # Startup banner
|   +-- logger/             # Logging
|
+-- api/                    # API definitions
|   +-- proto/              # Protobuf definitions
|   +-- types/              # Shared types
|
+-- pkg/                    # Generated code
|   +-- rtpmanager/         # Generated gRPC code
|
+-- docs/                   # Documentation
+-- deploy/                 # Deployment files
+-- Makefile                # Build automation
```

## Building

### Quick Build

```bash
# Build all services for current platform
go build -o switchboard-signaling ./cmd/signaling
go build -o switchboard-rtpmanager ./cmd/rtpmanager
go build -o switchboard-ui ./cmd/ui
```

### Using Makefile

```bash
# Build all for macOS
make build-all

# Build for Linux (deployment)
make build

# Build individual services
make build-signaling
make build-rtpmanager
make build-ui
```

### Cross-Compilation

```bash
# Linux AMD64
GOOS=linux GOARCH=amd64 go build -o switchboard-signaling-linux ./cmd/signaling

# Linux ARM64
GOOS=linux GOARCH=arm64 go build -o switchboard-signaling-arm64 ./cmd/signaling
```

## Running Locally

### All Services

```bash
# Using Makefile
make run

# Or manually
./switchboard-rtpmanager --grpc-port 9090 &
./switchboard-signaling --rtpmanager localhost:9090 &
./switchboard-ui --backends http://localhost:8080
```

### Individual Services

```bash
# RTP Manager only
make run-rtpmanager

# Signaling only (requires RTP Manager)
make run-signaling

# UI only (requires Signaling)
make run-ui
```

### Debug Mode

```bash
# With verbose logging
./switchboard-signaling --loglevel debug --rtpmanager localhost:9090
```

## Code Generation

### Regenerate gRPC Code

When modifying `api/proto/rtpmanager/v1/rtpmanager.proto`:

```bash
# Using Makefile
make proto

# Or manually
protoc --go_out=. --go-grpc_out=. api/proto/rtpmanager/v1/rtpmanager.proto
```

Generated files:
- `pkg/rtpmanager/v1/rtpmanager.pb.go`
- `pkg/rtpmanager/v1/rtpmanager_grpc.pb.go`

## Testing

### Run All Tests

```bash
go test ./...
```

### Run Tests with Verbose Output

```bash
go test -v ./...
```

### Run Specific Package Tests

```bash
go test -v ./internal/signaling/dialog/...
go test -v ./internal/rtpmanager/media/...
```

### Run with Race Detector

```bash
go test -race ./...
```

### Run with Coverage

```bash
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

## Code Quality

### Format Code

```bash
go fmt ./...
```

### Vet Code

```bash
go vet ./...
```

### Run staticcheck

```bash
# Install if needed
go install honnef.co/go/tools/cmd/staticcheck@latest

# Run
staticcheck ./...
```

## Dependencies

### Update Dependencies

```bash
go get -u ./...
go mod tidy
```

### Verify Dependencies

```bash
go mod verify
```

### List Dependencies

```bash
go list -m all
```

## Adding New Features

### Adding a New Dialplan Action

1. Create action file: `internal/signaling/dialplan/action_myaction.go`

```go
package dialplan

import "context"

type MyAction struct {
    param1 string
}

func NewMyAction(params map[string]interface{}) (*MyAction, error) {
    param1, ok := params["param1"].(string)
    if !ok {
        return nil, fmt.Errorf("param1 required")
    }
    return &MyAction{param1: param1}, nil
}

func (a *MyAction) Execute(ctx context.Context, session CallSession) error {
    // Implementation
    return nil
}
```

2. Register in `internal/signaling/dialplan/action.go`:

```go
func init() {
    RegisterAction("my_action", func(params map[string]interface{}) (Action, error) {
        return NewMyAction(params)
    })
}
```

### Adding a New API Endpoint

1. Add handler to `internal/signaling/api/server.go`:

```go
func (s *Server) handleMyEndpoint(w http.ResponseWriter, r *http.Request) {
    // Implementation
    json.NewEncoder(w).Encode(response)
}
```

2. Register route:

```go
func (s *Server) setupRoutes() {
    s.mux.HandleFunc("/api/v1/myendpoint", s.handleMyEndpoint)
}
```

### Adding a New gRPC Method

1. Update `api/proto/rtpmanager/v1/rtpmanager.proto`:

```protobuf
service RTPManagerService {
    // Existing methods...
    rpc MyMethod(MyRequest) returns (MyResponse);
}

message MyRequest {
    string field = 1;
}

message MyResponse {
    bool success = 1;
}
```

2. Regenerate code:

```bash
make proto
```

3. Implement in `internal/rtpmanager/server/server.go`:

```go
func (s *Server) MyMethod(ctx context.Context, req *pb.MyRequest) (*pb.MyResponse, error) {
    // Implementation
    return &pb.MyResponse{Success: true}, nil
}
```

4. Add client method in `internal/signaling/mediaclient/grpc.go`.

## Debugging

### Enable Debug Logs

```bash
./switchboard-signaling --loglevel debug
```

### Use Delve Debugger

```bash
# Install
go install github.com/go-delve/delve/cmd/dlv@latest

# Debug signaling server
dlv debug ./cmd/signaling -- --rtpmanager localhost:9090
```

### Profile CPU

```bash
go test -cpuprofile cpu.out ./internal/rtpmanager/media/...
go tool pprof cpu.out
```

### Profile Memory

```bash
go test -memprofile mem.out ./internal/rtpmanager/media/...
go tool pprof mem.out
```

## SIP Testing Tools

### sipexer

```bash
# Install
go install github.com/miconda/sipexer@latest

# Register
sipexer -register -user 1001 udp:localhost:5060

# INVITE
sipexer -invite -user 1001 sip:1002@localhost:5060
```

### sipp

For load testing and complex scenarios.

## Contributing

### Before Submitting

1. Run all tests: `go test ./...`
2. Format code: `go fmt ./...`
3. Check for issues: `go vet ./...`
4. Update documentation if needed

### Commit Messages

Use clear, descriptive commit messages:

```
feat: add ring group support to dialplan

- Add DialParallel method to CallService
- Update dial action to support multiple targets
- Add ring_group action type
```

### Pull Request Guidelines

1. Create feature branch from main
2. Keep changes focused and atomic
3. Include tests for new functionality
4. Update documentation as needed
5. Ensure CI passes before merge

## Troubleshooting

### Build Errors

**protoc not found:**
```bash
# Install protoc
brew install protobuf  # macOS
apt install protobuf-compiler  # Ubuntu
```

**Missing Go protoc plugins:**
```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
export PATH="$PATH:$(go env GOPATH)/bin"
```

### Runtime Errors

**Port already in use:**
```bash
# Find process using port
lsof -i :5060
lsof -i :8080
lsof -i :9090

# Kill process
kill -9 <PID>
```

**RTP Manager connection refused:**
```bash
# Verify RTP Manager is running
curl localhost:9090  # Should fail with gRPC error, not connection refused
```

## Related Documents

- [Getting Started](GETTING_STARTED.md) - Initial setup
- [Architecture](ARCHITECTURE.md) - System design
- [Code Map](CODE_MAP.md) - Codebase navigation
- [API Reference](API_REFERENCE.md) - API documentation

---

*Last updated: January 2026*
