#!/usr/bin/env bash
# install-native-host.sh — Register X-Ray native messaging host with Chrome.
# Usage: XRAY_EXT_ID=<your-extension-id> bash scripts/install-native-host.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LAUNCHER="$SCRIPT_DIR/xray-launcher.sh"
HOST_NAME="com.agentic_research.xray"

chmod +x "$LAUNCHER"

# Get extension ID.
echo "Native messaging host installer for X-Ray"
echo ""
if [ -n "${XRAY_EXT_ID:-}" ]; then
  EXT_ID="$XRAY_EXT_ID"
  echo "Using extension ID from env: $EXT_ID"
else
  echo "Enter your Chrome extension ID (from chrome://extensions):"
  read -r EXT_ID
  if [ -z "$EXT_ID" ]; then
    EXT_ID="EXTENSION_ID_PLACEHOLDER"
    echo "Warning: No ID provided. You must edit the manifest later."
  fi
fi

# Platform-specific host directory.
if [[ "$(uname)" == "Darwin" ]]; then
  HOSTS_DIR="$HOME/Library/Application Support/Google/Chrome/NativeMessagingHosts"
else
  HOSTS_DIR="$HOME/.config/google-chrome/NativeMessagingHosts"
fi

mkdir -p "$HOSTS_DIR"
MANIFEST="$HOSTS_DIR/$HOST_NAME.json"

cat > "$MANIFEST" <<JSON
{
  "name": "$HOST_NAME",
  "description": "X-Ray agentd launcher",
  "path": "$LAUNCHER",
  "type": "stdio",
  "allowed_origins": [
    "chrome-extension://${EXT_ID}/"
  ]
}
JSON

echo ""
echo "Installed: $MANIFEST"
echo "Launcher:  $LAUNCHER"
if [ "$EXT_ID" = "EXTENSION_ID_PLACEHOLDER" ]; then
  echo ""
  echo "UPDATE the allowed_origins in $MANIFEST with your extension ID!"
fi
