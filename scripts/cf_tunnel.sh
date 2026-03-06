#!/usr/bin/env bash
# cf_tunnel.sh — Start cloudflared quick tunnels for WebArena Docker services.
#
# Usage:
#   source scripts/cf_tunnel.sh          # Start tunnels + export env vars
#   scripts/cf_tunnel.sh --env-only      # Print env export commands (for eval)
#   scripts/cf_tunnel.sh --stop          # Kill running tunnels
#
# Each service gets a *.trycloudflare.com URL. The script exports:
#   WEBARENA_SHOPPING_URL, WEBARENA_SHOPPING_ADMIN_URL, WEBARENA_REDDIT_URL,
#   WEBARENA_GITLAB_URL, WEBARENA_WIKIPEDIA_URL, WEBARENA_MAP_URL

set -euo pipefail

TUNNEL_DIR="${HOME}/.xray/tunnels"
mkdir -p "$TUNNEL_DIR"

# Service name → local port
declare -A SERVICES=(
  [shopping]=7770
  [shopping_admin]=7780
  [reddit]=9999
  [gitlab]=8023
  [wikipedia]=8888
  [map]=3000
)

# Map service name → env var name
declare -A ENV_VARS=(
  [shopping]=WEBARENA_SHOPPING_URL
  [shopping_admin]=WEBARENA_SHOPPING_ADMIN_URL
  [reddit]=WEBARENA_REDDIT_URL
  [gitlab]=WEBARENA_GITLAB_URL
  [wikipedia]=WEBARENA_WIKIPEDIA_URL
  [map]=WEBARENA_MAP_URL
)

stop_tunnels() {
  local count=0
  for svc in "${!SERVICES[@]}"; do
    local pidfile="$TUNNEL_DIR/${svc}.pid"
    if [ -f "$pidfile" ]; then
      local pid
      pid=$(cat "$pidfile")
      if kill -0 "$pid" 2>/dev/null; then
        kill "$pid" 2>/dev/null || true
        count=$((count + 1))
      fi
      rm -f "$pidfile"
    fi
  done
  rm -f "$TUNNEL_DIR"/*.url
  echo "Stopped $count tunnel(s)."
}

if [ "${1:-}" = "--stop" ]; then
  stop_tunnels
  exit 0
fi

# Check dependencies.
if ! command -v cloudflared &>/dev/null; then
  echo "ERROR: cloudflared not found. Install with: brew install cloudflared" >&2
  exit 1
fi

# Stop any existing tunnels first.
stop_tunnels 2>/dev/null || true

echo "Starting cloudflared quick tunnels for WebArena services..."
echo ""

for svc in shopping shopping_admin reddit gitlab wikipedia map; do
  port="${SERVICES[$svc]}"
  logfile="$TUNNEL_DIR/${svc}.log"
  pidfile="$TUNNEL_DIR/${svc}.pid"
  urlfile="$TUNNEL_DIR/${svc}.url"

  # Check if the local service is actually running.
  if ! curl -sf -o /dev/null --max-time 2 "http://localhost:${port}" 2>/dev/null; then
    echo "  SKIP $svc (localhost:${port} not reachable)"
    continue
  fi

  # Start cloudflared in background.
  cloudflared tunnel --url "http://localhost:${port}" \
    --no-autoupdate \
    > "$logfile" 2>&1 &
  echo $! > "$pidfile"

  # Wait for the tunnel URL to appear in the log (up to 15s).
  tunnel_url=""
  for i in $(seq 1 30); do
    tunnel_url=$(grep -oE 'https://[a-z0-9-]+\.trycloudflare\.com' "$logfile" 2>/dev/null | head -1 || true)
    if [ -n "$tunnel_url" ]; then
      break
    fi
    sleep 0.5
  done

  if [ -z "$tunnel_url" ]; then
    echo "  FAIL $svc (tunnel URL not found after 15s, check $logfile)"
    kill "$(cat "$pidfile")" 2>/dev/null || true
    continue
  fi

  echo "$tunnel_url" > "$urlfile"
  echo "  OK   $svc → $tunnel_url (localhost:${port})"
done

echo ""

# Output env var exports.
env_lines=""
for svc in "${!ENV_VARS[@]}"; do
  urlfile="$TUNNEL_DIR/${svc}.url"
  if [ -f "$urlfile" ]; then
    tunnel_url=$(cat "$urlfile")
    var="${ENV_VARS[$svc]}"
    env_lines="${env_lines}export ${var}=\"${tunnel_url}\"\n"
  fi
done

if [ "${1:-}" = "--env-only" ]; then
  echo -e "$env_lines"
  exit 0
fi

# If sourced, export the vars directly.
for svc in "${!ENV_VARS[@]}"; do
  urlfile="$TUNNEL_DIR/${svc}.url"
  if [ -f "$urlfile" ]; then
    tunnel_url=$(cat "$urlfile")
    var="${ENV_VARS[$svc]}"
    export "${var}=${tunnel_url}"
    echo "  export ${var}=${tunnel_url}"
  fi
done

echo ""
echo "Tunnels running. Stop with: scripts/cf_tunnel.sh --stop"
echo "PIDs stored in $TUNNEL_DIR/"
