# Switchboard Documentation

This directory contains the complete documentation for the Switchboard VoIP platform.

## Quick Navigation

| Document | Description |
|----------|-------------|
| [Architecture](ARCHITECTURE.md) | System design, philosophy, and component overview |
| [Getting Started](GETTING_STARTED.md) | Installation, prerequisites, and quick start guide |
| [Configuration](CONFIGURATION.md) | Environment variables, flags, and service configuration |
| [API Reference](API_REFERENCE.md) | REST API endpoints and gRPC protocol |
| [Call Flows](CALL_FLOWS.md) | Detailed call flow diagrams and sequences |
| [B2BUA Design](B2BUA.md) | Back-to-Back User Agent implementation details |
| [Dialplan](DIALPLAN.md) | Route matching, actions, and variable substitution |
| [Code Map](CODE_MAP.md) | Codebase navigation and package descriptions |
| [Development](DEVELOPMENT.md) | Build instructions, testing, and contribution guide |
| [Deployment](DEPLOYMENT.md) | Docker containers and Kubernetes (k3s) deployment |
| [Roadmap](ROADMAP.md) | Planned features and future direction |
| [AI Agent](../resources/config/settings.md) | AI agent LLM action contract and response format |

## Overview

Switchboard is a VoIP platform that separates signaling and media into independently scalable components, with integrated AI services for voice agent capabilities:

```
                    +------------------+
                    |    UI Server     | :3000
                    |   (Dashboard)    |
                    +--------+---------+
                             | HTTP/REST
                    +--------v---------+
    SIP :5060 ----> |    Signaling     | :8080 API
    (INVITE/BYE)    |     Server       |-------> Ollama (LLM)
                    +--------+---------+
                             | gRPC
                    +--------v---------+
                    |   RTP Manager    | :9090
                    |  (Media/Audio)   |-------> Whisper (ASR) / TTS
                    +--------+---------+
                             | RTP/UDP
                    +--------v---------+
                    |   SIP Clients    |
                    +------------------+
```

**Signaling Server** - SIP protocol handling, B2BUA call bridging, dialplan engine, location service, LLM conversation management

**RTP Manager** - Media streaming, RTP bridging between call legs, SDP generation, port allocation, ASR and TTS integration

**UI Server** - Admin dashboard aggregating data from multiple signaling servers

**AI Services** (external) - Ollama for LLM inference, Whisper for speech-to-text, TTS server for text-to-speech. The signaling server holds conversation context while RTP managers handle media I/O (audio capture, transcription, synthesis).

## Document Conventions

- Code blocks with `bash` indicate shell commands
- Code blocks with `go` indicate Go source code
- Code blocks with `json` indicate configuration files
- ASCII diagrams illustrate architecture and flows
- Tables summarize configuration options and API endpoints

## External Resources

- [sipgo](https://github.com/emiago/sipgo) - Pure Go SIP stack
- [diago](https://github.com/emiago/diago) - B2BUA patterns and reference
- [Pion](https://github.com/pion) - RTP, SDP, and WebRTC libraries

---

*Last updated: March 2026*
