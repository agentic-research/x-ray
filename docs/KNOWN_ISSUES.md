# Known Architectural Issues & Bugs

> **Note**: This document tracks current known limitations and architectural constraints. For previously resolved concurrency and race condition bugs, see the [Resolved Issues](#resolved-issues) section below.

## 1. Iframe and Shadow DOM Visibility
**Symptom:** Elements inside cross-origin `<iframe>`s or Custom Elements using `Shadow DOM` (e.g., Salesforce Lightning, Lit components) are entirely invisible to the Cartographer and Navigator.
**Root Cause:** The `content.js` script operates via standard `document.querySelectorAll()` and recursive DOM traversal, which cannot pierce Shadow Root boundaries or cross-origin iframe boundaries due to browser security models.
**Fix required:** Implement a Chrome DevTools Protocol (CDP) based DOM traversal approach or inject content scripts into all frames.

## 2. O(N³) Scaling Limit in Tropical Cartographer (500 Element Cap)
**Symptom:** On extremely large pages (e.g., massive dashboards or data tables), some interactive elements are missing from the virtual filesystem.
**Root Cause:** The `TropicalCartographer` uses the Saitou-Nei Neighbor-Joining (NJ) algorithm for phylogenetic clustering, which scales at `O(N³)` relative to the number of elements. To keep latency sub-second, the system hardcaps the input at 500 elements. Elements beyond this limit are pre-filtered out, potentially losing important structural containers.

## 3. CSS-in-JS Structural Fiber Degradation
**Symptom:** The `TropicalCartographer` sometimes fails to group related elements accurately on sites built with Tailwind or styled-components.
**Root Cause:** The structural distance metric relies on CSS paths (e.g., `div.post > h3.title > a`). When frameworks generate randomized or utility class names (e.g., `div.sc-bXCLTC > div.sc-kDDrLX`), the CSS path carries no semantic information, degrading the structural fiber to a simple tag-only comparison.

## 4. Visual RGB Fiber is Coarse (3x3 Center Sample)
**Symptom:** Elements with visual significance at their borders (e.g., an outlined button with a transparent center) might not cluster properly based on color.
**Root Cause:** The `TropicalCartographer` samples a single 3x3 pixel region at the geometric center of an element to build the RGB visual fiber. This misses gradients, textures, borders, and any visual feature not exactly at the center.

## 5. Ordinal Instability Across Scrolls
**Symptom:** The Navigator attempts to click `_c/3` after a scroll, but it hits a different element than it expected.
**Root Cause:** The `_c/` ordinal directories are rebuilt sequentially on every `LoadChildren` call. When a user scrolls or the DOM mutates and new items are discovered, the list of children is regenerated, meaning the element that was previously at index 3 might now be at index 8.

## 6. Hardcoded Canvas Edge Detection Thresholds
**Symptom:** Some subtle or low-contrast UI elements inside a `<canvas>` are not detected by the Canny edge pipeline, or a very noisy canvas generates too many false positive regions.
**Root Cause:** The server-side Canny edge detection uses fixed thresholds (low=50, high=150) and a fixed minimum region area (400px²). These were tuned for standard web UI contrasts but are not adaptive to the image's overall contrast, brightness, or DPI.

---

## Resolved Issues (As of March 2026)

The following bugs were previously tracked but have been fixed and verified with regression tests (run `task test` to verify):

### Concurrency & Session Management (`internal/api/known_bugs_test.go`)

*   **Data Race: TabSession.SchemaReady & Engine Pointer:** Fixed by introducing proper mutexes in `TabSession`. (`TestBug_SchemaReadyDataRace`, `TestBug_EnginePointerDataRace`)
*   **Shared Navigator Callback Overwrites:** Callbacks are now correctly scoped and cleaned up. (`TestBug_NavigatorCallbackOverwrite`)
*   **Goroutine Leak on Tab Close:** The `Doer` run loop now properly listens for context cancellation when a tab closes. (`TestBug_DoerGoroutineLeakOnTabClose`)
*   **Gemini Live GoAway Reconnection Bug:** Sender goroutines now use session-scoped contexts that gracefully exit during `GoAway` reconnects. (`TestBug_GoAwaySenderGoroutineLeak`)
*   **Phantom Session Creation:** `DOM_MUTATED` and `DOM_UPDATE` for unknown tabs no longer create phantom sessions. (`TestBug_DOMMutatedNoPhantomSession`, `TestBug_DOMUpdateNoPhantomSession`)
*   **Tab Close Voice Fallback:** Closing the active voice tab correctly falls back to another session (or 0 if none remain). (`TestBug_TabCloseFallbackToOtherSession`, `TestBug_TabCloseNoFallback`)
*   **Cross-Tab Cache Poisoning:** Same-URL pages with different element positions (e.g., responsive layout vs desktop) no longer share cached schemas. `ValidateSchemaBounds` detects center-point drift. (`TestBug_CrossTabCachePoisoning`, `TestBug_CrossTabCachePoisoningE2E`)
*   **Session Reset on Extension Reconnect:** All sessions get fresh SchemaReady channels after WebSocket reconnect, preventing stale schema state. Sessions survive reconnect for Doer continuity. (`TestBug_ReconnectResetsSessionSchemaState`, `TestBug_ReconnectPreservesSessionsForDoer`)
*   **Doer Teleportation (Tab 0 Rebind):** Doer starting on Tab 0 (disconnected extension) correctly rebinds to the real tab when the extension wakes up mid-goal. (`TestBug_DoerTeleportationTab0`)
*   **TAB_ACTIVATED Voice UI Filtering:** Voice UI tabs no longer pollute `activeVoiceTab`. Extension-side filtering via `schemaReadyTabs` prevents false activations. (`TestBug_TabActivatedVoiceUIFiltered`, `TestBug_TabActivatedVoiceUIViaWebSocket`)

### Data Races & Algorithmic Bugs (2026-03-01 adversarial review)

*   **C1: Agent.SetGraph() data race:** Navigator graph access now goes through mache `HotSwapGraph` (v0.5.2). `SetGraph()` → `hotswap.Swap()`. All tool calls get per-call RLock automatically. (`ab5472c`)
*   **C2: CVRegions unsynchronized read/write:** Added `GetCVRegions()`/`SetCVRegions()` on `TabSession` under `schemaMu`. (`ab5472c`)
*   **C3: RescanPath unsynchronized read/write:** Added `ConsumeRescanPath()`/`SetRescanPath()` on `TabSession` under `schemaMu`. (`ab5472c`)
*   **C4: CurrentURL unprotected in Doer:** Doer now uses `GetCurrentURL()`/`SetCurrentURL()` accessors. (`ab5472c`)
*   **C5: Planner passes wrong tabID:** Changed `req.TabID` to resolved `tabID`. (`ab5472c`)
*   **H7: H⁰ folding violates minZones:** Post-folding guard keeps pre-fold zones if folded count < minZ. (`TestBug_H0FoldingViolatesMinZones`, `ab5472c`)
*   **S1: Prefilter drops structural containers:** Added structural container priority tier (nav, main, section, etc.) with reserved budget. (`TestPrefilterElements_StructuralContainersSurvive`, `c5b00ec`)

### Cartographer (`internal/cartographer/known_bugs_test.go`)

*   **cv-* Region as Zone Root:** Edge-detected canvas regions (`cv-*`) are synthetic IDs that don't exist in the browser DOM. `buildMounts` now skips `cv-*` and `ax-*` when selecting zone root elements. (`TestBug_CVRegionAsZoneRoot`, `TestBug_CVRegionOnlyZone`)
*   **Duplicate Header Fragmentation:** SPAs rendering duplicate DOM subtrees (mobile/desktop nav) produced redundant zones. H⁰ cohomology folding now merges zones with identical fiber signatures (same text/tag distribution + spatial overlap). (`TestBug_DuplicateHeaderFragmentation`)
*   **ax-* Accessibility Elements as Zone Roots:** macOS Accessibility elements (`ax-*`) are excluded from mount IDs and primary_items, same pattern as cv-* filtering. (`TestBug_AXOnlyZone`, `TestBug_AXMixedWithDOM`)

### Browser Extension

*   **Chrome Window Focus (macOS):** Solved by implementing fallbacks for window activation.
*   **Chrome Extension WebSocket Sleep:** Fixed via keep-alive pings and early tab promotion logic.
*   **The Stale Extension ID Bug:** Mache IDs are now properly garbage-collected during full-page re-navigations.
*   **Overlay Drift & Premature Snapshotting:** Corrected by debouncing snapshots until layout shifts settle.
