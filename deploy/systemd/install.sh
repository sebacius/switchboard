#!/bin/bash
# Switchboard systemd installation script
# Run as root: sudo ./install.sh

set -e

INSTALL_DIR="/opt/switchboard"
CONFIG_DIR="/etc/switchboard"
SYSTEMD_DIR="/etc/systemd/system"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "=== Switchboard Installation ==="

# Create switchboard user if not exists
if ! id -u switchboard &>/dev/null; then
    echo "Creating switchboard user..."
    useradd --system --no-create-home --shell /usr/sbin/nologin switchboard
fi

# Create directories
echo "Creating directories..."
mkdir -p "$INSTALL_DIR"
mkdir -p "$INSTALL_DIR/audio"
mkdir -p "$CONFIG_DIR"

# Copy binaries (assumes they're in the same directory as this script or parent)
if [ -f "$SCRIPT_DIR/../switchboard-signaling-linux" ]; then
    echo "Copying binaries..."
    cp "$SCRIPT_DIR/../switchboard-signaling-linux" "$INSTALL_DIR/switchboard-signaling"
    cp "$SCRIPT_DIR/../switchboard-rtpmanager-linux" "$INSTALL_DIR/switchboard-rtpmanager"
    cp "$SCRIPT_DIR/../switchboard-ui-linux" "$INSTALL_DIR/switchboard-ui"
    chmod +x "$INSTALL_DIR/switchboard-signaling"
    chmod +x "$INSTALL_DIR/switchboard-rtpmanager"
    chmod +x "$INSTALL_DIR/switchboard-ui"
fi

# Copy environment files (don't overwrite existing)
echo "Copying configuration templates..."
[ ! -f "$CONFIG_DIR/rtpmanager.env" ] && cp "$SCRIPT_DIR/rtpmanager.env" "$CONFIG_DIR/"
[ ! -f "$CONFIG_DIR/signaling.env" ] && cp "$SCRIPT_DIR/signaling.env" "$CONFIG_DIR/"
[ ! -f "$CONFIG_DIR/ui.env" ] && cp "$SCRIPT_DIR/ui.env" "$CONFIG_DIR/"

# Copy systemd files
echo "Installing systemd services..."
cp "$SCRIPT_DIR/switchboard-rtpmanager.service" "$SYSTEMD_DIR/"
cp "$SCRIPT_DIR/switchboard-signaling.service" "$SYSTEMD_DIR/"
cp "$SCRIPT_DIR/switchboard-ui.service" "$SYSTEMD_DIR/"
cp "$SCRIPT_DIR/switchboard.target" "$SYSTEMD_DIR/"

# Set ownership
echo "Setting permissions..."
chown -R switchboard:switchboard "$INSTALL_DIR"
chown -R root:switchboard "$CONFIG_DIR"
chmod 640 "$CONFIG_DIR"/*.env

# Reload systemd
echo "Reloading systemd..."
systemctl daemon-reload

# Enable services
echo "Enabling services..."
systemctl enable switchboard.target
systemctl enable switchboard-rtpmanager.service
systemctl enable switchboard-signaling.service
systemctl enable switchboard-ui.service

echo ""
echo "=== Installation Complete ==="
echo ""
echo "Next steps:"
echo "1. Edit configuration files in $CONFIG_DIR/"
echo "   - Set SIP_ADVERTISE in signaling.env to your server's IP"
echo "   - Adjust other settings as needed"
echo ""
echo "2. Copy your dialplan.json to $INSTALL_DIR/"
echo ""
echo "3. Start all services:"
echo "   systemctl start switchboard.target"
echo ""
echo "4. Check status:"
echo "   systemctl status switchboard-rtpmanager"
echo "   systemctl status switchboard-signaling"
echo "   systemctl status switchboard-ui"
echo ""
echo "5. View logs:"
echo "   journalctl -u switchboard-signaling -f"
echo ""
