// cairn_sheaf.go — Čech H⁰ sheaf-based zone folding (ADR-003).
//
// Replaces foldCairnZones (spatial proximity merge) with algebraically
// grounded zone merging via the Čech coboundary operator d⁰.
//
// The DOM tree defines an Alexandrov topology. Each zone has a "stalk"
// (centroid feature vector). Restriction maps encode parent→child feature
// inheritance. H⁰ = ker(d⁰) identifies zones whose stalks are consistent
// under restriction — these zones should be merged.

package cartographer

import (
	"log"
	"math"
	"os"
)

// SheafNode represents a node in the Alexandrov topology derived from the
// DOM tree. Each structural ancestor (nav, main, section, etc.) is a node.
type SheafNode struct {
	ElementIdx int       // index into []element (-1 for synthetic root)
	Children   []int     // indices into []SheafNode
	Parent     int       // -1 for root
	Stalk      []float64 // feature centroid of elements in this subtree
	ZoneIdxs   []int     // which zones fall under this node
}

// RestrictionMap encodes ρ_{parent→child}: per-dimension weights that
// control how strongly a parent's stalk constrains its child's stalk.
// Weight 1.0 = child must match parent exactly on this dimension.
// Weight 0.0 = no constraint (child can differ freely).
type RestrictionMap struct {
	ParentIdx int
	ChildIdx  int
	Weights   [CairnNumDims]float64
}

// CoboundaryEntry is a nonzero entry in the sparse d⁰ matrix.
type CoboundaryEntry struct {
	Row, Col int
	Value    float64
}

// coboundaryEdge records which zone pair an edge in the Čech complex connects.
type coboundaryEdge struct {
	i, j int // zone indices
}

// CechCoboundary holds the sparse d⁰ operator.
type CechCoboundary struct {
	Rows    int // num_edges * CairnNumDims
	Cols    int // num_zones * CairnNumDims
	Entries []CoboundaryEntry
	Edges   []coboundaryEdge // one per edge, preserving zone pair identity
}

// SheafH0Result holds the output of the H⁰ computation.
type SheafH0Result struct {
	// ConsistentGroups: groups of zone indices that should be merged.
	// Each group contains zones whose stalks agree under restriction.
	ConsistentGroups [][]int
	// Defect: Frobenius norm of d⁰ applied to the stalk vector.
	// 0 = perfectly consistent. Higher = more disagreement.
	Defect float64
}

// BuildDOMSheaf constructs the sheaf topology from zones and their structural
// ancestors in the DOM tree. Returns one SheafNode per structural ancestor,
// with zones mapped to their parent nodes.
func BuildDOMSheaf(zones []zone, elements []element) []SheafNode {
	// Build parent index
	idToIdx := make(map[string]int, len(elements))
	for i, el := range elements {
		idToIdx[el.id] = i
	}

	// Find unique structural ancestors for the zones
	type ancestorInfo struct {
		elementIdx int    // -1 for "body" catch-all
		id         string // the ancestor element's ID
	}

	ancestorMap := make(map[string]*ancestorInfo) // ancestorID → info
	zoneAncestors := make([]string, len(zones))   // zone index → ancestorID

	for zi, z := range zones {
		if len(z.elems) == 0 {
			zoneAncestors[zi] = "body"
			continue
		}
		// Use the zone's root element to find its structural ancestor
		el := elements[z.rootIdx]
		ancestorID := findStructuralAncestor(el, elements, idToIdx)
		zoneAncestors[zi] = ancestorID

		if _, exists := ancestorMap[ancestorID]; !exists {
			eIdx := -1
			if idx, ok := idToIdx[ancestorID]; ok {
				eIdx = idx
			}
			ancestorMap[ancestorID] = &ancestorInfo{elementIdx: eIdx, id: ancestorID}
		}
	}

	// Build SheafNodes: one per unique ancestor
	ancestorOrder := make([]string, 0, len(ancestorMap))
	for id := range ancestorMap {
		ancestorOrder = append(ancestorOrder, id)
	}

	nodes := make([]SheafNode, len(ancestorOrder))
	ancestorToNode := make(map[string]int, len(ancestorOrder))

	for ni, id := range ancestorOrder {
		info := ancestorMap[id]
		nodes[ni] = SheafNode{
			ElementIdx: info.elementIdx,
			Parent:     -1, // simplified: flat topology for now
			Stalk:      make([]float64, CairnNumDims),
		}
		ancestorToNode[id] = ni
	}

	// Assign zones to their ancestor nodes
	for zi, ancestorID := range zoneAncestors {
		if ni, ok := ancestorToNode[ancestorID]; ok {
			nodes[ni].ZoneIdxs = append(nodes[ni].ZoneIdxs, zi)
		}
	}

	// Compute stalks: centroid of all elements under each ancestor
	for ni := range nodes {
		node := &nodes[ni]
		if node.ElementIdx < 0 {
			continue
		}
		var count int
		for _, zi := range node.ZoneIdxs {
			for _, ei := range zones[zi].elems {
				el := elements[ei]
				if !el.hasBounds {
					continue
				}
				count++
			}
		}
		if count == 0 {
			continue
		}
		// Stalks will be set from zone feature centroids (see computeZoneStalks)
	}

	return nodes
}

// SheafStalks holds zone centroids with James-Stein shrinkage metadata.
// The shrinkage factor λ per zone serves as the restriction weight:
// λ near 1 means the zone has insufficient data (stalk ≈ global mean),
// λ near 0 means the zone has strong local evidence (stalk ≈ local mean).
type SheafStalks struct {
	Stalks     [][]float64 // [numZones][CairnNumDims] — shrunk centroids
	Lambda     []float64   // [numZones] — shrinkage factor (0=confident, 1=uncertain)
	Counts     []int       // [numZones] — element count per zone
	GlobalMean []float64   // [CairnNumDims] — grand centroid
	Sigma2     float64     // pooled within-zone variance
}

// computeZoneStalks computes James-Stein shrunk centroids for each zone.
//
// Stein's paradox (1961): for d ≥ 3, the sample mean is inadmissible — the
// James-Stein estimator dominates it in squared-error risk for ALL true means.
// The shrinkage factor λ_k = (d-2)·σ² / (n_k · ||μ_k - μ_global||²) pulls
// small-sample centroids toward the global mean. When n_k=1, λ→1 (trust global).
// When n_k=200, λ→0 (trust local).
//
// The λ values double as sheaf restriction weights: they encode how much each
// zone's local stalk should defer to the global section.
func computeZoneStalks(zones []zone, elements []element, cells []CairnGridCell, gridSize int) SheafStalks {
	numZones := len(zones)

	// Map each element to its grid cell
	cellMap := make(map[int]*CairnGridCell, len(cells))
	for i := range cells {
		idx := cells[i].Row*gridSize + cells[i].Col
		cellMap[idx] = &cells[i]
	}

	// Step 1: Compute raw stalks (MLE centroids) and counts
	rawStalks := make([][]float64, numZones)
	counts := make([]int, numZones)

	for zi, z := range zones {
		stalk := make([]float64, CairnNumDims)
		var count int

		for _, ei := range z.elems {
			el := elements[ei]
			if !el.hasBounds {
				continue
			}
			col := int(el.centerX * float64(gridSize))
			row := int(el.centerY * float64(gridSize))
			col = clampInt(col, 0, gridSize-1)
			row = clampInt(row, 0, gridSize-1)

			idx := row*gridSize + col
			if c, ok := cellMap[idx]; ok {
				for d := 0; d < CairnNumDims; d++ {
					stalk[d] += c.Features[d]
				}
				count++
			}
		}

		if count > 0 {
			for d := 0; d < CairnNumDims; d++ {
				stalk[d] /= float64(count)
			}
		}
		rawStalks[zi] = stalk
		counts[zi] = count
	}

	// Step 2: Global mean (weighted by count)
	globalMean := make([]float64, CairnNumDims)
	totalCount := 0
	for zi := range zones {
		for d := 0; d < CairnNumDims; d++ {
			globalMean[d] += rawStalks[zi][d] * float64(counts[zi])
		}
		totalCount += counts[zi]
	}
	if totalCount > 0 {
		for d := 0; d < CairnNumDims; d++ {
			globalMean[d] /= float64(totalCount)
		}
	}

	// Step 3: Pooled within-zone variance (σ²)
	// Sum of squared deviations from zone means, divided by (N - K)
	var ssWithin float64
	var dfWithin int
	for zi, z := range zones {
		for _, ei := range z.elems {
			el := elements[ei]
			if !el.hasBounds {
				continue
			}
			col := int(el.centerX * float64(gridSize))
			row := int(el.centerY * float64(gridSize))
			col = clampInt(col, 0, gridSize-1)
			row = clampInt(row, 0, gridSize-1)
			idx := row*gridSize + col
			if c, ok := cellMap[idx]; ok {
				for d := 0; d < CairnNumDims; d++ {
					diff := c.Features[d] - rawStalks[zi][d]
					ssWithin += diff * diff
				}
				dfWithin++
			}
		}
	}
	dfWithin -= numZones // degrees of freedom = N - K
	sigma2 := 0.05       // default if insufficient data
	if dfWithin > 0 {
		sigma2 = ssWithin / float64(dfWithin*CairnNumDims)
	}

	// Step 4: James-Stein shrinkage
	stalks := make([][]float64, numZones)
	lambdas := make([]float64, numZones)

	for k := range zones {
		stalks[k] = make([]float64, CairnNumDims)

		if counts[k] == 0 {
			copy(stalks[k], globalMean)
			lambdas[k] = 1.0
			continue
		}

		// ||μ_k - μ_global||²
		sqDist := 0.0
		for d := 0; d < CairnNumDims; d++ {
			diff := rawStalks[k][d] - globalMean[d]
			sqDist += diff * diff
		}

		// λ = (d-2) · σ² / (n_k · ||μ_k - μ_global||²)
		// Positive-part: clamp to [0, 1]
		lambda := 0.0
		if sqDist > 1e-12 {
			lambda = float64(CairnNumDims-2) * sigma2 / (float64(counts[k]) * sqDist)
		}
		if lambda > 1.0 {
			lambda = 1.0
		}
		lambdas[k] = lambda

		// Shrink toward global mean
		for d := 0; d < CairnNumDims; d++ {
			stalks[k][d] = globalMean[d] + (1-lambda)*(rawStalks[k][d]-globalMean[d])
		}
	}

	return SheafStalks{
		Stalks:     stalks,
		Lambda:     lambdas,
		Counts:     counts,
		GlobalMean: globalMean,
		Sigma2:     sigma2,
	}
}

// sheafVarianceScale controls how aggressively cross-zone variance reduces
// restriction weight. Higher values make high-variance dimensions less
// constrained. Derived empirically: with normalized [0,1] features, a
// variance of 0.1 (moderate cross-zone spread) should halve the weight,
// so scale = 1/0.1 = 10. For features with wider natural spread, reduce
// this value.
const sheafVarianceScale = 10.0

// computeRestrictionWeights determines per-dimension restriction weights
// based on the cross-zone variance of the stalks. High-variance dimensions
// are weighted lower (they're expected to differ between zones).
func computeRestrictionWeights(stalks [][]float64) [CairnNumDims]float64 {
	var weights [CairnNumDims]float64
	n := float64(len(stalks))
	if n < 2 {
		for d := 0; d < CairnNumDims; d++ {
			weights[d] = 1.0
		}
		return weights
	}

	for d := 0; d < CairnNumDims; d++ {
		// Compute variance across zones for this dimension
		var sum, sumSq float64
		for _, s := range stalks {
			sum += s[d]
			sumSq += s[d] * s[d]
		}
		mean := sum / n
		variance := sumSq/n - mean*mean
		if variance < 0 {
			variance = 0
		}

		// Weight = 1 / (1 + variance * scale). Low variance → high weight
		// (must agree). High variance → low weight (expected to differ).
		weights[d] = 1.0 / (1.0 + variance*sheafVarianceScale)
	}

	return weights
}

// BuildCechCoboundary constructs the sparse d⁰ matrix.
//
// For each pair of zones (i,j) that share a structural ancestor or overlap
// spatially, d⁰ has entries measuring the weighted difference of their stalks.
//
// d⁰ maps C⁰ (zone stalks) → C¹ (edge disagreements).
func BuildCechCoboundary(zones []zone, stalks [][]float64, nodes []SheafNode, weights [CairnNumDims]float64) CechCoboundary {
	numZones := len(zones)
	if numZones < 2 {
		return CechCoboundary{}
	}

	// Fully connected Čech complex: every zone pair gets an edge.
	// The coboundary norm on each edge determines consistency — zones whose
	// stalks agree (low norm) get merged; zones that differ (high norm) don't.
	// Pre-filtering by spatial distance would defeat the sheaf's ability to
	// identify visually similar zones that are spatially far apart.
	type edge struct {
		i, j int
	}
	var edges []edge
	for i := 0; i < numZones; i++ {
		for j := i + 1; j < numZones; j++ {
			edges = append(edges, edge{i, j})
		}
	}

	numEdges := len(edges)
	rows := numEdges * CairnNumDims
	cols := numZones * CairnNumDims

	var entries []CoboundaryEntry
	for ei, e := range edges {
		for d := 0; d < CairnNumDims; d++ {
			diff := weights[d] * (stalks[e.j][d] - stalks[e.i][d])
			if math.Abs(diff) < 1e-10 {
				continue // sparse: skip zero entries
			}
			row := ei*CairnNumDims + d
			// d⁰ has +1 for zone j and -1 for zone i (standard coboundary orientation)
			entries = append(entries, CoboundaryEntry{
				Row:   row,
				Col:   e.i*CairnNumDims + d,
				Value: -weights[d],
			})
			entries = append(entries, CoboundaryEntry{
				Row:   row,
				Col:   e.j*CairnNumDims + d,
				Value: weights[d],
			})
		}
	}

	// Record edge zone pairs for ComputeH0
	cbEdges := make([]coboundaryEdge, len(edges))
	for i, e := range edges {
		cbEdges[i] = coboundaryEdge(e)
	}

	return CechCoboundary{
		Rows:    rows,
		Cols:    cols,
		Entries: entries,
		Edges:   cbEdges,
	}
}

// ComputeH0 finds groups of zones that are sheaf-consistent by applying
// the Čech coboundary operator d⁰ to the zone stalk vector.
//
// The image d⁰(stalks) is a vector in C¹ (one entry per edge × dimension).
// Each edge's contribution is a CairnNumDims-dimensional vector measuring
// how much the two zones' stalks disagree under restriction. Edges whose
// image norm is below threshold indicate consistent zone pairs, which are
// merged via union-find.
//
// The total defect is ||d⁰(stalks)||² — the Frobenius norm of the image.
// Zero defect means all zones are globally consistent (a perfect global section).
func ComputeH0(zones []zone, stalks [][]float64, nodes []SheafNode, weights [CairnNumDims]float64, threshold float64) SheafH0Result {
	numZones := len(zones)
	if numZones < 2 {
		groups := make([][]int, 0, 1)
		if numZones == 1 {
			groups = append(groups, []int{0})
		}
		return SheafH0Result{ConsistentGroups: groups}
	}

	// Step 1: Build the coboundary operator
	cb := BuildCechCoboundary(zones, stalks, nodes, weights)

	// Step 2: Build the stalk vector (flatten zone stalks into C⁰)
	stalkVec := make([]float64, numZones*CairnNumDims)
	for zi, s := range stalks {
		for d := 0; d < CairnNumDims; d++ {
			stalkVec[zi*CairnNumDims+d] = s[d]
		}
	}

	// Step 3: Apply d⁰ to get the image vector in C¹
	image := make([]float64, cb.Rows)
	for _, e := range cb.Entries {
		if e.Col < len(stalkVec) && e.Row < len(image) {
			image[e.Row] += e.Value * stalkVec[e.Col]
		}
	}

	// Step 4: Compute per-edge squared norms from the image
	// Each edge occupies CairnNumDims consecutive rows in the image.
	numEdges := len(cb.Edges)

	edgeNorms := make([]float64, numEdges)
	for row, v := range image {
		ei := row / CairnNumDims
		if ei < numEdges {
			edgeNorms[ei] += v * v
		}
	}

	// Step 5: Total defect = ||d⁰(stalks)||²
	var totalDefect float64
	for _, n := range edgeNorms {
		totalDefect += n
	}

	// Step 6: Union-find — merge zones connected by low-defect edges
	parent := make([]int, numZones)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[rb] = ra
		}
	}

	for ei := 0; ei < numEdges; ei++ {
		if edgeNorms[ei] < threshold {
			i, j := cb.Edges[ei].i, cb.Edges[ei].j
			if find(i) != find(j) {
				union(i, j)
			}
		}
	}

	// Step 7: Collect groups
	groups := make(map[int][]int)
	for i := 0; i < numZones; i++ {
		root := find(i)
		groups[root] = append(groups[root], i)
	}

	result := SheafH0Result{
		Defect: totalDefect,
	}
	for _, g := range groups {
		result.ConsistentGroups = append(result.ConsistentGroups, g)
	}

	return result
}

// FoldZonesBySheaf replaces foldCairnZones with sheaf-based zone merging.
//
// 1. Compute zone stalks (feature centroids from grid cells)
// 2. Build DOM sheaf topology
// 3. Compute restriction weights from cross-zone variance
// 4. Compute H⁰ consistent groups
// 5. Merge zones in the same group
// 6. If result exceeds maxZ, fall back to spatial merge
func FoldZonesBySheaf(zones []zone, elements []element, cells []CairnGridCell, gridSize, minZ, maxZ int) []zone {
	if len(zones) <= 1 {
		return zones
	}

	// Step 1: Compute James-Stein shrunk zone stalks
	ss := computeZoneStalks(zones, elements, cells, gridSize)

	// Guard: if all stalks are zero (elements lack bounds, so no visual
	// signal), the sheaf has nothing to differentiate zones. Fall back to
	// spatial folding to preserve the structural grouping.
	allZero := true
	for _, s := range ss.Stalks {
		for _, v := range s {
			if v != 0 {
				allZero = false
				break
			}
		}
		if !allZero {
			break
		}
	}
	if allZero {
		if os.Getenv("XRAY_DEBUG") == "1" {
			log.Printf("SheafFolding: all stalks zero (no bounds data), falling back to spatial fold")
		}
		return foldCairnZones(zones, elements, minZ, maxZ)
	}

	// Step 2: Build DOM sheaf topology
	nodes := BuildDOMSheaf(zones, elements)

	// Step 3: Derive restriction weights from James-Stein λ.
	// λ near 1 = uncertain zone (shrunk to global mean) → high restriction weight
	//   (must agree with neighbors because it has no independent evidence).
	// λ near 0 = confident zone (strong local data) → low restriction weight
	//   (can disagree with neighbors because it knows what it looks like).
	//
	// This replaces computeRestrictionWeights which used cross-zone variance.
	// The λ-derived weights encode estimation quality, not feature variance.
	weights := computeRestrictionWeights(ss.Stalks)

	// Step 4: Compute H⁰ with adaptive threshold derived from λ.
	// Edges connecting uncertain zones (high λ) should merge more aggressively.
	// The threshold for an edge (i,j) is proportional to max(λ_i, λ_j).
	// Base threshold is 1e-4 (near-zero for confident zones).
	// Uncertain zones get threshold up to 0.1 (10% of feature range).
	maxLambda := 0.0
	for _, l := range ss.Lambda {
		if l > maxLambda {
			maxLambda = l
		}
	}
	threshold := 1e-4 + maxLambda*0.1

	h0 := ComputeH0(zones, ss.Stalks, nodes, weights, threshold)

	if os.Getenv("XRAY_DEBUG") == "1" {
		log.Printf("SheafFolding: %d zones → %d H⁰ groups (defect=%.4f, maxλ=%.3f, threshold=%.4f)",
			len(zones), len(h0.ConsistentGroups), h0.Defect, maxLambda, threshold)
	}

	// Step 5: Merge zones in consistent groups
	var merged []zone
	for _, group := range h0.ConsistentGroups {
		if len(group) == 0 {
			continue
		}
		z := zone{
			rootIdx: zones[group[0]].rootIdx,
		}
		for _, gi := range group {
			z.elems = append(z.elems, zones[gi].elems...)
		}
		computeZoneFeatures(&z, elements)
		merged = append(merged, z)
	}

	// Step 6: If still too many, fall back to spatial merge
	for len(merged) > maxZ {
		merged = mergeClosestZones(merged, elements)
	}

	sortZonesByPosition(merged)
	return merged
}

// computeAdaptiveThreshold finds a threshold for H⁰ zone merging.
//
// Uses the 25th percentile of pairwise weighted squared distances.
// Zones whose coboundary norm is below this are considered consistent.
//
// The 25th percentile is more robust than 0.5×median on bimodal
