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

*   **Chrome Window Focus (macOS):** Solved by implementing fallbacks for window activation.
*   **Chrome Extension WebSocket Sleep:** Fixed via keep-alive pings and early tab promotion logic.
*   **Data Race: TabSession.SchemaReady & Engine Pointer:** Fixed by introducing proper mutexes in `TabSession`.
*   **Shared Navigator Callback Overwrites:** Callbacks are now correctly scoped and cleaned up.
*   **Goroutine Leak on Tab Close:** The `Doer` run loop now properly listens for context cancellation when a tab closes.
*   **The Stale Extension ID Bug:** Mache IDs are now properly garbage-collected during full-page re-navigations.
*   **Gemini Live GoAway Reconnection Bug:** Sender goroutines now use session-scoped contexts that gracefully exit during `GoAway` reconnects.
*   **Overlay Drift & Premature Snapshotting:** Corrected by debouncing snapshots until layout shifts settle.
