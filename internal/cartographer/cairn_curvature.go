// cairn_curvature.go — Discrete curvature via SO(2) transport maps (ADR-004).
//
// Detects visual contours (V2-level) by computing holonomy of edge orientation
// transport around triangles in the grid adjacency graph.
//
// The 12×12 grid has 4-connected adjacency (264 edges, 242 triangles).
// For each adjacent pair, the SO(2) transport angle measures how much the
// dominant edge orientation rotates. Non-zero holonomy around a triangle
// indicates curvature — a visual boundary passes through that region.
//
// This is H¹ of the Čech complex with SO(2)-valued cochains.
//
// LIMITATION: With 3 Sobel orientation bins (horiz/vert/diag), the circular
// mean direction ranges from [0, π/2]. Pairwise differences never exceed
// π/2, so angle wrapping never triggers and holonomy always telescopes to
// zero (scalar SO(2) transport is abelian). Transport MAGNITUDES at zone
// boundaries are still useful for boundary detection, but nontrivial
// holonomy (H¹ > 0) requires finer orientation data — a Gabor filter bank
// with ≥6 orientation bins would resolve this.

package cartographer

import (
	"math"
)

// GridEdge represents an adjacency between two grid cells.
type GridEdge struct {
	I, J       int // cell indices (row*gridSize + col)
	Row1, Col1 int
	Row2, Col2 int
}

// GridTriangle represents a triangle in the grid complex.
// Formed from each 2×2 square: two triangles with consistent winding.
type GridTriangle struct {
	I, J, K int // cell indices (counterclockwise winding)
}

// TransportMap holds the SO(2) rotation angle between adjacent cells.
type TransportMap struct {
	Edge     GridEdge
	Angle    float64 // rotation angle in radians
	Strength float64 // confidence: min(edgeDensity_i, edgeDensity_j)
}

// Holonomy holds the curvature measurement at a triangle.
type Holonomy struct {
	Triangle GridTriangle
	Angle    float64 // total rotation around triangle (0 = flat)
	IsCurved bool    // true if |Angle| > curvature threshold
}

// MountBoundaries holds per-side boundary strength for a zone mount.
type MountBoundaries struct {
	Top    float64 `json:"top"`
	Bottom float64 `json:"bottom"`
	Left   float64 `json:"left"`
	Right  float64 `json:"right"`
}

// CurvatureResult holds the full curvature computation output.
type CurvatureResult struct {
	TransportMaps []TransportMap
	Holonomies    []Holonomy
	ContourCells  []int      // cell indices on detected contours
	ContourEdges  []GridEdge // edges lying on zone boundaries
	H1Dim         int        // number of independent closed contours
}

// BuildGridAdjacency constructs 4-connected edges and triangles for the grid.
// Returns edges (horizontal + vertical adjacencies) and triangles (from 2×2 squares).
func BuildGridAdjacency(gridSize int) ([]GridEdge, []GridTriangle) {
	var edges []GridEdge
	var triangles []GridTriangle

	idx := func(r, c int) int { return r*gridSize + c }

	// Horizontal edges: (r,c) — (r,c+1)
	for r := 0; r < gridSize; r++ {
		for c := 0; c < gridSize-1; c++ {
			edges = append(edges, GridEdge{
				I: idx(r, c), J: idx(r, c+1),
				Row1: r, Col1: c,
				Row2: r, Col2: c + 1,
			})
		}
	}

	// Vertical edges: (r,c) — (r+1,c)
	for r := 0; r < gridSize-1; r++ {
		for c := 0; c < gridSize; c++ {
			edges = append(edges, GridEdge{
				I: idx(r, c), J: idx(r+1, c),
				Row1: r, Col1: c,
				Row2: r + 1, Col2: c,
			})
		}
	}

	// Triangles from 2×2 squares: each square gives 2 triangles.
	// Square (r,c)-(r,c+1)-(r+1,c+1)-(r+1,c):
	//   Triangle 1: (r,c) → (r,c+1) → (r+1,c)     (upper-left)
	//   Triangle 2: (r,c+1) → (r+1,c+1) → (r+1,c)  (lower-right)
	for r := 0; r < gridSize-1; r++ {
		for c := 0; c < gridSize-1; c++ {
			tl := idx(r, c)
			tr := idx(r, c+1)
			bl := idx(r+1, c)
			br := idx(r+1, c+1)

			triangles = append(triangles, GridTriangle{I: tl, J: tr, K: bl})
			triangles = append(triangles, GridTriangle{I: tr, J: br, K: bl})
		}
	}

	return edges, triangles
}

// circularMeanDirection computes the dominant edge orientation from
// Sobel energy features (dims 5-7: horizEnergy, vertEnergy, diagEnergy).
//
// Uses circular statistics: sum energy-weighted unit vectors at each
// bin's angle, then take atan2 of the resultant.
//
// Returns angle in [-π/2, π/2] and the resultant length R ∈ [0,1].
func circularMeanDirection(features [CairnNumDims]float64) (angle, R float64) {
	// Bin angles (doubled for orientation, since 0° and 180° are the same edge)
	// horizEnergy (dim 5) → horizontal edges → θ = π/2 (90°)
	// vertEnergy  (dim 6) → vertical edges   → θ = 0
	// diagEnergy  (dim 7) → diagonal edges   → θ = π/4 (45°)
	binAngles := [3]float64{math.Pi / 2, 0, math.Pi / 4}
	energies := [3]float64{features[5], features[6], features[7]}

	var sinSum, cosSum, weightSum float64
	for k := 0; k < 3; k++ {
		// Double the angle for axial (orientation) data
		sinSum += energies[k] * math.Sin(2*binAngles[k])
		cosSum += energies[k] * math.Cos(2*binAngles[k])
		weightSum += energies[k]
	}

	if weightSum < 1e-10 {
		return 0, 0
	}

	meanSin := sinSum / weightSum
	meanCos := cosSum / weightSum
	R = math.Sqrt(meanSin*meanSin + meanCos*meanCos)

	// Halve to get back from doubled angle to actual orientation
	angle = math.Atan2(meanSin, meanCos) / 2

	return angle, R
}

// ComputeTransportMaps computes SO(2) transport maps for all adjacent cell pairs.
//
// The transport angle between cells i and j is the difference in their
// dominant edge orientations, weighted by the minimum edge density
// (low-edge cells contribute less confident transport).
func ComputeTransportMaps(cells []CairnGridCell, edges []GridEdge, gridSize int) []TransportMap {
	// Build cell lookup by index
	cellByIdx := make(map[int]*CairnGridCell, len(cells))
	for i := range cells {
		idx := cells[i].Row*gridSize + cells[i].Col
		cellByIdx[idx] = &cells[i]
	}

	transports := make([]TransportMap, 0, len(edges))

	for _, e := range edges {
		ci, ok1 := cellByIdx[e.I]
		cj, ok2 := cellByIdx[e.J]
		if !ok1 || !ok2 {
			continue
		}

		thetaI, _ := circularMeanDirection(ci.Features)
		thetaJ, _ := circularMeanDirection(cj.Features)

		// Transport angle: how much orientation rotates from i to j
		angle := thetaJ - thetaI
		// Normalize to [-π/2, π/2] (orientation, not direction)
		for angle > math.Pi/2 {
			angle -= math.Pi
		}
		for angle < -math.Pi/2 {
			angle += math.Pi
		}

		// Confidence: minimum edge density of the two cells
		strength := math.Min(ci.Features[4], cj.Features[4])

		transports = append(transports, TransportMap{
			Edge:     e,
			Angle:    angle,
			Strength: strength,
		})
	}

	return transports
}

// ComputeHolonomies computes holonomy around each triangle.
//
// For triangle (i,j,k) with transport maps g_ij, g_jk, g_ki:
//
//	holonomy = g_ij + g_jk + g_ki
//
// Non-zero holonomy indicates curvature (a contour passes through).
func ComputeHolonomies(transports []TransportMap, triangles []GridTriangle, curvatureThreshold float64) []Holonomy {
	// Index transports by edge (i,j) for fast lookup
	type edgeKey struct{ i, j int }
	transportIdx := make(map[edgeKey]int, len(transports))
	for ti, t := range transports {
		transportIdx[edgeKey{t.Edge.I, t.Edge.J}] = ti
	}

	// Helper to get transport angle from i to j (with sign flip for reverse)
	getTransport := func(i, j int) (float64, float64, bool) {
		if ti, ok := transportIdx[edgeKey{i, j}]; ok {
			return transports[ti].Angle, transports[ti].Strength, true
		}
		if ti, ok := transportIdx[edgeKey{j, i}]; ok {
			return -transports[ti].Angle, transports[ti].Strength, true
		}
		return 0, 0, false
	}

	holonomies := make([]Holonomy, 0, len(triangles))

	for _, tri := range triangles {
		gIJ, sIJ, ok1 := getTransport(tri.I, tri.J)
		gJK, sJK, ok2 := getTransport(tri.J, tri.K)
		gKI, sKI, ok3 := getTransport(tri.K, tri.I)

		if !ok1 || !ok2 || !ok3 {
			continue
		}

		// Holonomy = sum of transport angles around the triangle
		angle := gIJ + gJK + gKI

		// Weight by minimum edge strength around the triangle
		minStrength := math.Min(sIJ, math.Min(sJK, sKI))

		// Weighted holonomy: suppress noise in low-edge-density regions
		weightedAngle := angle * minStrength

		holonomies = append(holonomies, Holonomy{
			Triangle: tri,
			Angle:    weightedAngle,
			IsCurved: math.Abs(weightedAngle) > curvatureThreshold,
		})
	}

	return holonomies
}

// DetectContours identifies cells and edges on visual contours.
// A cell is on a contour if any of its adjacent triangles is curved.
func DetectContours(holonomies []Holonomy, gridSize int) (contourCells []int, contourEdges []GridEdge) {
	cellSet := make(map[int]bool)
	edgeSet := make(map[[2]int]bool)

	for _, h := range holonomies {
		if !h.IsCurved {
			continue
		}
		// All three vertices of a curved triangle are on a contour
		cellSet[h.Triangle.I] = true
		cellSet[h.Triangle.J] = true
		cellSet[h.Triangle.K] = true

		// All three edges of the triangle are contour edges
		addEdge := func(a, b int) {
			if a > b {
				a, b = b, a
			}
			edgeSet[[2]int{a, b}] = true
		}
		addEdge(h.Triangle.I, h.Triangle.J)
		addEdge(h.Triangle.J, h.Triangle.K)
		addEdge(h.Triangle.K, h.Triangle.I)
	}

	for idx := range cellSet {
		contourCells = append(contourCells, idx)
	}

	for key := range edgeSet {
		r1, c1 := key[0]/gridSize, key[0]%gridSize
		r2, c2 := key[1]/gridSize, key[1]%gridSize
		contourEdges = append(contourEdges, GridEdge{
			I: key[0], J: key[1],
			Row1: r1, Col1: c1,
			Row2: r2, Col2: c2,
		})
	}

	return contourCells, contourEdges
}

// ComputeCurvature is the top-level function that runs the full H¹ pipeline.
//
// 1. Build grid adjacency (edges + triangles)
// 2. Compute SO(2) transport maps from Sobel orientation features
// 3. Compute holonomy around each triangle
// 4. Detect contour cells and edges
func ComputeCurvature(cells []CairnGridCell, gridSize int) CurvatureResult {
	// Step 1: Build adjacency
	edges, triangles := BuildGridAdjacency(gridSize)

	// Step 2: Compute transport maps
	transports := ComputeTransportMaps(cells, edges, gridSize)

	// Step 3: Compute holonomies with adaptive threshold
	threshold := computeCurvatureThreshold(transports)
	holonomies := ComputeHolonomies(transports, triangles, threshold)

	// Step 4: Detect contours
	contourCells, contourEdges := DetectContours(holonomies, gridSize)

	// Step 5: Count independent contours (H¹ dimension)
	// On the subcomplex of curved triangles, H¹ = connected components of
	// curved regions minus 1 (each closed contour loop = one H¹ class).
	h1Dim := countContourLoops(holonomies, gridSize)

	return CurvatureResult{
		TransportMaps: transports,
		Holonomies:    holonomies,
		ContourCells:  contourCells,
		ContourEdges:  contourEdges,
		H1Dim:         h1Dim,
	}
}

// computeCurvatureThreshold determines the holonomy threshold for detecting contours.
// Uses the 75th percentile of absolute weighted holonomy values.
// Only holonomies above this are considered "curved" (contour-bearing).
func computeCurvatureThreshold(transports []TransportMap) float64 {
	if len(transports) == 0 {
		return 0.1
	}

	// Compute statistics of transport angle magnitudes
	var angles []float64
	for _, t := range transports {
		if t.Strength > 0.01 { // ignore very low-edge cells
			angles = append(angles, math.Abs(t.Angle)*t.Strength)
		}
	}

	if len(angles) == 0 {
		return 0.1
	}

	// Sort and take 75th percentile
	for i := 0; i < len(angles); i++ {
		for j := i + 1; j < len(angles); j++ {
			if angles[j] < angles[i] {
				angles[i], angles[j] = angles[j], angles[i]
			}
		}
	}

	p75Idx := len(angles) * 3 / 4
	if p75Idx >= len(angles) {
		p75Idx = len(angles) - 1
	}

	// Threshold = 75th percentile, with a minimum floor
	threshold := angles[p75Idx]
	if threshold < 0.01 {
		threshold = 0.01
	}

	return threshold
}

// countContourLoops estimates the number of independent closed contour loops.
// Uses connected components of curved triangles — each component that forms
// a closed ring contributes one to H¹.
func countContourLoops(holonomies []Holonomy, gridSize int) int {
	// Collect curved triangle vertices into a graph
	adjacency := make(map[int]map[int]bool)
	addLink := func(a, b int) {
		if adjacency[a] == nil {
			adjacency[a] = make(map[int]bool)
		}
		adjacency[a][b] = true
		if adjacency[b] == nil {
			adjacency[b] = make(map[int]bool)
		}
		adjacency[b][a] = true
	}

	curvedCells := make(map[int]bool)
	numCurvedEdges := 0
	for _, h := range holonomies {
		if !h.IsCurved {
			continue
		}
		addLink(h.Triangle.I, h.Triangle.J)
		addLink(h.Triangle.J, h.Triangle.K)
		addLink(h.Triangle.K, h.Triangle.I)
		curvedCells[h.Triangle.I] = true
		curvedCells[h.Triangle.J] = true
		curvedCells[h.Triangle.K] = true
		numCurvedEdges += 3
	}

	if len(curvedCells) == 0 {
		return 0
	}

	// Count connected components via BFS
	visited := make(map[int]bool)
	numComponents := 0
	for cell := range curvedCells {
		if visited[cell] {
			continue
		}
		numComponents++
		queue := []int{cell}
		visited[cell] = true
		for len(queue) > 0 {
			curr := queue[0]
			queue = queue[1:]
			for neighbor := range adjacency[curr] {
				if !visited[neighbor] {
					visited[neighbor] = true
					queue = append(queue, neighbor)
				}
			}
		}
	}

	// Deduplicate edges (each was counted from both triangles of a square)
	uniqueEdges := make(map[[2]int]bool)
	for _, h := range holonomies {
		if !h.IsCurved {
			continue
		}
		addUniqueEdge := func(a, b int) {
			if a > b {
				a, b = b, a
			}
			uniqueEdges[[2]int{a, b}] = true
		}
		addUniqueEdge(h.Triangle.I, h.Triangle.J)
		addUniqueEdge(h.Triangle.J, h.Triangle.K)
		addUniqueEdge(h.Triangle.K, h.Triangle.I)
	}

	// Euler characteristic: V - E + F = χ
	// For a planar graph: H¹ = E - V + components (= 1 - χ + components)
	V := len(curvedCells)
	E := len(uniqueEdges)

	// H¹ = E - V + components (first Betti number for a graph)
	h1 := E - V + numComponents
	if h1 < 0 {
		h1 = 0
	}

	return h1
}

// AnnotateZoneBoundaries maps detected contours to per-zone boundary strengths.
// For each zone, checks how many contour cells lie on each edge of the zone's
// bounding box. Returns a map from zone index to boundary strengths.
func AnnotateZoneBoundaries(zones []zone, curvature CurvatureResult, elements []element, gridSize int) map[int]*MountBoundaries {
	if len(curvature.ContourCells) == 0 {
		return nil
	}

	// Build contour cell set for fast lookup
	contourSet := make(map[int]bool, len(curvature.ContourCells))
	for _, idx := range curvature.ContourCells {
		contourSet[idx] = true
	}

	// For each zone, compute its bounding box in grid coordinates and
	// count contour cells on each side
	result := make(map[int]*MountBoundaries)

	for zi, z := range zones {
		if len(z.elems) == 0 {
			continue
		}

		// Find zone's extent in grid coordinates
		minRow, maxRow := gridSize, 0
		minCol, maxCol := gridSize, 0

		for _, ei := range z.elems {
			el := elements[ei]
			if !el.hasBounds {
				continue
			}
			col := clampInt(int(el.centerX*float64(gridSize)), 0, gridSize-1)
			row := clampInt(int(el.centerY*float64(gridSize)), 0, gridSize-1)
			if row < minRow {
				minRow = row
			}
			if row > maxRow {
				maxRow = row
			}
			if col < minCol {
				minCol = col
			}
			if col > maxCol {
				maxCol = col
			}
		}

		if minRow > maxRow || minCol > maxCol {
			continue
		}

		var topCount, bottomCount, leftCount, rightCount int
		var topTotal, bottomTotal, leftTotal, rightTotal int

		// Check cells along each boundary edge
		for c := minCol; c <= maxCol; c++ {
			// Top row
			topTotal++
			if contourSet[minRow*gridSize+c] {
				topCount++
			}
			// Bottom row
			bottomTotal++
			if contourSet[maxRow*gridSize+c] {
				bottomCount++
			}
		}
		for r := minRow; r <= maxRow; r++ {
			// Left column
			leftTotal++
			if contourSet[r*gridSize+minCol] {
				leftCount++
			}
			// Right column
			rightTotal++
			if contourSet[r*gridSize+maxCol] {
				rightCount++
			}
		}

		boundaries := &MountBoundaries{}
		if topTotal > 0 {
			boundaries.Top = float64(topCount) / float64(topTotal)
		}
		if bottomTotal > 0 {
			boundaries.Bottom = float64(bottomCount) / float64(bottomTotal)
		}
		if leftTotal > 0 {
			boundaries.Left = float64(leftCount) / float64(leftTotal)
		}
		if rightTotal > 0 {
			boundaries.Right = float64(rightCount) / float64(rightTotal)
		}

		// Only include if there's a meaningful boundary
		if boundaries.Top > 0 || boundaries.Bottom > 0 || boundaries.Left > 0 || boundaries.Right > 0 {
			result[zi] = boundaries
		}
	}

	return result
}
