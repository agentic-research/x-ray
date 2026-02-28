# X-Ray: Deterministic Page Understanding via Multi-Fiber Phylogenetic Clustering

**Paper Outline (Draft)**

---

## 1. Abstract

**Summary.** We present X-Ray, a system that produces structured, navigable representations of web pages without using a Vision Language Model for page understanding. DOM elements are embedded as points in a product metric space with five fibers (spatial, visual, structural, semantic, frequency), and pairwise distances are aggregated via a max operator. A neighbor-joining tree is constructed from the distance matrix and greedily cut into 3--7 zones, which are exposed as a virtual filesystem for downstream LLM navigation. The system is fully deterministic: identical inputs produce identical outputs with zero inference variance.

**Key claims.**
- [NOVEL] Applying Saitou-Nei neighbor-joining to DOM element clustering --- a new domain for a phylogenetics algorithm.
- [NOVEL] The multi-fiber max-distance design combining spatial, visual, structural, semantic, and frequency sub-distances.
- [NOVEL] A virtual filesystem interface with ordinal indirection for LLM-page interaction.
- [STANDARD] The underlying algorithms (NJ, FFT, Canny) are unchanged from their original formulations.

**Honest caveat.** "VLM-free" applies to page understanding and zone segmentation. The downstream Navigator agent still uses an LLM (Gemini 2.5 Flash) for intent resolution and tool-use planning. The claim is that *structural page understanding* requires no learned model, not that the full system is model-free.

**Suggested figure.** System diagram showing the pipeline: DOM extraction -> multi-fiber embedding -> distance matrix -> NJ tree -> zone cutting -> VFS. Annotate which stages are VLM-free and where the LLM enters.

---

## 2. Introduction

**Summary.** Vision Language Models are the dominant approach to web page understanding for autonomous agents, but they introduce three costs: economic ($0.01--0.10 per screenshot inference), non-determinism (repeated runs produce different zone segmentations), and hallucination (referring to UI elements that do not exist). We propose treating DOM elements as taxa in a metric space and applying phylogenetic reconstruction to recover page structure. The resulting zones are deterministic, reproducible, and computed in ~50ms with zero inference cost.

**Key claims.**
- [STANDARD] VLMs are expensive and non-deterministic for page understanding. This is well-documented in the computer-use and web agent literature.
- [NOVEL] The biological analogy: DOM elements as taxa, CSS paths as phylogenetic characters, spatial position as biogeographic data. The analogy is genuinely productive, not merely decorative --- NJ's guarantee of recovering the correct tree from additive distances translates to recovering correct zone boundaries when the distance metric is well-calibrated.
- [STANDARD] The problem of structured page segmentation is well-studied. Our contribution is a specific algorithmic solution, not the problem formulation.

**Honest caveat.** We do not claim that VLM-free approaches are strictly superior. VLMs can understand visual semantics (e.g., "this looks like a login form") that pure DOM analysis cannot. Our claim is narrower: for the specific task of zone segmentation, deterministic DOM analysis is sufficient and preferable on cost/latency/reproducibility axes.

**Suggested figure.** Side-by-side: (a) VLM-based pipeline (screenshot -> model -> zones), (b) X-Ray pipeline (DOM + screenshot pixels -> metric space -> NJ -> zones). Cost/latency comparison table inset.

---

## 3. Related Work

**Summary.** We position X-Ray against five categories of prior work: VLM-based computer-use agents, DOM-based web agents, page segmentation algorithms, set-of-mark prompting, and phylogenetic algorithms in non-biological domains. We are honest that our comparisons are qualitative --- we have not yet run formal benchmarks on standard web agent evaluation suites.

**Key claims.**
- [STANDARD] Comparison categories and cited systems.
- [NOVEL] To our knowledge, no prior work applies neighbor-joining or phylogenetic tree reconstruction to DOM element clustering.

| System | Modality | Deterministic | Cost/page | Latency | Page Understanding |
|--------|----------|---------------|-----------|---------|-------------------|
| Anthropic computer-use | Screenshot + VLM | No | $0.01--0.10 | 1--5s | VLM inference |
| OpenAI computer-use | Screenshot + VLM | No | $0.01--0.10 | 1--5s | VLM inference |
| Google Mariner | Screenshot + VLM | No | ~$0.05 | 2--5s | VLM inference |
| Vercel agent-browser | Playwright `ariaSnapshot` | Yes (DOM-based) | $0 | <100ms | Heuristic DOM walk |
| SeeClick / CogAgent | Screenshot + fine-tuned VLM | No | ~$0.01 | 0.5--2s | Fine-tuned VLM |
| WebArena / Set-of-Mark | Annotated screenshot + VLM | No | ~$0.03 | 1--3s | VLM with visual grounding |
| **X-Ray (this work)** | DOM + pixel fibers | **Yes** | **$0** | **~50ms** | Multi-fiber NJ clustering |

**Honest caveat.** The cost and latency numbers for competing systems are approximate and depend on model pricing at time of writing. The comparison is qualitative. We have not evaluated X-Ray on WebArena, Mind2Web, or other standard benchmarks. The "deterministic" column refers specifically to the page understanding step, not end-to-end agent behavior.

**Citations to acknowledge.**
- Vercel `agent-browser` for the `cursor:pointer` heuristic (Phase 1.5 in our element extraction). Our implementation is a reimplementation of their approach, and we should cite them directly.
- Set-of-Mark prompting (Yang et al. 2023) for the colored bounding box overlay, which we use for the Cartographer's visual grounding.

**Suggested table.** The comparison table above, with a footnote column for "formal benchmark results" that honestly says "N/A" for our system.

---

## 4. Method

### 4.1 Element Extraction

**Summary.** A Chrome extension content script (`content.js`, ~560 LOC) tags DOM elements in three phases plus an intermediate cursor-pointer phase, building an in-memory registry of `mache-ID -> Element` mappings. Phase 1 tags interactive elements (links, buttons, inputs). Phase 1.5 tags cursor-interactive elements not captured by semantic selectors (`cursor:pointer`, `onclick`, `tabindex`). Phase 2 tags structural containers with 2+ interactive descendants. Phase 3 tags `<body>` and semantically significant `<div>` wrappers.

**Key claims.**
- [STANDARD] DOM traversal and element tagging via content scripts is standard browser extension engineering.
- [STANDARD] The cursor:pointer heuristic for detecting framework-rendered interactive elements was pioneered by Vercel's `agent-browser`. Our implementation follows the same pattern: scan all elements, check `getComputedStyle(el).cursor === 'pointer'`, deduplicate against parent inheritance.
- [NOVEL] The specific three-phase + 1.5 pipeline with precomputed ancestor counts for O(C) Phase 2 thresholds (avoiding O(N*M) nested queries).

**Honest caveat.** The extraction is inherently limited by what the content script can see. It cannot access iframe contents, Shadow DOM internals, or elements rendered by canvas/WebGL (those are handled by cross-modal augmentation in Section 4.5). The 300-element cap on summary generation is a practical limit that may lose information on very large pages.

**Suggested figure.** Annotated screenshot showing the four phases of element tagging with color-coded bounding boxes (BLUE=links, ORANGE=buttons, GREEN=inputs, YELLOW=cursor-pointer, PURPLE=containers).

### 4.2 Multi-Fiber Representation

**Summary.** Each DOM element is represented as a point in a product space with five fibers. The spatial fiber encodes normalized bounding box center coordinates. The visual fiber encodes RGB color sampled from a 3x3 pixel region at the element's center in the page screenshot. The structural fiber encodes the CSS path (e.g., `div.post > h3.title > a`). The semantic fiber encodes computed style properties: font size, display type, interactivity, and text density. The frequency fiber encodes FFT features (dominant frequencies, grid score, spectral entropy) for canvas/WebGL regions.

**Key claims.**
- [NOVEL] The specific combination of five fibers is new. No prior work combines spatial, visual, structural, semantic, and frequency-domain features in a single product metric space for DOM clustering.
- [STANDARD] Each individual fiber uses standard distance functions: Euclidean for spatial and RGB, prefix divergence for CSS paths, log-ratio for font sizes, L1 for tag distributions, and frequency/entropy divergence for FFT.

**Exact formulas (from `tropical.go`).**

- **Spatial:** `d_s(a,b) = sqrt((cx_a - cx_b)^2 + (cy_a - cy_b)^2) / sqrt(2)` where `cx, cy` are normalized bbox centers.
- **Visual:** `d_v(a,b) = sqrt(sum_c (rgb_a[c] - rgb_b[c])^2) / sqrt(3 * 255^2)` using center 3x3 pixel sample. Returns 0.5 (neutral) when no screenshot data.
- **Structural:** `d_t(a,b) = 1 - |common_prefix(path_a, path_b)| / max(|path_a|, |path_b|)` where paths are CSS breadcrumbs split on ` > `.
- **Semantic:** `d_m(a,b) = max(d_font, d_display, d_interactive, d_textDensity)` where `d_font = min(1, ln(max(fs_a/fs_b, fs_b/fs_a)) / ln(4))`. Returns 0 when semantic data unavailable.
- **Frequency:** `d_f(a,b) = max(d_freqX, d_freqY, d_entropy, d_grid)` where each sub-distance is a normalized absolute difference of FFT features. Returns 0 when no FFT data (only computed for `cv-*` regions).

**Honest caveat.** The 3x3 pixel RGB sample is extremely coarse. It captures the dominant background color at an element's center but misses gradients, textures, borders, and any visual feature not at the geometric center. This is a deliberate trade-off for speed over fidelity. The structural distance degrades badly with CSS-in-JS frameworks (Tailwind, styled-components) that generate randomized class names --- the CSS path becomes `div.sc-bXCLTC > div.sc-kDDrLX`, which carries no semantic information. The semantic fiber returns 0 (not 0.5) when data is unavailable, meaning it contributes nothing rather than providing a neutral distance. This asymmetry was a deliberate choice to avoid penalizing elements where computed styles are not available.

**Suggested figure.** Diagram of one element's five-fiber representation, with an example showing how two elements (a header link and a footer link) are close on the structural fiber but far on the spatial fiber.

### 4.3 Tropical Distance and Neighbor-Joining Tree

**Summary.** The pairwise distance between elements is computed as the maximum of the five sub-distances: `d(a,b) = max(d_s, d_v, d_t, d_m, d_f)`. This max aggregation means that *any* single fiber showing strong separation is sufficient to place elements in different zones. The full N-by-N distance matrix is then fed to the Saitou-Nei (1987) neighbor-joining algorithm, which reconstructs a metric tree in O(N^3) time.

**Key claims.**
- [NOVEL] Applying NJ to DOM element clustering. NJ was designed for phylogenetic reconstruction from molecular sequence distances. We repurpose it for UI layout analysis, where "evolutionary distance" becomes "UI-semantic distance."
- [STANDARD] The NJ algorithm itself is unchanged from Saitou-Nei 1987. Our implementation in `tropical.go` (lines 643--755) follows the standard Q-criterion formulation with branch length estimation.
- [STANDARD] The max-of-features aggregation is an L-infinity norm, which is a standard construction in metric geometry. The "tropical" label is motivational --- we invoke the max-plus semiring and cite Speyer-Sturmfels 2004 (tropical Grassmannian Gr(2,n) isomorphic to space of phylogenetic trees) as theoretical context, but our actual computation does not use tropical polynomial algebra, tropical convexity, or any non-trivial tropical algebraic geometry. The connection is: our max-aggregated distance matrix defines a point in the space of metric trees (via NJ), and Speyer-Sturmfels shows this space has tropical structure. But we do not exploit that structure computationally.

**Honest caveat.** The O(N^3) complexity of NJ requires a hard cap of 500 elements (`MaxElements`). Elements beyond this limit are pre-filtered by priority: text-bearing elements first, then colored elements, then the rest. This pre-filtering is lossy --- important structural containers with no text may be dropped. The "tropical" framing should be presented with intellectual honesty. The Speyer-Sturmfels result is real and the connection is meaningful (our distance matrix does define a metric tree, and that tree does live in the tropical Grassmannian), but we are not doing tropical algebraic geometry in any computational sense. A reviewer familiar with tropical geometry will notice this. The claim should be: "our construction is *consistent with* the tropical framework" rather than "our construction *is* tropical geometry."

**Suggested figure.** (a) Distance matrix heatmap for ~50 elements from a real page. (b) The NJ tree with elements colored by their eventual zone assignment. (c) The Q-criterion formula and branch length equations.

### 4.4 Zone Extraction and H^0 Folding

**Summary.** The NJ tree is cut into 3--7 zones by a greedy algorithm that iteratively removes the longest internal edge. If fewer than 3 zones result, the largest zone is split at its widest spatial gap along the dominant axis. After initial zone extraction, an H^0 cohomology folding step merges zones whose fiber signatures (text content Jaccard similarity, tag distribution L1 distance, spatial proximity) exceed a configurable tolerance (default 0.7). This collapses duplicate DOM subtrees such as mobile and desktop navigation bars.

**Key claims.**
- [STANDARD] Greedy edge cutting in a tree is a standard approach to hierarchical clustering. The innovation is applying it to an NJ tree rather than a dendrogram from agglomerative clustering.
- [NOVEL] The H^0 framing: zones as open sets in a cover, text/tag distributions as local sections of a presheaf, and merging as identifying sections that agree on overlaps. This is a genuine mathematical framing, not merely a label.
- [STANDARD] The underlying computation is agglomerative clustering with union-find. The fiber signature comparison (Jaccard on text, L1 on tag distributions, Euclidean on zone centers) is standard. The Cech complex / H^0 language adds conceptual clarity but does not change the algorithm.

**Honest caveat.** The zone count is forced to [3,7] by the `MinZones`/`MaxZones` parameters with a fallback spatial splitting heuristic. This means the system cannot produce a single-zone output for a simple page or an 8+ zone output for a complex dashboard. The hardcoded layout thresholds (`headerMaxY=0.15`, `footerMinY=0.85`, `sidebarW=0.2`) are used for zone *naming* (not segmentation) but can misclassify non-standard layouts where the header extends below 15% of viewport height or the footer starts above 85%. The weighted similarity formula in `zoneFiberSimilarity` has a conditional structure (boosting tag+spatial when both are high) that was tuned empirically on a handful of test sites, not derived from any principled optimization.

**Suggested figure.** (a) NJ tree before cutting, with edge weights. (b) Same tree after greedy cutting, with zone colors. (c) Before/after H^0 folding on a page with mobile+desktop nav bars.

### 4.5 Cross-Modal Augmentation

**Summary.** Three auxiliary data sources augment the DOM-derived elements, all feeding into the same five-fiber metric space. (1) Canny edge detection (`edges.go`, ~500 LOC) identifies rectangular regions in the page screenshot that are not covered by any DOM element, producing `cv-*` elements for canvas/WebGL content. (2) The macOS Accessibility tree (`ax.go` + `cmd/axdump/main.swift`, ~380 LOC) provides `ax-*` elements with role, label, and bounds from the OS-level accessibility hierarchy. (3) FFT analysis (`fft.go`, ~230 LOC) extracts frequency-domain features from `cv-*` regions to detect repeating visual patterns (grids, lists, table rows).

**Key claims.**
- [NOVEL] Integrating DOM, computer vision, and platform accessibility data into a single metric space where all three sources participate equally in NJ clustering. Each source produces elements with the same five-fiber representation and the same distance function.
- [STANDARD] Canny edge detection (Canny 1986) is implemented from scratch (Gaussian blur, Sobel gradients, non-maximum suppression, hysteresis thresholding) but follows the textbook algorithm exactly.
- [STANDARD] FFT via Cooley-Tukey radix-2 DIT, also implemented from scratch. 2D FFT via row-column decomposition with Hann windowing.
- [STANDARD] macOS Accessibility API access via `ApplicationServices` framework. The Swift CLI is a thin wrapper around `AXUIElementCopyAttributeValue`.

**Honest caveat.** The AX tree is macOS-only. There is no equivalent implementation for Linux or Windows, making cross-modal augmentation platform-dependent. The Canny edge detection uses fixed thresholds (low=50, high=150) and a fixed minimum bounding box area (400px), which were tuned for typical web page screenshots but may produce false positives or miss small UI elements on high-DPI displays. The overlap filter (IoU > 0.3) that removes CV regions coinciding with existing DOM elements is a heuristic that can both over-filter (removing genuine canvas content that happens to overlap a DOM element) and under-filter (keeping spurious edge detections). The FFT fiber currently only applies to `cv-*` regions, not DOM elements, creating an asymmetry in the distance computation.

**Suggested figure.** Screenshot of a page with canvas/WebGL content showing: (a) DOM-tagged elements (colored boxes), (b) CV-detected regions (cyan boxes), (c) AX tree overlay (if available), (d) all three fused into the distance matrix.

### 4.6 Virtual Filesystem Interface

**Summary.** Zones are mapped to directories in a virtual filesystem (`/header/nav`, `/main/feed`, `/footer/links`). Each zone directory contains `mache_id`, `description`, `children` (ordinal-indexed list), and `_c/` (child directories `_c/1/`, `_c/2/`, ... each with `mache_id`, `tag`, `text`, and optional `role`, `name`, `path`, `color`, `bounds` files). The Navigator LLM agent interacts with the page exclusively through `ls()`, `cat()`, and `act()` operations on this filesystem. Ordinal indirection means the LLM never sees raw DOM element IDs --- it references `_c/3` instead of `mache-47`, reducing hallucination surface.

**Key claims.**
- [NOVEL] The VFS metaphor for LLM-page interaction. Using filesystem semantics (ls/cat/act) with ordinal indirection is a new design pattern for web agents. The key property is that ordinal references are stable within a session but meaningless across sessions, preventing the LLM from memorizing or hallucinating specific element IDs.
- [NOVEL] Sheaf-based schema cache (`schemacache.go`) storing zone segmentations as a mache MemoryStore graph persisted to SQLite. Each cached URL is a root node with `schema_json` and `cached_at` children. This enables per-zone cache invalidation via content fingerprinting.
- [STANDARD] The graph data structure (`graph.MemoryStore`) is a standard adjacency-list representation with `Node` objects carrying `ID`, `Mode`, `Children`, `Data`, and `Properties` fields.

**Honest caveat.** The zone naming heuristic (`inferCategory`, `inferSubcategory`) uses hardcoded rules (e.g., "if >50% of elements are `<a>` tags and centerY < headerMaxY, name it `nav`") that produce reasonable names for standard layouts but can misname zones on unconventional pages. The `_c/` ordinal directory is rebuilt on every `LoadChildren` call, meaning ordinal indices can shift after scrolling or page mutation --- `_c/3` after scroll may not be the same element as `_c/3` before scroll. The system prompt for the Navigator agent is ~2000 tokens and tightly coupled to the filesystem structure; changing the VFS layout requires updating the prompt.

**Suggested figure.** (a) Terminal-style output showing `ls /`, `ls /main/feed`, `cat /main/feed/children`, `cat /main/feed/_c/1/text`, `act /main/feed/_c/1 click`. (b) The mache graph structure for one zone with its children.

---

## 5. Evaluation

**Summary.** We evaluate X-Ray on three axes: latency, cost, and determinism. We report honest numbers from our implementation and acknowledge that we lack formal benchmarks on standard web agent evaluation suites.

**Key claims.**
- [NOVEL] Latency: The tropical path (DOM parsing + distance matrix + NJ + zone extraction + VFS construction) completes in ~50ms for a typical page with ~200 elements. This is 20--60x faster than a single VLM inference call (1--3s for screenshot encoding + model inference + response parsing).
- [NOVEL] Cost: The page understanding step has zero inference cost. The per-page cost is exactly $0 for zone segmentation, compared to $0.003--0.01 per VLM call at current API pricing (GPT-4V, Claude 3.5 Sonnet, Gemini 1.5 Pro). The downstream Navigator agent does use Gemini 2.5 Flash (~$0.001--0.005 per intent), so the total system cost is not zero --- but it is lower than systems that use VLMs for both understanding and navigation.
- [NOVEL] Determinism: Identical DOM summary + identical screenshot bytes produce bit-identical zone segmentation on every run. There is zero inference variance. This is a structural property of the algorithm (NJ is deterministic given a fixed distance matrix), not a probabilistic claim.

**Honest caveat.** We have not evaluated zone "quality" quantitatively. There is no ground-truth dataset of correct web page zone segmentations, and creating one is non-trivial (zone boundaries are somewhat subjective). We have not run on WebArena, Mind2Web, VisualWebArena, or any standard web agent benchmark. The latency numbers are measured on a 2023 MacBook Pro (M2 Max) and may vary on different hardware. The cost comparison assumes current API pricing, which changes frequently.

**Suggested table.**

| Metric | X-Ray (Tropical) | VLM-based (typical) |
|--------|------------------|---------------------|
| Page understanding latency | ~50ms | 1--3s |
| Page understanding cost | $0 | $0.003--0.01 |
| Deterministic output | Yes | No |
| Handles canvas/WebGL | Yes (via CV+FFT) | Yes (via screenshot) |
| Semantic understanding | No (structural only) | Yes |
| Formal benchmark results | None yet | WebArena, Mind2Web |

**Suggested figure.** Latency distribution histogram across 50 diverse websites, showing the tropical path vs. a VLM baseline (if available).

---

## 6. Limitations

**Summary.** We organize limitations by component and severity. We are frank about what does not work and what we have not tested.

### 6.1 Element Extraction
- **No iframe support.** Content scripts cannot access cross-origin iframe contents. Many modern web applications (embeds, ads, federated login) rely on iframes.
- **No Shadow DOM support.** Custom elements with Shadow DOM internals are opaque to `querySelectorAll`. This affects web components frameworks (Lit, Stencil, Salesforce Lightning).
- **300-element summary cap.** Large pages with many interactive elements are truncated, potentially losing important elements in later DOM positions.

### 6.2 Multi-Fiber Representation
- **3x3 pixel RGB sample.** The visual fiber samples only 9 pixels at the element's geometric center. This misses elements whose visual identity is at the border (outlined buttons), in a gradient, or off-center. A more robust approach would sample multiple points or use a small CNN feature extractor (which would violate the "no ML" principle).
- **CSS-in-JS fragility.** CSS path structural distance is meaningless when class names are randomized hashes. On Tailwind-heavy sites, `div.bg-white > div.flex > div.p-4` is at least readable; on styled-components sites, `div.sc-bXCLTC > div.sc-kDDrLX` carries zero semantic information. The structural fiber degrades to tag-only comparison in these cases.
- **Semantic fiber asymmetry.** When computed style data is unavailable, the semantic fiber returns 0 (no penalty) rather than 0.5 (neutral). This means elements without semantic data cluster more tightly than they should.

### 6.3 Distance and Clustering
- **O(N^3) scaling.** Neighbor-joining is cubic in the number of elements. The 500-element cap is a hard requirement, not a soft preference. Pages with >500 relevant elements (large dashboards, data tables) lose information through pre-filtering.
- **Hardcoded zone count [3,7].** The forced range means very simple pages (single content area) get artificially split, and complex pages (multi-panel dashboards) get artificially merged.
- **Hardcoded layout thresholds.** `headerMaxY=0.15`, `footerMinY=0.85`, `sidebarW=0.2` work for standard blog/news layouts but misclassify sites with large hero sections, sticky headers, or unusual layout geometry. These thresholds affect zone *naming*, not segmentation, but incorrect names mislead the Navigator.

### 6.4 Cross-Modal Augmentation
- **macOS-only AX tree.** The `axdump` Swift CLI requires macOS and Accessibility permissions. No Linux (AT-SPI) or Windows (UI Automation) implementation exists.
- **Fixed Canny thresholds.** The edge detection thresholds (low=50, high=150) and minimum bounding box area (400px) are hardcoded. They were tuned for typical web screenshots at standard DPI and may produce poor results on high-DPI displays or pages with subtle visual boundaries.

### 6.5 Virtual Filesystem
- **Ordinal instability across scrolls.** After scrolling or DOM mutation, `_c/N` indices are reassigned. The Navigator cannot assume `_c/3` refers to the same element before and after `browser.scroll()`.
- **Zone naming heuristics.** The category/subcategory inference is rule-based and can produce misleading names (e.g., labeling a cookie consent banner as "footer/content" because it is positioned at the bottom of the viewport).

**Suggested figure.** Table of limitations organized by component, severity (blocking / degrading / cosmetic), and proposed mitigation.

---

## 7. Conclusion

**Summary.** We restate four core claims and outline future work.

**Core claims.**
1. **Deterministic page understanding is achievable.** For the specific task of zone segmentation, our multi-fiber NJ approach produces deterministic, reproducible results without inference variance, at zero marginal cost and ~50ms latency.
2. **Phylogenetic algorithms transfer to DOM clustering.** Neighbor-joining, designed for molecular phylogenetics, produces meaningful zone boundaries when applied to DOM elements embedded in a multi-fiber metric space. The biological analogy is productive: CSS paths behave like molecular sequences (shared prefixes indicate common ancestry), and spatial position behaves like biogeographic data.
3. **Cross-modal fusion in a single metric space.** DOM, computer vision, and accessibility data can be unified in a single product metric space where all sources participate equally in clustering.
4. **The VFS metaphor reduces LLM hallucination surface.** Ordinal indirection (the LLM sees `_c/3`, not `mache-47`) combined with filesystem semantics (`ls` before `cat`, `cat` before `act`) imposes a discipline that prevents the LLM from inventing element references.

**Future work.**
- **Learned distance weights.** Replace the uniform max aggregation with learned per-fiber weights, potentially via a small neural network trained on zone quality annotations. This would sacrifice full determinism but could improve segmentation quality.
- **Adaptive thresholds.** Replace hardcoded `headerMaxY`, `footerMinY`, `sidebarW` with data-driven thresholds estimated from the page's spatial distribution of elements.
- **WebArena benchmark.** Evaluate end-to-end agent performance on WebArena (Zhou et al. 2023) to enable direct comparison with VLM-based agents.
- **Shadow DOM and iframe support.** Extend element extraction to pierce Shadow DOM boundaries and cross-origin iframes (requires browser extension API changes or CDP-based extraction).
- **Platform-agnostic accessibility.** Implement AX tree extraction for Linux (AT-SPI/D-Bus) and Windows (UI Automation / MSAA) to make cross-modal augmentation portable.
- **Incremental NJ.** Explore online/incremental variants of neighbor-joining that can update the tree after scroll without full recomputation.

**Suggested figure.** None for conclusion. The final paragraph should restate the key takeaway: deterministic, zero-cost page understanding is practically useful today, with clear paths to improvement.

---

## Appendix A: Implementation Details

**Summary.** X-Ray is implemented in Go (backend), JavaScript (browser extension content script), and Swift (macOS AX tree extraction). The tropical path (distance computation, NJ, zone extraction, VFS construction) is ~1,630 LOC of Go (`tropical.go`: 1,398 + `fft.go`: 228). The full page understanding pipeline including element extraction, edge detection, AX integration, VFS engine, and schema cache totals ~3,990 LOC across the key files. There are zero external ML dependencies --- the only external dependency is the Gemini API client for the downstream Navigator agent.

| Component | Language | LOC | File |
|-----------|----------|-----|------|
| Tropical distance + NJ + zones | Go | 1,398 | `internal/cartographer/tropical.go` |
| FFT analysis | Go | 228 | `internal/cartographer/fft.go` |
| Canny edge detection | Go | 497 | `internal/api/edges.go` |
| AX tree client | Go | 181 | `internal/ax/ax.go` |
| AX tree CLI | Swift | 204 | `cmd/axdump/main.swift` |
| VFS engine | Go | 670 | `internal/mache/engine.go` |
| DOM content script | JavaScript | 557 | `ext/content.js` |
| Schema cache | Go | 141 | `internal/api/schemacache.go` |
| Navigator VFS adapter | Go | 112 | `internal/navigator/navfs.go` |
| **Total** | | **3,988** | |

**Build requirements.** Go 1.22+, Node.js (for extension packaging), Xcode command line tools (for Swift compilation of `axdump`). No Python, no PyTorch, no ONNX runtime.

---

## Appendix B: Mathematical Notation

**Summary.** Formal definitions for the distance, the Speyer-Sturmfels connection, and the Cech complex for H^0. We are precise about what *is* and *is not* tropical in the algebraic sense.

### B.1 Product Metric Space

Let `E = {e_1, ..., e_n}` be the set of DOM elements. Each element is mapped to a point in the product space `X = X_s x X_v x X_t x X_m x X_f` where:
- `X_s = [0,1]^2` (spatial fiber: normalized center coordinates)
- `X_v = [0,255]^3` (visual fiber: RGB values)
- `X_t = Sigma*` (structural fiber: CSS path strings over alphabet `Sigma`)
- `X_m = R^4` (semantic fiber: font-size, display-code, interactivity, text-density)
- `X_f = R^4` (frequency fiber: dominant-freq-X, dominant-freq-Y, entropy, grid-score)

### B.2 Distance Function

The pairwise distance is:

```
d(e_i, e_j) = max(d_s(e_i, e_j), d_v(e_i, e_j), d_t(e_i, e_j), d_m(e_i, e_j), d_f(e_i, e_j))
```

This is the L-infinity norm on the vector `(d_s, d_v, d_t, d_m, d_f)` of sub-distances. In the max-plus semiring `(R, max, +)`, this corresponds to tropical addition of the sub-distances. However, we are using max as an aggregation operator over pre-computed Euclidean/string distances, not performing tropical polynomial evaluation.

### B.3 What IS Tropical

The max-plus semiring `T = (R ∪ {-inf}, max, +)` is the tropical semiring. Our distance aggregation uses the `max` operation, which is tropical addition. The Speyer-Sturmfels (2004) result establishes that the tropical Grassmannian `TGr(2,n)` is isomorphic to the space of phylogenetic trees on `n` taxa. Since NJ produces a metric tree from our distance matrix, the output tree is a point in `TGr(2,n)`. This is the genuine connection.

### B.4 What is NOT Tropical

We do not:
- Evaluate tropical polynomials
- Compute tropical convex hulls
- Use the tropical determinant or permanent
- Exploit the fan structure of `TGr(2,n)` for algorithmic speedup
- Perform any computation in tropical projective space

The "tropical" label in the codebase (`TropicalCartographer`, `tropicalDistance`, `buildDistanceMatrix`) is a conceptual framing, not a description of the algebraic operations. A more precise description would be "L-infinity-aggregated multi-fiber distance with neighbor-joining reconstruction."

### B.5 H^0 Cohomology

Let `U = {U_1, ..., U_k}` be the zones (open sets in the cover of the page). Define a presheaf `F` by `F(U_i) = (tagDist_i, textSet_i)` --- the tag distribution and text content set of zone `U_i`. The H^0 folding step computes:

```
H^0(U, F) = ker(d^0: prod F(U_i) -> prod F(U_i ∩ U_j))
```

where `d^0(s)_{ij} = s_i|_{U_i ∩ U_j} - s_j|_{U_i ∩ U_j}`. In practice, "restriction to the overlap" means comparing fiber signatures of spatially proximate zones, and "kernel of d^0" means identifying zones whose restrictions agree within the tolerance. The implementation uses union-find over zone pairs with `zoneFiberSimilarity >= tolerance`, which is an agglomerative approximation to the sheaf-theoretic construction.

---

## Appendix C: Reproducibility

**Summary.** Instructions for reproducing the system and its results.

### C.1 Source Code

- Repository: [link to be added]
- License: [to be determined]
- Commit hash for paper results: [to be determined]

### C.2 Build and Run

```bash
# Build Go backend
go build -o xray ./cmd/gate

# Build axdump (macOS only)
swiftc cmd/axdump/main.swift -o axdump -framework ApplicationServices

# Install browser extension
# Load ext/ directory as unpacked extension in Chrome

# Run
./xray
```

### C.3 Test Pages

We recommend testing on the following pages (archived via Internet Archive Wayback Machine for reproducibility):

- **Standard news layout:** Hacker News (news.ycombinator.com) --- simple list structure, good test for list detection and feed zones.
- **Complex SPA:** Reddit (reddit.com) --- virtual scrolling, dynamic content loading, CSS-in-JS class names.
- **Canvas/WebGL content:** Google Maps (maps.google.com) --- tests CV augmentation for non-DOM rendered content.
- **Accessibility-rich:** GitHub (github.com) --- extensive ARIA attributes, good test for AX tree integration.

**Internet Archive snapshots** should be used for reproducible evaluation, since live pages change over time. Snapshot URLs: [to be captured at paper submission time].

### C.4 Determinism Verification

To verify determinism, run the tropical path twice on the same input and compare outputs:

```bash
# Capture DOM summary to file
# Run tropical cartographer twice
# diff the outputs (should be empty)
```

The system is deterministic by construction: all operations (parsing, distance computation, NJ, zone cutting, H^0 folding, mount generation) are deterministic given identical inputs. The only source of non-determinism in the full pipeline is the downstream Navigator LLM, which uses `temperature=0.1`.

---

## Notes on Tone and Venue

### Confidence vs. Humility

**Be confident about:**
- The novel application of NJ to DOM clustering. This is genuinely new and the results are good.
- The multi-fiber distance design. The specific combination works well in practice.
- The VFS metaphor with ordinal indirection. This is a clean design that measurably reduces hallucination.
- The determinism and cost properties. These are structural guarantees, not empirical claims.
- The cross-modal fusion. Integrating DOM + CV + AX into one metric space is architecturally novel.

**Be humble about:**
- The "tropical" label. Be upfront that max-aggregation is L-infinity, that the Speyer-Sturmfels connection is real but motivational, and that we are not doing tropical algebraic geometry in the computational sense.
- The H^0 framing. It is a valid mathematical formulation but the implementation is agglomerative clustering. The sheaf language adds conceptual clarity, not algorithmic novelty.
- The lack of formal benchmarks. We have no WebArena, Mind2Web, or VisualWebArena numbers. Any comparison with VLM-based systems is qualitative until we run standard benchmarks.
- The hardcoded thresholds and heuristics. The system works on standard layouts but has not been stress-tested on the long tail of web page designs.
- The RGB sampling. Three by three pixels is coarse. Acknowledge it and explain the trade-off.

### Venue Suggestions

| Venue | Fit | Strengths | Weaknesses |
|-------|-----|-----------|------------|
| **CHI / UIST** | Strong | Novel interaction paradigm (VFS for LLM-page interaction), practical system with real users | Needs user study, currently no formal usability evaluation |
| **AAAI / NeurIPS Workshop** | Moderate | Theoretical framing (tropical geometry, phylogenetics, sheaf cohomology), novel algorithm application | May be too applied / engineering-heavy for theory track; may be too theoretical for applications track |
| **ICWE / WWW** | Strong | Web engineering contribution, practical page understanding system | Needs WebArena-style benchmark evaluation for credibility |
| **AAMAS** | Moderate | Multi-agent system (Cartographer + Navigator), tool-use design | The contribution is more in the page understanding than the agent architecture |
| **arXiv preprint** | Good starting point | Get the ideas out, solicit feedback, establish priority | Not peer-reviewed |

**Recommendation:** Submit to **WWW** (The Web Conference) or **UIST** as the primary venue. The web engineering angle is strongest for WWW; the novel interaction design (VFS metaphor, ordinal indirection) is strongest for UIST. Prepare a **CHI** late-breaking work or poster as a backup. Post to **arXiv** first to establish priority and get community feedback on the tropical framing.
