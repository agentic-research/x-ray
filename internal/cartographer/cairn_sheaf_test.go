package cartographer

import (
	"math"
	"testing"
)

func TestBuildDOMSheaf_BasicStructure(t *testing.T) {
	elements := []element{
		{id: "nav-1", tag: "nav", hasBounds: true, centerX: 0.5, centerY: 0.05},
		{id: "a-1", tag: "a", parentID: "nav-1", hasBounds: true, centerX: 0.3, centerY: 0.05},
		{id: "a-2", tag: "a", parentID: "nav-1", hasBounds: true, centerX: 0.7, centerY: 0.05},
		{id: "main-1", tag: "main", hasBounds: true, centerX: 0.5, centerY: 0.5},
		{id: "div-1", tag: "div", parentID: "main-1", hasBounds: true, centerX: 0.5, centerY: 0.5},
	}

	zones := []zone{
		{rootIdx: 0, elems: []int{0, 1, 2}, centerX: 0.5, centerY: 0.05},
		{rootIdx: 3, elems: []int{3, 4}, centerX: 0.5, centerY: 0.5},
	}

	nodes := BuildDOMSheaf(zones, elements)

	if len(nodes) == 0 {
		t.Fatal("expected at least one sheaf node")
	}

	// Should have nodes for structural ancestors (nav-1, main-1)
	totalZones := 0
	for _, n := range nodes {
		totalZones += len(n.ZoneIdxs)
	}
	if totalZones != 2 {
		t.Errorf("expected 2 zones mapped to nodes, got %d", totalZones)
	}
}

func TestComputeZoneStalks(t *testing.T) {
	cells := []CairnGridCell{
		{Row: 0, Col: 0, Features: [CairnNumDims]float64{0.5, 0.3}},
		{Row: 0, Col: 1, Features: [CairnNumDims]float64{0.7, 0.1}},
		{Row: 1, Col: 0, Features: [CairnNumDims]float64{0.2, 0.8}},
	}
	elements := []element{
		{hasBounds: true, centerX: 0.0, centerY: 0.0}, // maps to (0,0)
		{hasBounds: true, centerX: 0.4, centerY: 0.0}, // maps to (0,0) with gridSize=2
		{hasBounds: true, centerX: 0.0, centerY: 0.9}, // maps to (1,0)
	}
	zones := []zone{
		{rootIdx: 0, elems: []int{0, 1}},
		{rootIdx: 2, elems: []int{2}},
	}

	stalks := computeZoneStalks(zones, elements, cells, 2)

	if len(stalks) != 2 {
		t.Fatalf("expected 2 stalks, got %d", len(stalks))
	}

	// Zone 0: both elements map to cell (0,0) → stalk should be cell (0,0) features
	if math.Abs(stalks[0][0]-0.5) > 0.01 {
		t.Errorf("zone 0 stalk[0] = %.3f, want ~0.5", stalks[0][0])
	}

	// Zone 1: element maps to cell (1,0) → stalk should be cell (1,0) features
	if math.Abs(stalks[1][0]-0.2) > 0.01 {
		t.Errorf("zone 1 stalk[0] = %.3f, want ~0.2", stalks[1][0])
	}
}

func TestComputeRestrictionWeights(t *testing.T) {
	// Two stalks that are identical on dim 0, differ on dim 1
	stalks := [][]float64{
		{0.5, 0.1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		{0.5, 0.9, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
	}

	weights := computeRestrictionWeights(stalks)

	// Dim 0: zero variance → weight should be 1.0
	if math.Abs(weights[0]-1.0) > 0.01 {
		t.Errorf("weight[0] = %.3f, expected 1.0 (zero variance)", weights[0])
	}

	// Dim 1: high variance → weight should be lower
	if weights[1] >= weights[0] {
		t.Errorf("weight[1] = %.3f should be less than weight[0] = %.3f", weights[1], weights[0])
	}
}

func TestComputeH0_ConsistentZones(t *testing.T) {
	// Two zones with identical stalks under the same ancestor → should merge
	zones := []zone{
		{rootIdx: 0, elems: []int{0}, centerX: 0.3, centerY: 0.1},
		{rootIdx: 1, elems: []int{1}, centerX: 0.7, centerY: 0.1},
	}
	stalks := [][]float64{
		{0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		{0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
	}
	nodes := []SheafNode{
		{ElementIdx: -1, ZoneIdxs: []int{0, 1}},
	}

	var weights [CairnNumDims]float64
	for d := 0; d < CairnNumDims; d++ {
		weights[d] = 1.0
	}

	h0 := ComputeH0(zones, stalks, nodes, weights, 0.1)

	if len(h0.ConsistentGroups) != 1 {
		t.Errorf("expected 1 consistent group (identical stalks), got %d", len(h0.ConsistentGroups))
	}
	if h0.Defect > 0.01 {
		t.Errorf("defect should be ~0 for identical stalks, got %.4f", h0.Defect)
	}
}

func TestComputeH0_InconsistentZones(t *testing.T) {
	// Two zones with very different stalks → should NOT merge
	zones := []zone{
		{rootIdx: 0, elems: []int{0}, centerX: 0.3, centerY: 0.1},
		{rootIdx: 1, elems: []int{1}, centerX: 0.7, centerY: 0.9},
	}
	stalks := [][]float64{
		{1.0, 0.0, 1.0, 0.0, 1.0, 0.0, 1.0, 0.0, 1.0, 0.0, 1.0, 0.0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		{0.0, 1.0, 0.0, 1.0, 0.0, 1.0, 0.0, 1.0, 0.0, 1.0, 0.0, 1.0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
	}
	nodes := []SheafNode{
		{ElementIdx: -1, ZoneIdxs: []int{0, 1}},
	}

	var weights [CairnNumDims]float64
	for d := 0; d < CairnNumDims; d++ {
		weights[d] = 1.0
	}

	h0 := ComputeH0(zones, stalks, nodes, weights, 0.1)

	if len(h0.ConsistentGroups) < 2 {
		t.Errorf("expected 2 groups (different stalks), got %d", len(h0.ConsistentGroups))
	}
	if h0.Defect < 1.0 {
		t.Errorf("defect should be large for opposite stalks, got %.4f", h0.Defect)
	}
}

func TestCoboundary_ZeroForConsistentSections(t *testing.T) {
	// If all zone stalks are identical, d⁰ should produce zero entries
	stalks := [][]float64{
		{0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		{0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
	}
	zones := []zone{
		{rootIdx: 0, centerX: 0.3, centerY: 0.1},
		{rootIdx: 1, centerX: 0.3, centerY: 0.1}, // same position → spatial edge
	}
	nodes := []SheafNode{
		{ElementIdx: -1, ZoneIdxs: []int{0, 1}},
	}
	var weights [CairnNumDims]float64
	for d := 0; d < CairnNumDims; d++ {
		weights[d] = 1.0
	}

	cb := BuildCechCoboundary(zones, stalks, nodes, weights)

	// All entries should have Value ~0 because stalks are identical
	for _, e := range cb.Entries {
		// Entries exist (the structure entries for ±weight) but
		// they cancel when applied to identical stalks
		_ = e
	}

	// Apply d⁰ to the stalk vector and check result is zero
	stalkVec := make([]float64, len(zones)*CairnNumDims)
	for zi, s := range stalks {
		for d := 0; d < CairnNumDims; d++ {
			stalkVec[zi*CairnNumDims+d] = s[d]
		}
	}

	result := make([]float64, cb.Rows)
	for _, e := range cb.Entries {
		if e.Col < len(stalkVec) {
			result[e.Row] += e.Value * stalkVec[e.Col]
		}
	}

	var norm float64
	for _, v := range result {
		norm += v * v
	}
	if math.Sqrt(norm) > 1e-8 {
		t.Errorf("d⁰ applied to consistent stalks should be 0, got norm=%.8f", math.Sqrt(norm))
	}
}

func TestFoldZonesBySheaf_PreservesMinZones(t *testing.T) {
	elements := []element{
		{id: "nav-1", tag: "nav", hasBounds: true, centerX: 0.5, centerY: 0.05},
		{id: "main-1", tag: "main", hasBounds: true, centerX: 0.5, centerY: 0.5},
		{id: "foot-1", tag: "footer", hasBounds: true, centerX: 0.5, centerY: 0.95},
	}
	zones := []zone{
		{rootIdx: 0, elems: []int{0}, centerX: 0.5, centerY: 0.05},
		{rootIdx: 1, elems: []int{1}, centerX: 0.5, centerY: 0.5},
		{rootIdx: 2, elems: []int{2}, centerX: 0.5, centerY: 0.95},
	}

	// Create minimal cells
	cells := make([]CairnGridCell, 0)
	for r := 0; r < 4; r++ {
		for c := 0; c < 4; c++ {
			cells = append(cells, CairnGridCell{Row: r, Col: c})
		}
	}

	result := FoldZonesBySheaf(zones, elements, cells, 4, 3, 7)

	if len(result) < 3 {
		t.Errorf("expected at least 3 zones (minZ=3), got %d", len(result))
	}
}
