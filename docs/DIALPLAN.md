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

## Related Documents

- [Configuration](CONFIGURATION.md) - DIALPLAN_PATH setting
- [Call Flows](CALL_FLOWS.md) - How dialplan fits in call setup
- [Code Map](CODE_MAP.md) - Dialplan implementation details

---

*Last updated: January 2026*
