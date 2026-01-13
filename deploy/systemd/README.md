# Switchboard Systemd Deployment

Systemd service files for running Switchboard on Linux.

## Files

| File | Description |
|------|-------------|
| `switchboard-rtpmanager.service` | RTP Manager service (media handling) |
| `switchboard-signaling.service` | SIP Signaling service |
| `switchboard-ui.service` | Web UI dashboard |
| `switchboard.target` | Target to manage all services together |
| `*.env` | Environment configuration templates |
| `install.sh` | Installation script |
| `uninstall.sh` | Uninstallation script |

## Quick Install

```bash
# Build Linux binaries
make build

# Copy to server
scp switchboard-*-linux user@server:/tmp/
scp -r deploy/systemd user@server:/tmp/

# On server
cd /tmp/systemd
sudo ./install.sh

# Configure
sudo vim /etc/switchboard/signaling.env  # Set SIP_ADVERTISE
sudo cp /path/to/dialplan.json /opt/switchboard/

# Start
sudo systemctl start switchboard.target
```

## Service Management

```bash
# Start all services
sudo systemctl start switchboard.target

# Stop all services
sudo systemctl stop switchboard.target

# Restart all services
sudo systemctl restart switchboard.target

# Check status
sudo systemctl status switchboard.target
sudo systemctl status switchboard-signaling
sudo systemctl status switchboard-rtpmanager
sudo systemctl status switchboard-ui

# Follow logs (live)
sudo journalctl -u switchboard-signaling -f
sudo journalctl -u switchboard-rtpmanager -f
sudo journalctl -u switchboard-ui -f

# Follow all switchboard logs
sudo journalctl -u 'switchboard-*' -f

# View recent logs (last 100 lines)
sudo journalctl -u switchboard-signaling -n 100

# View logs since boot
sudo journalctl -u switchboard-signaling -b

# View logs from specific time
sudo journalctl -u switchboard-signaling --since "10 minutes ago"
sudo journalctl -u switchboard-signaling --since "2024-01-15 10:00:00"

# Filter by priority (emerg, alert, crit, err, warning, notice, info, debug)
sudo journalctl -u switchboard-signaling -p err    # Errors only
sudo journalctl -u switchboard-signaling -p warning  # Warnings and above

# Export logs to file
sudo journalctl -u switchboard-signaling --no-pager > signaling.log
```

## Configuration

Environment files are in `/etc/switchboard/`:

- `rtpmanager.env` - RTP ports, audio path
- `signaling.env` - SIP port, advertise address, RTP manager addresses
- `ui.env` - HTTP port, backend addresses

## Directory Structure

```
/opt/switchboard/
├── switchboard-signaling    # Signaling binary
├── switchboard-rtpmanager   # RTP Manager binary
├── switchboard-ui           # UI binary
├── dialplan.json            # Dialplan configuration
└── audio/                   # Audio files for playback

/etc/switchboard/
├── rtpmanager.env
├── signaling.env
└── ui.env
```

## Service Dependencies

```
switchboard.target
├── switchboard-rtpmanager.service  (starts first)
├── switchboard-signaling.service   (requires rtpmanager)
└── switchboard-ui.service          (wants signaling)
```
