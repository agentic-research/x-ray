#!/usr/bin/env bash
# golden_loop.sh — Self-contained test harness for the YouTube golden path.
#
# Manages full lifecycle: start agentd → wait for ready → run golden path →
# capture logs → stop agentd → analyze → repeat.
#
# This is the "iterate and improve" loop. Make a code change, run this,
# see if it helped.
#
# Prerequisites:
#   - Chrome open with X-Ray extension loaded
#   - YouTube open in a tab (the test navigates FROM whatever is open)
#   - GEMINI_API_KEY set
#
# Usage:
#   ./scripts/golden_loop.sh                    # single run
#   ./scripts/golden_loop.sh --runs 3           # 3 runs, compare results
#   ./scripts/golden_loop.sh --runs 3 --tag v1  # tag results for A/B comparison
#   ./scripts/golden_loop.sh --skip-build       # skip go build (faster iteration)

set -euo pipefail

AGENTD_URL="http://localhost:8080"
AGENTD_PID=""
RUNS=1
TAG=""
SKIP_BUILD=false
RESULTS_ROOT="results/golden_loop"
TIMEOUT_PER_STEP=90

# Golden path steps
STEPS=(
  "Go to youtube.com"
  "Search for Minecraft speedruns"
  "Click on the first video in the results"
  "What is the title of this video?"
)

# Parse args
while [[ $# -gt 0 ]]; do
  case "$1" in
    --runs)       RUNS="$2"; shift 2 ;;
    --tag)        TAG="$2"; shift 2 ;;
    --skip-build) SKIP_BUILD=true; shift ;;
    --timeout)    TIMEOUT_PER_STEP="$2"; shift 2 ;;
    *)            echo "Unknown arg: $1"; exit 1 ;;
  esac
done

# Dirs
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
RUN_DIR="${RESULTS_ROOT}/${TIMESTAMP}${TAG:+_${TAG}}"
mkdir -p "$RUN_DIR"

# Colors
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[0;33m'
CYAN='\033[0;36m'; BOLD='\033[1m'; NC='\033[0m'
log()  { echo -e "${CYAN}[$(date +%H:%M:%S)]${NC} $*"; }
ok()   { echo -e "${GREEN}[OK]${NC} $*"; }
fail() { echo -e "${RED}[FAIL]${NC} $*"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }

# Cleanup on exit
cleanup() {
  if [[ -n "$AGENTD_PID" ]] && kill -0 "$AGENTD_PID" 2>/dev/null; then
    log "Stopping agentd (PID $AGENTD_PID)"
    kill "$AGENTD_PID" 2>/dev/null || true
    wait "$AGENTD_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT

# Build
build_agentd() {
  if $SKIP_BUILD; then
    log "Skipping build (--skip-build)"
    return
  fi
  log "Building agentd..."
  go build -o bin/agentd ./cmd/agentd 2>&1 | tee "${RUN_DIR}/build.log"
  ok "Build complete"
}

# Start agentd with demo-video config
start_agentd() {
  local run_log="$1"
  log "Starting agentd..."

  CARTOGRAPHER_MODE=cairn \
  CAIRN_SHEAF=1 \
  CAIRN_CURVATURE=1 \
  NAV_SPEED=fast \
  NAVIGATOR_MODEL="" \
  NAVIGATOR_ENDPOINT="" \
  NAVIGATOR_FORMAT="" \
  XRAY_DEBUG=1 \
  ./bin/agentd > "$run_log" 2>&1 &
  AGENTD_PID=$!

  # Wait for agentd to be ready
  local waited=0
  while [[ $waited -lt 30 ]]; do
    if curl -sf "${AGENTD_URL}/status" >/dev/null 2>&1; then
      ok "agentd ready (PID $AGENTD_PID) in ${waited}s"
      return 0
    fi
    sleep 1
    ((waited++))
    # Check if process died
    if ! kill -0 "$AGENTD_PID" 2>/dev/null; then
      fail "agentd died during startup. Log:"
      tail -20 "$run_log"
      return 1
    fi
  done
  fail "agentd startup timeout (30s)"
  tail -20 "$run_log"
  return 1
}

stop_agentd() {
  if [[ -n "$AGENTD_PID" ]] && kill -0 "$AGENTD_PID" 2>/dev/null; then
    kill "$AGENTD_PID" 2>/dev/null || true
    wait "$AGENTD_PID" 2>/dev/null || true
    log "agentd stopped"
  fi
  AGENTD_PID=""
}

# Run one step through the Doer HTTP API
run_step() {
  local intent="$1"
  local step_num="$2"
  local step_log="$3"
  local start_ts=$(date +%s)

  # Submit
  local resp
  resp=$(curl -sf -X POST "${AGENTD_URL}/doer" \
    -H "Content-Type: application/json" \
    -d "{\"intent\":\"${intent}\",\"tab_id\":0}" 2>&1) || {
    echo "{\"step\":${step_num},\"intent\":\"${intent}\",\"status\":\"submit_failed\",\"duration_s\":0,\"error\":\"${resp}\"}" > "$step_log"
    return 1
  }

  # Poll
  local elapsed=0
  while [[ $elapsed -lt $TIMEOUT_PER_STEP ]]; do
    sleep 2
    elapsed=$(( $(date +%s) - start_ts ))

    local poll
    poll=$(curl -sf "${AGENTD_URL}/status?tab_id=0" 2>/dev/null) || continue

    local status
    status=$(echo "$poll" | python3 -c "import sys,json; print(json.load(sys.stdin).get('status',''))" 2>/dev/null || echo "")

    case "$status" in
      completed|idle|ready)
        local dur=$(( $(date +%s) - start_ts ))
        local summary
        summary=$(echo "$poll" | python3 -c "import sys,json; print(json.load(sys.stdin).get('summary','')[:200])" 2>/dev/null || echo "")
        echo "{\"step\":${step_num},\"intent\":$(python3 -c "import json;print(json.dumps('$intent'))" 2>/dev/null),\"status\":\"pass\",\"duration_s\":${dur},\"summary\":$(python3 -c "import json;print(json.dumps('''$summary'''))" 2>/dev/null || echo '""')}" > "$step_log"
        return 0
        ;;
      failed)
        local dur=$(( $(date +%s) - start_ts ))
        local err
        err=$(echo "$poll" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('error','') or d.get('summary',''))[:200]" 2>/dev/null || echo "unknown")
        echo "{\"step\":${step_num},\"intent\":$(python3 -c "import json;print(json.dumps('$intent'))" 2>/dev/null),\"status\":\"fail\",\"duration_s\":${dur},\"error\":$(python3 -c "import json;print(json.dumps('''$err'''))" 2>/dev/null || echo '""')}" > "$step_log"
        return 1
        ;;
      in_progress)
        local step_desc
        step_desc=$(echo "$poll" | python3 -c "import sys,json; print(json.load(sys.stdin).get('step','')[:60])" 2>/dev/null || echo "")
        printf "\r  ${YELLOW}[%ds]${NC} %s " "$elapsed" "$step_desc"
        ;;
    esac
  done

  local dur=$(( $(date +%s) - start_ts ))
  echo "{\"step\":${step_num},\"intent\":$(python3 -c "import json;print(json.dumps('$intent'))" 2>/dev/null),\"status\":\"timeout\",\"duration_s\":${dur}}" > "$step_log"
  return 2
}

# Run one full golden path
run_golden_path() {
  local run_num="$1"
  local run_subdir="${RUN_DIR}/run_${run_num}"
  local agentd_log="${run_subdir}/agentd.log"
  mkdir -p "$run_subdir"

  echo ""
  echo -e "${BOLD}━━━━━━━━━━━━━━━━━━━━ RUN ${run_num}/${RUNS} ━━━━━━━━━━━━━━━━━━━━${NC}"

  start_agentd "$agentd_log" || return 1

  # Reset between runs
  curl -sf -X POST "${AGENTD_URL}/agent/reset" >/dev/null 2>&1 || true
  sleep 1

  local passed=0
  local failed=0
  local total_time=0
  local run_start=$(date +%s)

  for i in "${!STEPS[@]}"; do
    local step_num=$((i + 1))
    local step_log="${run_subdir}/step_${step_num}.json"
    echo ""
    log "Step ${step_num}/${#STEPS[@]}: ${STEPS[$i]}"

    if run_step "${STEPS[$i]}" "$step_num" "$step_log"; then
      local dur=$(python3 -c "import json; print(json.load(open('${step_log}')).get('duration_s',0))" 2>/dev/null || echo "?")
      ok "Step ${step_num} passed (${dur}s)"
      passed=$((passed + 1))
    else
      local status=$(python3 -c "import json; print(json.load(open('${step_log}')).get('status','?'))" 2>/dev/null || echo "?")
      local err=$(python3 -c "import json; print(json.load(open('${step_log}')).get('error','')[:80])" 2>/dev/null || echo "")
      fail "Step ${step_num} ${status}${err:+: $err}"
      failed=$((failed + 1))
    fi
    echo ""
    sleep 2
  done

  local run_duration=$(( $(date +%s) - run_start ))
  stop_agentd

  # Write run summary
  local summary_file="${run_subdir}/summary.json"
  python3 -c "
import json, glob, os
steps = []
for f in sorted(glob.glob('${run_subdir}/step_*.json')):
    with open(f) as fh:
        steps.append(json.load(fh))
summary = {
    'run': ${run_num},
    'passed': ${passed},
    'failed': ${failed},
    'total': ${#STEPS[@]},
    'duration_s': ${run_duration},
    'steps': steps,
    'tag': '${TAG}',
}
with open('${summary_file}', 'w') as f:
    json.dump(summary, f, indent=2)
" 2>/dev/null

  if [[ $failed -eq 0 ]]; then
    ok "Run ${run_num}: ${passed}/${#STEPS[@]} PASSED in ${run_duration}s"
  else
    fail "Run ${run_num}: ${passed}/${#STEPS[@]} passed, ${failed} failed in ${run_duration}s"
  fi

  return $failed
}

# Final analysis across all runs
analyze() {
  echo ""
  echo -e "${BOLD}╔══════════════════════════════════════════════════════════╗${NC}"
  echo -e "${BOLD}║  ANALYSIS${TAG:+ — tag: ${TAG}}$(printf '%*s' $((45 - ${#TAG})) '')║${NC}"
  echo -e "${BOLD}╚══════════════════════════════════════════════════════════╝${NC}"

  python3 -c "
import json, glob, os

summaries = []
for f in sorted(glob.glob('${RUN_DIR}/run_*/summary.json')):
    with open(f) as fh:
        summaries.append(json.load(fh))

if not summaries:
    print('  No results found')
    exit()

total_runs = len(summaries)
clean_runs = sum(1 for s in summaries if s['failed'] == 0)
total_steps = sum(s['total'] for s in summaries)
total_passed = sum(s['passed'] for s in summaries)
total_duration = sum(s['duration_s'] for s in summaries)

print(f'  Runs: {clean_runs}/{total_runs} clean')
print(f'  Steps: {total_passed}/{total_steps} passed')
print(f'  Total time: {total_duration}s (avg {total_duration/total_runs:.0f}s/run)')
print()

# Per-step breakdown
num_steps = max(s['total'] for s in summaries)
print('  Per-step breakdown:')
print('  ┌─────┬────────────────────────────────────────┬───────┬──────────┐')
print('  │ # ○ │ Intent                                 │ Pass  │ Latency  │')
print('  ├─────┼────────────────────────────────────────┼───────┼──────────┤')
for si in range(num_steps):
    durations = []
    passes = 0
    intent = ''
    for s in summaries:
        if si < len(s['steps']):
            step = s['steps'][si]
            intent = step.get('intent', '')[:38]
            if step['status'] == 'pass':
                passes += 1
                durations.append(step['duration_s'])
    rate = f'{passes}/{total_runs}'
    if durations:
        avg_d = sum(durations) / len(durations)
        min_d = min(durations)
        max_d = max(durations)
        lat = f'{avg_d:.0f}s ({min_d}-{max_d})'
    else:
        lat = 'n/a'
    status = '●' if passes == total_runs else ('◐' if passes > 0 else '○')
    print(f'  │ {si+1} {status} │ {intent:<38} │ {rate:<5} │ {lat:<8} │')
print('  └─────┴────────────────────────────────────────┴───────┴──────────┘')

# Failure details
failures = []
for s in summaries:
    for step in s['steps']:
        if step['status'] != 'pass':
            failures.append(step)
if failures:
    print()
    print('  Failures:')
    for f in failures:
        err = f.get('error', f.get('status', ''))[:60]
        print(f'    Step {f[\"step\"]}: {f[\"status\"]} — {err}')

# Comparison hint
prev_dirs = sorted(glob.glob('${RESULTS_ROOT}/*/'))
prev_dirs = [d for d in prev_dirs if d.rstrip('/') != '${RUN_DIR}']
if prev_dirs:
    prev = os.path.basename(prev_dirs[-1].rstrip('/'))
    print()
    print(f'  Previous run: {prev}')
    print(f'  Compare: diff ${RESULTS_ROOT}/{prev}/run_1/summary.json ${RUN_DIR}/run_1/summary.json')
" 2>/dev/null || warn "Analysis requires python3"

  echo ""
  log "Results: ${RUN_DIR}/"
  log "Agentd logs: ${RUN_DIR}/run_*/agentd.log"
}

# Main
echo -e "${BOLD}X-Ray Golden Loop${NC} — YouTube navigation test harness"
echo "  Runs: ${RUNS}  Tag: ${TAG:-<none>}  Timeout: ${TIMEOUT_PER_STEP}s/step"
echo "  Results: ${RUN_DIR}/"
echo ""

build_agentd

total_clean=0
for run in $(seq 1 "$RUNS"); do
  if run_golden_path "$run"; then
    total_clean=$((total_clean + 1))
  fi
done

analyze

if [[ $total_clean -eq $RUNS ]]; then
  echo -e "${GREEN}${BOLD}ALL RUNS CLEAN${NC}"
else
  echo -e "${RED}${BOLD}${total_clean}/${RUNS} CLEAN${NC}"
fi
