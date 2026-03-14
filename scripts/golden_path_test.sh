#!/usr/bin/env bash
# golden_path_test.sh — Automated golden path test loop for demo reliability.
#
# Sends the demo sequence (YouTube search → click → read) through the Doer HTTP API,
# polls for completion, and logs timing + failures. Run this in a loop to identify
# flaky steps and measure latency.
#
# Prerequisites:
#   - agentd running (task demo-video or task demo-fast)
#   - Chrome open with X-Ray extension + YouTube loaded in a tab
#
# Usage:
#   ./scripts/golden_path_test.sh              # single run
#   ./scripts/golden_path_test.sh --loop 5     # 5 iterations
#   ./scripts/golden_path_test.sh --loop 5 --tab 123456789  # specific tab

set -euo pipefail

AGENTD_URL="${AGENTD_URL:-http://localhost:8080}"
TAB_ID="${TAB_ID:-0}"
POLL_INTERVAL=2
MAX_POLL=60  # seconds
ITERATIONS=1
LOG_DIR="results/golden_path_$(date +%Y%m%d_%H%M%S)"

# Parse args
while [[ $# -gt 0 ]]; do
  case "$1" in
    --loop) ITERATIONS="$2"; shift 2 ;;
    --tab)  TAB_ID="$2"; shift 2 ;;
    --url)  AGENTD_URL="$2"; shift 2 ;;
    *)      echo "Unknown arg: $1"; exit 1 ;;
  esac
done

mkdir -p "$LOG_DIR"

# Golden path steps — each is a Doer intent.
STEPS=(
  "Go to youtube.com"
  "Click on the search bar and type Minecraft speedruns then press enter"
  "Click on the first video in the search results"
  "What is the title of this video?"
)

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
CYAN='\033[0;36m'
NC='\033[0m'

log() { echo -e "${CYAN}[$(date +%H:%M:%S)]${NC} $*"; }
ok()  { echo -e "${GREEN}[OK]${NC} $*"; }
fail(){ echo -e "${RED}[FAIL]${NC} $*"; }
warn(){ echo -e "${YELLOW}[WARN]${NC} $*"; }

# Check agentd is reachable
check_agentd() {
  if ! curl -sf "${AGENTD_URL}/status" >/dev/null 2>&1; then
    fail "agentd not reachable at ${AGENTD_URL}"
    echo "  Start it with: task demo-video"
    exit 1
  fi
  ok "agentd reachable at ${AGENTD_URL}"
}

# Submit an intent to the Doer and poll until completion.
# Returns: 0=success, 1=failure, 2=timeout
run_step() {
  local intent="$1"
  local step_num="$2"
  local iter="$3"
  local step_log="${LOG_DIR}/iter${iter}_step${step_num}.json"
  local start_ts=$(date +%s)

  log "Step ${step_num}: \"${intent}\""

  # Submit to Doer
  local submit_resp
  submit_resp=$(curl -sf -X POST "${AGENTD_URL}/doer" \
    -H "Content-Type: application/json" \
    -d "{\"intent\":\"${intent}\",\"tab_id\":${TAB_ID}}" 2>&1) || {
    fail "POST /doer failed: ${submit_resp}"
    echo "{\"step\":${step_num},\"intent\":\"${intent}\",\"error\":\"submit_failed\",\"response\":\"${submit_resp}\"}" > "$step_log"
    return 1
  }

  # Poll for completion
  local elapsed=0
  local status="unknown"
  local summary=""
  local error=""
  while [[ $elapsed -lt $MAX_POLL ]]; do
    sleep "$POLL_INTERVAL"
    elapsed=$(( $(date +%s) - start_ts ))

    local poll_resp
    poll_resp=$(curl -sf "${AGENTD_URL}/status?tab_id=${TAB_ID}" 2>/dev/null) || continue

    status=$(echo "$poll_resp" | python3 -c "import sys,json; print(json.load(sys.stdin).get('status',''))" 2>/dev/null || echo "parse_error")
    summary=$(echo "$poll_resp" | python3 -c "import sys,json; print(json.load(sys.stdin).get('summary',''))" 2>/dev/null || echo "")
    error=$(echo "$poll_resp" | python3 -c "import sys,json; print(json.load(sys.stdin).get('error',''))" 2>/dev/null || echo "")

    case "$status" in
      completed|idle|ready)
        local duration=$(( $(date +%s) - start_ts ))
        ok "Step ${step_num} completed in ${duration}s"
        [[ -n "$summary" ]] && log "  Summary: ${summary:0:120}"
        echo "{\"step\":${step_num},\"intent\":\"${intent}\",\"status\":\"${status}\",\"duration_s\":${duration},\"summary\":$(python3 -c "import json; print(json.dumps('${summary:0:200}'))" 2>/dev/null || echo '""')}" > "$step_log"
        return 0
        ;;
      failed)
        local duration=$(( $(date +%s) - start_ts ))
        fail "Step ${step_num} FAILED in ${duration}s: ${error:-${summary:-no details}}"
        echo "{\"step\":${step_num},\"intent\":\"${intent}\",\"status\":\"failed\",\"duration_s\":${duration},\"error\":$(python3 -c "import json; print(json.dumps('${error:-unknown}'))" 2>/dev/null || echo '""'),\"summary\":$(python3 -c "import json; print(json.dumps('${summary}'))" 2>/dev/null || echo '""')}" > "$step_log"
        return 1
        ;;
      in_progress)
        # Still working — show progress
        local step_desc
        step_desc=$(echo "$poll_resp" | python3 -c "import sys,json; print(json.load(sys.stdin).get('step',''))" 2>/dev/null || echo "")
        printf "\r  ${YELLOW}[%ds]${NC} %s: %s" "$elapsed" "$status" "${step_desc:0:60}"
        ;;
    esac
  done

  # Timeout
  local duration=$(( $(date +%s) - start_ts ))
  warn "Step ${step_num} TIMEOUT after ${duration}s (status: ${status})"
  echo "{\"step\":${step_num},\"intent\":\"${intent}\",\"status\":\"timeout\",\"duration_s\":${duration},\"last_status\":\"${status}\"}" > "$step_log"
  return 2
}

# Run one full golden path iteration.
run_iteration() {
  local iter="$1"
  local iter_start=$(date +%s)
  local passed=0
  local failed=0

  echo ""
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
  log "ITERATION ${iter}/${ITERATIONS}"
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

  # Reset between iterations
  curl -sf -X POST "${AGENTD_URL}/agent/reset" >/dev/null 2>&1 || true
  sleep 1

  for i in "${!STEPS[@]}"; do
    local step_num=$((i + 1))
    echo ""
    if run_step "${STEPS[$i]}" "$step_num" "$iter"; then
      ((passed++))
    else
      ((failed++))
      # Continue to next step — don't abort on failure, we want to see the full pattern
    fi
    # Brief pause between steps for page settle
    sleep 2
  done

  local iter_duration=$(( $(date +%s) - iter_start ))

  echo ""
  echo "──────────────────────────────────────────────────────────"
  if [[ $failed -eq 0 ]]; then
    ok "Iteration ${iter}: ALL ${passed}/${#STEPS[@]} PASSED in ${iter_duration}s"
  else
    fail "Iteration ${iter}: ${passed}/${#STEPS[@]} passed, ${failed} failed in ${iter_duration}s"
  fi
  echo "──────────────────────────────────────────────────────────"

  # Write iteration summary
  echo "{\"iteration\":${iter},\"passed\":${passed},\"failed\":${failed},\"total\":${#STEPS[@]},\"duration_s\":${iter_duration}}" > "${LOG_DIR}/iter${iter}_summary.json"

  return $failed
}

# Main
echo "╔══════════════════════════════════════════════════════════╗"
echo "║  X-Ray Golden Path Test                                 ║"
echo "╠══════════════════════════════════════════════════════════╣"
echo "║  Steps: ${#STEPS[@]} (go to YouTube → search → click → read)       ║"
echo "║  Iterations: ${ITERATIONS}                                          ║"
echo "║  Agentd: ${AGENTD_URL}                            ║"
echo "║  Tab: ${TAB_ID}                                                ║"
echo "║  Logs: ${LOG_DIR}   ║"
echo "╚══════════════════════════════════════════════════════════╝"
echo ""

check_agentd

total_passed=0
total_failed=0
total_start=$(date +%s)

for iter in $(seq 1 "$ITERATIONS"); do
  if run_iteration "$iter"; then
    ((total_passed++))
  else
    ((total_failed++))
  fi
done

total_duration=$(( $(date +%s) - total_start ))

echo ""
echo "╔══════════════════════════════════════════════════════════╗"
echo "║  FINAL RESULTS                                          ║"
echo "╠══════════════════════════════════════════════════════════╣"
echo "║  Iterations: ${total_passed} passed / ${total_failed} failed of ${ITERATIONS}             ║"
echo "║  Total time: ${total_duration}s                                       ║"
echo "║  Logs: ${LOG_DIR}   ║"
echo "╚══════════════════════════════════════════════════════════╝"

# Generate analysis if multiple iterations
if [[ $ITERATIONS -gt 1 ]]; then
  echo ""
  log "Per-step analysis:"
  for i in "${!STEPS[@]}"; do
    step_num=$((i + 1))
    step_pass=0
    step_fail=0
    step_times=()
    for iter in $(seq 1 "$ITERATIONS"); do
      log_file="${LOG_DIR}/iter${iter}_step${step_num}.json"
      if [[ -f "$log_file" ]]; then
        s=$(python3 -c "import json; d=json.load(open('${log_file}')); print(d.get('status',''))" 2>/dev/null || echo "")
        t=$(python3 -c "import json; d=json.load(open('${log_file}')); print(d.get('duration_s',0))" 2>/dev/null || echo "0")
        if [[ "$s" == "completed" || "$s" == "idle" || "$s" == "ready" ]]; then
          ((step_pass++))
          step_times+=("$t")
        else
          ((step_fail++))
        fi
      fi
    done
    if [[ ${#step_times[@]} -gt 0 ]]; then
      avg=$(python3 -c "times=[${step_times[*]}]; print(f'{sum(times)/len(times):.1f}')" 2>/dev/null || echo "?")
      min_t=$(python3 -c "times=[${step_times[*]}]; print(min(times))" 2>/dev/null || echo "?")
      max_t=$(python3 -c "times=[${step_times[*]}]; print(max(times))" 2>/dev/null || echo "?")
      echo "  Step ${step_num}: ${step_pass}/${ITERATIONS} pass | avg ${avg}s (${min_t}-${max_t}s) | \"${STEPS[$i]:0:40}\""
    else
      echo "  Step ${step_num}: ${step_pass}/${ITERATIONS} pass | no timing | \"${STEPS[$i]:0:40}\""
    fi
  done
fi
