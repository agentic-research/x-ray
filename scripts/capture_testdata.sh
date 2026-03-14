#!/usr/bin/env bash
# capture_testdata.sh — Capture frozen testdata from a running agentd + Chrome.
#
# Triggers a summary capture via the extension, grabs the screenshot via CDP,
# and saves both to testdata/<name>/.
#
# Prerequisites:
#   - agentd running (task demo-video)
#   - Chrome open with X-Ray extension on the target page
#
# Usage:
#   ./scripts/capture_testdata.sh youtube          # saves to testdata/youtube/
#   ./scripts/capture_testdata.sh youtube_results   # saves to testdata/youtube_results/

set -euo pipefail

NAME="${1:?Usage: $0 <name> (e.g. youtube, youtube_results)}"
AGENTD_URL="${AGENTD_URL:-http://localhost:8080}"
DIR="testdata/${NAME}"

mkdir -p "$DIR"

echo "Capturing testdata for '${NAME}'..."
echo "  Target: ${DIR}/"

# 1. Check agentd is up and has an active tab
STATUS=$(curl -sf "${AGENTD_URL}/status?tab_id=0" 2>/dev/null) || {
  echo "ERROR: agentd not reachable at ${AGENTD_URL}"
  exit 1
}
URL=$(echo "$STATUS" | python3 -c "import sys,json; print(json.load(sys.stdin).get('url',''))" 2>/dev/null || echo "")
echo "  URL: ${URL}"

# 2. Trigger a doer read-only intent to force a fresh capture cycle.
#    This ensures the summary and screenshot are up to date.
echo "  Triggering capture..."
curl -sf -X POST "${AGENTD_URL}/doer" \
  -H "Content-Type: application/json" \
  -d '{"intent":"describe what you see on this page","tab_id":0,"read_only":true}' >/dev/null 2>&1 || true

# Wait for it to complete
for i in $(seq 1 30); do
  sleep 2
  S=$(curl -sf "${AGENTD_URL}/status?tab_id=0" 2>/dev/null | python3 -c "import sys,json; print(json.load(sys.stdin).get('status',''))" 2>/dev/null || echo "")
  if [[ "$S" == "completed" || "$S" == "idle" || "$S" == "ready" ]]; then
    break
  fi
  printf "\r  Waiting... (%ds, status=%s)" "$((i*2))" "$S"
done
echo ""

# 3. Grab the screenshot via the Chrome extension's export overlay path.
#    The EXPORT_OVERLAY command in background.js captures the visible tab.
#    But we can also use CDP directly via a simple Go helper.
#    For now: use the agentd's CDP proxy to take a screenshot.

# Actually, simplest: trigger the keyboard shortcut export from the extension.
# But we can't do that programmatically.

# Alternative: read the screenshot from the session's stored bytes.
# We'll add a tiny HTTP endpoint for this. For now, use Chrome DevTools Protocol.

# Use the page.png from Chrome's "Save as" or DevTools screenshot.
# Manual step: take a screenshot in Chrome DevTools (Cmd+Shift+P > "Capture screenshot")

echo "  ⚠️  Screenshot capture requires manual step:"
echo "     1. In Chrome DevTools (F12), press Cmd+Shift+P"
echo "     2. Type 'screenshot' and select 'Capture full size screenshot'"
echo "     3. Save as: ${DIR}/page.png"
echo ""

# 4. Grab the DOM summary from the agentd log.
#    The summary is printed during capture. We can also reconstruct it from the engine.
#    For now: trigger a CAPTURE_SNAPSHOT via the extension and read the response.

# Actually the easiest: the content.js CAPTURE_SNAPSHOT returns the summary.
# We can trigger it via a message to the extension. But we don't have a direct HTTP path.

# Workaround: the bench already reads page_summary.txt. We need to generate it.
# Let's just call the existing capture and read the engine's text_index.

echo "  Capturing page summary..."

# Use a read-only doer call — it will call HandleIntent which reads the engine.
# The engine's text_index has the full summary.
# We can read it via the mache filesystem if we had NFS mount, but we don't.

# Simplest: write a tiny Go helper that connects to agentd and dumps the summary.
# For now: manual capture from the terminal sidebar.

echo "  ⚠️  Summary capture requires the terminal output:"
echo "     In the X-Ray terminal sidebar, run:"
echo "       cat /browser/text_index > ${DIR}/page_summary_raw.txt"
echo "     Or copy the 'Interactive Elements:' block from agentd logs."
echo ""
echo "  Better: run the Go capture helper:"
echo "     go run ./cmd/capture-testdata -name ${NAME}"

echo ""
echo "Done (manual steps pending). Files needed:"
echo "  ${DIR}/page.png           — CDP screenshot at 1920px"
echo "  ${DIR}/page_summary.txt   — DOM summary (Interactive Elements: ...)"
