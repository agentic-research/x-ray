#!/usr/bin/env bash
# Set up the WebArena evaluation environment using webarena-verified CLI.
# Reference: https://servicenow.github.io/webarena-verified/latest/
#
# Usage:
#   bash scripts/webarena_setup.sh
#
# Prerequisites: Docker, uv (or pip with webarena-verified installed)

set -euo pipefail

DATA_DIR="docker/webarena-data"
CONFIG="docker/webarena-config.json"
mkdir -p "$DATA_DIR"

WA="uvx webarena-verified"

echo "=== WebArena Environment Setup ==="
echo ""

# Start each site (webarena-verified manages Docker images automatically).
echo "1/6 Starting shopping site (port 7770)..."
$WA env start --site shopping --port 7770 --no-wait

echo "2/6 Starting shopping admin (port 7780)..."
$WA env start --site shopping_admin --port 7780 --no-wait

echo "3/6 Starting reddit/forum (port 9999)..."
$WA env start --site reddit --port 9999 --no-wait

echo "4/6 Starting gitlab (port 8023)..."
$WA env start --site gitlab --port 8023 --no-wait

echo "5/6 Setting up wikipedia data..."
$WA env setup init --site wikipedia --data-dir "$DATA_DIR"
echo "   Starting wikipedia (port 8888)..."
$WA env start --site wikipedia --port 8888 --data-dir "$DATA_DIR" --no-wait

echo "6/6 Setting up map data..."
$WA env setup init --site map --data-dir "$DATA_DIR"
echo "   Starting map (port 3000)..."
$WA env start --site map --port 3000 --data-dir "$DATA_DIR" --no-wait

echo ""
echo "=== Sites Starting ==="
echo "  Shopping       → http://localhost:7770"
echo "  Shopping Admin → http://localhost:7780"
echo "  Reddit/Forum   → http://localhost:9999"
echo "  GitLab         → http://localhost:8023  (allow ~5 min boot)"
echo "  Wikipedia      → http://localhost:8888"
echo "  Map            → http://localhost:3000"
echo ""

# Write config file for webarena-verified eval.
cat > "$CONFIG" <<'ENDJSON'
{
  "sites": {
    "shopping": "http://localhost:7770",
    "shopping_admin": "http://localhost:7780",
    "reddit": "http://localhost:9999",
    "gitlab": "http://localhost:8023",
    "wikipedia": "http://localhost:8888",
    "map": "http://localhost:3000"
  }
}
ENDJSON
echo "Config written to $CONFIG"

# Export task data for the harness.
echo "Exporting task data..."
$WA agent-input-get --config "$CONFIG" --output testdata/webarena_tasks.json
echo "Tasks exported to testdata/webarena_tasks.json"

echo ""
echo "=== Setup Complete ==="
echo "Wait for all sites to be ready, then run: task webarena"
