# Handoff: Cloudflare Browser Rendering Backend

## What Was Built

A `BrowserBackend` interface + Cloudflare Worker + Go HTTP client that lets x-ray
drive headless Chromium on CF's edge instead of requiring a local Chrome + extension.
This is the "CF mode" — the extension path remains untouched for interactive use.

### Architecture

```
Extension mode:  agentd → WebSocket → background.js → content.js + CDP → local Chrome
CF mode:         agentd → HTTP       → CF Worker     → Puppeteer       → CF Chromium
```

### Files Added

| File | Purpose |
|------|---------|
| `internal/api/browser_backend.go` | `BrowserBackend` interface (10 methods) |
| `internal/cfbrowser/client.go` | Go HTTP client implementing the interface |
| `internal/cfbrowser/client_test.go` | 10 unit tests (all passing) |
| `deploy/cf-worker/src/index.ts` | Worker entry: routes to Durable Objects |
| `deploy/cf-worker/src/session.ts` | Durable Object: Puppeteer browser + page lifecycle |
| `deploy/cf-worker/src/content-bridge.ts` | content.js registry logic injected via `page.evaluate()` |
| `deploy/cf-worker/src/ax-mapper.ts` | CDP AX tree → enriched summary lines |
| `deploy/cf-worker/wrangler.toml` | CF deployment config |
| `scripts/cf_tunnel.sh` | cloudflared quick tunnel launcher (less useful — see below) |

### Files Modified

| File | Change |
|------|--------|
| `internal/api/capture.go` | Backend-aware capture path (`captureViaBackend`) |
| `internal/api/doer.go` | Backend-aware goto/rescan/action/scroll dispatch |
| `internal/api/websocket.go` | `backend` field + getter/setter |
| `internal/api/planner.go` | `createBackendSession`, backend branch in `HandleAgentTask` |
| `internal/config/config.go` | `CFBrowserConfig` struct, env vars `CF_BROWSER_URL`/`CF_BROWSER_TOKEN` |
| `cmd/agentd/main.go` | Wire backend from config |
| `cmd/webarena/main.go` | Site URLs overridable via `WEBARENA_*_URL` env vars |
| `cmd/webarena/eval.go` | `WEBARENA_RESULTS_DIR` override |
| `Taskfile.yml` | `cf-install`, `cf-dev`, `cf-deploy`, `cf-smoke`, `cf-run`, `cf-webarena` tasks |

## What Works

1. **Session lifecycle**: Create → navigate → capture → action → close
2. **Content script injection**: Registry builds correctly on CF Chromium pages
3. **Summary/screenshot/AX tree/page text**: All endpoints return data
4. **MutationObserver DOM settling**: After click actions, waits for 500ms mutation silence (max 5s)
5. **ngrok interstitial bypass**: Worker auto-clicks "Visit Site" on ngrok free-tier pages
6. **Cookie injection**: Pre-auth cookies work (WebArena login flow)
7. **Go client**: Full test coverage, compile-time interface check

## What Doesn't Work Yet

### 1. CF Worker 1101 Errors (Primary Blocker)

The Durable Object frequently dies with HTTP 1101 (Worker exceeded CPU/memory limits).
This happens on:
- Heavy Magento pages (search results, product pages with many elements)
- The `/summary` endpoint (content.js registry walk is CPU-intensive)
- The `/ax-tree` endpoint (CDP `DOM.querySelectorAll` + `DOM.describeNode` per node)

**Root cause**: CF Workers have a 30s CPU time limit per request (paid plan).
The content registry walk + AX tree enrichment can exceed this on complex pages.

**Possible fixes**:
- Paginate the AX tree enrichment (batch `DOM.describeNode` calls)
- Skip AX enrichment initially (use summary-only mode)
- Increase Worker CPU limit via CF Enterprise
- Reduce content.js registry scope (fewer elements)

### 2. Summary Returns 0 Elements

On product pages that successfully load, `requestSummary` sometimes returns
`{elements: []}`. The content script's `__xrayBuildRegistry()` runs but the
registry is empty, possibly because:
- The page hasn't fully rendered when the registry runs
- Magento uses lazy-loading JS that hasn't executed
- The visibility check (`getComputedStyle` → `display:none`) filters too aggressively

### 3. Reviews Don't Appear After Tab Click

WebArena task 21: "Get name(s) of reviewer(s) who mention ear cups being small."
After clicking the "12 Reviews" tab, review content doesn't appear in the schema.

**What we verified**:
- Page text shows "12 Reviews" link text on initial load
- MutationObserver settling is deployed (500ms silence or 5s max)
- The page text endpoint (`/page-text`) returns page content correctly

**What we didn't verify** (session died before we could):
- Whether review HTML is actually in the DOM after click (vs loaded via AJAX)
- Whether reviews are filtered by visibility checks
- Whether the click targets the right element (tab control vs link)

### 4. Task 21 Timed Out With 0 Turns

The background eval run (`PLANNER_MODEL=gemini-2.5-pro`) timed out at 300s with
no Planner turns recorded. Likely cause: schema never became ready (1101 crash
during initial capture) → Planner's 30s schema wait expired → proceeded with
empty schema → Navigator had nothing to work with → timeout.

### 5. Gemini 2.5-flash Ghost 429s

`gemini-2.5-flash` returns `RESOURCE_EXHAUSTED` (429) from Go genai library even
when quota is available. Known Gemini bug since Dec 2025 (p0). Workaround:
`PLANNER_MODEL=gemini-2.5-pro`.

## Networking: CF Chromium → WebArena Docker

CF Chromium **cannot reach localhost**. WebArena Docker services run locally.
The bridge is **ngrok tunnels** (free tier works with interstitial bypass).

### What We Tried (and What Failed)

| Approach | Result |
|----------|--------|
| `*.trycloudflare.com` quick tunnels | `ERR_BLOCKED_BY_ADMINISTRATOR` — CF blocks its own tunnel domains |
| Named cloudflared tunnel (`*.q-q.dev`) | Same — CF blocks ALL CF-proxied domains |
| `page.setExtraHTTPHeaders()` | Triggers CF security policy even on allowed domains |
| **ngrok tunnels** | **Works** — `*.ngrok-free.dev` URLs are accessible from CF Chromium |

### Magento base_url Requirement

After ngrok tunnels are up, Magento's `base_url` must be updated or it 302-redirects
to `http://localhost:7770`:

```sql
-- Run inside the Magento Docker container:
UPDATE core_config_data SET value = 'https://YOUR-NGROK-URL.ngrok-free.dev/'
WHERE path IN ('web/unsecure/base_url', 'web/secure/base_url');
-- Then flush cache:
php bin/magento cache:flush
```

**Remember to revert this when done**, or local extension-mode evals will break.

## How to Run

```bash
# 1. Start WebArena Docker services
task webarena-up

# 2. Start ngrok tunnels (one per service)
ngrok http 7770   # shopping
ngrok http 7780   # shopping_admin
ngrok http 9999   # reddit
# ... etc

# 3. Update Magento base_url (see above)

# 4. Deploy CF Worker (if not already)
task cf-deploy

# 5. Run eval
CF_BROWSER_URL="https://xray-browser.YOUR-ACCOUNT.workers.dev" \
WEBARENA_SHOPPING_URL="https://YOUR-NGROK.ngrok-free.dev" \
PLANNER_MODEL="gemini-2.5-pro" \
WEBARENA_MODE=1 \
WEBARENA_SUBSET=21 \
go run ./cmd/webarena

# Or use the Taskfile:
task cf-webarena
```

## CF Limits (Workers Paid, $5/mo)

| Resource | Limit |
|----------|-------|
| Concurrent browsers | 30 |
| New browsers/min | 30 |
| Free hours/month | 10 |
| Additional cost | $0.09/hr |
| CPU per request | 30s |
| Worker memory | 128 MB |

## Next Steps (Priority Order)

1. **Fix 1101 crashes**: Profile CPU usage of registry + AX enrichment. Consider
   splitting into smaller requests or skipping AX on first pass.
2. **Verify reviews DOM**: Use a manual session to check raw HTML after clicking
   the reviews tab — determine if it's a DOM issue or a visibility filter issue.
3. **Add session cleanup to eval**: The eval harness should call `CloseSession`
   after each task to avoid orphaned browsers consuming quota.
4. **Retry logic in Go client**: 1101 errors are transient; a single retry with
   backoff would help.
5. **Consider paid ngrok**: The free-tier interstitial adds latency and complexity.
   A paid ngrok plan ($8/mo) gives static domains and no interstitial.
