# FFT + AX + Semantic Ingestion Plan

## Context

TropicalCartographer is live and working (~50ms, deterministic, zero API). But three gaps remain:

1. **Canvas/WebGL/opaque UI** — Canny edge detection produces `cv-*` regions but these are just bounding boxes with no structural information. FFT can detect repeating visual patterns (grids, lists, spacing) from raw pixels.
2. **Native macOS apps** — No DOM at all. macOS Accessibility API (`AXUIElement`) provides hierarchy, roles, labels, and bounds for native apps.
3. **Broken/incomplete DOM capture** — Two bugs discovered during investigation:
   - `content.js` never writes `data-mache-id` attributes to the DOM, so `background.js`'s CDP AX enrichment (`Accessibility.getFullAXTree` join) silently returns nothing
   - `<body>`, `<div>`, `<span>` are excluded from capture unless they have ARIA roles — the "body not captured" gap
4. **Semantic ingestion** — The color-typed overlay (blue=links, orange=buttons) was designed for VLM consumption. The algebraic cartographer doesn't use color as a semantic signal — it needs richer fiber data: ARIA roles, computed styles, text density, interactivity.

## Phase Ordering

- **Phase 0**: Fix Broken Fundamentals (prerequisite)
- **Phase 1**: Semantic Ingestion Layer
- **Phase 2**: FFT Visual Structure Detection
- **Phase 3**: macOS AXUIElement Integration

---

## Phase 0 — Fix Broken Fundamentals

### 0a. Fix AX enrichment (`ext/background.js` + `ext/content.js`)

**Bug**: `background.js:enrichSummaryWithAX()` does `DOM.querySelectorAll([data-mache-id])` but `content.js` never writes `data-mache-id` to the DOM. The `axMap` is always empty — AX enrichment has been silently broken since inception.

**Fix in `content.js`**: After assigning `mache-N` IDs in `buildRegistry()`, write them as DOM attributes:
```js
node.setAttribute('data-mache-id', id);
```
Add cleanup in registry reset to remove stale attributes before re-tagging.

**Files**: `ext/content.js` (write attrs), `ext/background.js` (verify join works)

### 0b. Capture body + structural divs (`ext/content.js`)

**Bug**: Phase 2 containers query is `main, section, article, nav, header, footer, aside, form, ul, ol, dl, table, tbody, [role="navigation"], [role="main"], [role="list"], [role="group"], [role="region"]`. This misses `<body>` and semantically meaningful `<div>` wrappers.

**Fix**: Add `body` to the Phase 2 query. For `<div>` elements, add them if they have: (a) 3+ interactive descendants, or (b) an explicit `id` or `class` containing semantic keywords ("content", "main", "sidebar", "footer", "wrapper", "container"), or (c) `role` attribute present. Don't add all divs — that would flood the 300-element cap with useless layout wrappers.

**Files**: `ext/content.js`

---

## Phase 1 — Semantic Ingestion Layer

**Goal**: Replace VLM color-typing heuristic with computed semantic features that feed directly into tropical distance as fiber data.

### What to capture (in `content.js` `generateSummary()`)

Append to each element's summary line:
```
| FontSize: 16 | Display: flex | Interactive: true | TextDensity: 0.73 | AXRole: link | AXName: "Home"
```

Fields:
- `FontSize`: `parseFloat(getComputedStyle(el).fontSize)` — distinguishes headings from body text
- `Display`: `getComputedStyle(el).display` — identifies flex/grid containers
- `Interactive`: `true` if element is focusable or has click handlers
- `TextDensity`: `textContent.length / (boundingRect.width * boundingRect.height)` — normalized text fill ratio
- `AXRole` / `AXName`: From CDP Accessibility API (once Phase 0a fix lands)

### Performance: Avoid Layout Thrashing

**Risk**: Calling `getComputedStyle(el)` inside a loop causes synchronous layout thrashing (forced reflows). On 300 elements this could spike execution from 54ms to 200ms+.

**Fix**: Two-pass approach:
1. **Pass 1** (geometry): Read all `getBoundingClientRect()` in one loop — triggers one reflow
2. **Pass 2** (styles): Read all `getComputedStyle()` in a second loop — browser can batch

Or use `requestAnimationFrame` to defer the style reads to the next frame, keeping the main summary generation non-blocking.

### Integration with tropical distance

Add `d_semantic` term to `tropicalDistance()`:
```go
func semanticDistance(a, b *element) float64 {
    // Role divergence: same role → 0, different → scaled by role hierarchy distance
    // FontSize ratio: |log(fs_a/fs_b)| / log(maxRatio), capped at 1.0
    // Display compatibility: same display type → 0, different → 0.5
    // TextDensity difference: |td_a - td_b|
    return max(roleDist, fontDist, displayDist, textDensityDist) // inner tropical max
}
```

Then in `tropicalDistance()`:
```go
return math.Max(ds, math.Max(dv, math.Max(dt, dSemantic)))
```

### Files to modify
- `ext/content.js` — add computed style extraction to `generateSummary()` (two-pass)
- `internal/cartographer/tropical.go` — add `semantic*` fields to `element` struct, parse them in `parseElements()`, implement `semanticDistance()`, add to `tropicalDistance()`

---

## Phase 2 — FFT Visual Structure Detection

**Goal**: For canvas/WebGL regions (and as supplementary data for all regions), detect repeating visual patterns via 2D FFT on the screenshot.

### Mathematical Foundation

A 2D DFT of a grayscale image region I(x,y) of size M×N:

```
F(u,v) = Σ_x Σ_y I(x,y) · exp(-2πi(ux/M + vy/N))
```

**Key insight**: Repeating UI patterns (list items, grid cells, table rows) create strong peaks in the frequency domain at their repetition frequency. A list with items spaced 50px apart on a 1000px region produces a peak at v = 1000/50 = 20 cycles/region.

**What peaks tell us**:
- Peak at (0, f_y) → horizontal bands repeating every `H/f_y` pixels (list items)
- Peak at (f_x, 0) → vertical bands repeating every `W/f_x` pixels (columns)
- Peak at (f_x, f_y) → grid pattern with both row and column spacing
- No significant peaks → non-repeating content (hero image, text block)

This means we can mathematically deduce the exact row and column spacing of a WebGL spreadsheet without reading a single line of text or knowing what a "spreadsheet" is.

### Implementation

**New file**: `internal/cartographer/fft.go` (~250 lines)

```go
type FFTFeatures struct {
    DominantFreqX float64 // strongest horizontal repetition frequency (0 = none)
    DominantFreqY float64 // strongest vertical repetition frequency
    PeakStrength  float64 // magnitude of strongest peak relative to DC component
    GridScore     float64 // 0-1, how grid-like the region is
    Entropy       float64 // spectral entropy — high = complex/noisy, low = regular
}

func AnalyzeRegion(gray []float64, w, h int) FFTFeatures
```

**Algorithm**:
1. Extract grayscale subimage for each element's bounding box (or cv-* region)
2. Apply Hann window to reduce spectral leakage
3. Compute 2D FFT via row-column decomposition (1D FFT on each row, then each column)
4. Compute power spectrum: `P(u,v) = |F(u,v)|²`
5. Find peaks in P (excluding DC at (0,0)): local maxima above `mean + 3σ`
6. Extract dominant frequencies, grid score, spectral entropy

**1D FFT**: Cooley-Tukey radix-2 DIT, ~60 lines of Go. Zero-pad to next power of 2. Pure stdlib (`math`, `math/cmplx`).

**Performance**: For a 300×400 region (typical zone), 2D FFT is ~0.5ms. Running on 5-7 zones: ~3ms total. Well within the 50ms budget.

### Integration with tropical distance

Add `d_frequency` to `tropicalDistance()`:
```go
func frequencyDistance(a, b *element) float64 {
    // Compare FFT features of the regions containing each element
    // Same repetition frequency → structurally similar (both in a list)
    // Different frequency → different zone types
    freqDiff := math.Abs(a.fftFreqY-b.fftFreqY) / maxFreq
    entropyDiff := math.Abs(a.fftEntropy - b.fftEntropy)
    return math.Max(freqDiff, entropyDiff)
}
```

### What this enables

- **Canvas/WebGL**: A canvas rendering a spreadsheet has strong grid-frequency peaks. FFT detects the row/column spacing without any DOM. The cartographer can create mount points with `primary_items` count derived from `regionHeight / rowSpacing`.
- **Better list detection**: Current list detection uses structural pattern matching on tag paths. FFT provides a complementary geometric signal — even if DOM elements have inconsistent structure, their visual repetition frequency is identical.
- **cv-* enrichment**: Instead of treating cv-* regions as opaque boxes, FFT provides fiber data (grid score, entropy, dominant frequencies) that feeds into the distance matrix.

### Files
- `internal/cartographer/fft.go` — new: FFT implementation + feature extraction
- `internal/cartographer/fft_test.go` — new: test with synthetic repeating patterns
- `internal/cartographer/tropical.go` — add FFT fields to `element`, call `AnalyzeRegion` in pipeline, add `frequencyDistance` to `tropicalDistance`

---

## Phase 3 — macOS AXUIElement Integration

**Goal**: For native macOS apps (and as fallback for canvas), use the Accessibility API to get UI structure.

### Architecture: Swift CLI, not CGo

**Rationale**: Adding CGo just for macOS Accessibility introduces cross-compilation headaches, memory-safety risks, and ties the binary strictly to Darwin. Since the rest of the system uses IPC (WebSocket, Unix sockets), the clean approach is a separate Swift binary that dumps the AX tree to JSON.

**New**: `cmd/axdump/` — a small Swift CLI (~150 lines)

```swift
// axdump dumps the macOS Accessibility tree for a given PID as JSON.
// Usage: axdump --pid 1234 --max-depth 10
import ApplicationServices
import Foundation

struct AXNode: Codable {
    let role: String
    let label: String?
    let value: String?
    let bounds: [Double]  // [x, y, w, h] normalized to screen
    let children: [AXNode]
}
```

**Go side**: `internal/ax/ax.go` calls `axdump` via `os/exec`:
```go
type AXNode struct {
    Role     string    `json:"role"`
    Label    string    `json:"label,omitempty"`
    Value    string    `json:"value,omitempty"`
    Bounds   [4]float64 `json:"bounds"`
    Children []AXNode  `json:"children"`
}

func GetAppTree(pid int, maxDepth int) ([]AXNode, error) {
    cmd := exec.Command("axdump", "--pid", strconv.Itoa(pid), "--max-depth", strconv.Itoa(maxDepth))
    // ...
}
```

This keeps the Go core pure and cross-platform. The Swift binary is pre-compiled and shipped alongside the agent binary (or built via `go generate` on macOS).

### How it maps to sheaf fibers

AX nodes become elements in the TropicalCartographer with a source flag:
```go
type element struct {
    // ... existing fields ...
    source string // "dom", "cv", "ax"
}
```

An AX node maps to:
- `id` → `ax-N` (distinct prefix from `mache-*` and `cv-*`)
- `tag` → AXRole lowercased ("button", "textfield", "group")
- `text` → AXTitle or AXDescription or AXValue
- `bounds` → AXFrame normalized to screen dimensions
- `path` → AX hierarchy path ("AXApplication > AXWindow > AXGroup > AXButton")

### Hybrid mode

For a browser tab with canvas:
1. DOM elements → `mache-*` (from content.js)
2. Edge-detected regions → `cv-*` (from edges.go, enriched with FFT)
3. AX nodes inside canvas → `ax-*` (from axdump, if available)

All three sources feed into the same distance matrix. The tropical max ensures that elements from different sources that are spatially close but structurally unrelated still get separated.

### Files
- `cmd/axdump/main.swift` — new: Swift CLI for AX tree extraction
- `internal/ax/ax.go` — new: Go wrapper calling axdump via os/exec
- `internal/ax/ax_test.go` — new: tests (requires macOS Accessibility permission)
- `internal/cartographer/tropical.go` — merge AX nodes into element slice

---

## Key Files Reference

| File | Role |
|------|------|
| `ext/content.js` | DOM capture, registry, summary generation |
| `ext/background.js` | CDP orchestration, AX enrichment, WebSocket |
| `internal/api/edges.go` | Canny edge detection → cv-* regions |
| `internal/api/websocket.go` | Schema pipeline, appends cv-* to summary |
| `internal/cartographer/tropical.go` | TropicalCartographer (modify) |
| `internal/mache/engine.go` | Mount types, ValidateSchema |
| `internal/iterm/` | Existing native app bridge pattern (WebSocket/protobuf IPC) |
| `docs/KNOWN_ISSUES.md` | Documents AX enrichment as known broken |

## Dependencies

- **Phase 0-2**: Zero new dependencies (stdlib only: `math`, `math/cmplx`)
- **Phase 3**: Pre-compiled Swift binary (`cmd/axdump`), Go calls via `os/exec` — no CGo

## Verification

```bash
# Phase 0: Verify AX enrichment fix
cd ~/remotes/art/x-ray
# Load extension, navigate to a page, check logs for "AXRole:" in summary lines

# Phase 1: Semantic ingestion
go test -v ./internal/cartographer/ -run TestSemantic

# Phase 2: FFT
go test -v ./internal/cartographer/ -run TestFFT
# Integration: navigate to a canvas-heavy page (Google Sheets), check zone detection

# Phase 3: AX
swift build cmd/axdump/main.swift
go test -v ./internal/ax/ -run TestAX
# Requires: System Settings > Privacy > Accessibility permission for terminal

# Full pipeline
CARTOGRAPHER_MODE=tropical task run
# Navigate to pages with canvas, native apps — verify zones detected
```

## End State

By the end of Phase 3, the system maps:
- **Standard Web DOMs** (existing TropicalCartographer)
- **Shadow DOMs / ARIA states** (Phase 0 fix + Phase 1 semantic ingestion)
- **Opaque WebGL/Canvas UIs** (Phase 2 FFT)
- **Native Desktop Applications** (Phase 3 AX via Swift)

All without a VLM, all running locally, all under ~100ms.
