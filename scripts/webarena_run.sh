#!/usr/bin/env bash
# Unified WebArena eval runner.
#
# Usage:
#   scripts/webarena_run.sh                     # all tasks
#   scripts/webarena_run.sh shopping             # just shopping tasks
#   scripts/webarena_run.sh 22,28               # specific task IDs
#   scripts/webarena_run.sh reddit              # just reddit tasks
#   scripts/webarena_run.sh --no-server shopping # skip starting agentd (already running)
#
# Environment:
#   NAVIGATOR_MODEL   (default: gemini-3.1-flash)
#   WEBARENA_TIMEOUT  (default: 120)
#   CARTOGRAPHER_MODE (default: cairn)

set -euo pipefail

# ---------------------------------------------------------------------------
# Parse flags
# ---------------------------------------------------------------------------
START_SERVER=true
SUBSET="${WEBARENA_SUBSET:-full}"
SKIP_CONTAINERS=false
SCORE_VERIFIED=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    --no-server)   START_SERVER=false; shift ;;
    --no-containers) SKIP_CONTAINERS=true; shift ;;
    --verified)    SCORE_VERIFIED=true; shift ;;
    -*)            echo "Unknown flag: $1" >&2; exit 1 ;;
    *)             SUBSET="$1"; shift ;;
  esac
done

# ---------------------------------------------------------------------------
# Defaults
# ---------------------------------------------------------------------------
NAVIGATOR_MODEL="${NAVIGATOR_MODEL:-gemini-3.1-flash}"
WEBARENA_TIMEOUT="${WEBARENA_TIMEOUT:-300}"
CARTOGRAPHER_MODE="${CARTOGRAPHER_MODE:-cairn}"
DATA_DIR="docker/webarena-data"
CONFIG="docker/webarena-config.json"
TASKS="testdata/webarena_tasks.json"
WA="uvx webarena-verified"
AGENTD_PID=""

# ---------------------------------------------------------------------------
# Cleanup on exit
# ---------------------------------------------------------------------------
cleanup() {
  echo ""
  echo "=== Cleaning up ==="
  if [[ -n "$AGENTD_PID" ]] && kill -0 "$AGENTD_PID" 2>/dev/null; then
    echo "Stopping agentd (pid $AGENTD_PID)..."
    kill "$AGENTD_PID" 2>/dev/null || true
    wait "$AGENTD_PID" 2>/dev/null || true
  fi
  echo "Done. Containers left running (use 'docker stop \$(docker ps -q --filter name=webarena)' to stop)."
}
trap cleanup EXIT

# ---------------------------------------------------------------------------
# Determine which sites are needed
# ---------------------------------------------------------------------------
sites_for_subset() {
  case "$1" in
    shopping)       echo "shopping" ;;
    shopping_admin) echo "shopping_admin" ;;
    reddit)         echo "reddit" ;;
    gitlab)         echo "gitlab" ;;
    wikipedia)      echo "wikipedia" ;;
    map)            echo "map" ;;
    full|hard)      echo "shopping shopping_admin reddit gitlab wikipedia" ;;
    *)
      # Comma-separated task IDs — look up actual sites from tasks JSON.
      if [[ -f "$TASKS" ]]; then
        # Extract sites for matching task IDs using python (jq-free).
        python3 -c "
import json, sys
ids = {int(x) for x in '${1}'.split(',')}
tasks = json.load(open('${TASKS}'))
sites = set()
for t in tasks:
    if t['task_id'] in ids:
        sites.update(t.get('sites', []))
print(' '.join(sorted(sites)) if sites else 'shopping shopping_admin reddit gitlab wikipedia')
"
      else
        echo "shopping shopping_admin reddit gitlab wikipedia"
      fi
      ;;
  esac
}

NEEDED_SITES=$(sites_for_subset "$SUBSET")

# ---------------------------------------------------------------------------
# Site → port mapping
# ---------------------------------------------------------------------------
port_for_site() {
  case "$1" in
    shopping)       echo 7770 ;;
    shopping_admin) echo 7780 ;;
    reddit)         echo 9999 ;;
    gitlab)         echo 8023 ;;
    wikipedia)      echo 8888 ;;
    map)            echo 3000 ;;
  esac
}

# ---------------------------------------------------------------------------
# 1. Start containers
# ---------------------------------------------------------------------------
if [[ "$SKIP_CONTAINERS" == false ]]; then
  echo "=== Starting containers (subset: $SUBSET) ==="
  mkdir -p "$DATA_DIR"

  for site in $NEEDED_SITES; do
    port=$(port_for_site "$site")
    container="webarena_verified_${site}"

    # Check if already running on the right port.
    if docker ps --format '{{.Names}}' 2>/dev/null | grep -q "^${container}$"; then
      echo "  [skip] $site already running on :$port"
      continue
    fi

    echo "  [start] $site on :$port..."
    if [[ "$site" == "wikipedia" ]]; then
      $WA env setup init --site "$site" --data-dir "$DATA_DIR" 2>&1 | sed 's/^/    /'
      $WA env start --site "$site" --port "$port" --data-dir "$DATA_DIR" --no-wait 2>&1 | sed 's/^/    /'
    elif [[ "$site" == "map" ]]; then
      $WA env setup init --site "$site" --data-dir "$DATA_DIR" 2>&1 | sed 's/^/    /'
      $WA env start --site "$site" --port "$port" --data-dir "$DATA_DIR" --no-wait 2>&1 | sed 's/^/    /'
    else
      $WA env start --site "$site" --port "$port" --no-wait 2>&1 | sed 's/^/    /'
    fi
  done

  # Write/update config.
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

  # Export tasks if missing.
  if [[ ! -f "$TASKS" ]]; then
    echo "Exporting tasks..."
    $WA agent-input-get --config "$CONFIG" --output "$TASKS"
  fi
else
  echo "=== Skipping container startup ==="
fi

# ---------------------------------------------------------------------------
# 2. Wait for sites to be reachable
# ---------------------------------------------------------------------------
echo ""
echo "=== Waiting for sites ==="
for site in $NEEDED_SITES; do
  port=$(port_for_site "$site")
  url="http://localhost:$port"
  printf "  Waiting for %-16s " "$site (:$port)..."

  retries=0
  max_retries=60  # 60s
  if [[ "$site" == "gitlab" ]]; then
    max_retries=180  # gitlab is slow
  fi

  # Use -L to follow redirects (wikipedia/kiwix redirects on /).
  # Use -w to check for any HTTP response rather than requiring 200.
  while ! curl -sL -o /dev/null --max-time 3 -w '' "$url" 2>/dev/null; do
    retries=$((retries + 1))
    if [[ $retries -ge $max_retries ]]; then
      echo "TIMEOUT (${max_retries}s)"
      echo "ERROR: $site did not become ready. Check: docker logs webarena_verified_${site}" >&2
      exit 1
    fi
    sleep 1
  done
  echo "ready (${retries}s)"
done

# ---------------------------------------------------------------------------
# 3. Start agentd
# ---------------------------------------------------------------------------
if [[ "$START_SERVER" == true ]]; then
  echo ""
  echo "=== Starting agentd ==="

  # Clear stale schemas.
  rm -f ~/.xray/schemas.db

  # Build first so we run the binary directly (not go run, which spawns
  # a child process that survives kill of the go-run wrapper).
  go build -o bin/agentd ./cmd/agentd

  NAVIGATOR_ENDPOINT="" \
  NAVIGATOR_MODEL="$NAVIGATOR_MODEL" \
  NAVIGATOR_FORMAT="" \
  CARTOGRAPHER_MODE="$CARTOGRAPHER_MODE" \
    ./bin/agentd &
  AGENTD_PID=$!
  echo "  agentd pid: $AGENTD_PID"

  # Wait for agentd to be ready.
  printf "  Waiting for agentd..."
  retries=0
  while ! curl -sf -o /dev/null --max-time 2 "http://localhost:8080/status" 2>/dev/null; do
    retries=$((retries + 1))
    if [[ $retries -ge 30 ]]; then
      echo " TIMEOUT"
      echo "ERROR: agentd did not start" >&2
      exit 1
    fi
    sleep 1
  done
  echo " ready (${retries}s)"
else
  echo ""
  echo "=== Skipping agentd (--no-server) ==="
  # Verify it's already running.
  if ! curl -sf -o /dev/null --max-time 2 "http://localhost:8080/status" 2>/dev/null; then
    echo "ERROR: agentd not reachable at http://localhost:8080" >&2
    exit 1
  fi
  echo "  agentd already running"
fi

# ---------------------------------------------------------------------------
# 4. Run eval
# ---------------------------------------------------------------------------
echo ""
echo "=== Running eval (subset: $SUBSET, timeout: ${WEBARENA_TIMEOUT}s) ==="
echo ""

WEBARENA_SUBSET="$SUBSET" \
WEBARENA_TIMEOUT="$WEBARENA_TIMEOUT" \
  go run ./cmd/webarena

# ---------------------------------------------------------------------------
# 5. Score results
# ---------------------------------------------------------------------------
RESULTS_DIR="results/webarena_latest"
if [[ -d "$RESULTS_DIR" ]]; then
  echo ""
  echo "=== Scoring ==="
  uv run scripts/webarena_eval.py "$RESULTS_DIR" ${SCORE_VERIFIED:+"--verified"}
fi

echo ""
echo "=== Complete ==="
