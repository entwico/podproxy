#!/usr/bin/env bash
set -euo pipefail

LABEL="com.github.entwico.podproxy"
PLIST_DST="$HOME/Library/LaunchAgents/$LABEL.plist"
INSTALL_BIN="/usr/local/bin/podproxy"

echo "uninstalling podproxy..."

# unload the agent
if launchctl list "$LABEL" &>/dev/null; then
    launchctl unload "$PLIST_DST" 2>/dev/null || true
    echo "  agent unloaded"
fi

# remove plist
if [ -f "$PLIST_DST" ]; then
    rm "$PLIST_DST"
    echo "  removed $PLIST_DST"
fi

# remove binary
if [ -f "$INSTALL_BIN" ]; then
    sudo rm "$INSTALL_BIN"
    echo "  removed $INSTALL_BIN"
fi

echo ""
echo "podproxy has been uninstalled."
echo "  config left intact: ~/.config/podproxy/"
echo "  logs left intact:   ~/Library/Logs/podproxy.std{out,err}.log"
