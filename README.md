# Switchboard

![Switchboard avatar](resources/img/switchboard.png)

> **WARNING: EXPERIMENTAL PROJECT**
> This is a **learning project** in active development. It is **pre-alpha**, **unstable**, and **not suitable for any production use**. The architecture is still being decided. Entire subsystems may be rewritten without notice. APIs will break. Config formats will change. Here be dragons.

## About

Switchboard is a VoIP platform that separates signaling and media into independently scalable components. It uses SIP for call control, RTP for media transport, and gRPC to coordinate services.

```mermaid
flowchart TB
  %% Nodes
  ClientsTop["SIP Clients"]
  Signaling["Signaling Server<br/>(SIP B2BUA + REST)"]
  ClientsBottom["SIP Clients"]

  subgraph MediaPlane["Media Plane"]
    direction LR
    RTP1["RTP Manager #1<br/>(Media Bridge)"]
    RTP2["RTP Manager #2<br/>(Media Bridge)"]
    RTPN["RTP Manager #N<br/>(Media Bridge)"]
  end

  UI["UI Server<br/>(Admin Dashboard)<br/>Aggregates backends"]

  %% Edges
  ClientsTop -->|"SIP :5060"| Signaling
  Signaling -->|"gRPC :9090"| RTP1
  Signaling -->|"gRPC :9090"| RTP2
  Signaling -->|"gRPC :9090"| RTPN
  RTP1 -->|"RTP"| ClientsBottom
  RTP2 -->|"RTP"| ClientsBottom
  RTPN -->|"RTP"| ClientsBottom
  UI <-->|"HTTP :3000"| Signaling

  %% Styling
  classDef clients fill:#0b3d91,stroke:#0b3d91,color:#ffffff,stroke-width:2px;
  classDef signaling fill:#6a00ff,stroke:#6a00ff,color:#ffffff,stroke-width:2px;
  classDef media fill:#00a86b,stroke:#00a86b,color:#ffffff,stroke-width:2px;
  classDef ui fill:#ff7a00,stroke:#ff7a00,color:#ffffff,stroke-width:2px;
  classDef plane fill:#111827,stroke:#9ca3af,color:#ffffff,stroke-width:1px;

  class ClientsTop,ClientsBottom clients;
  class Signaling signaling;
  class RTP1,RTP2,RTPN media;
  class UI ui;
  class MediaPlane plane;

  %% Link styling (index-based)
  linkStyle 0 stroke:#0b3d91,stroke-width:3px;
  linkStyle 1 stroke:#6a00ff,stroke-width:3px;
  linkStyle 2 stroke:#6a00ff,stroke-width:3px;
  linkStyle 3 stroke:#6a00ff,stroke-width:3px;
  linkStyle 4 stroke:#00a86b,stroke-width:3px;
  linkStyle 5 stroke:#00a86b,stroke-width:3px;
  linkStyle 6 stroke:#00a86b,stroke-width:3px;
  linkStyle 7 stroke:#ff7a00,stroke-width:3px,stroke-dasharray: 5 5;
```

## Quick Start

```bash
# Clone
git clone https://github.com/sebas/switchboard.git
cd switchboard

# Build
make build-all

# Run all services
make run

# Or run individually
./switchboard-rtpmanager --grpc-port 9090 &
./switchboard-signaling --rtpmanager localhost:9090 &
./switchboard-ui --backends http://localhost:8080
```

## AI Voice Agent

Switchboard includes an AI-powered voice agent that can answer calls, converse with callers, and take actions like transferring or parking calls. It connects three external services: speech-to-text (Whisper), text-to-speech (Piper), and an LLM (Ollama).

```mermaid
sequenceDiagram
    participant Caller
    participant Signaling
    participant RTP as RTP Manager
    participant ASR as Whisper (ASR)
    participant TTS as Piper (TTS)
    participant LLM as Ollama (LLM)

    Caller->>Signaling: SIP INVITE
    Signaling->>RTP: CreateSession (gRPC)
    Signaling->>Caller: 200 OK + SDP

    Note over Signaling: Dialplan matches ai_agent action

    Signaling->>TTS: Synthesize greeting
    TTS-->>RTP: Audio
    RTP-->>Caller: RTP (greeting)

    loop Conversational mode
        Caller-->>RTP: RTP (speech)
        RTP->>ASR: Transcribe audio
        ASR-->>Signaling: Text
        Signaling->>LLM: Chat completion
        LLM-->>Signaling: Response + optional ACTION
        Signaling->>TTS: Synthesize response
        TTS-->>RTP: Audio
        RTP-->>Caller: RTP (response)
    end

    Note over Signaling: LLM returns ACTION: hangup/transfer/park
    Signaling->>Caller: BYE
```

### Operating Modes

The AI agent operates in two modes, configured per-route in the dialplan:

- **Conversational** (default) -- Multi-turn dialogue. The agent greets the caller, listens for speech, sends it to the LLM, speaks the response, and repeats. The LLM can trigger actions (transfer, park, hangup) at any point.
- **Routing** -- Single-shot. The agent plays a greeting, asks the LLM for a routing decision based on tenant instructions (no caller input), speaks the response, executes the action, and hangs up. Use this for after-hours messages, announcements, or simple call routing.

### Configuration

The AI agent uses a two-layer prompt system:

1. **Settings** (`resources/config/settings.md`) -- Loaded once at startup. Defines the action contract (available actions, response format, rules). Shared across all tenants.
2. **Tenant config** (`resources/tenants/<name>.md`) -- Loaded per-call. Defines the business context, personality, and instructions for the agent. Selected via the `config` param in the dialplan route.

### Quick Start

```bash
# 1. Start the AI services (Docker)
make services-start    # TTS + Whisper

# 2. Start Ollama (install from ollama.com if not installed)
ollama serve &
ollama pull llama3.1:8b

# 3. Run Switchboard with LLM enabled
./build/switchboard-rtpmanager --grpc-port 9090 &
./build/switchboard-signaling --rtpmanager localhost:9090 --llm-server http://localhost:11434 &

# 4. Call extension 600 from a SIP client to reach the AI agent
```

### Dialplan Example

```json
{
  "id": "after_hours",
  "pattern": "600",
  "actions": [
    {
      "type": "ai_agent",
      "params": {
        "config": "default",
        "voice": "alloy",
        "model": "llama3.1:8b",
        "mode": "routing"
      }
    }
  ]
}
```

See the [Dialplan Reference](docs/DIALPLAN.md) for all `ai_agent` parameters.

### Kubernetes Deployment

```bash
# Deploy AI services to k3s
make k8s-deploy-ai     # Deploys TTS, ASR, and Ollama

# Or individually
make k8s-deploy-tts
make k8s-deploy-asr
make k8s-deploy-ollama
```

## Documentation

Complete documentation is available in the [docs/](docs/) folder:

| Document | Description |
|----------|-------------|
| [Getting Started](docs/GETTING_STARTED.md) | Installation and quick start guide |
| [Architecture](docs/ARCHITECTURE.md) | System design and philosophy |
| [Configuration](docs/CONFIGURATION.md) | Environment variables and flags |
| [API Reference](docs/API_REFERENCE.md) | REST and gRPC documentation |
| [Call Flows](docs/CALL_FLOWS.md) | Detailed call sequence diagrams |
| [Dialplan](docs/DIALPLAN.md) | Route matching and actions |
| [B2BUA Design](docs/B2BUA.md) | Back-to-Back User Agent details |
| [Code Map](docs/CODE_MAP.md) | Codebase navigation guide |
| [Development](docs/DEVELOPMENT.md) | Build, test, and contribute |
| [Deployment](docs/DEPLOYMENT.md) | Docker and Kubernetes deployment |
| [Roadmap](docs/ROADMAP.md) | Planned features |

## Technology Stack

- **Go 1.24** - Single binaries, goroutines, and a great standard library
- **[sipgo](https://github.com/emiago/sipgo)** - Pure Go SIP stack
- **[diago](https://github.com/emiago/diago)** - B2BUA patterns and inspiration
- **[Pion](https://github.com/pion)** - RTP, SDP, and WebRTC ecosystem
- **gRPC** - Service communication between signaling and media
- **HTMX + Tailwind** - Dashboard UI

## Acknowledgments

### [Pion](https://github.com/pion)
The Pion project provides the entire foundation for RTP, SDP, and WebRTC in Go. Without Pion's clean, well-tested libraries for packet handling, SDP parsing, and media transport, building something like this would take years instead of weeks.

### [sipgo](https://github.com/emiago/sipgo) & [diago](https://github.com/emiago/diago)
Emiago's sipgo library is a pure-Go SIP stack that actually makes sense. The diago project, built on top of sipgo, provided invaluable patterns for B2BUA implementation, dialog management, and call handling.

**Thank you to all these projects. Switchboard is an experiment built on your foundations.**

## Contributing

Contributions are welcome, but please understand what you are getting into:

1. **This is unstable** - Things will break. APIs will change. Your PR might become irrelevant overnight.
2. **No promises** - This is a side project for learning. Response times will vary.
3. **Discussion first** - For anything non-trivial, open an issue to discuss before submitting a PR.

If you are also curious about VoIP systems and want to experiment together, pull up a chair. If this project somehow helps you learn something, that is the whole point.
