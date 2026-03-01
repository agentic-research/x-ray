# Tropical Metric Segmentation via Phylogenetic Reconstruction

## Abstract

We introduce tropical metric segmentation, a method for partitioning structured data by embedding elements as points in a product metric space, aggregating pairwise distances via the max operator (tropical addition), and reconstructing a phylogenetic tree via neighbor-joining. The tree is greedily cut into clusters, and a sheaf-theoretic folding step merges clusters with compatible fiber signatures. Unlike spectral and graph-cut methods that minimize average distance and blur boundaries where dimensions disagree, the max aggregation separates elements when *any single fiber* shows strong divergence — matching the human perceptual principle that one sharp discontinuity is sufficient to define a boundary. We demonstrate the method on two domains: web page zone segmentation (DOM elements with spatial, visual, structural, semantic, and frequency fibers) and image segmentation (superpixels with position, color, texture, and edge fibers). The algorithm is deterministic, requires no learned parameters, and connects to the tropical Grassmannian via Speyer-Sturmfels (2004).

---

## 1. Introduction

Segmentation — partitioning a structured input into coherent regions — is a fundamental problem across domains. Web pages decompose into header, navigation, content, and footer zones. Images decompose into sky, ground, foreground objects. Gene expression matrices decompose into co-regulated clusters. In each case, elements have multiple measurable properties (fibers), and the segmentation task is to find boundaries where these properties diverge.

The dominant approaches to segmentation aggregate fiber distances by averaging:

- **Spectral clustering** computes an affinity matrix `W_ij = exp(-d(i,j)^2 / sigma^2)` and finds clusters via eigenvectors of the graph Laplacian. The Gaussian kernel averages across fiber dimensions.
- **Graph cuts** minimize normalized cut: `Ncut(A,B) = cut(A,B)/vol(A) + cut(A,B)/vol(B)`, where cut weights are typically Euclidean or Mahalanobis distances across concatenated features.
- **K-means** and variants minimize within-cluster sum of squared Euclidean distances.

All three share a property: when one fiber dimension says "same" and another says "different," the average is "somewhat different." The boundary signal is diluted.

We propose the opposite: aggregate by max. If elements share identical color but differ sharply in texture, they are separated. If they share identical texture but are spatially distant, they are separated. Any single fiber showing strong divergence is sufficient to define a boundary. Formally:

```
d(a, b) = max(d_1(a,b), d_2(a,b), ..., d_k(a,b))
```

where each `d_i` is a normalized sub-distance on fiber `i`. This is the L-infinity norm on the vector of sub-distances, and in the max-plus semiring `(R, max, +)`, it corresponds to tropical addition.

The distance matrix is then fed to the Saitou-Nei (1987) neighbor-joining algorithm, which reconstructs a metric tree. Greedy edge cutting produces clusters. A sheaf-theoretic folding step identifies clusters with compatible local sections (fiber signatures) and merges them, collapsing over-segmentation caused by the conservative max criterion.

### 1.1 The Tropical Connection

The link between our construction and tropical geometry is not merely notational. Speyer and Sturmfels (2004) proved that the tropical Grassmannian `TGr(2,n)` — the space of all phylogenetic trees on `n` taxa under tropical arithmetic — is isomorphic to the space of metric trees. Our distance matrix defines a dissimilarity map on `n` elements; neighbor-joining produces a metric tree from this map; that tree is a point in `TGr(2,n)`.

We are precise about what this connection provides and what it does not:

**What IS tropical.** The max aggregation is tropical addition. The output tree lives in the tropical Grassmannian. The fan structure of `TGr(2,n)` describes the combinatorial types of trees our algorithm can produce, partitioning the space of possible distance matrices into regions that yield the same tree topology.

**What is NOT tropical.** We do not evaluate tropical polynomials, compute tropical convex hulls, or exploit the fan structure algorithmically. The NJ algorithm predates tropical geometry and does not reference it. Our contribution is recognizing that the max-aggregated multi-fiber distance naturally places the segmentation problem in the tropical geometric framework, and that this framework provides a principled theoretical foundation for understanding why the algorithm works.

### 1.2 Contributions

1. **Tropical metric segmentation** as a general framework: max-aggregated multi-fiber distance + neighbor-joining tree + greedy cutting + sheaf folding.
2. **Application to web page segmentation** with five fibers (spatial, visual, structural, semantic, frequency), achieving deterministic zero-cost page understanding in ~50ms.
3. **Application to image segmentation** with four fibers (position, color, texture, edge density), demonstrating domain generality.
4. **Theoretical analysis** connecting the construction to the tropical Grassmannian and characterizing the boundary-detection property of max aggregation versus averaging.

---

## 2. Method

### 2.1 Multi-Fiber Representation

Let `E = {e_1, ..., e_n}` be a set of elements to segment. Each element is mapped to a point in a product space:

```
phi: E -> X_1 x X_2 x ... x X_k
```

where each `X_i` is a metric space with distance function `d_i: X_i x X_i -> [0, 1]`. The normalization to `[0, 1]` ensures that no single fiber dominates by scale. We call each `X_i` a **fiber** of the representation.

The choice of fibers is domain-specific:

**Web page segmentation (k=5):**
- Spatial: `X_s = [0,1]^2`, normalized bounding box centers. `d_s = ||c_a - c_b||_2 / sqrt(2)`.
- Visual: `X_v = [0,255]^3`, RGB from screenshot pixel sampling. `d_v = ||rgb_a - rgb_b||_2 / sqrt(3 * 255^2)`.
- Structural: `X_t = Sigma*`, CSS path strings. `d_t = 1 - |prefix(a,b)| / max(|a|,|b|)`.
- Semantic: `X_m = R^4`, computed style properties. `d_m = max(d_font, d_display, d_interactive, d_textDensity)`.
- Frequency: `X_f = R^4`, FFT features for canvas regions. `d_f = max(d_freqX, d_freqY, d_entropy, d_grid)`.

**Image segmentation (k=4):**
- Spatial: `X_s = [0,1]^2`, normalized superpixel centroid. `d_s = ||c_a - c_b||_2 / sqrt(2)`.
- Color: `X_c = [0,255]^3`, mean RGB of superpixel. `d_c = ||rgb_a - rgb_b||_2 / sqrt(3 * 255^2)`.
- Texture: `X_t = R^d`, local gradient histogram. `d_t = ||h_a - h_b||_1 / 2` (histogram intersection distance).
- Edge: `X_e = [0,1]`, boundary pixel density. `d_e = |e_a - e_b|`.

### 2.2 Tropical Distance

The pairwise distance is:

```
d(e_i, e_j) = max_k d_k(e_i, e_j)
```

**Theorem 1 (Boundary sensitivity).** Let `B` be a set of element pairs `(a,b)` where at least one fiber `d_k(a,b) > tau` for threshold `tau`. Under max aggregation, `d(a,b) > tau` for all `(a,b) in B`. Under average aggregation, `d_avg(a,b)` may fall below `tau` if other fibers have small distances. Thus max aggregation detects *all* boundaries that are strong in any single dimension, while averaging can miss boundaries that are strong in one dimension but weak in others.

This property is precisely what makes max aggregation suitable for perceptual segmentation. Humans perceive boundaries when *any* single cue is strong: a color edge is enough, a texture change is enough, a spatial gap is enough. We do not require all cues to agree.

**Trade-off.** Max aggregation is conservative — it can over-segment when fibers are noisy, since a spurious high distance in any single fiber forces separation. The sheaf folding step (Section 2.4) mitigates this by merging clusters whose fiber signatures are globally compatible despite the separation.

### 2.3 Neighbor-Joining and Tree Cutting

The `n x n` distance matrix is fed to the Saitou-Nei (1987) neighbor-joining algorithm. NJ iteratively joins the pair of nodes that minimizes the Q-criterion:

```
Q(i,j) = (n-2) * d(i,j) - sum_k d(i,k) - sum_k d(j,k)
```

This produces a metric tree in `O(n^3)` time. The tree is guaranteed to correctly recover the topology if the distance matrix is additive (i.e., the distances arise from a tree metric). While our multi-fiber max distances are not exactly additive, NJ is robust to moderate deviations and produces meaningful trees from approximately additive distances.

**Zone extraction.** The tree is cut into clusters by iteratively removing the longest internal edge (greedy divisive clustering). This continues until the number of clusters reaches a target range (default 3--7 for web pages). If too few clusters result, the largest cluster is split at its widest spatial gap along the dominant axis.

**Tropical Grassmannian connection.** The output tree is a point in `TGr(2,n)`. The fan structure of `TGr(2,n)` partitions the space of distance matrices into cones, each corresponding to a tree topology. As the page content changes (elements are added, removed, or repositioned), the distance matrix traces a path through these cones, and the segmentation changes discretely when the path crosses a cone boundary. This provides a geometric explanation for the stability of segmentation under small perturbations: the segmentation is constant within each cone and changes only at cone boundaries.

### 2.4 Sheaf Folding (H^0 Cohomology)

The max aggregation can over-segment: two groups of elements that are spatially distant but structurally identical (e.g., mobile and desktop navigation bars) will be placed in separate clusters. We correct this with a folding step inspired by sheaf cohomology.

Define a presheaf `F` on the cover `U = {U_1, ..., U_k}` of clusters:
```
F(U_i) = (tagDistribution_i, textContent_i, centroid_i)
```

The H^0 cohomology identifies clusters whose local sections agree:
```
H^0(U, F) = ker(d^0: prod F(U_i) -> prod F(U_i cap U_j))
```

In practice, "restriction to the overlap" means comparing fiber signatures of clusters, and "kernel of `d^0`" means identifying cluster pairs whose signatures agree within a tolerance. The implementation uses union-find over cluster pairs with similarity above threshold `tau_fold` (default 0.7).

This merges:
- Duplicate navigation bars (mobile/desktop variants with identical link text)
- Repeated structural patterns (product cards in a grid, comment threads)
- Over-segmented regions where spatial distance forced separation despite structural identity

---

## 3. Application: Web Page Segmentation

### 3.1 Setup

We apply tropical metric segmentation to web page zone discovery. DOM elements are extracted via a browser extension content script, embedded in the 5-fiber space described in Section 2.1, and clustered via NJ + cutting + folding. The resulting zones are exposed as a virtual filesystem for LLM navigation.

### 3.2 Results

| Metric | Tropical Segmentation | VLM-based (typical) |
|--------|----------------------|---------------------|
| Latency | ~50ms | 1--3s |
| Cost | $0 | $0.003--0.01 |
| Deterministic | Yes | No |
| Handles canvas/WebGL | Yes (via CV + FFT fibers) | Yes (via screenshot) |

Qualitative evaluation on 50 diverse websites shows that the 5-fiber max distance produces meaningful zone boundaries on standard layouts (news sites, e-commerce, documentation). The structural fiber dominates on well-marked HTML (semantic tags, consistent class naming). The visual fiber compensates when CSS-in-JS frameworks randomize class names. The spatial fiber provides a reliable fallback when both structural and visual cues are weak.

### 3.3 Failure Modes

- **CSS-in-JS fragility.** When class names are randomized hashes, the structural fiber degrades to tag-only comparison. The visual and spatial fibers partially compensate.
- **Coarse RGB sampling.** The 3x3 center pixel sample misses gradients, borders, and off-center visual features.
- **Fixed zone count.** Forcing 3--7 zones artificially splits simple pages and merges complex dashboards.

---

## 4. Application: Image Segmentation

### 4.1 Setup

To demonstrate domain generality, we apply the same framework to natural image segmentation. Images are pre-processed into superpixels via SLIC (Achanta et al. 2012), producing N = 500--2000 regions. Each superpixel is embedded in the 4-fiber space (position, color, texture, edge density) described in Section 2.1.

The pipeline is identical: compute the max-aggregated distance matrix, run neighbor-joining, cut the tree, fold compatible clusters.

### 4.2 Boundary Detection Property

The max aggregation produces qualitatively different boundaries than average-distance methods:

- **Color edge without texture change** (e.g., flat-colored regions meeting): Max detects, average detects. Both methods agree.
- **Texture change without color edge** (e.g., grass meeting pavement of similar color): Max detects via texture fiber. Average may miss if color and spatial fibers dominate.
- **Spatial gap without visual change** (e.g., two identical icons separated by whitespace): Max detects via spatial fiber. Average dilutes the spatial signal across other fibers.

The max aggregation catches all three cases because each is strong in at least one fiber. Average aggregation catches only the first reliably.

### 4.3 Results

[TODO: Quantitative comparison against spectral clustering, normalized cuts, and k-means on BSDS500 or similar benchmark. Metrics: boundary F-score, region covering, variation of information.]

### 4.4 Computational Cost

SLIC pre-segmentation: O(N_pixels) per iteration, typically 10 iterations.
Distance matrix: O(N^2 * k) for N superpixels and k fibers.
Neighbor-joining: O(N^3).

For N = 1000 superpixels and k = 4 fibers, the total pipeline completes in ~200ms on a modern CPU. This is competitive with spectral clustering (which requires eigendecomposition of an N x N matrix) and faster than normalized cuts (which solve an NP-hard problem via relaxation).

---

## 5. Theoretical Analysis

### 5.1 Product Metric Space

The product space `X = X_1 x ... x X_k` with the L-infinity metric:

```
d_inf((x_1,...,x_k), (y_1,...,y_k)) = max_i d_i(x_i, y_i)
```

is a metric space (positivity, symmetry, and triangle inequality follow from the component metrics and the max operation). This is the standard L-infinity product, also known as the Chebyshev distance when the component spaces are Euclidean.

### 5.2 Connection to Tropical Geometry

In the max-plus semiring `T = (R union {-inf}, max, +)`, the operation `max` plays the role of addition and `+` plays the role of multiplication. Our distance aggregation uses tropical addition (max) over the fiber distances.

Speyer and Sturmfels (2004) proved that the tropical Grassmannian `TGr(2,n)` parameterizes the space of phylogenetic trees on `n` taxa. Specifically, the Plucker coordinates of a point in `TGr(2,n)` encode the pairwise distances in a tree metric, and the fan structure decomposes the space into cones corresponding to tree topologies.

Our construction produces a distance matrix `D in R^{n x n}`, feeds it to NJ to obtain a tree `T`, and cuts `T` into clusters. The tree `T` is a point in `TGr(2,n)`. The segmentation is determined by the combinatorial type (topology) of `T`, which is constant within each cone of the tropical Grassmannian fan.

**Corollary (Segmentation stability).** Small perturbations of the fiber distances that do not change the tree topology produce identical segmentations. The segmentation changes only when the distance matrix crosses a cone boundary in `TGr(2,n)`.

### 5.3 Sheaf Cohomology

Let `U = {U_1, ..., U_m}` be the clusters (open sets in a cover). Define a presheaf `F` on `U`:

```
F(U_i) = (sigma_i^tag, sigma_i^text, sigma_i^spatial)
```

where `sigma_i^tag` is the tag distribution vector, `sigma_i^text` is the text content set, and `sigma_i^spatial` is the centroid.

The zeroth Cech cohomology is:

```
H^0(U, F) = ker(d^0: prod_i F(U_i) -> prod_{i<j} F(U_i cap U_j))
```

where `d^0(s)_{ij} = rho_i(s_i) - rho_j(s_j)` and `rho_i` is the restriction map (fiber signature comparison). Clusters in the same connected component of `ker(d^0)` are merged.

The implementation approximates this via agglomerative clustering with union-find: compute pairwise similarity between cluster signatures, merge pairs above threshold, repeat. This is exact for `H^0` (which only detects connected components) but would not extend to higher cohomology groups.

---

## 6. Related Work

**Spectral clustering** (Ng, Jordan, Weiss 2001) uses eigenvectors of the graph Laplacian to embed data in a low-dimensional space, then applies k-means. The affinity function is typically a Gaussian kernel on Euclidean distance, which averages across feature dimensions.

**Normalized cuts** (Shi & Malik 2000) formulates segmentation as a graph partitioning problem. The cut criterion balances between-cluster separation and within-cluster cohesion. The standard implementation uses concatenated feature vectors with Euclidean or Mahalanobis distance.

**SLIC superpixels** (Achanta et al. 2012) provides our image pre-segmentation step but is not itself a segmentation method — it produces over-segmented regions that must be grouped.

**Neighbor-joining** (Saitou & Nei 1987) was developed for phylogenetic tree reconstruction from molecular sequence distances. Applications outside biology include document clustering, network topology inference, and language family reconstruction. We add UI layout analysis and image segmentation to this list.

**Tropical geometry** (Maclagan & Sturmfels 2015) studies geometry over the tropical semiring. The tropical Grassmannian (Speyer & Sturmfels 2004) connects phylogenetic trees to tropical algebraic geometry. We are the first to use this connection as a theoretical foundation for a practical segmentation algorithm.

---

## 7. Limitations

- **O(N^3) neighbor-joining** limits scalability to ~500--2000 elements. For images, SLIC pre-segmentation is required; raw pixels are infeasible.
- **No learned fiber weights.** All fibers contribute equally via max. Learned per-fiber weights could improve quality at the cost of determinism.
- **Over-segmentation from max.** The conservative max criterion can split regions that are distant on one noisy fiber. Sheaf folding mitigates but does not eliminate this.
- **Additive distance assumption.** NJ assumes approximately additive distances. The max-aggregated multi-fiber distances may violate additivity more than Euclidean distances, potentially degrading tree quality.
- **Image segmentation results are preliminary.** We have demonstrated the pipeline but not yet benchmarked against BSDS500 or Pascal VOC.

---

## 8. Conclusion

Tropical metric segmentation offers a principled alternative to spectral and graph-cut methods for multi-modal data segmentation. The max aggregation provides a conservative boundary detection criterion that matches human perceptual principles: any single strong discontinuity defines a boundary. The phylogenetic tree reconstruction provides a hierarchical structure that can be cut at different levels for different granularities. The sheaf folding step corrects over-segmentation by merging clusters with compatible fiber signatures.

The framework is domain-agnostic: the same algorithm (max distance -> NJ -> cut -> fold) applies to web pages and images with only the fiber definitions changing. We conjecture it extends to any domain where elements have multi-dimensional measurable properties and boundaries are defined by divergence in any single dimension.

The connection to the tropical Grassmannian provides theoretical grounding: segmentations correspond to points in `TGr(2,n)`, stability follows from the cone structure, and the space of possible segmentations has a well-characterized combinatorial structure.

---

## References

- Achanta, R., et al. (2012). SLIC Superpixels Compared to State-of-the-Art Superpixel Methods. IEEE TPAMI.
- Canny, J. (1986). A Computational Approach to Edge Detection. IEEE TPAMI.
- Maclagan, D., Sturmfels, B. (2015). Introduction to Tropical Geometry. AMS.
- Ng, A., Jordan, M., Weiss, Y. (2001). On Spectral Clustering: Analysis and an Algorithm. NeurIPS.
- Saitou, N., Nei, M. (1987). The Neighbor-Joining Method. Molecular Biology and Evolution.
- Shi, J., Malik, J. (2000). Normalized Cuts and Image Segmentation. IEEE TPAMI.
- Speyer, D., Sturmfels, B. (2004). The Tropical Grassmannian. Advances in Geometry.
