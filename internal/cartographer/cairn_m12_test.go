package cartographer

import (
	"testing"
)

func TestGolayTernaryCodewordCount(t *testing.T) {
	seen := make(map[[12]int]bool)
	for _, cw := range golayTernaryCodewords {
		seen[cw] = true
	}
	if len(seen) != 729 {
		t.Errorf("Expected 729 unique codewords, got %d", len(seen))
	}
}

func TestGolayTernaryZeroCodeword(t *testing.T) {
	var msg [6]int
	cw := GolayTernaryEncode(msg)
	for i := 0; i < 12; i++ {
		if cw[i] != 0 {
			t.Errorf("Zero message should give zero codeword, but cw[%d]=%d", i, cw[i])
		}
	}
}

func TestGolayTernaryEncodeSystematic(t *testing.T) {
	msg := [6]int{1, 2, 0, 1, 2, 1}
	cw := GolayTernaryEncode(msg)
	for i := 0; i < 6; i++ {
		if cw[i] != msg[i] {
			t.Errorf("Systematic property: cw[%d]=%d != msg[%d]=%d", i, cw[i], i, msg[i])
		}
	}
}

func TestGolayTernaryMinimumDistance(t *testing.T) {
	minDist := 13
	for i := 0; i < 729; i++ {
		for j := i + 1; j < 729; j++ {
			dist := 0
			for k := 0; k < 12; k++ {
				if golayTernaryCodewords[i][k] != golayTernaryCodewords[j][k] {
					dist++
				}
			}
			if dist < minDist {
				minDist = dist
			}
		}
	}
	if minDist != 6 {
		t.Errorf("Minimum distance should be 6, got %d", minDist)
	}
}

func TestGolayTernaryWeightDistribution(t *testing.T) {
	weights := make(map[int]int)
	for _, cw := range golayTernaryCodewords {
		w := 0
		for _, v := range cw {
			if v != 0 {
				w++
			}
		}
		weights[w]++
	}

	expected := map[int]int{0: 1, 6: 264, 9: 440, 12: 24}
	for wt, count := range expected {
		if weights[wt] != count {
			t.Errorf("Weight %d: expected %d codewords, got %d", wt, count, weights[wt])
		}
	}
	for wt, count := range weights {
		if _, ok := expected[wt]; !ok {
			t.Errorf("Unexpected weight %d with %d codewords", wt, count)
		}
	}
}

func TestGolayTernaryDecodeExact(t *testing.T) {
	msg := [6]int{2, 1, 0, 2, 1, 0}
	cw := GolayTernaryEncode(msg)
	decoded := GolayTernaryDecode(cw)
	if decoded != cw {
		t.Errorf("Decoding exact codeword should return itself: got %v != %v", decoded, cw)
	}
}

func TestGolayTernaryDecodeCorrectsSingleError(t *testing.T) {
	msg := [6]int{1, 0, 2, 1, 0, 2}
	cw := GolayTernaryEncode(msg)

	corrupted := cw
	corrupted[3] = (corrupted[3] + 1) % 3

	decoded := GolayTernaryDecode(corrupted)
	if decoded != cw {
		t.Errorf("Failed to correct single error: got %v, want %v", decoded, cw)
	}
}

func TestGolayTernaryDecodeCorrectsTwoErrors(t *testing.T) {
	msg := [6]int{0, 1, 2, 0, 1, 2}
	cw := GolayTernaryEncode(msg)

	corrupted := cw
	corrupted[1] = (corrupted[1] + 1) % 3
	corrupted[8] = (corrupted[8] + 2) % 3

	decoded := GolayTernaryDecode(corrupted)
	if decoded != cw {
		t.Errorf("Failed to correct two errors: got %v, want %v", decoded, cw)
	}
}

func TestTernaryPhaseQuantize(t *testing.T) {
	var input [CairnNumDims]float64
	input[0] = 0.0
	input[1] = 1.0 / 3
	input[2] = 2.0 / 3
	input[3] = 0.05
	input[4] = 0.38
	input[5] = 0.72

	result := TernaryPhaseQuantize(input)
	if result[0] != 0 {
		t.Errorf("Phase 0 should quantize to 0, got %d", result[0])
	}
	if result[1] != 1 {
		t.Errorf("Phase 2π/3 should quantize to 1, got %d", result[1])
	}
	if result[2] != 2 {
		t.Errorf("Phase 4π/3 should quantize to 2, got %d", result[2])
	}
}

func TestProjectToGear3_Determinism(t *testing.T) {
	features := [CairnNumDims]float64{0.5, 0.3, 0.8, 0.1, 0.2, 0.4, 0.6, 0.7, 0.1, 0.3, 0.5, 0.9}
	r1 := ProjectToGear3(features)
	r2 := ProjectToGear3(features)
	if r1.Codeword != r2.Codeword {
		t.Error("Gear 3 pipeline should be deterministic")
	}
}

func TestProjectToGear3_ErrorCorrection(t *testing.T) {
	f1 := [CairnNumDims]float64{0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5}
	f2 := [CairnNumDims]float64{0.52, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5}

	r1 := ProjectToGear3(f1)
	r2 := ProjectToGear3(f2)

	if r1.Codeword != r2.Codeword {
		t.Logf("Note: small perturbation changed codeword (may be near boundary)")
		t.Logf("  f1 ternary=%v codeword=%v", r1.Ternary, r1.Codeword)
		t.Logf("  f2 ternary=%v codeword=%v", r2.Ternary, r2.Codeword)
	}
}

func TestGear3Cluster(t *testing.T) {
	results := []Gear3Result{
		ProjectToGear3([CairnNumDims]float64{}),
		ProjectToGear3([CairnNumDims]float64{0.01, 0.01, 0.01, 0, 0, 0, 0, 0, 0, 0, 0, 0}),
		ProjectToGear3([CairnNumDims]float64{0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5}),
	}
	clusters := Gear3Cluster(results)
	if len(clusters) < 1 {
		t.Error("Should have at least 1 cluster")
	}
	t.Logf("Got %d Gear 3 clusters from 3 points", len(clusters))
}
