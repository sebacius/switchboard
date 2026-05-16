# Dialplan Reference

The dialplan defines how calls are routed and what actions are executed when a call matches a route.

## Configuration File

The dialplan is configured via a JSON file (default: `dialplan.json`). Set the path with:

```bash
./switchboard-signaling --dialplan /etc/switchboard/dialplan.json
```

Or via environment variable:

```bash
export DIALPLAN_PATH=/etc/switchboard/dialplan.json
```

## File Format

```json
{
  "version": "1.0",
  "routes": [
    {
      "id": "route_id",
      "name": "Human-readable name",
      "pattern": "pattern",
      "priority": 10,
      "enabled": true,
      "actions": [
        {
          "type": "action_type",
          "params": { ... }
        }
      ]
    }
  ]
}
```

## Route Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `id` | string | Yes | Unique route identifier |
| `name` | string | No | Human-readable description |
| `pattern` | string | Yes | Glob pattern to match destination |
| `priority` | int | No | Lower values match first (default: 100) |
| `enabled` | bool | No | Whether route is active (default: true) |
| `actions` | array | Yes | List of actions to execute |

## Pattern Matching

Routes are matched against the dialed destination number. Patterns use glob-style matching:

| Pattern | Matches | Description |
|---------|---------|-------------|
| `500` | 500 | Exact match |
| `1*` | 1001, 1999, 12345 | Starts with 1 |
| `*500` | 500, 1500, 8005500 | Ends with 500 |
| `10*` | 1000-1099, 10123 | Starts with 10 |
| `*` | anything | Catch-all |

### Pattern Examples

```json
{
  "routes": [
    {"id": "ivr", "pattern": "500", "priority": 10},
    {"id": "extensions", "pattern": "1*", "priority": 50},
    {"id": "external", "pattern": "9*", "priority": 50},
    {"id": "catchall", "pattern": "*", "priority": 999}
  ]
}
```

### Matching Order

1. Routes are sorted by priority (ascending)
2. First matching pattern wins
3. If no match, call receives 404 Not Found

## Actions

Actions are executed sequentially. If an action fails, execution stops and the call may be terminated.

### play_audio

Streams an audio file to the caller.

```json
{
  "type": "play_audio",
  "params": {
    "file": "audio/welcome.wav"
  }
}
```

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `file` | string | Yes | Path to WAV file (relative to `AUDIO_PATH`) |

**Audio Requirements:**
- Format: WAV (PCM)
- Sample rate: 8000 Hz (will be resampled if different)
- Channels: Mono (stereo will be downmixed)
- Bits: 16-bit

**Behavior:**
- Blocks until playback completes
- Respects context cancellation (stops on hangup)
- Returns error if file not found

### dial

Originates a call to a target and bridges media.

```json
{
  "type": "dial",
  "params": {
    "target": "user/${destination}",
    "timeout": 30
  }
}
```

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `target` | string | Yes | Dial target (see Target Formats) |
| `timeout` | int | No | Ring timeout in seconds (default: 30) |

**Behavior:**
- Blocks until target answers, rejects, or timeout
- On answer, creates media bridge between caller and target
- Bridge remains until either party hangs up
- Original caller is hung up when bridge terminates

### ai_agent

Starts an AI voice conversation powered by an LLM, with speech-to-text and text-to-speech.

```json
{
  "type": "ai_agent",
  "params": {
    "config": "default",
    "voice": "alloy",
    "model": "llama3.1:8b",
    "mode": "routing"
  }
}
```

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `config` | string | No | Tenant config file name (without `.md`). Loaded from `resources/tenants/`. Supports `${domain}` substitution. Default: `"default"` |
| `voice` | string | No | TTS voice name. Default: `"alloy"` |
| `model` | string | No | LLM model name. Default: `"llama3"` |
| `mode` | string | No | `"conversational"` (multi-turn) or `"routing"` (single-shot). Default: `"conversational"` |
| `greeting` | string | No | Initial greeting spoken to the caller. Defaults vary by mode |
| `max_turns` | int | No | Max conversation turns before auto-hangup. Default: `10` |
| `silence_timeout_ms` | int | No | Silence detection timeout in ms. Default: `2000` |
| `max_listen_ms` | int | No | Max listen duration per turn in ms. Default: `15000` |
| `tenants_path` | string | No | Path to tenant config directory. Default: `"resources/tenants"` |

**Modes:**

- **Conversational** -- Greets the caller, then enters a listen/respond loop. Each turn: capture speech (ASR), send to LLM, speak response (TTS). Continues until the LLM emits an action (transfer, hangup, park) or max turns is reached.
- **Routing** -- Greets the caller, then asks the LLM for a single routing decision based on the tenant config. Speaks the response, executes the action, and returns. No caller input is captured. Use for after-hours messages or simple call routing.

**LLM Actions:**

The LLM can trigger actions by appending an `ACTION:` block to its response. Available actions:

- `ACTION: transfer` with `extension: <number>` -- Transfer the call to a registered extension
- `ACTION: hangup` -- End the call
- `ACTION: park` with optional `slot: <number>` -- Park the call with music on hold

The action contract is defined in `resources/config/settings.md` (loaded at startup).

**Tenant Configuration:**

Tenant configs are Markdown files in `resources/tenants/` that define the agent's personality and business context. Example (`resources/tenants/default.md`):

```markdown
You are a helpful voice assistant for a phone system.

## Context
You are answering calls for a general business. Nobody is available
to take calls right now. Let the caller know politely and hang up.
```

**Requirements:**
- LLM server (Ollama or OpenAI-compatible) -- set `--llm-server` flag
- TTS server (openedai-speech) -- used by RTP Manager
- ASR server (faster-whisper) -- used by RTP Manager for conversational mode

### tts

Synthesizes text to speech and plays it to the caller.

```json
{
  "type": "tts",
  "params": {
    "text": "Welcome to our service.",
    "voice": "alloy"
  }
}
```

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `text` | string | Yes | Text to synthesize and play |
| `voice` | string | No | TTS voice name. Default: `"alloy"` |

**Behavior:**
- Blocks until playback completes
- Respects context cancellation (stops on hangup)
- Requires TTS server configured on the RTP Manager

### hangup

Terminates the call.

```json
{
  "type": "hangup",
  "params": {
    "reason": "normal"
  }
}
```

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `reason` | string | No | Hangup reason (default: "normal") |

**Reasons:**
- `normal` - Normal call clearing
- `busy` - User busy
- `rejected` - Call rejected
- `unavailable` - User unavailable

## Target Formats

The `dial` action supports multiple target formats:

### User Target

Look up user in the location service.

```
user/1001
user/${destination}
```

Resolves registered contacts for the user. Fails if user not registered.

### Direct SIP URI

Dial a specific SIP endpoint.

```
sip:user@192.168.1.100:5060
sip:+15551234567@gateway.example.com
```

No lookup required - dials the URI directly.

### Gateway Target (Future)

Route through a configured gateway.

```
gateway/carrier
gateway/emergency
```

Uses gateway configuration for trunk routing.

## Variable Substitution

Action parameters support variable substitution using `${variable}` syntax.

| Variable | Description |
|----------|-------------|
| `${destination}` | Dialed number (To header user part) |
| `${caller_id}` | Caller's number (From header user part) |
| `${caller_name}` | Caller's display name |
| `${call_id}` | SIP Call-ID |
| `${domain}` | SIP domain (To header host part) |

### Examples

```json
{
  "type": "dial",
  "params": {
    "target": "user/${destination}"
  }
}
```

```json
{
  "type": "play_audio",
  "params": {
    "file": "greetings/${caller_id}.wav"
  }
}
```

## Complete Examples

### Simple IVR

Play a welcome message and hang up.

```json
{
  "version": "1.0",
  "routes": [
    {
      "id": "ivr",
      "name": "Main IVR",
      "pattern": "500",
      "priority": 10,
      "enabled": true,
      "actions": [
        {
          "type": "play_audio",
          "params": {"file": "audio/welcome.wav"}
        },
        {
          "type": "hangup",
          "params": {"reason": "normal"}
        }
      ]
    }
  ]
}
```

### Internal Extensions

Route internal calls to registered users.

```json
{
  "version": "1.0",
  "routes": [
    {
      "id": "internal",
      "name": "Internal Extensions",
      "pattern": "1*",
      "priority": 50,
      "enabled": true,
      "actions": [
        {
          "type": "dial",
          "params": {
            "target": "user/${destination}",
            "timeout": 30
          }
        }
      ]
    }
  ]
}
```

### External Calls via Gateway

Route external calls through a SIP trunk.

```json
{
  "version": "1.0",
  "routes": [
    {
      "id": "external",
      "name": "External via Gateway",
      "pattern": "9*",
      "priority": 50,
      "enabled": true,
      "actions": [
        {
          "type": "dial",
          "params": {
            "target": "sip:${destination}@gateway.example.com",
            "timeout": 60
          }
        }
      ]
    }
  ]
}
```

### Multi-Action Route

Play announcement, then dial.

```json
{
  "version": "1.0",
  "routes": [
    {
      "id": "operator",
      "name": "Operator with Announcement",
      "pattern": "0",
      "priority": 10,
      "enabled": true,
      "actions": [
        {
          "type": "play_audio",
          "params": {"file": "audio/please-hold.wav"}
        },
        {
          "type": "dial",
          "params": {
            "target": "user/operator",
            "timeout": 30
          }
        }
      ]
    }
  ]
}
```

### Catch-All Route

Handle unmatched destinations.

```json
{
  "version": "1.0",
  "routes": [
    {
      "id": "catchall",
      "name": "Catch-All",
      "pattern": "*",
      "priority": 999,
      "enabled": true,
      "actions": [
        {
          "type": "play_audio",
          "params": {"file": "audio/invalid-number.wav"}
        },
        {
          "type": "hangup",
          "params": {"reason": "rejected"}
        }
      ]
    }
  ]
}
```

## Hot Reload

The dialplan supports hot reload without restarting the service. Changes take effect immediately for new calls.

Currently, hot reload must be triggered manually (API endpoint planned).

## Error Handling

| Scenario | Behavior |
|----------|----------|
| No matching route | 404 Not Found sent to caller |
| Action fails | Call terminated with error |
| File not found | Action fails, call terminated |
| Target not found | Dial fails, execution continues (or terminates) |
| Timeout | Dial fails, execution continues |

## Debugging

Enable debug logging to see dialplan matching and execution:

```bash
./switchboard-signaling --loglevel debug
```

Log output includes:
- Route matching attempts
- Selected route and priority
- Action execution start/end
- Variable substitution results
- Error details

## Future Enhancements

Planned dialplan features:

- **Conditions**: Match based on time, caller, headers
- **Parallel dial**: Ring multiple targets simultaneously
- **Queues**: Hold callers and distribute to agents
- **DTMF input**: Collect digits for menu navigation
- **Variables**: Set and read custom variables
- **Loops**: Repeat actions based on conditions
- **Callbacks**: HTTP webhooks for external logic
- **Streaming ASR**: Real-time transcription with barge-in detection

## Related Documents

- [Configuration](CONFIGURATION.md) - DIALPLAN_PATH setting

---

*Last updated: March 2026*
