package cartographer

import (
	"math"
	"testing"
)

func TestBuildGridAdjacency_12x12(t *testing.T) {
	edges, triangles := BuildGridAdjacency(12)

	// Horizontal edges: 12 rows × 11 per row = 132
	// Vertical edges: 11 rows × 12 per row = 132
	// Total: 264
	wantEdges := 264
	if len(edges) != wantEdges {
		t.Errorf("edges: got %d, want %d", len(edges), wantEdges)
	}

	// Triangles: 11×11 squares × 2 per square = 242
	wantTriangles := 242
	if len(triangles) != wantTriangles {
		t.Errorf("triangles: got %d, want %d", len(triangles), wantTriangles)
	}
}

func TestBuildGridAdjacency_4x4(t *testing.T) {
	edges, triangles := BuildGridAdjacency(4)

	// Horizontal: 4 rows × 3 = 12
	// Vertical: 3 rows × 4 = 12
	wantEdges := 24
	if len(edges) != wantEdges {
		t.Errorf("edges: got %d, want %d", len(edges), wantEdges)
	}

	// 3×3 squares × 2 = 18
	wantTriangles := 18
	if len(triangles) != wantTriangles {
		t.Errorf("triangles: got %d, want %d", len(triangles), wantTriangles)
	}
}

func TestCircularMeanDirection_Vertical(t *testing.T) {
	// All energy in vertical edges (dim 6) → θ should be ~0
	var features [CairnNumDims]float64
	features[5] = 0.0 // horiz
	features[6] = 1.0 // vert
	features[7] = 0.0 // diag

	angle, R := circularMeanDirection(features)

	if math.Abs(angle) > 0.01 {
		t.Errorf("vertical-only: angle=%.4f, want ~0", angle)
	}
	if R < 0.9 {
		t.Errorf("vertical-only: R=%.4f, want ~1.0", R)
	}
}

func TestCircularMeanDirection_Horizontal(t *testing.T) {
	// All energy in horizontal edges (dim 5) → θ should be ~π/2
	var features [CairnNumDims]float64
	features[5] = 1.0 // horiz
	features[6] = 0.0 // vert
	features[7] = 0.0 // diag

	angle, R := circularMeanDirection(features)

	// π/2 or -π/2 (both valid for horizontal)
	if math.Abs(math.Abs(angle)-math.Pi/2) > 0.01 {
		t.Errorf("horizontal-only: |angle|=%.4f, want ~π/2=%.4f", math.Abs(angle), math.Pi/2)
	}
	if R < 0.9 {
		t.Errorf("horizontal-only: R=%.4f, want ~1.0", R)
	}
}

func TestCircularMeanDirection_Uniform(t *testing.T) {
	// Equal energy in all bins → R should be low (no dominant direction)
	var features [CairnNumDims]float64
	features[5] = 0.333 // horiz
	features[6] = 0.333 // vert
	features[7] = 0.334 // diag

	_, R := circularMeanDirection(features)

	if R > 0.5 {
		t.Errorf("uniform energy: R=%.4f, want <0.5 (no dominant direction)", R)
	}
}

func TestComputeHolonomies_UniformField(t *testing.T) {
	// Uniform orientation field → all holonomies should be ~0
	gridSize := 4
	edges, triangles := BuildGridAdjacency(gridSize)

	cells := make([]CairnGridCell, gridSize*gridSize)
	for r := 0; r < gridSize; r++ {
		for c := 0; c < gridSize; c++ {
			idx := r*gridSize + c
			cells[idx] = CairnGridCell{
				Row: r, Col: c,
				Features: [CairnNumDims]float64{
					0, 0, 0, 0,
					0.5,  // edgeDensity (dim 4)
					0.0,  // horizEnergy (dim 5)
					1.0,  // vertEnergy (dim 6) — all vertical
					0.0,  // diagEnergy (dim 7)
				},
			}
		}
	}

	transports := ComputeTransportMaps(cells, edges, gridSize)
	holonomies := ComputeHolonomies(transports, triangles, 0.01)

	for _, h := range holonomies {
		if h.IsCurved {
			t.Errorf("uniform field should have no curvature, got curved triangle at (%d,%d,%d) angle=%.4f",
				h.Triangle.I, h.Triangle.J, h.Triangle.K, h.Angle)
			break
		}
	}
}

func TestComputeTransportMaps_BoundaryDetection(t *testing.T) {
	// With 3 Sobel bins, scalar SO(2) holonomy always telescopes to zero
	// because the mean direction range [0, π/2] never triggers wrapping.
	// Nontrivial holonomy requires finer orientation bins (Gabor filter bank).
	//
	// However, the TRANSPORT MAGNITUDES at zone boundaries ARE nonzero and
	// useful. Verify that transport angles are large at the orientation boundary.
	gridSize := 4
	edges, _ := BuildGridAdjacency(gridSize)

	cells := make([]CairnGridCell, gridSize*gridSize)
	for r := 0; r < gridSize; r++ {
		for c := 0; c < gridSize; c++ {
			idx := r*gridSize + c
			var features [CairnNumDims]float64
			features[4] = 0.8 // strong edges everywhere
			if r < 2 {
				features[6] = 1.0 // top: vertical
			} else {
				features[5] = 1.0 // bottom: horizontal
			}
			cells[idx] = CairnGridCell{Row: r, Col: c, Features: features}
		}
	}

	transports := ComputeTransportMaps(cells, edges, gridSize)

	// Transport angles between same-orientation cells should be ~0
	// Transport angles across the boundary (row 1→2) should be ~π/2
	var maxSameAngle, minBoundaryAngle float64
	minBoundaryAngle = math.Inf(1)

	for _, tr := range transports {
		absAngle := math.Abs(tr.Angle)
		sameRegion := (tr.Edge.Row1 < 2 && tr.Edge.Row2 < 2) ||
			(tr.Edge.Row1 >= 2 && tr.Edge.Row2 >= 2)

		if sameRegion {
			if absAngle > maxSameAngle {
				maxSameAngle = absAngle
			}
		} else {
			if absAngle < minBoundaryAngle {
				minBoundaryAngle = absAngle
			}
		}
	}

	if maxSameAngle > 0.01 {
		t.Errorf("same-region transport should be ~0, got max=%.4f", maxSameAngle)
	}
	if minBoundaryAngle < math.Pi/4 {
		t.Errorf("boundary transport should be large (~π/2), got min=%.4f", minBoundaryAngle)
	}
}

func TestComputeCurvature_EndToEnd(t *testing.T) {
	gridSize := 4
	cells := make([]CairnGridCell, gridSize*gridSize)

	for r := 0; r < gridSize; r++ {
		for c := 0; c < gridSize; c++ {
			idx := r*gridSize + c
			var features [CairnNumDims]float64
			features[4] = 0.5
			features[6] = 1.0 // all vertical
			cells[idx] = CairnGridCell{Row: r, Col: c, Features: features}
		}
	}

	result := ComputeCurvature(cells, gridSize)

	// Uniform field → no contours expected
	if len(result.TransportMaps) == 0 {
		t.Error("expected transport maps to be computed")
	}

	// H¹ should be 0 for a uniform field
	if result.H1Dim != 0 {
		t.Errorf("H¹ should be 0 for uniform field, got %d", result.H1Dim)
	}
}

func TestAnnotateZoneBoundaries_Basic(t *testing.T) {
	gridSize := 4
	elements := []element{
		{hasBounds: true, centerX: 0.1, centerY: 0.1},
		{hasBounds: true, centerX: 0.9, centerY: 0.9},
	}
	zones := []zone{
		{rootIdx: 0, elems: []int{0}, centerX: 0.1, centerY: 0.1},
		{rootIdx: 1, elems: []int{1}, centerX: 0.9, centerY: 0.9},
	}

	// Create a curvature result with some contour cells
	curvature := CurvatureResult{
		ContourCells: []int{0, 1, 4, 5}, // cells near top-left
	}

	result := AnnotateZoneBoundaries(zones, curvature, elements, gridSize)

	// Zone 0 should have some boundary annotation (its cells overlap contour cells)
	if result == nil {
		t.Fatal("expected non-nil boundary annotations")
	}
}

func TestCountContourLoops_NoCurvature(t *testing.T) {
	holonomies := []Holonomy{
		{Triangle: GridTriangle{0, 1, 4}, Angle: 0, IsCurved: false},
		{Triangle: GridTriangle{1, 5, 4}, Angle: 0, IsCurved: false},
	}

	h1 := countContourLoops(holonomies, 4)
	if h1 != 0 {
		t.Errorf("H¹ should be 0 with no curved triangles, got %d", h1)
	}
}
