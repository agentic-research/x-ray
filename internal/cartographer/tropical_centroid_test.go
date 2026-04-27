package cartographer

import (
	"testing"
)

func TestCentroidDistanceMatrix(t *testing.T) {
	// 3 centroids with known feature vectors
	centroids := [][CairnNumDims]float64{
		{
			0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 0.1, 0.2, 0.3,
			0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 0.1, 0.0, 0.0, 0.3, 0.4, 0.5,
		},
		{
			0.9, 0.8, 0.7, 0.6, 0.5, 0.4, 0.3, 0.2, 0.1, 0.9, 0.8, 0.7,
			0.6, 0.5, 0.4, 0.3, 0.2, 0.1, 0.9, 1.0, 1.0, 0.7, 0.6, 0.5,
		},
		{
			0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5,
			0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5,
		},
	}
	bounds := [][4]float64{
		{0.0, 0.0, 0.5, 0.3},
		{0.5, 0.0, 0.5, 0.3},
		{0.0, 0.5, 1.0, 0.5},
	}

	dist := BuildCentroidDistanceMatrix(centroids, bounds)
	if len(dist) != 3 {
		t.Fatalf("expected 3x3 matrix, got %dx%d", len(dist), len(dist))
	}

	// Symmetric
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			if dist[i][j] != dist[j][i] {
				t.Errorf("not symmetric: dist[%d][%d]=%.4f != dist[%d][%d]=%.4f",
					i, j, dist[i][j], j, i, dist[j][i])
			}
		}
		// Diagonal = 0
		if dist[i][i] != 0 {
			t.Errorf("diagonal not zero: dist[%d][%d]=%.4f", i, i, dist[i][i])
		}
	}

	// Non-identical centroids should have positive distance
	if dist[0][1] <= 0 {
		t.Errorf("expected positive distance between different centroids, got %.4f", dist[0][1])
	}
}

func TestTropicalRefineZones(t *testing.T) {
	// Parse test elements
	elements := parseElements(cairnTestSummary)
	if len(elements) == 0 {
		t.Fatal("no elements parsed")
	}

	// Create initial zones from DOM structure
	zones := structuralFallbackZones(elements)
	if len(zones) < 2 {
		t.Skipf("need at least 2 zones for tropical, got %d", len(zones))
	}

	// Without cells, tropicalRefineZones should still work
	// (stalks will be zero, distances will be spatial-only)
	refined := tropicalRefineZones(zones, elements, nil, 12)
	if len(refined) == 0 {
		t.Fatal("tropicalRefineZones returned empty result")
	}

	// All original elements should still be accounted for
	origElems := make(map[int]bool)
	for _, z := range zones {
		for _, ei := range z.elems {
			origElems[ei] = true
		}
	}

	refinedElems := make(map[int]bool)
	for _, z := range refined {
		for _, ei := range z.elems {
			refinedElems[ei] = true
		}
	}

	for ei := range origElems {
		if !refinedElems[ei] {
			t.Errorf("element %d lost during tropical refinement", ei)
		}
	}

	t.Logf("Tropical refinement: %d zones -> %d zones", len(zones), len(refined))
}

func BenchmarkTropicalCentroidNJ(b *testing.B) {
	// Simulate 20 zone centroids (typical count)
	k := 20
	centroids := make([][CairnNumDims]float64, k)
	bounds := make([][4]float64, k)
	for i := 0; i < k; i++ {
		for d := 0; d < CairnNumDims; d++ {
			centroids[i][d] = float64(i*CairnNumDims+d) / float64(k*CairnNumDims)
		}
		bounds[i] = [4]float64{
			float64(i%5) * 0.2, float64(i/5) * 0.25, 0.2, 0.25,
		}
	}

	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		dist := BuildCentroidDistanceMatrix(centroids, bounds)
		neighborJoining(dist, k)
	}
}
