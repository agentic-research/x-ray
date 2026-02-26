# Investigation Log

## 2026-02-24: Voice Mode Bug Fixes (5 issues)

### Bugs Fixed
1. **Stale voice prompt** — voiceSystemPrompt was a stale fork with `_c/mache-ID` paths and `--- Item N ---` format. Replaced with composition: voice behavioral preamble + shared `navigator.NavigatorSystemPrompt`. Single source of truth.
2. **No scroll in voice** — `SetScrollFunc` was never called for voice sessions. Added `scrollVoice` method on Handler that uses the extension WebSocket (`h.conn`) to send SCROLL commands, same as text mode.
3. **No schema-ready gate** — Voice connected to Gemini Live immediately (good for UX) but tool calls against empty engine returned nothing. Added `<-sess.SchemaReady` channel block before tool execution with 30s timeout. Mic stays hot, tools just wait.
4. **offscreen.js mic_stop** — `MIC_OFF` handler set `recording = false` but never sent `{"type":"mic_stop"}` to trigger `AudioStreamEnd`. Gemini kept waiting for speech.
5. **Nil mutex race** — Early `sendVoiceJSON` call passed `nil` mutex. Moved `var wsMu sync.Mutex` declaration before first use.

### Key Design Decision
Bug 3 uses the "blocking approach" — tool calls hang on the schema channel rather than returning empty results. The voice connection opens immediately (fast UX), but if Gemini fires ls("/") before schema arrives, it blocks until the Cartographer finishes. On timeout, returns an error message asking Gemini to tell the user to wait.

---

## 2026-02-24: Bench CI on arm64 + amd64

Added `bench` job to `.github/workflows/ci.yml` — runs `cmd/bench` on both `ubuntu-latest` (amd64) and `ubuntu-24.04-arm` (arm64). Only fires on push to main and `workflow_dispatch` (not PRs — secrets aren't available from forks). Gracefully skips if `GEMINI_API_KEY` secret is missing.

---

## 2026-02-24: Automated Navigation Benchmark (`cmd/bench`)

### What
Built an automated benchmark that runs the full X-Ray pipeline (Cartographer → Engine → Navigator) against captured testdata snapshots and verifies the correct element gets clicked.

### Design Decisions
- **Schema cached per site** — multiple intents on the same page share one Cartographer call, cutting API costs and latency
- **`mache.ValidateSchema()` gate** — any hallucinated IDs in the schema fail the case immediately rather than producing confusing Navigator failures
- **Generator-agnostic** — respects `NAVIGATOR_ENDPOINT`/`NAVIGATOR_MODEL`/`NAVIGATOR_FORMAT` env vars so the same benchmark works for Gemini cloud and local Qwen/Gemma models
- **Iteration count estimated from latency** — Navigator doesn't expose iteration count externally; rough estimate (1s/iter) is sufficient for the benchmark table

### Test Cases
5 cases across 2 sites (hackernews ×3, lobsters ×2). Each verifies exact `mache_id` match. Expected IDs derived from `page_summary.txt` content — e.g., hackernews mache-11 = "Timeframe" (first story link).

### Files
- `cmd/bench/main.go` — benchmark runner (~190 lines)
- `testdata/bench_cases.json` — 5 test case definitions
- `Taskfile.yml` — added `bench` task

---

## 2026-02-24: Voice Offscreen Restoration + Mic Permission Fix

### Problem
Voice session via offscreen document was broken — `getUserMedia()` in offscreen.js failed with "Permission dismissed" because Chrome MV3 offscreen documents have no visible UI surface to show the browser's mic permission prompt.

### Root Cause
Chrome MV3 offscreen documents cannot trigger permission prompts. The `getUserMedia()` call fails immediately with `NotAllowedError: Permission dismissed` because there's no tab/popup/window attached to the offscreen context.

### Key Discovery
All `chrome-extension://` contexts (popup, options page, offscreen document) share the **same origin**. A mic permission grant in any visible context persists to all other contexts including offscreen. The popup has a visible UI and runs in a user-gesture context when buttons are clicked — making it the right place to request mic permission.

### Fix (revised)
Popup `getUserMedia()` also fails silently (no Chrome prompt shown in extension popup). Final approach:
1. `startSession()` in background.js checks `navigator.permissions.query({name: 'microphone'})`
2. If not granted, opens `mic-setup.html` as a standalone popup window via `chrome.windows.create()`
3. User clicks "Grant Mic Access" in the popup window — Chrome shows the real permission dialog
4. On grant, sends `MIC_GRANTED` message to background, which retries `startSession()`
5. MV3 CSP requires external JS file (`mic-setup.js`) — inline scripts are blocked

### Additional Improvements
- `pendingSnapshots` Set prevents duplicate Cartographer runs (popup auto-snapshot + user click)
- `SCHEMA_READY_EVENT` broadcast fixes popup status stuck on "Generating schema..."
- `TOGGLE_MIC` auto-starts session if none exists (no more "click Snapshot first" error)
- `voice.go`: non-blocking schema wait + audio throughput logging every 5s
- `voice.html`: `?tab=` parameter for standalone testing

### Files Changed
- `ext/background.js` — pendingSnapshots, SCHEMA_READY_EVENT, TOGGLE_MIC auto-start
- `ext/popup.js` — SCHEMA_READY listener, pending check, mic permission pre-grant
- `internal/api/voice.go` — non-blocking schema, audio logging
- `static/voice.html` — tab parameter support

---

## 2026-02-24: Content Script Not Loaded After Extension Reload

### Problem
After reloading the extension, clicking Snapshot gets stuck at "Capturing..." forever. Agentd logs show the WebSocket connects but no DOM_SNAPSHOT arrives.

### Root Cause
`chrome.tabs.sendMessage(tabId, { type: 'CAPTURE_SNAPSHOT' })` throws "Could not establish connection. Receiving end does not exist." because Chrome MV3 content scripts only auto-inject on **new** page loads. Tabs that were already open when the extension was reloaded/installed don't have `content.js`.

Additionally, `pendingSnapshots` was never cleared on failure, so subsequent snapshot attempts for that tab were silently deduped (the guard `if (pendingSnapshots.has(tabId)) return` fired).

### Fix
`captureAndSend()` now catches the sendMessage error, injects `content.js` via `chrome.scripting.executeScript()`, waits 200ms for initialization, then retries. All error paths clear `pendingSnapshots.delete(tabId)` so the tab isn't permanently stuck.

---

## 2026-02-24: Text Input on Voice WebSocket (Same-Session Corrections)

### Problem
Voice said "click the third page" — Navigator interpreted "page" as pagination instead of story #3. User wanted to type a correction ("click the third article") into the same Gemini Live session without re-speaking.

### Key Discovery
Gemini Live API's `SendClientContent` accepts text input on an active audio session. Text and audio share the same conversation context — tool history, schema, everything. This means you can mix voice and text freely within one session.

### Implementation
- `voice.go`: TextMessage handler now parses `voiceMessage` struct instead of anonymous struct. New `text_input` case calls `session.SendClientContent()` with the typed text.
- `voice.html`: Added text input row (input + Send button). Enabled when session is ready. Enter key or click sends `{"type":"text_input","text":"..."}` over the existing WebSocket. Spacebar PTT shortcut skipped when text input is focused.

### Implication
This unblocks testing the full voice pipeline without talking — type commands instead of speaking. Same tool loop, same schema, same session.

---

## 2026-02-24: CLI Daemon Pivot - Native Audio (sox)

### What
Began the pivot from Chrome Extension WebAudio to a native OS-level Go daemon for voice interactions.

### Why
Chrome Manifest V3 and browser audio sandbox (requiring offscreen documents and active permissions) is fragile and causes latency/state loss. Moving audio to the Go daemon via `os/exec` wrapping `sox` commands (`rec` and `play`) provides instant, system-level microphone and speaker access without browser limitations.

### Implementation
- Added `internal/audio/audio.go` which uses `sox` to capture 16kHz PCM audio for Gemini Live input and play 24kHz PCM audio from Gemini Live output.
- `Available()` function checks if `sox` is installed on the host machine.

---

## 2026-02-25: Full Codebase Review (4 categories)

### Findings

**1. Duplicated Code (6 instances)**
- `Mount` + `CartographerOutput` structs re-declared in `cmd/warm/main.go` and `cmd/gate/main.go` instead of importing from `internal/mache` / `internal/cartographer`
- `systemPrompt` + `getSchemaDefinition()` + `validateSchema()` copy-pasted into `cmd/warm/main.go` from internal packages
- `TabInfo` struct identical in `internal/api/messages.go` and `internal/navigator/agent.go`
- Voice session setup (`LiveConnectConfig`, tool definitions, tool dispatch switch) duplicated between `HandleVoice` and `StartVoiceLoop` in `internal/api/voice.go`
- CSS selector resolution block duplicated between SCROLL and RESOLVE_SELECTORS handlers in `ext/content.js`
- `buildNavGenerator()` in `cmd/bench/main.go` duplicates generator selection from `cmd/agentd/main.go`

**2. Bad Patterns**
- **Data race**: `Doer.SetResultNotifyFn` / `SetActionNotifyFn` write function pointers without mutex while the Doer goroutine reads them
- **Disk leak**: `saveLog()` in websocket.go writes to `x-ray-logs/` on every snapshot with no rotation or cleanup
- **O(n²)**: `appendUnique()` in engine.go is O(n) per call, used in loops → quadratic for large pages
- **No graceful shutdown**: `select {}` in main.go with no signal handling

**3. Design Anti-Patterns**
- 5-place tool registration (noted in interfaces.go TODO) — voice layer re-declares tool definitions separately
- Giant inline system prompt const (60+ lines) — better as `//go:embed`
- Time.Sleep polling in doer_test.go — should use channel signaling
- `TestDoerGotoTimeout` tests context cancellation, not the actual 30s timeout

**4. Fake Tests**
- `TestAvailable` (audio_test.go) — no assertion, just logs
- `TestOllamaIntegrationToolFormatDiagnostic` (model_test.go) — self-described "not a pass/fail test"
- `TestVoiceWaitingForSchema` / `TestVoiceConnectsWithSchema` (voice_test.go) — test Engine construction, not voice behavior
- `TestVoiceMessageJSON` (voice_test.go) — tests `json.Marshal` on a tagged struct (stdlib behavior)

### Fixes Applied

1. **Data race** — Renamed `cancelMu`→`mu` in Doer, extended to protect `resultNotifyFn`/`actionNotifyFn` setters and readers (copy-under-lock pattern). Verified with `go test -race`.
2. **TabInfo dedup** — `api.TabInfo` replaced with type alias to `navigator.TabInfo`. Eliminated field-by-field copy in `doer.go:wireNavigatorCallbacks`.
3. **cmd/warm dedup** — Added `CSSSelector` field to `mache.Mount` (omitempty). `cmd/warm` now imports `mache.Mount`/`mache.CartographerOutput` instead of re-declaring. `systemPrompt`, `validateSchema`, `getSchemaDefinition` left separate (intentionally different for URL-based pre-warming).
4. **Voice dedup** — Extracted `buildLiveConfig()` helper in voice.go. Tool dispatch switch left separate (intentional difference: `doer` vs `resolveDoer()`).
5. **Fake tests** — Deleted `TestAvailable`. Renamed `TestVoiceWaitingForSchema`→`TestSessionFreshHasNoSchema`, `TestVoiceConnectsWithSchema`→`TestSessionApplySchemaWorks`, `TestDoerGotoTimeout`→`TestDoerGotoCancellation`.
6. **saveLog guard** — Made opt-in via `XRAY_SAVE_LOGS=1` env var.

### Corrections from Initial Review
- `cmd/gate/main.go` does NOT duplicate types (already imports from internal packages)
- `cmd/warm/main.go`'s `systemPrompt` and `validateSchema` are intentionally different (URL context vs DOM+screenshot), not duplicates
- Voice tool dispatch switch has an intentional difference (`doer` vs `resolveDoer()` for tab-switching support)
