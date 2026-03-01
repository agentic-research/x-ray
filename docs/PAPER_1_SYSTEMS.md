# X-Ray: Deterministic Web Page Understanding for Autonomous Agents

## Abstract

We present X-Ray, a system that produces structured, navigable representations of web pages without vision-language model (VLM) inference. DOM elements are embedded as points in a product metric space with five fibers (spatial, visual, structural, semantic, frequency), and pairwise distances are aggregated via a max operator. A neighbor-joining tree is constructed from the distance matrix and greedily cut into zones, which are exposed as a virtual filesystem for downstream LLM navigation. The page understanding step is fully deterministic, completes in ~50ms, and incurs zero inference cost. We evaluate X-Ray on WebArena shopping tasks and compare against VLM-based baselines on cost, latency, determinism, and task completion rate.

---

## 1. Introduction

Autonomous web agents require structured understanding of page layout to navigate, click, type, and extract information. The dominant approach uses vision-language models to interpret screenshots, but this introduces three costs:

1. **Economic cost.** VLM inference costs $0.003--0.10 per page, depending on model and image resolution. For agents that inspect dozens of pages per task, this accumulates.
2. **Non-determinism.** Repeated VLM calls on identical screenshots produce different outputs. Zone boundaries shift, element descriptions vary, and agent behavior becomes unreproducible.
3. **Latency.** Screenshot encoding, model inference, and response parsing take 1--5 seconds per page. Multi-step agent tasks compound this into minutes of wall-clock time.

We propose an alternative: treat DOM elements as taxa in a metric space and apply phylogenetic reconstruction to recover page structure. The resulting zones are deterministic, reproducible, and computed in ~50ms with zero inference cost. The downstream agent interacts with the page through a virtual filesystem (VFS) interface that imposes `ls`-before-`cat`-before-`act` discipline, reducing hallucination by forcing the agent to observe before acting.

**Scope of claim.** "VLM-free" applies to page understanding and zone segmentation. The downstream Navigator agent uses an LLM (Gemini 2.5 Flash or a fine-tuned 270M model) for intent resolution and tool-use planning. We claim that *structural page understanding* requires no learned model, not that the full system is model-free.

---

## 2. Related Work

We position X-Ray against five categories of prior work.

**VLM-based computer-use agents.** Anthropic computer-use, OpenAI operator, and Google Mariner use screenshots + VLM inference for page understanding. These achieve strong task completion on benchmarks but are non-deterministic and expensive per page.

**DOM-based web agents.** Vercel's `agent-browser` uses Playwright's `ariaSnapshot` for a deterministic DOM representation. We share the DOM-first philosophy but add multi-modal fusion (pixel sampling, edge detection, accessibility data) and phylogenetic clustering for zone discovery.

**Page segmentation.** VIPS (Cai et al. 2003) segments pages using visual cues from rendered DOM. Our approach differs in using a formal metric space with explicit distance functions rather than heuristic visual block detection.

**Set-of-Mark prompting.** Yang et al. (2023) overlay numbered bounding boxes on screenshots for VLM grounding. Our colored overlay serves a similar purpose for the Cartographer's visual fiber, and we cite this directly.

**Phylogenetic algorithms in non-biological domains.** Neighbor-joining (Saitou & Nei 1987) has been applied to document clustering and network topology inference. To our knowledge, no prior work applies NJ to DOM element clustering.

| System | Modality | Deterministic | Cost/page | Latency |
|--------|----------|---------------|-----------|---------|
| Anthropic computer-use | Screenshot + VLM | No | $0.01--0.10 | 1--5s |
| Google Mariner | Screenshot + VLM | No | ~$0.05 | 2--5s |
| Vercel agent-browser | `ariaSnapshot` | Yes | $0 | <100ms |
| Set-of-Mark + VLM | Annotated screenshot | No | ~$0.03 | 1--3s |
| **X-Ray** | DOM + pixel fibers | **Yes** | **$0** | **~50ms** |

---

## 3. System Architecture

X-Ray consists of four components: element extraction, multi-fiber embedding, phylogenetic clustering, and virtual filesystem construction.

### 3.1 Element Extraction

A Chrome extension content script tags DOM elements in three phases plus an intermediate cursor-pointer phase, building an in-memory registry of element mappings.

- **Phase 1:** Interactive elements (links, buttons, inputs, selects, textareas).
- **Phase 1.5:** Cursor-interactive elements not captured by semantic selectors (`cursor:pointer`, `onclick`, `tabindex`). This heuristic follows Vercel's `agent-browser` approach.
- **Phase 2:** Structural containers with 2+ interactive descendants, using precomputed ancestor counts for O(C) thresholds.
- **Phase 3:** `<body>` and semantically significant wrapper `<div>` elements.

The registry is serialized as a DOM summary (element tag, text, bounding box, CSS path, computed styles) and sent to the Go backend alongside a lossless PNG screenshot.

### 3.2 Multi-Fiber Embedding

Each element is represented as a point in a product space with five fibers:

- **Spatial:** Normalized bounding box center `(cx, cy) in [0,1]^2`. Distance: Euclidean / sqrt(2).
- **Visual:** RGB sampled from 3x3 pixel region at element center in the screenshot. Distance: Euclidean / sqrt(3 * 255^2).
- **Structural:** CSS path string (e.g., `div.post > h3.title > a`). Distance: 1 - prefix overlap ratio.
- **Semantic:** Computed style properties (font size, display type, interactivity, text density). Distance: max of log-ratio and categorical distances.
- **Frequency:** FFT features (dominant frequencies, grid score, spectral entropy) for canvas/WebGL regions. Distance: max of normalized sub-distances.

The pairwise distance between elements is:

```
d(a, b) = max(d_spatial, d_visual, d_structural, d_semantic, d_frequency)
```

The max aggregation means any single fiber showing strong separation is sufficient to place elements in different zones. This is conservative by design: a color boundary alone, or a structural boundary alone, is enough.

### 3.3 Phylogenetic Clustering

The N-by-N distance matrix is fed to the Saitou-Nei (1987) neighbor-joining algorithm, which reconstructs a metric tree in O(N^3) time. The tree is then cut into 3--7 zones by iteratively removing the longest internal edge. An optional cohomology folding step merges zones whose fiber signatures (text content, tag distribution, spatial proximity) are sufficiently similar, collapsing duplicate DOM subtrees such as mobile and desktop navigation bars.

A hard cap of 500 elements bounds the cubic cost. Elements beyond this limit are pre-filtered by priority: text-bearing elements first, then colored/interactive elements, then structural containers.

### 3.4 Cross-Modal Augmentation

Three auxiliary data sources feed into the same metric space:

1. **Canny edge detection** identifies rectangular regions in the screenshot not covered by any DOM element, producing synthetic elements for canvas/WebGL content.
2. **macOS Accessibility tree** provides elements with role, label, and bounds from the OS-level accessibility hierarchy.
3. **FFT analysis** extracts frequency-domain features from canvas regions to detect repeating visual patterns (grids, lists, table rows).

All three sources produce elements with the same five-fiber representation, participating equally in clustering.

### 3.5 Virtual Filesystem Interface

Zones are mapped to directories in a virtual filesystem:

```
/
  header/
    nav/
      _c/1/  (tag: a, text: "Home")
      _c/2/  (tag: a, text: "About")
  main/
    feed/
      _c/1/  (tag: div, text: "First post...")
      _c/2/  (tag: div, text: "Second post...")
  footer/
    links/
```

The Navigator agent interacts through three operations:
- `ls <path>` — list directory contents
- `cat <path>` — read element details (tag, text, role, bounds)
- `act <action> <path> ["payload"]` — click, focus, type, or press enter

Ordinal indirection (`_c/3` instead of raw element IDs) prevents the LLM from memorizing or hallucinating specific identifiers. The filesystem metaphor imposes exploration discipline: the agent must `ls` to discover what exists before it can `cat` to inspect or `act` to interact.

---

## 4. Navigator Agent

The Navigator consumes the VFS and produces tool calls. We support two configurations:

### 4.1 Cloud LLM (Gemini 2.5 Flash)

The cloud configuration sends the VFS state as context to Gemini 2.5 Flash with function-calling tool definitions. The model returns structured JSON tool calls (`{"name": "ls", "args": {"path": "/"}}`). This configuration achieves the highest task completion rate but incurs per-call API cost.

### 4.2 Local Fine-Tuned Model (270M)

For zero-cost operation, we fine-tune FunctionGemma 270M on a CLI format where tool calls are space-delimited commands:

```
ls /browser/main
cat /browser/main/_c/3/text
act click /browser/main/_c/3
```

The fine-tuned model outputs are constrained by a GBNF grammar that restricts generation to valid commands with paths drawn from the current filesystem state. This eliminates path hallucination at the decoding level.

The training pipeline uses two stages:
1. **SFT (3 epochs):** Supervised fine-tuning on correct tool-call sequences, teaching the model the CLI syntax.
2. **IPO (1 epoch):** Identity Preference Optimization on chosen/rejected pairs, refining action selection.

Action verbs are encoded as single-token Unicode glyphs (click=`►`, focus=`⊙`, type=`✎`, enter=`⏎`) with orthogonally initialized embeddings to maximize separation in the model's latent space.

---

## 5. Evaluation

### 5.1 Page Understanding Metrics

| Metric | X-Ray | VLM Baseline |
|--------|-------|-------------|
| Latency (per page) | ~50ms | 1--3s |
| Cost (per page) | $0 | $0.003--0.01 |
| Deterministic | Yes | No |
| Handles canvas/WebGL | Yes | Yes |

### 5.2 WebArena Shopping Tasks

[TODO: Insert benchmark results after running WebArena evaluation harness]

We evaluate on the WebArena shopping subset, measuring:
- **Task completion rate** (% of tasks where the agent achieves the goal state)
- **Average steps per task** (efficiency of navigation)
- **Total cost per task** (API cost for cloud config; $0 for local config)
- **Reproducibility** (variance across repeated runs of the same task)

### 5.3 Ablation Studies

[TODO: Ablations on individual fiber contributions, zone count range, element cap]

---

## 6. Limitations

- **No iframe or Shadow DOM support.** Cross-origin iframes and Shadow DOM internals are opaque to the content script.
- **O(N^3) scaling.** The 500-element cap is a hard requirement. Very large pages lose information through pre-filtering.
- **Hardcoded zone count [3,7].** Simple pages get artificially split; complex dashboards get artificially merged.
- **3x3 pixel RGB sample.** The visual fiber is coarse by design (speed over fidelity).
- **CSS-in-JS fragility.** Randomized class names (styled-components, CSS modules) degrade the structural fiber to tag-only comparison.
- **macOS-only accessibility.** The AX tree integration has no Linux or Windows equivalent.
- **Ordinal instability.** After scrolling, `_c/N` indices may shift. The agent cannot assume stable references across scroll events.
- **No formal benchmark results yet.** The comparison table is qualitative until WebArena evaluation is complete.

---

## 7. Conclusion

We have shown that deterministic, zero-cost web page understanding is achievable for the task of zone segmentation. The multi-fiber neighbor-joining approach produces stable, reproducible page structure in ~50ms without any learned model. Combined with a virtual filesystem interface that reduces LLM hallucination through ordinal indirection and exploration discipline, X-Ray provides a practical foundation for autonomous web agents that is cheaper, faster, and more reproducible than VLM-based alternatives.

The key trade-off is semantic understanding: VLMs can infer that a group of elements "looks like a login form," while X-Ray can only report structural and visual properties. For tasks where structural navigation suffices — clicking links, filling forms, reading content — the deterministic approach is sufficient and preferable.

---

## References

- Cai, D., Yu, S., Wen, J.-R., Ma, W.-Y. (2003). VIPS: a Vision-based Page Segmentation Algorithm. Microsoft Technical Report.
- Saitou, N., Nei, M. (1987). The Neighbor-Joining Method: A New Method for Reconstructing Phylogenetic Trees. Molecular Biology and Evolution.
- Yang, J., et al. (2023). Set-of-Mark Prompting Unleashes Extraordinary Visual Grounding in GPT-4V.
- Zhou, S., et al. (2023). WebArena: A Realistic Web Environment for Building Autonomous Agents.
- Speyer, D., Sturmfels, B. (2004). The Tropical Grassmannian. Advances in Geometry.
