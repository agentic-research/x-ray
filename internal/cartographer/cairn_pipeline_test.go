package cartographer

import (
	"math"
	"testing"
)

func TestConstruct24D_ZeroSum(t *testing.T) {
	e8 := [8]float64{1, 2, 3, 4, 5, 6, 7, 8}
	v := Construct24D(e8)
	residual := VerifyZeroSum(v)
	if residual > 1e-10 {
		t.Errorf("Zero-sum violation: residual = %e", residual)
	}
}

func TestConstruct24D_Structure(t *testing.T) {
	e8 := [8]float64{1, 0, -1, 2, 0, 0, 0, 0}
	v := Construct24D(e8)

	for i := 0; i < 8; i++ {
		if v[i] != e8[i] {
			t.Errorf("Block 0 mismatch at %d: %f != %f", i, v[i], e8[i])
		}
	}
	for i := 0; i < 8; i++ {
		if v[i+8] != e8[i] {
			t.Errorf("Block 1 mismatch at %d: %f != %f", i, v[i+8], e8[i])
		}
	}
	for i := 0; i < 8; i++ {
		if v[i+16] != -2*e8[i] {
			t.Errorf("Block 2 mismatch at %d: %f != %f", i, v[i+16], -2*e8[i])
		}
	}
}

func TestProjectToLeech_Zero(t *testing.T) {
	var features [CairnNumDims]float64
	result := ProjectToLeech(features, 1.0)
	if result.E8Dist != 0 {
		t.Errorf("Zero features should have zero E8 distance, got %f", result.E8Dist)
	}
}

func TestProjectToLeech_Determinism(t *testing.T) {
	features := [CairnNumDims]float64{0.5, 0.3, 0.8, 0.1, 0.2, 0.4, 0.6, 0.7, 0.1, 0.3, 0.5, 0.2}
	r1 := ProjectToLeech(features, 10.0)
	r2 := ProjectToLeech(features, 10.0)
	if r1.LeechPoint != r2.LeechPoint {
		t.Error("Pipeline should be deterministic")
	}
}

func TestProjectToLeech_ScaleAffectsGranularity(t *testing.T) {
	f1 := [CairnNumDims]float64{0.1, 0.2, 0.3, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0}
	f2 := [CairnNumDims]float64{0.15, 0.25, 0.35, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0}

	r1Low := ProjectToLeech(f1, 1.0)
	r2Low := ProjectToLeech(f2, 1.0)

	r1High := ProjectToLeech(f1, 100.0)
	r2High := ProjectToLeech(f2, 100.0)

	sameLow := r1Low.LeechPoint == r2Low.LeechPoint
	sameHigh := r1High.LeechPoint == r2High.LeechPoint

	if sameLow == sameHigh && sameLow {
		t.Log("Both scales map to same point — features may be too similar")
	}
	t.Logf("Scale=1: same=%v, Scale=100: same=%v", sameLow, sameHigh)
}

func TestLeechDistance(t *testing.T) {
	var a, b [24]float64
	a[0] = 2
	b[0] = 0
	d := LeechDistance(a, b)
	if math.Abs(d-2.0) > 1e-10 {
		t.Errorf("Expected distance 2, got %f", d)
	}
}

func TestLeechCluster(t *testing.T) {
	results := []LeechResult{
		ProjectToLeech([CairnNumDims]float64{}, 1.0),
		ProjectToLeech([CairnNumDims]float64{0.01, 0.01, 0.01, 0, 0, 0, 0, 0, 0, 0, 0, 0}, 1.0),
		ProjectToLeech([CairnNumDims]float64{10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10, 10}, 1.0),
	}
	clusters := LeechCluster(results)
	if len(clusters) < 1 {
		t.Error("Should have at least 1 cluster")
	}
	t.Logf("Got %d clusters from 3 points", len(clusters))
}

func TestProjectToGear2(t *testing.T) {
	features := [CairnNumDims]float64{
		0.5, 0.1, -0.2, 0.3, 0.7, 0.4, 0.3, 0.2, 0.6, 0.8, 0.15, 0.9,
	}
	g2 := ProjectToGear2(features)
	expected := [8]float64{0.5, 0.1, -0.2, 0.3, 0.7, 0.6, 0.8, 0.9}
	for i := 0; i < 8; i++ {
		if math.Abs(g2[i]-expected[i]) > 1e-10 {
			t.Errorf("Gear2 dim %d: expected %f, got %f", i, expected[i], g2[i])
		}
	}
}
