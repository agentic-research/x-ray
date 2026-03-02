package cartographer

import (
	"math"
	"testing"
)

func TestRM15CodewordCount(t *testing.T) {
	seen := make(map[[32]byte]bool)
	for _, cw := range rm15Codewords {
		seen[cw] = true
	}
	if len(seen) != 64 {
		t.Errorf("Expected 64 unique RM(1,5) codewords, got %d", len(seen))
	}
}

func TestRM15ZeroCodeword(t *testing.T) {
	var expected [32]byte
	if rm15Codewords[0] != expected {
		t.Error("Message 0 should give all-zero codeword")
	}
}

func TestRM15AllOnesCodeword(t *testing.T) {
	var expected [32]byte
	for i := range expected {
		expected[i] = 1
	}
	if rm15Codewords[1] != expected {
		t.Errorf("Message 1 should give all-ones codeword, got %v", rm15Codewords[1])
	}
}

func TestRM15MinimumDistance(t *testing.T) {
	minDist := 33
	for i := 0; i < 64; i++ {
		for j := i + 1; j < 64; j++ {
			dist := 0
			for k := 0; k < 32; k++ {
				if rm15Codewords[i][k] != rm15Codewords[j][k] {
					dist++
				}
			}
			if dist < minDist {
				minDist = dist
			}
		}
	}
	if minDist != 16 {
		t.Errorf("RM(1,5) minimum distance should be 16, got %d", minDist)
	}
}

func TestRM15DecodeExact(t *testing.T) {
	for idx := 0; idx < 64; idx++ {
		decoded := RM15Decode(rm15Codewords[idx])
		if decoded != rm15Codewords[idx] {
			t.Errorf("Codeword %d not decoded to itself", idx)
		}
	}
}

func TestRM15DecodeSingleError(t *testing.T) {
	cw := rm15Codewords[42]
	corrupted := cw
	corrupted[5] ^= 1
	decoded := RM15Decode(corrupted)
	if decoded != cw {
		t.Error("Failed to correct single bit error")
	}
}

func TestDecodeBW32_Zero(t *testing.T) {
	var x [32]float64
	bw := DecodeBW32(x)
	for i := 0; i < 32; i++ {
		if bw[i] != 0 {
			t.Errorf("Zero input should decode to zero, bw[%d]=%f", i, bw[i])
		}
	}
}

func TestDecodeBW32_Determinism(t *testing.T) {
	var x [32]float64
	for i := range x {
		x[i] = float64(i) * 0.3
	}
	bw1 := DecodeBW32(x)
	bw2 := DecodeBW32(x)
	if bw1 != bw2 {
		t.Error("BW32 decoder should be deterministic")
	}
}

func TestDecodeBW32_IntegerPoint(t *testing.T) {
	var x [32]float64
	for i := range x {
		x[i] = 2.0
	}
	bw := DecodeBW32(x)
	for i := 0; i < 32; i++ {
		if bw[i] != 2.0 {
			t.Errorf("All-2s should be a lattice point, but bw[%d]=%f", i, bw[i])
		}
	}
}

func TestBW32Distance_AtLatticePoint(t *testing.T) {
	var x [32]float64
	d := BW32Distance(x)
	if d > 1e-10 {
		t.Errorf("Distance at lattice point should be 0, got %f", d)
	}
}

func TestBW32Distance_BoundedByCoveringRadius(t *testing.T) {
	var x [32]float64
	for i := range x {
		x[i] = float64(i%5) * 0.7
	}
	d := BW32Distance(x)
	if d > BW32CoveringRadius+0.01 {
		t.Errorf("Distance %f exceeds covering radius %f", d, BW32CoveringRadius)
	}
}

func TestProjectToGear6_Determinism(t *testing.T) {
	features := [CairnNumDims]float64{0.5, 0.3, 0.8, 0.1, 0.2, 0.4, 0.6, 0.7, 0.1, 0.3, 0.5, 0.9}
	r1 := ProjectToGear6(features, 5.0)
	r2 := ProjectToGear6(features, 5.0)
	if r1.BW32Point != r2.BW32Point {
		t.Error("Gear 6 pipeline should be deterministic")
	}
}

func TestGear6Cluster(t *testing.T) {
	results := []Gear6Result{
		ProjectToGear6([CairnNumDims]float64{}, 5.0),
		ProjectToGear6([CairnNumDims]float64{0.01, 0.01, 0.01, 0, 0, 0, 0, 0, 0, 0, 0, 0}, 5.0),
		ProjectToGear6([CairnNumDims]float64{0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5}, 5.0),
	}
	clusters := Gear6Cluster(results)
	if len(clusters) < 1 {
		t.Error("Should have at least 1 cluster")
	}
	t.Logf("Got %d Gear 6 clusters from 3 points", len(clusters))
}

func TestConstruct32D(t *testing.T) {
	var leech [24]float64
	var e8 [8]float64
	leech[0] = 1.0
	e8[0] = 2.0

	v := Construct32D(leech, e8)
	if v[0] != 1.0 {
		t.Errorf("v[0] should be leech[0]=1.0, got %f", v[0])
	}
	if v[24] != 2.0 {
		t.Errorf("v[24] should be e8[0]=2.0, got %f", v[24])
	}
	d := BW32Distance(v)
	if math.IsNaN(d) {
		t.Error("Distance should not be NaN")
	}
}
