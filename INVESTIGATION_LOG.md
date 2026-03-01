# Investigation Log

## 2026-02-28: Navigator Fine-Tuning — JSON → CLI Format Pivot

### Context
Fine-tuning FunctionGemma 270M (`google/functiongemma-270m-it`) as a local Navigator model for the mache VFS. Two-stage pipeline: SFT (teach format) → DPO (refine preferences). 800 training examples on M3 Max (MPS backend).

### Problem: JSON Overhead on 270M Model
Original training targets were full JSON tool calls:
`{"name": "act", "parameters": {"action": "click", "path": "/browser/main/feed/_c/3"}}`

For a 270M parameter model, ~80% of generation tokens went to syntax (`{`, `"`, `:`, `}`) rather than the actual decision (which path, which action). Model achieved 98.3% token accuracy on SFT but showed path construction errors and intent classification mistakes at inference — attention budget wasted on bracket balancing.

### Key Discovery: MPS float16 Inference Bug
Training in float16 on MPS works fine. But float16 *inference* on MPS causes complete logit collapse — model outputs nothing but pad tokens (ID 0). This affects even the base FunctionGemma model (not a fine-tuning issue). **Fix: always use float32 for inference on MPS.**

### Solution: CLI Format
The Navigator tools ARE filesystem commands (`ls`, `cat`, `act`). Dropped JSON, switched to space-delimited CLI syntax:
- `act click /browser/main/feed/_c/3`
- `ls /`
- `act type /iterm/windows/0/tabs/1 "git status"`
- `browser.goto https://github.com`

**Measured savings:**
- System prompt: 8,177 → 438 chars (95% reduction)
- Response chars: 146,273 → 59,173 across dataset (60% reduction)
- Every token now goes to the actual navigational decision

### Files
- `experiments/cli-ft/compress_dataset.py` — converts JSON dataset to CLI format
- `experiments/cli-ft/finetune_dpo.py` — two-stage SFT+DPO pipeline
- `experiments/cli-ft/test_inference.py` — inference test harness
- `experiments/cli-ft/dpo_dataset_cli.jsonl` — CLI-format training data (800 examples)

### GBNF Constrained Decoding (implemented)
Even with CLI format, a 270M model can hallucinate paths (`act click /browser/nonexistent_button`). Solution: dynamically generate a GBNF grammar from the live mache filesystem state and pass it to the inference server (llama.cpp / Ollama). The grammar constrains the model to only emit paths that physically exist in the VFS.

**Architecture:**
1. `EnumeratePaths(fs)` walks the mache VFS recursively (max depth 8)
2. `BuildGBNF(paths, excludeAct)` generates grammar with paths as alternatives in `valid-path` rule
3. Grammar sent as `"grammar"` field in request body before each GenerateContent call
4. `ParseCLICommand()` auto-detects CLI format; falls back to JSON regex for backward compat
5. `CLIMode` flag on GemmaGenerator controls prompt format, history format, and grammar activation
6. Grammar skipped in readOnly mode (model needs free text for info responses)

**Key design decision:** grammar only applied in non-readOnly (action) mode. ReadOnly queries need free text output which can't be easily constrained. Action mode is where path hallucination matters.

### Files (Go)
- `internal/navigator/grammar.go` — `BuildGBNF()`, `EnumeratePaths()`
- `internal/navigator/model.go` — `ParseCLICommand()`, `FunctionCallToCLI()`, CLIMode support
- `internal/navigator/cli_test.go` — 16 tests (CLI parsing, round-trips, grammar, integration)
- `internal/navigator/agent.go` — grammar wiring in HandleIntent loop
- `cmd/agentd/main.go` — `NAVIGATOR_CLI=1` env var

### Next Steps
- Retrain with CLI-format dataset (SFT ~27min, DPO ~30min)
- Verify GBNF grammar works with Ollama's native API (OpenAI-compat endpoint may ignore `grammar` field; may need `/api/chat` endpoint)
- Evaluate whether 270M is sufficient or if larger model needed for path construction quality

---

## 2026-02-27: Phase 7b — Palette Redesign + Crop-to-Uncolored

### Problem: Tight Palette Separation
Original palette had YELLOW(255,220,0) and ORANGE(255,165,0) at distance 55 (dist²=3025). With `maxDistSq=3600` (dist~60), the decision boundary was razor-thin. Alpha-blended fills could land in the ambiguous zone.

### Solution: RGB Cube Vertices
Replaced all 6 colors with non-black/non-white corners of the RGB cube: MAGENTA(255,0,255), LIME(0,255,0), CYAN(0,255,255), YELLOW(255,255,0), BLUE(0,0,255), RED(255,0,0). Every pair differs by exactly 255 in at least one channel. Min pairwise dist² jumped from 3025 to 65025. Dropped `maxDistSq` from 3600 to 900 — conservative enough for JPEG artifacts, strict enough to reject all web content colors.

### Problem: Mask Boundary Artifacts
Setting overlay pixels to gray(128) before Canny created sharp transitions at mask edges that Canny detected as new false edges. On synthetic images, masking produced equal or MORE regions than unmasked. The production benefit only appeared with alpha-blended fills over varied page content.

### Solution: Crop-to-Uncolored
Instead of masking, find connected components of non-overlay pixels via 4-connected flood-fill, crop the original image to each uncolored region, and run Canny independently on each crop. Results are offset back to full-image coordinates and globally merged.

Key details:
- **FindUncoloredRegions**: 4-connected (not 8) flood-fill on `m.Data[i] < 0` pixels. Min size filter: 100x100. Cap: 20 largest regions by area.
- **6px crop padding**: Prevents Gaussian blur (5x5 kernel) from generating spurious edges at crop boundaries. Padding extends slightly into overlay territory, which can create a thin boundary edge at the overlay/content border — acceptable since it's a real color transition.
- **Implicit coverage gate**: No uncolored region >= 100x100 → zero Canny runs. Replaces the explicit `CoverageRatio() > 0.70` gate.
- **MaskImage/Dilate**: Kept in colormap.go (exported methods) but no longer called in production.

### cv-N Annotation Recolor
CYAN(0,255,255) became a palette color (input). Changed cv-N annotation boxes from cyan to white to avoid palette collision. Changed cv-N summary label from `Color: CYAN` to `Color: CV`.

### Crop Boundary Edge Observation
The 6px padding extends the crop into the overlay border zone. At the exact overlay/content boundary, Canny sees a real color transition (blue→white) and produces a thin 1-2px wide detection. This is visible as `cv-0: norm=(0.398, 0.003, 0.005, 0.995)` — a nearly full-height, ~2px wide edge at the overlay boundary. This is technically correct (it IS a real edge in the image) and doesn't affect downstream Cartographer behavior since it's filtered by the overlap check against existing DOM bounds.

---

## 2026-02-27: Voice Enter Key Wiring

### Problem
Pressing Enter on an empty popup text field did nothing (`if (!intent) return;` early exit). The voice UI at `static/voice.html` existed but had no trigger from the popup.

### Solution
- `popup.js`: Empty Enter now calls `openVoiceUI()` which sends `OPEN_VOICE` message to background, then closes the popup.
- `background.js`: New `OPEN_VOICE` handler queries the active tab and opens `voice.html?tab=<tabId>` in a new Chrome tab.
- `popup.html`: Placeholder updated to hint the voice shortcut.
- No server changes — `voice.go` already reads `?tab=` param and `voice.html` auto-connects on load.

---

## 2026-02-27: Phase 7 — Overlay Color Readback (Visual Steganography Protocol)

### The Problem
Extension draws semantic color overlays on each page before screenshot capture. The server **ignored the pixel data** and read affordance from the `Color: BLUE` text string in the DOM summary. Meanwhile, Canny edge detection ran on this screenshot and re-detected overlay borders as cv-regions — producing 118 false positives on Reddit.

### Key Insight: Overlays Are a Deterministic Protocol
The overlay colors are a **deterministic protocol encoded into the pixel buffer** — visual steganography. Reading them back on the Go server closes the loop: pixel-level ground truth for element positions and affordance types, with zero computer vision.

### Extension Changes (PNG + Opacity)
- **PNG capture**: Switched CDP `Page.captureScreenshot` and all `captureVisibleTab` fallbacks from JPEG to PNG. Lossless compression preserves exact overlay RGB values — no DCT block blur.
- **100% border opacity**: Changed overlay borders from `rgba(..., 0.9)` to `rgb(...)` so border pixels are exact palette values in the screenshot.
- **15% minimum fill opacity**: Raised from 5% so even large structural containers have detectable color fill. Formula: `Math.max(0.15, 0.6 - areaRatio * 0.45)`.

### Overlay Classification (`colormap.go`)
Nearest-neighbor pixel classification with configurable squared Euclidean distance threshold. For each pixel, find closest palette color; accept if within `maxDistSq`, reject otherwise.

### Canny Pipeline
Format-agnostic decode (PNG/JPEG via magic bytes). Pre-decoded image pass-through to avoid double decode. Crop-to-uncolored replaces masking (see Phase 7b above).

### Double Decode Elimination
Review caught that `snapshot.go` decoded the screenshot once for `ClassifyOverlay`, then `DetectCanvasRegions` decoded it again. Added `decodedImg image.Image` parameter — when non-nil, skips internal decode. Snapshot pipeline now decodes once and passes the `image.Image` to both consumers.

### Tolerance Tradeoff
With cube-vertex palette, `maxDistSq=900` works for borders AND fills. Fills at 15-60% opacity are alpha-blended with background — cube vertices have such extreme channel values that even heavily blended fills remain within distance 30 of their nearest vertex.

---

## 2026-02-27: Phase 6 — Partial Zone Regeneration + websocket.go Decomposition

### The Problem
When any cached zone was stale (its mache_ids disappeared from the DOM), the system fell through to **full** Cartographer regeneration — even if only 1 of 5 zones changed. This wasted API calls and added 10-15s latency for minor page updates (e.g., a feed refresh while header/footer remain stable).

### Partial Regeneration Architecture
For each stale zone independently:
1. **Crop screenshot** to zone's bounding box (`mache.CropScreenshot`) — Cartographer sees only the relevant region
2. **Filter DOM summary** to elements overlapping the zone (`mache.FilterSummaryByBounds` with 0.05 margin) — eliminates irrelevant elements from the prompt
3. **Run Cartographer** on the cropped inputs — generates schema for just that zone
4. **MergeSchema** into the existing engine — fresh zones untouched, stale zones replaced

Decision logic in `handleDOMSnapshot`:
- **0 stale zones**: cache hit, skip Cartographer entirely
- **Some stale, some fresh**: partial regen (N small Cartographer calls)
- **All stale**: fall through to full regen (single large call is cheaper than N separate calls)
- **Partial regen fails** (hallucinated IDs, engine merge error): fall through to full regen as safety net

### websocket.go Decomposition (969 → 634 lines)
Extracted from the monolith:
- `session.go` (99 lines): `TabSession` struct, `pendingAction`, `DOMUpdate`, session methods
- `snapshot.go` (287 lines): `handleDOMSnapshot`, `resolveAndFinalize` (shared helper for full + partial paths), `countCachedZones`
- `partial.go` (199 lines): `RegenerateStaleZones` orchestrator, `attemptPartialRegen`, `extractStaleZoneInfos`

Key extraction: `resolveAndFinalize` was pulled out as a shared helper used by both the full-scan and partial-regen code paths — resolves CSS selectors, loads children, signals schema ready, sends SCHEMA_READY message.

### Summary Filtering by Bounds
`FilterSummaryByBounds` parses each line's `Bounds: [x,y,w,h]` and checks overlap with the zone region (expanded by `margin` in all directions, clamped to [0,1]). Non-ID header lines ("Interactive Elements:") are preserved. ID lines without Bounds are excluded (can't determine spatial relevance).

### Screenshot Cropping
`CropScreenshot` decodes JPEG, computes pixel rect from normalized bounds, draws sub-image, re-encodes. Nil input returns nil (no-op). Zero-area returns error. Out-of-bounds coordinates clamped.

### Test Coverage Added
- 11 tests for `FilterSummaryByBounds` (overlap, partial, none-inside, margin, format round-trip)
- 6 tests for `CropScreenshot` (full region, quarter, clamp, zero-area, invalid JPEG, nil)
- 9 tests for `MergeSchema` (previously zero coverage — basic, parent eviction, prefix-safety, concurrent, preserves children)
- 9 tests for SchemaCache zone ops (PutZones, InvalidateZone, GetAllZones, SQLite persistence)
- 9 tests for partial regen orchestrator (single stale, all stale, error, cropped inputs, zero bounds)
- 4 E2E WebSocket tests (partial regen single zone, all stale, cache update, cache hit after partial)

Total: 48 new tests, all passing with `-race`.

### Cross-Package Test Helper Gotcha
Initially tried to export `makeTestJPEG` from `mache/crop_test.go` via a `testhelper_test.go` file — Go's `_test.go` files are package-private even within the same module. Duplicated the helper locally in `api/partial_test.go` instead.

---

## 2026-02-27: Sheaf-Based Schema Cache

### The Problem (Three Bugs Converging)
1. Schema cache was one opaque JSON blob per URL — any code change (e.g., cursor:pointer heuristic) served stale cached schemas until you manually `rm ~/.xray/schemas.db`.
2. Magnifying glass rescan of `/main/player` overwrote the full-page cache with just the sub-zone JSON. Next cold load returned only sub-zones.
3. `MergeSchema` appended sub-mounts without evicting the parent mount — old `/main/player` coexisted with new `/main/player/controls`.

### Sheaf Theory → Cache Architecture
Modeled the cache as a **sheaf over spatial open sets**:
- Each zone is a section with its own bounding box (AABB from constituent elements), content fingerprint (sha256 of sorted `(tag, text[:30])` pairs), and cache entry.
- **Restriction maps**: when magnifying glass refines a zone, `MergeSchema` evicts the parent mount (presheaf condition) and `InvalidateZone` removes the parent cache entry.
- **Per-zone staleness**: `ValidateSchemaZones` returns `map[zonePath]reason` instead of a flat ID list. Foundation for partial regeneration (Phase 6).
- **v1/v2 backward compat**: `Get()` checks for `meta/version=2` marker; old monolithic blobs still load. Auto-migrate on next `PutZones`.

### Fingerprint Design Decision
Critical: fingerprints hash `(tag, text[:30])` NOT `(mache_id, text)`. Mache-IDs are temporal — `idCounter` resets per page load and element order can shift. Tag+text is reload-stable: identical DOM → identical fingerprint.

### Zone Bounding Box
Added `computeZoneBounds` — min/max AABB over constituent element bounds. Stored on `Mount.Bounds [4]float64`. This defines the spatial "open set" for each sheaf section and enables future partial regeneration (restrict Cartographer to stale zones' bounding boxes).

---

## 2026-02-27: Vercel agent-browser Comparison + cursor:pointer Heuristic

### Vercel agent-browser Architecture (Surprising Findings)
Reverse-engineered `vercel-labs/agent-browser` v0.15.1. Key surprise: **Vercel does NOT use `DOMSnapshot.captureSnapshot`** or raw CDP for DOM extraction. Their snapshot pipeline is entirely built on Playwright's `locator.ariaSnapshot()` — a high-level abstraction that returns an ARIA tree as structured text. They assign `eN` ref IDs to interactive/content nodes and resolve them back to DOM via `page.getByRole()`.

Their architecture is Rust CLI → Unix socket IPC → Node.js daemon → Playwright → Chromium. No direct CDP snapshot commands.

### X-Ray vs Vercel: Where We're Stronger
- **Structural containers**: X-Ray's 3-phase registry captures layout hierarchy (nav, section, article, semantic divs). Vercel has nothing equivalent — their output is flat ARIA.
- **Eager spatial data**: X-Ray computes normalized [0,1] bounding boxes for every element at snapshot time. Vercel fetches boxes lazily on-demand.
- **Semantic fiber data**: X-Ray extracts fontSize, display, textDensity, interactive per element. Vercel: role + name only.
- **AX enrichment**: X-Ray uses CDP `Accessibility.getFullAXTree` with `backendNodeId` joins. Vercel: opaque Playwright abstraction, no DOM↔AX correlation.

### The One Gap: cursor:pointer Heuristic
Vercel's `findCursorInteractiveElements()` catches styled `<div>`s that are behaviorally interactive but lack semantic HTML/ARIA. Checks: `getComputedStyle(el).cursor === 'pointer'`, `el.hasAttribute('onclick')`, positive `tabindex`. Key dedup: inherited cursor:pointer from parent is skipped (parent gets tagged instead).

Reimplemented as Phase 1.5 in `buildRegistry()` — runs after Phase 1 (semantic selectors), before Phase 2 (structural containers). Capped at 50 elements to prevent pathological pages from flooding the 300-element summary cap. Updates `interactiveAncestors` map so Phase 2 thresholds reflect cursor-interactive elements.

### Phase 1 Selector Expansion
Also added `[role="link"]`, `[role="tab"]`, `[contenteditable="true"]` to Phase 1 selector — these were in the original design intent but missing from the implementation.

---

## 2026-02-27: Phase 3 — macOS AXUIElement Integration

### Architecture: Swift CLI over CGo
Followed the same IPC pattern as `focus.GetFrontmostApp()` (osascript via os/exec) and `iterm.Client` (WebSocket). The Swift CLI (`cmd/axdump/main.swift`) dumps the AX tree as JSON to stdout; Go calls it via `os/exec`. Zero CGo — keeps the Go binary pure and cross-compilable.

Swift's ARC handles CoreFoundation memory (`CFString`, `AXValue`) automatically. A CGo implementation would require manual `CFRelease()` calls — one missed release and the long-running daemon leaks RAM.

### Element Source Classification
Added `source` field to the `element` struct in `tropical.go` with values `"dom"`, `"cv"`, `"ax"`. Classified automatically from ID prefix in `parseElements()`. This replaced scattered `strings.HasPrefix(id, "cv-")` checks in `buildMounts()` with a clean `el.source == "dom"` guard — ax-* elements get the same treatment as cv-* (valid for distance matrix computation, excluded from mount IDs and primary items).

### AX-to-Summary Translation
`FlattenTree()` + `ToSummaryLines()` produce the exact same `ID: | Tag: | Text: | Bounds: | Path:` format that `content.js` uses for DOM elements. The TropicalCartographer processes ax-* elements identically to mache-* elements — same 5-fiber distance matrix, same neighbor-joining, same H^0 folding. An `ax-42` macOS button is mathematically indistinguishable from a `mache-42` web button.

### Accessibility Permission Wall
`axdump` exits with code 2 when Accessibility permission isn't granted. The Go wrapper's `classifyError()` translates this into a human-readable message. The terminal app (iTerm2/Terminal.app) running `axdump` needs the permission — the binary inherits from its parent process.

### AXUIElement Timeout
Set `AXUIElementSetMessagingTimeout(appElement, 3.0)` to avoid blocking indefinitely on hung apps. Some apps (VS Code, Electron) can have thousands of AX nodes — `--max-depth` cap is critical.

### Next Steps
- Wire `axdump` into the websocket pipeline (similar to how cv-* regions are appended in `handleDOMSnapshot`)
- Test against real native apps (Finder, Calculator, System Settings) once Accessibility permission is granted
- Consider adding ax-* elements to the `ValidateSchema` allowlist (currently they'd be flagged as "hallucinated" if used as mount roots, but the source guard in `buildMounts` prevents this)

---

## 2026-02-27: FFT + Semantic Ingestion + AX Fix (Phases 0-2)

### AX Enrichment Was Silently Broken
`content.js` deliberately used an in-memory registry (no DOM mutation) to "avoid triggering React/Vue re-renders." But `background.js:enrichSummaryWithAX()` depends on `DOM.querySelectorAll([data-mache-id])` to join CDP AX nodes to mache IDs. Since the attributes were never written, `axMap` was always empty — AX enrichment has been a no-op since inception.

**Key insight**: React doesn't listen for external DOM attribute mutations — the concern was unfounded. And our own MutationObserver only watches `childList` (not `attributes`), so writing `data-mache-id` doesn't trigger `DOM_MUTATED`. The magnifying glass crop in `captureWithCDP` (line 437) also relies on these attrs.

### Body/div Not Captured
`content.js` Phase 2 containers query only covers semantic HTML5 elements (`main, section, article, nav...`). `<body>`, `<div>`, and `<span>` were entirely excluded unless they had a role attribute. Added Phase 3: always include `<body>`, include `<div>` only when semantically significant (role attr, semantic id/class keywords, or 3+ interactive descendants). The keyword heuristic prevents flooding the 300-element cap.

### Tropical Distance Now Has 5 Fibers
```
d(i,j) = max(d_spatial, d_visual, d_structural, d_semantic, d_frequency)
```

- **d_semantic**: Font size ratio (log-scaled), display type compatibility, interactivity divergence, text density. Returns 0 when unavailable (backward compat with old summary format).
- **d_frequency**: FFT-based — dominant freq divergence, spectral entropy, grid score. Only computed for cv-* regions. Returns 0 when both regions are unstructured (no frequencies, no grid) to avoid splitting equivalent opaque blobs.

### FFT: Hann Window Creates Artifacts at Bin 1
A uniform image under Hann windowing produces energy at frequency bin 1 (single cycle = the window shape itself). Fixed by starting peak search at bin 2 — a single cycle across the entire image isn't a meaningful repeating UI pattern anyway.

### Performance
- FFT on 300×400 region: ~10.8ms (M3 Max). Only runs on cv-* regions (0-3 per page), so 0-30ms added to the pipeline. Well within the 50ms total budget.
- Semantic distance adds negligible overhead (simple arithmetic on already-parsed fields).

### Next Steps
- Phase 3 (macOS AXUIElement): Plan calls for a Swift CLI (`cmd/axdump`) instead of CGo — keeps Go core pure. Needs Accessibility permissions.
- Build tag consideration: TropicalCartographer depends on the closed-source x-ray repo. May need `//go:build` tags to isolate it for open-source builds.
- Live test the semantic ingestion + AX enrichment fix on real sites.

---

## 2026-02-26: Cross-Domain Context Poisoning Prevention

### Problem
Generic tool names like `goto` and `rescan` caused local SLMs (Qwen via Ollama) to confuse browser-scoped tools with terminal-scoped actions. A model might try to `goto` a terminal path or `rescan` an iTerm session — "cross-domain context poisoning."

### Fix: App-Scoped Tool Names
Namespaced all tools to their domain:
- Browser tools: `browser.goto`, `browser.rescan`, `browser.scroll`, `browser.list_tabs`, `browser.switch_tab`
- Terminal tools: `iterm.new_window`, `iterm.new_tab` (extracted from `act()` into first-class tools)

The system prompt now groups tools by scope and states that `browser.*` tools only affect `/browser/`.

### Fix: JSON Guided Decoding
Added `response_format: {"type": "json_object"}` to OllamaGenerator requests, enforcing deterministic JSON output from Qwen models. Also fixed the `gemmaFnCallRe` regex from `(\w+)` to `([\w.]+)` so it matches dotted tool names like `browser.goto`.

### Key Insight
The `new_window`/`new_tab` actions were hidden inside `act()` as generic action strings routed through `Bridge.Act()`. Making them first-class tools means the model sees them in the tool schema and doesn't need to know the magic action strings. They still call through `NavFS.Act()` internally, so `iterm/bridge.go` was untouched.

### Test Fix: `TestDoerSchemaWaitSoftProceed`
This test was broken since commit `33b0690` (schema wait 3s→15s). The goroutine signaled `SchemaReady` at 4s — which now unblocked the initial wait *early* instead of after timeout. Then `dispatchAction`'s `browser.goto` called `ResetSchema()` (creating a new channel) that nobody signaled, causing a 30s hang vs the 10s test deadline. Fix: signal twice — once for initial wait, once for the post-goto wait.

### Stale TODO Removed
`interfaces.go` had a TODO suggesting a "tool registry pattern to consolidate." The `ToolRegistry` in `tool.go` already implements exactly this. Removed the dead comment.

---

## 2026-02-26: The "Room of Mirrors" iTerm Bug and Sync Reconciles

### The Log Inception Loop
Because `agentd` was running inside an active iTerm2 session, the Navigator sometimes asked to `cat(/iterm/active_session/buffer)` and ended up reading its own daemon logs. This created a "room of mirrors" where the JSON logs containing massive unescaped strings completely blew out the SLM's context window, causing it to retry commands wildly.

### Fix 1: The Blind Spot (`ITERM_SESSION_ID`)
iTerm2 injects `ITERM_SESSION_ID` into every spawned shell.
- Read this ID on daemon startup.
- In `reconcileSessions()`, the bridge now explicitly skips the agent's own terminal session.
- The daemon is now entirely blind to the terminal pane it is running in, preventing log inception.

### Fix 2: Context Overload Protection
When reading terminal buffers from other panes, they can still contain chaotic data.
- Reduced `DefaultBufferLines` from 100 to 20 to limit noise.
- Wrapped `FunctionResponse` output in `json.Marshal()` before appending it to the SLM history in `GemmaGenerator` and `OllamaGenerator`. This perfectly escapes newlines and raw text, preserving the JSON structure of the LLM prompt.

### Fix 3: Async Race Condition (`new_window`)
The iTerm2 API is highly asynchronous. Spawning a new tab returned a success message immediately, but the `LayoutChanged` event took 100ms+ to fire. The fast local SLM would run `ls`, not see the new window, assume failure, and retry, spawning multiple windows.
- Fixed by adding `time.Sleep(500 * time.Millisecond)` and forcing a synchronous `b.reconcileSessions(ctx)` inside the `Act` method immediately after creating a tab/window.

### Fix 4: Schema Wait Timeout
Increased the `SchemaReady` soft-wait in the Doer from 3 seconds to 15 seconds. Cartographer can take 10-15s to map complex browser pages. Allowing it to time out at 3s forced the Doer to proceed with an empty `/browser/` tree, which confused the Navigator, especially when routed through `/focus/`.

---

## 2026-02-26: Pure Go macOS Focus Router (No CGO)

### Problem
The Navigator needs to know which application is currently active so it can interpret relative spatial commands like "what am I looking at" or "click that" without the user explicitly specifying `/browser/` or `/iterm/`.

### False Start (The CGO Trap)
Initial instinct was to use macOS native APIs (CoreGraphics/AppKit) via CGO to query the WindowServer for the frontmost application. This is a massive trap:
1. Breaks cross-compilation (`GOOS=linux go build` fails)
2. Slows down build times
3. Introduces C memory-safety risks into a perfectly safe Go backend

### Solution
Used standard library `os/exec` to run a 3-line AppleScript (`osascript`) that queries the WindowServer. It returns the name of the frontmost app (e.g., "Google Chrome", "iTerm2") in 20-50ms with zero CGO dependencies.

### Architecture: The `/focus/` Symlink
Built a dynamic `focus.Router` that mounts at `/focus/` in the `CompositeGraph`.
- The router takes an app-to-prefix mapping (e.g., `{"Google Chrome": "browser", "iTerm2": "iterm"}`).
- When the Navigator accesses `/focus/some/path`, the router checks the frontmost app, looks up its prefix, prepends it (`/iterm/some/path`), and proxies the call back through the `CompositeGraph`.
- It acts as a true transparent proxy, making the active environment context-aware without coupling the graph implementations.

---

## 2026-02-26: Air-gapped Support & Universal ACI Documentation

### What was built
- Added `demo-local` task to `Taskfile.yml` which runs the entire ACI completely offline on local Apple Silicon. It overrides the Cartographer and Navigator models with local ones (`qwen2-vl:7b` for vision, `qwen2.5-coder:7b` for navigation) via environment variables.
- Added `internal/cartographer/ollama.go` to support connecting to local Vision-Language Models (VLMs) via an OpenAI-compatible endpoint.
- Updated `cmd/agentd/main.go` to read `CARTOGRAPHER_ENDPOINT` and `CARTOGRAPHER_MODEL` and initialize the new `OllamaAgent` if set.
- Major documentation update in `README.md` and `DEVPOST_README.md` to shift the narrative from "voice-driven browser agent" to "Pluggable Universal Agent OS". Highlighted the `CompositeGraph` architecture that mounts both `/browser/` (via Chrome CDP plugin and Cartographer) and `/iterm/` (via Unix Domain Socket, no vision model needed) and demonstrated the cross-domain swarm capabilities.


## 2026-02-26: Phase 1 Complete — CompositeGraph + Act() in mache

### What was built
On `feat/iterm-bridge` branch in mache (`50ee72b`):
- **`Act(id, action, payload)` on Graph interface** — makes the read-only Graph read-write. Passive graphs (MemoryStore, SQLiteGraph, WritableGraph) return `ErrActNotSupported`. Interactive graphs (browser, iTerm2, macOS AX) implement real actions.
- **`CompositeGraph`** — multi-mount router. `Mount("browser", g)` → all `/browser/...` paths delegate to `g` with prefix stripped. Root `ListChildren` returns mount names. All Graph methods route by prefix, including `Act()`.
- **Key design decision**: Navigator's ActTool calls `engine.Act()` — zero knowledge of mount points. No `strings.HasPrefix` routing in x-ray. The graph layer handles it.

### Architectural insight: AX belongs in mache, not x-ray
mache already gates optional platform integrations behind build tags (`//go:build leyline` for Rust FFI). AX → Graph projection is a schema concern. mache already has cgo deps (tree-sitter, cgofuse) and darwin-specific code, so AX adds no new dependency category. Any mache consumer gets native app navigation for free.

### gofumpt lesson
Pre-commit hooks run gofumpt which groups identical parameter types: `(id string, action string, payload string)` → `(id, action, payload string)`. Write it that way from the start.

## 2026-02-26: Universal ACI — Terminal as Proving Ground

### Context
Discussion about extending the stack beyond browser-only to universal Agent-Computer Interface (ACI). The progression: frame buffer → mache → HID. ADR-008 written in ley-line covering HID injection, frame capture, coordinate transforms, security model.

### Key Insight: macOS Accessibility API = DOM for Native Apps
On macOS, AXUIElement provides a hierarchical tree per application — role, title, value, position, size, children, actions. Covers ~85-90% of interactive elements on a typical desktop. Only custom-rendered content (canvas, games, Figma) needs vision/Canny/FFT.

### Terminal is the Best v1 for Universal ACI
Terminal content is pure text — no vision model, no screenshot, no Cartographer needed. The perception problem collapses to "read the buffer." This makes terminal the cheapest and most reliable proving ground for the mache abstraction beyond browsers.

### iTerm2 Approach Options

**Option A: iTerm2 WebSocket/Protobuf API**
iTerm2's Python API wraps a local WebSocket connection using protobufs. Could write a Go client that speaks the protobuf protocol directly — no Python dependency. Provides: session list, buffer content, send keystrokes, create/destroy panes.
- Pro: Rich, real-time, event-driven (subscribe to screen updates)
- Con: iTerm2-specific, need to reverse-engineer or use the published protobuf schema
- Risk: Protobuf API stability unclear — is it documented/stable or internal?

**Option B: macOS Accessibility API (AXUIElement)**
Works for ANY terminal (iTerm2, Terminal.app, Alacritty, Kitty). AX tree exposes windows, tabs, text areas. Can read buffer and inject keystrokes via CGEvent.
- Pro: Universal across all macOS terminal apps
- Con: Less rich than iTerm2's native API, no session management

**Option C: tmux control mode**
`tmux -CC` is what iTerm2 uses internally for tmux integration. Raw pipe-based protocol.
- Pro: Works headless, not tied to any GUI terminal
- Con: Requires tmux, different UX model

### Proposed Schema Projection (Terminal → Mache)
```
/iterm/
  window-1/
    tab-1/
      session-1/
        mache_id, process, working_dir, buffer, cursor
      session-2/
        ...
```

Navigator tools: `ls`, `cat` (read buffer), `act` (type text, send Ctrl sequences). Same tool contract as browser — Navigator doesn't know it's talking to a terminal.

### Meta-Circular Use Case: Gemini Voice → Claude Code
Voice → Gemini Live (Talker) → issue_command → Doer reads terminal buffer → types into Claude Code prompt → polls for completion → reports result via voice. AI managing AI through a terminal.

### Open Questions
1. Is iTerm2's protobuf WebSocket protocol stable/documented or internal?
2. Should we start with AX (universal) or iTerm2 API (richer)?
3. How to detect "command complete" in terminal buffer? (prompt detection heuristic)
4. Buffer size — how much scrollback to expose? Full history or last N lines?
5. Security: should the agent be allowed to `Ctrl+C` arbitrary processes?

---

## 2026-02-26: FFT for GUI Layout Detection

### Idea
Replace/augment Canny edge detection with FFT (Fast Fourier Transform) for detecting periodic UI structures (grids, lists, rows, columns). GUIs are fundamentally periodic — FFT detects the spatial rhythm directly instead of tracing individual edges.

### Approach: 1D Projections
- Sum pixels per row → 1D signal → FFT → peaks = row boundaries
- Sum pixels per column → 1D signal → FFT → peaks = column boundaries
- Much cheaper than 2D FFT, effective for structured layouts

### Implementation Location
SQLite extension in mache (not ley-line). Pure Rust `cdylib` using `rustfft` crate, loaded via `sqlite3_load_extension`. Exposes `fft_peaks(blob, axis)`, `row_projection(blob)`, `col_projection(blob)` as SQL functions.

### Combo Pipeline
- FFT → macro structure (grid spacing, zone boundaries)
- Canny → micro structure (widgets within grid cells)

### Limitation
FFT assumes periodicity. Asymmetric layouts, modal dialogs, freeform canvases still need Canny or vision model.

---

## 2026-02-25: Navigator Read-Only Intent Bug

### Bug
User asked "what was I watching recently?" on Crunchyroll. The Navigator correctly read the `continue_watching/children` file and identified 4 shows, but then **also** dispatched `act(click, /main/continue_watching/_c/2)` — clicking Jujutsu Kaisen. This was a read-only question that should have returned text only.

### Root Cause
The Navigator system prompt (`NavigatorSystemPrompt` in `internal/navigator/agent.go`) was entirely action-oriented. The strategy section always culminated in `act()`, and there was zero guidance for distinguishing informational questions from action commands. Small models (qwen2.5-coder:7b) are especially susceptible — they follow the dominant pattern in the prompt.

### Fix (two layers)

**Layer 1 — Prompt guidance** (initial fix):
Added an **INTENT CLASSIFICATION — READ vs ACT** section to the Navigator system prompt, placed before CRITICAL CONSTRAINTS. Tells the model to classify intent and never call `act()` for informational questions.

**Layer 2 — Programmatic guardrail** (belt and suspenders):
Added `read_only` boolean to the Talker → Doer → Navigator pipeline:
1. `issue_command` tool gains a `read_only` param; Talker prompt instructs Gemini to set it for questions
2. `DoerGoal.ReadOnly` carries the flag through the Doer
3. `HandleIntent(ctx, intent, readOnly)` — when `readOnly=true`, uses `ToolRegistry.DefinitionsExcluding("act")` to strip `act()` from the tool schema entirely
4. The 7B model literally cannot click — `act()` is not in the schema

This is defense in depth: Layer 1 helps capable models self-regulate; Layer 2 makes it physically impossible for any model to click on a read-only intent.

### Takeaway
Prompt-steered agents need explicit negative examples for small models. The absence of "don't click for questions" was interpreted as "always click." But prompt guidance alone is fragile — the real fix is **removing the tool from the schema**, which no amount of model confusion can override. The Talker (Gemini Live) is the right place to classify because it's already parsing user intent to formulate the goal.

---

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

---

## 2026-02-25: Canvas Blindspot — Canny Edge Detection for Pixel-Rendered UI

### Problem
Cartographer uses DOM parsing to identify interactive elements, but `<canvas>`, WebGL, and other pixel-rendered content (Google Maps, Figma, games) appear as a single opaque DOM element. No interactive sub-elements are discoverable.

### Solution
Pure-Go Canny edge detection pipeline that runs on the screenshot JPEG server-side, detects rectangular UI regions inside canvas elements, and enables CDP pixel-coordinate clicks.

### Pipeline (edges.go)
1. JPEG decode → grayscale → Gaussian blur (5×5, edge-clamped to avoid border artifacts)
2. Sobel edge detection → gradient magnitude + direction
3. Non-maximum suppression (thin to 1px) → double threshold + hysteresis (BFS flood)
4. Connected component flood-fill (8-connectivity) → bounding boxes → merge overlapping boxes
5. Filter: skip area < 400px² (noise) or > 50% image area; skip IoU > 0.3 with existing mache bounds
6. Assign `cv-N` IDs, draw cyan rectangles + labels on screenshot

### Integration
- `handleDOMSnapshot`: after screenshot decode, runs `DetectCanvasRegions()`, replaces screenshot with annotated version, appends `cv-N` entries to Cartographer summary
- `SendActionToExtension`: for `cv-` prefixed IDs, looks up pixel center from `TabSession.CVRegions` and populates `PixelX`/`PixelY` on the outbound message
- `background.js`: intercepts `EXECUTE_ACTION` with `pixel_x`/`pixel_y`, dispatches via CDP `Input.dispatchMouseEvent` (maps scaled screenshot coords back to viewport dimensions)

### Key Design Decisions
- **No bild dependency** — implemented Canny from scratch (~300 lines) to minimize dependency surface. Pure Go, zero cgo.
- **Edge-clamped Gaussian blur** — initial implementation zeroed border pixels, creating artificial edges that merged all components into one giant blob. Fixed by clamping kernel coordinates to image bounds.
- **8-connectivity flood fill** — must match hysteresis connectivity; 4-connectivity fragmented diagonal edge segments into separate components.
- **Overlap filtering via IoU** — prevents cv-N regions from duplicating already-tagged mache elements.

### Files
- `internal/api/edges.go` — DetectCanvasRegions + full Canny pipeline
- `internal/api/edges_test.go` — 7 tests (blank, rect detection, overlap filter, min-size, IoU math, invalid JPEG)
- `internal/api/messages.go` — PixelX/PixelY fields on OutboundMessage
- `internal/api/websocket.go` — CVRegions on TabSession, parseBounds, wiring in handleDOMSnapshot + SendActionToExtension
- `ext/background.js` — cdpPixelClick helper, cv-N interception in EXECUTE_ACTION
