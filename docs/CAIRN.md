# CAIRN Cartographer

## Overview

The **Cairn Cartographer** (`CairnCartographer`) is a highly efficient, deterministic visual tokenization engine used for zone segmentation in X-Ray. It completely replaces the need for a Vision Language Model (VLM) or expensive $O(N^3)$ operations like neighbor-joining trees (used in the older `TropicalCartographer`).

Instead, Cairn uses **error-correcting codes and multi-dimensional lattices** to project visual features from the screen into discrete, mathematically rigorous visual tokens. Elements that share a lattice point and a structural DOM ancestor are grouped into functional zones.

Because it operates locally and scales linearly ($O(N)$), it has no element cap (unlike previous 500-element limits) and provides instantaneous, air-gapped visual mapping.

## The 12D "Optic Nerve" Feature Vector

Cairn begins by overlaying a grid on the screenshot and extracting a biologically motivated **12-dimensional feature vector** for each cell. This vector is designed to mimic retinal and V1 cortex processing in human vision:

### Color Processing (Retina/LGN)
- **`[0] luminance`**: ITU-R BT.601 luminance ($0.299R + 0.587G + 0.114B$).
- **`[1] rgOpponent`**: Red-Green opponent channel ($R - G$, mimicking the L-M cone pathway).
- **`[2] byOpponent`**: Blue-Yellow opponent channel ($B - (R+G)/2$, mimicking the S-(L+M) pathway).
- **`[3] saturation`**: Color purity ($\max(R,G,B) - \min(R,G,B)$).

### Edge & Orientation (V1 Simple Cells)
Extracted using a Gaussian blur and Sobel filters to mimic orientation-selective columns:
- **`[4] edgeDensity`**: Fraction of pixels exhibiting a strong gradient.
- **`[5] horizEnergy`**: Energy in horizontal edges (detects text lines, horizontal dividers).
- **`[6] vertEnergy`**: Energy in vertical edges (detects columns, vertical borders).
- **`[7] diagEnergy`**: Energy in diagonal edges (often indicates photos or complex graphics).
- **`[8] dirVariance`**: Circular variance of edge direction (low variance = text/ui, high variance = photos).

### Spatial Frequency (V1 Complex Cells)
- **`[9] contrast`**: Grayscale dynamic range within the cell.
- **`[10] peakStrength`**: Dominant spectral frequency peak (via FFT).
- **`[11] entropy`**: Spectral complexity (via FFT).

## The Semantic Gearbox (Quantization)

Once the 12D continuous features are extracted and normalized, they are quantized into discrete visual tokens using the "Semantic Gearbox." This allows the system to adjust its visual discrimination resolution (how finely it splits visual zones).

- **Gear 1 (Coarsest):** 4D Tetracode $[4,2,3]$ — Projects into a space with only 9 possible codewords.
- **Gear 3:** 12D Ternary Golay $[12,6,6]$ — Projects into 729 discrete codewords.
- **Gear 5 (Default):** 24D Leech Lattice $\Lambda_{24}$ — A highly dense, continuous lattice projection.
- **Gear 6 (Finest):** 32D Barnes-Wall $BW_{32}$ — The highest resolution mapping.

### Gear 5: The Leech Lattice Pipeline

The default mode (Gear 5) uses the 24-dimensional Leech lattice. The projection pipeline works as follows:

1. **Down-Projection ($12D
ightarrow 8D$):** Selects the 8 most relevant dimensions (luminance, rg, by, sat, edge density, dir variance, contrast, entropy).
2. **$E_8$ Lattice Snap:** Scales the 8D vector and snaps it to the nearest point in the $E_8$ lattice.
3. **Zero-Sum Construction ($8D
ightarrow 24D$):** Expands the $E_8$ point ($x$) into 24 dimensions using the construction $[x, x, -2x]$, guaranteeing a sum of 0 across coordinates.
4. **Turyn Error Correction:** Decodes the resulting 24D point into the nearest valid **Leech lattice** point.

The resulting Leech lattice coordinate serves as the visual fingerprint for that grid cell.

## Zone Segmentation & Clustering

With visual tokens assigned to grid cells, Cairn clusters the raw DOM elements into functional zones:

1. **Visual Assignment:** Elements are assigned the visual token of the grid cell they overlap physically on the screen.
2. **Structural Grouping:** Elements are grouped by a compound key: `(Structural Ancestor, Visual Token)`. A structural ancestor is a semantic container like `<nav>`, `<main>`, `<header>`, or `<footer>`.
3. **Folding & Merging:** If the grouping yields too many zones, Cairn agglomeratively merges the spatially closest zones until it hits the target zone count (e.g., 3 to 7 zones).
4. **Virtual Filesystem:** The finalized zones are mounted as virtual directories (`header/`, `sidebar/`, `main/`) in the Mache VFS based on their layout bounding boxes.

## Advantages over Previous Architectures

- **$O(N)$ Time Complexity:** Runs in a single pass over the elements, easily handling 2000+ DOM nodes in milliseconds.
- **Deterministic:** The same screenshot and DOM will always yield the exact same filesystem schema. No temperature, no LLM hallucinations.
- **Fully Air-Gapped:** Uses pure Go math (Linear Algebra, FFTs, Error Correcting Codes). No API keys, zero cloud latency.
