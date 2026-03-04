package cartographer

import (
	"math"
	"testing"
)

func TestGolayBruteForce_Zero(t *testing.T) {
	var zeros [24]byte
	out := GolayDecodeBruteForce(zeros)
	if out != zeros {
		t.Errorf("All zeros should be a valid codeword, got %v", out)
	}
}

func TestGolayBruteForce_AllOnes(t *testing.T) {
	var ones [24]byte
	for i := range ones {
		ones[i] = 1
	}
	out := GolayDecodeBruteForce(ones)
	if out != ones {
		t.Errorf("All ones should be a valid codeword, got %v", out)
	}
}

func TestGolayBruteForce_CorrectsSingleError(t *testing.T) {
	var zeros [24]byte
	for pos := 0; pos < 24; pos++ {
		noisy := zeros
		noisy[pos] = 1
		out := GolayDecodeBruteForce(noisy)
		if out != zeros {
			t.Errorf("Failed to correct single error at position %d", pos)
		}
	}
}

func TestGolayBruteForce_ConsistentWithSyndrome(t *testing.T) {
	var zeros [24]byte
	noisy := zeros
	noisy[0] = 1
	noisy[12] = 1
	noisy[23] = 1

	syndromeResult := DecodeGolay24(noisy)
	bruteResult := GolayDecodeBruteForce(noisy)
	if syndromeResult != bruteResult {
		t.Errorf("Syndrome and brute-force decoders disagree: %v vs %v", syndromeResult, bruteResult)
	}
}

func TestDecodeLeechTuryn_Zero(t *testing.T) {
	var x [24]float64
	out := DecodeLeechTuryn(x)
	for i, v := range out {
		if v != 0 {
			t.Errorf("Zero should decode to zero, got %f at position %d", v, i)
		}
	}
}

func TestDecodeLeechTuryn_IntegerCoset(t *testing.T) {
	var x [24]float64
	x[0] = 2.1
	x[1] = 0.05
	out := DecodeLeechTuryn(x)
	dist := math.Sqrt(distSq24(x, out))
	if dist > LeechCoveringRadius+0.1 {
		t.Errorf("Leech distance %f exceeds covering radius %f", dist, LeechCoveringRadius)
	}
}

func TestDecodeLeechTuryn_HalfCoset(t *testing.T) {
	var x [24]float64
	for i := range x {
		x[i] = 0.5
	}
	out := DecodeLeechTuryn(x)
	dist := math.Sqrt(distSq24(x, out))
	if dist > LeechCoveringRadius+0.1 {
		t.Errorf("Leech distance %f for half-integer point exceeds covering radius", dist)
	}
}

func TestDecodeLeechTuryn_Determinism(t *testing.T) {
	x := [24]float64{
		1.3, -0.7, 2.1, 0.4, -1.8, 0.9, 0.0, 3.3,
		-0.5, 1.1, 2.2, -3.3, 0.1, 0.2, 0.3, 0.4,
		-0.1, -0.2, -0.3, -0.4, 1.5, 2.5, -1.5, -2.5,
	}
	y1 := DecodeLeechTuryn(x)
	y2 := DecodeLeechTuryn(x)
	if y1 != y2 {
		t.Error("Leech decoder should be deterministic")
	}
}

func TestDecodeLeechTuryn_NoiseCorrection(t *testing.T) {
	var x [24]float64
	for i := range x {
		x[i] = 0.1 * float64(i%3-1)
	}
	out := DecodeLeechTuryn(x)
	dist := math.Sqrt(distSq24(x, out))
	if dist > LeechCoveringRadius+0.01 {
		t.Errorf("Small noise should correct to nearby lattice point, dist=%f", dist)
	}
}
