#!/usr/bin/env bash
set -euo pipefail

LABEL="com.github.entwico.podproxy"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PLIST_SRC="$SCRIPT_DIR/com.github.entwico.podproxy.plist"
DIST_DIR="$(cd "$SCRIPT_DIR/.." && pwd)/dist"
INSTALL_BIN="/usr/local/bin/podproxy"
CONFIG_DIR="$HOME/.config/podproxy"
PLIST_DST="$HOME/Library/LaunchAgents/$LABEL.plist"

# map uname arch to goreleaser naming
arch=$(uname -m)
case "$arch" in
    arm64|aarch64) goreleaser_arch="arm64" ;;
    x86_64)        goreleaser_arch="amd64" ;;
    *)             echo "unsupported architecture: $arch" >&2; exit 1 ;;
esac

binary_name="podproxy_darwin_${goreleaser_arch}"
binary=""

# look in dist directory first, then in the script's directory
for dir in "$DIST_DIR" "$SCRIPT_DIR"; do
    if [ -f "$dir/$binary_name" ]; then
        binary="$dir/$binary_name"
        break
    fi
done

# if not found, ask the user for the path
if [ -z "$binary" ]; then
    echo "binary '$binary_name' not found in:"
    echo "  - $DIST_DIR"
    echo "  - $SCRIPT_DIR"
    printf "enter the full path to the binary: "
    read -r binary
    if [ ! -f "$binary" ]; then
        echo "file not found: $binary" >&2
        exit 1
    fi
fi

echo "installing podproxy (darwin/$goreleaser_arch)..."

# install binary
sudo install -m 755 "$binary" "$INSTALL_BIN"
echo "  binary -> $INSTALL_BIN"

# create config directory and copy config if not already present
mkdir -p "$CONFIG_DIR"
config_src="$(cd "$(dirname "$0")/.." && pwd)/config.yaml"
if [ -f "$config_src" ] && [ ! -f "$CONFIG_DIR/config.yaml" ]; then
    cp "$config_src" "$CONFIG_DIR/config.yaml"
    echo "  config -> $CONFIG_DIR/config.yaml"
elif [ -f "$CONFIG_DIR/config.yaml" ]; then
    echo "  config already exists, skipping"
fi

# install and configure plist
mkdir -p "$HOME/Library/LaunchAgents"
sed "s|__HOME__|$HOME|g" "$PLIST_SRC" > "$PLIST_DST"
echo "  plist  -> $PLIST_DST"

# reload the agent
if launchctl list "$LABEL" &>/dev/null; then
    launchctl unload "$PLIST_DST" 2>/dev/null || true
fi
launchctl load "$PLIST_DST"

echo ""
echo "podproxy is installed and running."
echo "  logs: ~/Library/Logs/podproxy.std{out,err}.log"
echo "  stop: launchctl unload $PLIST_DST"
