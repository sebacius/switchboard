#!/bin/bash
# Switchboard systemd uninstallation script
# Run as root: sudo ./uninstall.sh

set -e

SYSTEMD_DIR="/etc/systemd/system"

echo "=== Switchboard Uninstallation ==="

# Stop services
echo "Stopping services..."
systemctl stop switchboard.target 2>/dev/null || true
systemctl stop switchboard-ui.service 2>/dev/null || true
systemctl stop switchboard-signaling.service 2>/dev/null || true
systemctl stop switchboard-rtpmanager.service 2>/dev/null || true

# Disable services
echo "Disabling services..."
systemctl disable switchboard.target 2>/dev/null || true
systemctl disable switchboard-ui.service 2>/dev/null || true
systemctl disable switchboard-signaling.service 2>/dev/null || true
systemctl disable switchboard-rtpmanager.service 2>/dev/null || true

# Remove systemd files
echo "Removing systemd files..."
rm -f "$SYSTEMD_DIR/switchboard-rtpmanager.service"
rm -f "$SYSTEMD_DIR/switchboard-signaling.service"
rm -f "$SYSTEMD_DIR/switchboard-ui.service"
rm -f "$SYSTEMD_DIR/switchboard.target"

# Reload systemd
echo "Reloading systemd..."
systemctl daemon-reload

echo ""
echo "=== Uninstallation Complete ==="
echo ""
echo "Note: Binaries in /opt/switchboard/ and configs in /etc/switchboard/"
echo "      were NOT removed. Delete manually if needed."
echo ""
