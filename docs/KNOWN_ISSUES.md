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

## 7. Doer Settle Detection Cascade (70 LOC Nested Temporal Branching)
**Symptom:** After every interactive action, the Doer enters a 4-branch `select` (`doer.go:330-401`) to determine what happened: same-tab navigation, DOM mutation, timeout fallback, or cancel. Two branches contain *nested* 3-branch selects (SchemaReady / timeout / cancel). Total: ~70 lines of mutually exclusive timing-dependent paths, each with side effects (ResetSchema, sendRescan, session rebinding).
**Root Cause:** The settle logic was built incrementally — first navigation detection, then DOM mutation, then new-tab detection, then fallback rescan. Each was added as a new branch rather than refactored into a unified state machine.
**Evidence of fragility:** Integration tests cannot precisely simulate extension behavior. They use goroutines that flood `SignalSchemaReady()` in a loop to ensure a signal arrives in time. A change to timeout values or signal ordering can break tests without any code change.
**Fix required:** Extract settle detection into a `settleAfterAction(ctx, action) (outcome, summary, error)` method returning an enum. Each outcome becomes independently testable.

## 8. SchemaReady Signal Lost Between Dispatch and ResetSchema
**Symptom:** Fast DOM responses cause unnecessary 2s delays. Production logs show "schema wait timed out after settle rescan" on pages that loaded instantly.
**Root Cause:** `ResetSchema()` creates a new channel (`session.go:78-83`). If the extension responds *between* action dispatch and `ResetSchema()`, `SignalSchemaReady()` closes the old channel that nobody is listening to. The new channel blocks until the 2s `actionSettleTimeout` fires a fallback rescan. Sequence: dispatch → extension responds → SignalSchemaReady (old chan) → ResetSchema (new chan) → `<-GetSchemaReady()` blocks → 2s timeout.
**Fix required:** Use a monotonic generation counter. `ResetSchema()` bumps generation. `SignalSchemaReady(gen)` only closes the channel if generation matches. Prevents stale signals and lost signals.

## 9. Tab Rebinding Leaves Guardrails Bound to Wrong Session
**Symptom:** After a click opens a new tab, dedup and ref validation silently stop working. The `defer` cleanup targets the wrong session.
**Root Cause:** Two code paths mutate `d.tabID` and `d.sess` mid-goal (`doer.go:322-327`, `doer.go:367-382`). Guardrails are wired to the *original* session at goal start: `d.sess.Tasks.SetDedupFunc(gs.IsDuplicate)` (line 189), `d.sess.Navigator.SetRefValidateFunc(gs.ValidateActPath)` (line 196). After rebind, `d.sess` points to a new session whose Tasks/Navigator were never wired. The `defer` cleanup clears the *new* session (which was never set), leaving the original session's callbacks permanently installed.
**Fix required:** Track the "bound session" explicitly. Re-wire guardrails on rebind. Ensure defer cleans up the correct session.

## 10. Step Budget Not Designed for Guardrail Retries
**Symptom:** Multi-page extraction tasks fail to find all items despite having enough pages to check.
**Root Cause:** `maxGoalSteps = 5` (`doer.go:29`). Two retry mechanisms consume steps from this same budget: weak response retry (`doer.go:277`) and completeness retry (`doer.go:284`). A 3-page task: click page 1 (step 0), click page 2 (step 1), text response triggers completeness retry (step 2, retry burns step 3), retry response (step 3, last step — completeness check skipped even if still incomplete). Only 3 productive actions out of 5 steps.
**Fix required:** Either don't count retries against the step budget (separate retry counter), or increase `maxGoalSteps` to account for expected retries.

## 11. Stringly-Typed LLM Response Classification
**Symptom:** Weak responses slip through retry detection; completeness counter reports 0 items when items exist but are differently formatted.
**Root Cause:** Weak response detection (`doer.go:269-273`) checks 4 string prefixes ("I couldn't", "I could not", "Error:", "I was unable"). LLM responses like "Unfortunately, I wasn't able to..." bypass all checks. Completeness counting (`completeness.go:64-66`) only counts lines starting with "Found:", "- ", or "* ". If the Navigator writes `"Alice (reviewer)"` instead of `"Found: Alice"`, the counter misses it — causing infinite retries until the step budget runs out.
**Fix required:** For weak responses: classify as "actionable answer" vs "not" rather than matching failure phrases. For completeness: enforce scratch format in the Navigator's system prompt, or count all non-empty lines.

## 12. `dispatchAction` Monolith (120 LOC, 4 Action Types)
**Symptom:** Low immediate risk, but highest-friction code for adding new browser capabilities.
**Root Cause:** `dispatchAction` (`doer.go:414-535`) handles goto, rescan, switch_tab, and interactive actions in a single switch statement. The goto and full-rescan paths both do unmount→create→mount→SetGraph→wait with slightly different logic. Adding `browser.back` or `browser.close_tab` requires understanding all 4 paths.
**Fix required:** Extract "reset engine and wait for schema" lifecycle into a helper. Each action case shrinks to dispatch + lifecycle call.

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
