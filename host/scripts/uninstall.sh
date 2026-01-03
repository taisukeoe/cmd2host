#!/bin/bash
set -euo pipefail

INSTALL_DIR="$HOME/.cmd2host"
LAUNCHD_PLIST="$HOME/Library/LaunchAgents/com.user.cmd2host.plist"

echo "Uninstalling cmd2host..."

# Stop daemon (use direct check to avoid SIGPIPE with pipefail)
if launchctl list com.user.cmd2host >/dev/null 2>&1; then
    launchctl unload "$LAUNCHD_PLIST" 2>/dev/null || true
    echo "Daemon stopped"
fi

# Remove launchd plist
if [[ -f "$LAUNCHD_PLIST" ]]; then
    rm "$LAUNCHD_PLIST"
    echo "Removed $LAUNCHD_PLIST"
fi

# Remove install directory
if [[ -d "$INSTALL_DIR" ]]; then
    rm -rf "$INSTALL_DIR"
    echo "Removed $INSTALL_DIR"
fi

echo ""
echo "cmd2host uninstalled successfully"
