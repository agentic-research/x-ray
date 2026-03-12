#!/usr/bin/env bash
# Compare extension vs CF Browser backend on a small set of tasks.
#
# Usage:
#   scripts/compare_backends.sh [task_ids]
#
# Examples:
#   scripts/compare_backends.sh 124       # single task
#   scripts/compare_backends.sh 124,125   # multiple tasks
#   scripts/compare_backends.sh shopping  # all shopping tasks (slow!)
#
# Prerequisites:
#   - WebArena containers running
#   - CF Worker deployed (CF_BROWSER_URL set) or running locally (wrangler dev → localhost:8787)
#
# Output: results/compare_<timestamp>/{ext,cf}/ with traces + summary.

set -euo pipefail

SUBSET="${1:-124}"
TIMEOUT="${WEBARENA_TIMEOUT:-600}"
CF_URL="${CF_BROWSER_URL:-http://localhost:8787}"
CF_TOKEN="${CF_BROWSER_TOKEN:-}"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
OUTDIR="results/compare_${TIMESTAMP}"

mkdir -p "$OUTDIR/ext" "$OUTDIR/cf"

echo "=== Backend Comparison ==="
echo "  Tasks:    $SUBSET"
echo "  CF URL:   $CF_URL"
echo "  Output:   $OUTDIR"
echo ""

# Build once.
go build -o bin/agentd ./cmd/agentd
go build -o bin/webarena ./cmd/webarena

# ---------------------------------------------------------------------------
# Run 1: Extension backend (default)
# ---------------------------------------------------------------------------
echo "=== Run 1: Extension backend ==="

rm -f ~/.xray/schemas.db

NAVIGATOR_ENDPOINT="" \
NAVIGATOR_MODEL="${NAVIGATOR_MODEL:-gemini-3.1-flash}" \
CARTOGRAPHER_MODE="${CARTOGRAPHER_MODE:-cairn}" \
WEBARENA_MODE=1 \
  ./bin/agentd &
EXT_PID=$!
sleep 3

WEBARENA_SUBSET="$SUBSET" \
WEBARENA_TIMEOUT="$TIMEOUT" \
WEBARENA_RESULTS_DIR="$OUTDIR/ext" \
  ./bin/webarena 2>&1 | tee "$OUTDIR/ext/run.log" || true

kill "$EXT_PID" 2>/dev/null || true
wait "$EXT_PID" 2>/dev/null || true

echo ""

# ---------------------------------------------------------------------------
# Run 2: CF Browser backend
# ---------------------------------------------------------------------------
echo "=== Run 2: CF Browser backend ==="

rm -f ~/.xray/schemas.db

NAVIGATOR_ENDPOINT="" \
NAVIGATOR_MODEL="${NAVIGATOR_MODEL:-gemini-3.1-flash}" \
CARTOGRAPHER_MODE="${CARTOGRAPHER_MODE:-cairn}" \
WEBARENA_MODE=1 \
CF_BROWSER_URL="$CF_URL" \
CF_BROWSER_TOKEN="$CF_TOKEN" \
  ./bin/agentd &
CF_PID=$!
sleep 3

WEBARENA_SUBSET="$SUBSET" \
WEBARENA_TIMEOUT="$TIMEOUT" \
WEBARENA_RESULTS_DIR="$OUTDIR/cf" \
  ./bin/webarena 2>&1 | tee "$OUTDIR/cf/run.log" || true

kill "$CF_PID" 2>/dev/null || true
wait "$CF_PID" 2>/dev/null || true

echo ""

# ---------------------------------------------------------------------------
# Compare
# ---------------------------------------------------------------------------
echo "=== Comparison ==="

python3 - "$OUTDIR" <<'PYEOF'
import json, sys, os, glob

outdir = sys.argv[1]
ext_dir = os.path.join(outdir, "ext", "traces")
cf_dir = os.path.join(outdir, "cf", "traces")

ext_files = sorted(glob.glob(os.path.join(ext_dir, "*.json")))
cf_files = sorted(glob.glob(os.path.join(cf_dir, "*.json")))

ext_by_id = {}
for f in ext_files:
    d = json.load(open(f))
    ext_by_id[d["task_id"]] = d

cf_by_id = {}
for f in cf_files:
    d = json.load(open(f))
    cf_by_id[d["task_id"]] = d

all_ids = sorted(set(list(ext_by_id.keys()) + list(cf_by_id.keys())))

if not all_ids:
    print("No traces found.")
    sys.exit(0)

print(f"{'Task':>6} | {'Ext':>12} | {'CF':>12} | {'Ext Time':>10} | {'CF Time':>10} | Match")
print("-" * 72)

matches = 0
total = 0
for tid in all_ids:
    e = ext_by_id.get(tid, {})
    c = cf_by_id.get(tid, {})
    e_status = e.get("status", "missing")
    c_status = c.get("status", "missing")
    e_ok = e.get("success", False)
    c_ok = c.get("success", False)
    e_ms = e.get("elapsed_ms", 0)
    c_ms = c.get("elapsed_ms", 0)
    match = "YES" if e_ok == c_ok else "NO"
    if e_ok == c_ok:
        matches += 1
    total += 1
    e_str = f"{'PASS' if e_ok else 'FAIL'} ({e_status})"
    c_str = f"{'PASS' if c_ok else 'FAIL'} ({c_status})"
    print(f"{tid:>6} | {e_str:>12} | {c_str:>12} | {e_ms/1000:>8.1f}s | {c_ms/1000:>8.1f}s | {match}")

print("-" * 72)
print(f"Parity: {matches}/{total} tasks have same outcome ({100*matches/total:.0f}%)")

# Write summary JSON.
summary = {
    "total": total,
    "parity": matches,
    "parity_pct": round(100 * matches / total, 1) if total > 0 else 0,
    "tasks": {
        tid: {
            "ext_success": ext_by_id.get(tid, {}).get("success"),
            "cf_success": cf_by_id.get(tid, {}).get("success"),
            "ext_ms": ext_by_id.get(tid, {}).get("elapsed_ms"),
            "cf_ms": cf_by_id.get(tid, {}).get("elapsed_ms"),
        }
        for tid in all_ids
    },
}
with open(os.path.join(outdir, "comparison.json"), "w") as f:
    json.dump(summary, f, indent=2)
print(f"\nSummary written to {outdir}/comparison.json")
PYEOF
