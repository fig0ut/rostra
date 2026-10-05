#!/usr/bin/env bash

set -e -o pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/common.sh"

echo "Uninstalling rostra..."

if systemctl is-active --quiet rostra 2>/dev/null; then
    echo "Stopping rostra service..."
    sudo systemctl stop rostra
fi

if systemctl is-enabled --quiet rostra 2>/dev/null; then
    echo "Disabling rostra service..."
    sudo systemctl disable rostra
fi

echo "Removing files..."
sudo rm -rf "$INSTALL_DATA_PATH"
sudo rm -rf "$INSTALL_BIN_PATH/$BINARY_NAME"
sudo rm -rf "$INSTALL_CONFIG_PATH"
sudo rm -rf "$SYSTEMD_SERVICE_PATH/rostra.service"

echo "Reloading systemd..."
sudo systemctl daemon-reload

echo ""
echo "Uninstallation complete!"
