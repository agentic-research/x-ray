#!/usr/bin/env bash
# xray-launcher.sh — Chrome Native Messaging host for X-Ray agentd.
# Chrome launches this; we start agentd if needed, return the status.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
BINARY="$PROJECT_DIR/bin/agentd"
LOG_FILE="$PROJECT_DIR/logs/agentd.log"
PORT="${XRAY_PORT:-8080}"

# Native messaging protocol: read 4-byte LE length + JSON from stdin.
read_message() {
  local len_bytes
  len_bytes=$(dd bs=4 count=1 2>/dev/null | od -An -tu4 | tr -d ' ')
  if [ -n "$len_bytes" ] && [ "$len_bytes" -gt 0 ] 2>/dev/null; then
    dd bs=1 count="$len_bytes" 2>/dev/null
  fi
}

# Native messaging protocol: write 4-byte LE length + JSON to stdout.
write_message() {
  local msg="$1"
  local len=${#msg}
  printf "$(printf '\\x%02x\\x%02x\\x%02x\\x%02x' \
    $((len & 0xff)) $(( (len >> 8) & 0xff)) $(( (len >> 16) & 0xff)) $(( (len >> 24) & 0xff)))"
  printf '%s' "$msg"
}

# Source .envrc for API keys if it exists.
if [ -f "$PROJECT_DIR/.envrc" ]; then
  set -a
  # shellcheck disable=SC1091
  source "$PROJECT_DIR/.envrc" 2>/dev/null || true
  set +a
fi

# Read the incoming message (required by NM protocol).
MSG=$(read_message)

# Check if agentd is already running.
if curl -sf "http://localhost:${PORT}/status" --max-time 1 >/dev/null 2>&1; then
  PID=$(lsof -i ":$PORT" -sTCP:LISTEN -t 2>/dev/null | head -1 || echo "?")
  write_message "{\"status\":\"running\",\"pid\":\"$PID\",\"port\":$PORT}"
  exit 0
fi

# Build if binary missing.
if [ ! -f "$BINARY" ]; then
  cd "$PROJECT_DIR" && go build -o bin/agentd ./cmd/agentd >/dev/null 2>&1
fi

# Launch agentd detached.
mkdir -p "$PROJECT_DIR/logs"
cd "$PROJECT_DIR"
nohup "$BINARY" >"$LOG_FILE" 2>&1 &
AGENTD_PID=$!
disown "$AGENTD_PID"

# Wait briefly for startup.
sleep 1

if curl -sf "http://localhost:${PORT}/status" --max-time 1 >/dev/null 2>&1; then
  write_message "{\"status\":\"launched\",\"pid\":$AGENTD_PID,\"port\":$PORT}"
else
  write_message "{\"status\":\"launch_failed\",\"pid\":$AGENTD_PID,\"port\":$PORT}"
fi
exit 0
