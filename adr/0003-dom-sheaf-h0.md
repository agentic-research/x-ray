# ADR 3: DOM Sheaf — Zone Folding via Čech H⁰

**Status:** Proposed
**Date:** 2026-03-10
**Author:** James

## Context

Cairn's zone folding (`foldCairnZones` in cairn.go) merges zones by pure spatial proximity —
no fiber comparison at all. The tropical cartographer's `foldCoherentZones` is better: it uses
text Jaccard + tag L1 + spatial distance with weighted combination. But the weights are magic
numbers (0.5/0.2/0.3, 0.2/0.3/0.5, 0.4/0.6) tuned by hand, and the "agreement" threshold
is a single scalar tolerance.

Both approaches approximate the same thing: "are these zones describing the same content?"
That question has a precise algebraic formulation in sheaf theory.

A theoretical review (2026-03-10) confirmed that **"Context = H⁰"** is the strongest claim
in the sheaf-vision framework — the formalization of context as the global section that exists
when local sections from different data sources agree on overlaps has genuine mathematical
content and is close to a theorem. The review recommended formalizing this with explicit
restriction maps and computing H⁰ via the Čech coboundary operator.

## Decision

Replace `foldCairnZones` with sheaf-based zone folding:

1. **Alexandrov topology on the DOM tree**: Each structural node (nav, main, section, etc.)
   defines an "open set" — the subtree rooted at that node.

2. **Feature sheaf**: Stalks are feature vectors (centroids of subtree features). Each zone
   has a stalk computed from its constituent elements.

3. **Restriction maps**: Parent-to-child feature inheritance. The coboundary measures how
   much a child zone's features deviate from its structural ancestor's expected features.

4. **Čech coboundary d⁰**: A sparse matrix encoding the restriction map failures across
   all overlapping zone pairs. For zones that share a structural ancestor or overlap
   spatially, d⁰ measures disagreement.

5. **H⁰ = ker(d⁰)**: Zones in the same kernel class are sheaf-consistent — they should
   be merged. This replaces the ad-hoc weighted similarity + threshold.

## Architecture

```
                    DOM Tree (Alexandrov Topology)
                    ┌──────────────┐
                    │     body     │  ← open set U₀
                    └──────┬───────┘
               ┌──────────┼──────────┐
           ┌───┴───┐  ┌───┴───┐  ┌──┴──┐
           │  nav  │  │ main  │  │ foot │  ← open sets U₁, U₂, U₃
           └───┬───┘  └───┬───┘  └──┬──┘
               │          │         │
          Zone A,B    Zone C,D    Zone E    ← zones from buildDOMSubtreeGroups

    For each zone pair (A,B) under same ancestor:
        d⁰(A,B) = stalk(B) - ρ(ancestor→B) · stalk(A)

    H⁰ = ker(d⁰) → zones A,B are consistent → merge
```

### Pipeline Integration

```
BEFORE:  buildDOMSubtreeGroups → foldCairnZones → buildMounts
AFTER:   buildDOMSubtreeGroups → FoldZonesBySheaf → buildMounts
```

Feature-flagged via `SheafFolding bool` on `CairnCartographer`. Falls back to `mergeClosestZones`.

### Restriction Map Design

Per dimension d of the 24D stalk:

- **Visual dims 0-11**: identity restriction (child should match parent context)
  `d⁰[d] = child_stalk[d] - parent_stalk[d]`

- **Semantic dims 12-23**: weighted by cross-zone variance
  High-variance dimensions get lower weight (allow more variation)

- **Lattice agreement**: bonus weight if zones share the same Leech lattice fingerprint

The coboundary is a sparse matrix of size at most (21 × 24) × (7 × 24) = 504 × 168
for 7 zones. Solvable by Gaussian elimination in microseconds.

## Risks & Mitigations

| Risk | Mitigation |
|------|-----------|
| Identity restriction too strict → collapses everything | Per-dim weights proportional to cross-zone variance |
| Too permissive → merges distinct zones | Fallback to spatial merge if result has fewer zones than minZ |
| Overhead | Matrix is at most 504×168. O(Z³·D³) < 1ms |
| Regression | Feature-flagged; old behavior is one config toggle away |

## Consequences

- Zone folding becomes algebraically grounded instead of heuristic
- The "magic number" weights in zoneFiberSimilarity are replaced by restriction maps derived from the data
- Same interface: `[]zone → []zone`, drop-in replacement for `foldCairnZones`
- Enables future extension: multi-modal sheaf (visual + DOM + audio sections)
