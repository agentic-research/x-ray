# ADR 4: Discrete Curvature — Contour Detection via Čech H¹

**Status:** Proposed
**Date:** 2026-03-10
**Author:** James

## Context

Cairn groups grid cells by shared Leech lattice point — a clustering operation that identifies
cells that "look the same." What's missing is boundary detection: identifying where adjacent
cells look *different* and the pattern of difference traces a visual contour.

This is the V2 operation in the visual cortex hierarchy. V1 detects local edges (what cairn's
Sobel features already compute). V2 integrates those local edges into coherent contours by
checking whether adjacent edge orientations are consistent along a curve.

A theoretical review (2026-03-10) identified a concrete, implementable path: compute the H¹
of a Čech complex with SO(2)-valued transport maps over the grid adjacency graph. The holonomy
(total rotation) around each triangle detects curvature — a visual boundary passing through
that region.

## Decision

Add contour detection to the cairn pipeline:

1. **4-connected grid adjacency**: Each cell (r,c) connects to its 4 neighbors. On a 12×12
   grid: 264 edges, 242 triangles (from 11×11 squares, 2 triangles each).

2. **SO(2) transport maps**: For each adjacent pair (i,j), compute the rotation angle that
   best aligns their edge orientation distributions (from existing Sobel dims 5-7).

3. **Holonomy**: For each triangle (i,j,k), sum the transport angles: `g_ki + g_jk + g_ij`.
   Non-zero holonomy = curvature = contour passes through.

4. **Contour detection**: Threshold holonomy magnitudes. Cells adjacent to high-holonomy
   triangles are on visual contours.

5. **Zone boundary annotation**: Map detected contours to zone boundaries. Each zone gets
   per-side boundary strength [0,1].

## Architecture

```
    Grid Cells (12×12)              Transport Maps              Holonomy
    ┌──┬──┬──┬──┐                   ┌──┬──┬──┬──┐
    │  │  │  │  │   For each edge   │→ │→ │→ │  │   For each triangle
    ├──┼──┼──┼──┤   compute SO(2)   ├──┼──┼──┼──┤   sum angles around
    │  │  │  │  │   rotation angle  │↓ │↓ │↓ │  │   △: g_ki+g_jk+g_ij
    ├──┼──┼──┼──┤   from Sobel      ├──┼──┼──┼──┤
    │  │  │  │  │   orientation     │  │  │  │  │   Non-zero = curvature
    └──┴──┴──┴──┘   distributions   └──┴──┴──┴──┘   = contour detected

    Sobel dims 5-7 → circular mean direction θ per cell
    Transport angle: g_ij = θ_j - θ_i
    Weight by min(edgeDensity_i, edgeDensity_j)
```

### SO(2) Transport from Existing Features

The circular mean direction is derived from existing Sobel energy features (dims 5-7)
without adding new feature dimensions (which would break the 24D Leech lattice match):

```
bin_angles = [π/2, 0, π/4]  // horiz=90°, vert=0°, diag=45°
θ = atan2(Σ energy[k]·sin(2·angle[k]),
          Σ energy[k]·cos(2·angle[k])) / 2
```

### Pipeline Integration

```
BEFORE:  ExtractFusedFeatures → projectCells → buildDOMSubtreeGroups → fold → buildMounts
AFTER:   ExtractFusedFeatures → ComputeCurvature → projectCells → ... → AnnotateZoneBoundaries → buildMounts
```

Feature-flagged via `CurvatureDetection bool` on `CairnCartographer`.

### Output Format

Zone boundary annotations are added to the mount JSON with `omitempty`:

```json
{
  "virtual_path": "/page/main_content",
  "boundaries": {
    "top": 0.85,
    "bottom": 0.12,
    "left": 0.0,
    "right": 0.72
  }
}
```

High boundary strength = strong visual contour at that edge of the zone.

## Risks & Mitigations

| Risk | Mitigation |
|------|-----------|
| SO(2) from 3 bins is noisy (~45° quantization) | Weight by edge density; suppress low-edge cells |
| H¹ on simply-connected grid is always 0 | Compute on subcomplex of curved triangles only |
| Breaking 24D Leech dimension | Do NOT add features; derive mean direction from dims 5-7 |
| Overhead | O(G²) = O(144). All operations O(1) per element. Sub-millisecond. |

## Consequences

- Cairn gains V2-level contour detection without a VLM
- Zone boundaries become explicit (currently implicit from spatial proximity)
- Enables future: contour-guided zone splitting (split zones that contain strong internal contours)
- Boundary strength is a useful signal for the Navigator: "this zone is visually separated from its neighbors"
