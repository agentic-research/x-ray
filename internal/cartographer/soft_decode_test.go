package cartographer

import (
	"math"
	"math/rand"
	"testing"
)

// TestSoftVsHardDecoder compares the corrected Construction A decoder against
// the old broken approaches.
func TestSoftVsHardDecoder(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	const N = 1000

	type stats struct {
		name         string
		withinRadius int
		totalDist    float64
	}

	type testVec struct {
		input [24]float64
	}
	vecs := make([]testVec, N)
	for i := range vecs {
		for j := 0; j < 24; j++ {
			vecs[i].input[j] = rng.Float64() * 10
		}
	}

	// Current correct decoder (Construction A with scaling + sum constraint)
	soft := stats{name: "Soft-decision"}
	for _, v := range vecs {
		decoded := DecodeLeechTuryn(v.input)
		dist := math.Sqrt(distSq24(v.input, decoded))
		soft.totalDist += dist
		if dist <= LeechCoveringRadius+1e-6 {
			soft.withinRadius++
		}
	}

	// Old syndrome decoder
	syndrome := stats{name: "Syndrome (3-bit)"}
	for _, v := range vecs {
		decoded := decodeLeechWithSyndrome(v.input)
		dist := math.Sqrt(distSq24(v.input, decoded))
		syndrome.totalDist += dist
		if dist <= LeechCoveringRadius+1e-6 {
			syndrome.withinRadius++
		}
	}

	// Old brute-force Hamming decoder
	bruteHamming := stats{name: "Brute Hamming"}
	for _, v := range vecs {
		decoded := decodeLeechWithBruteHamming(v.input)
		dist := math.Sqrt(distSq24(v.input, decoded))
		bruteHamming.totalDist += dist
		if dist <= LeechCoveringRadius+1e-6 {
			bruteHamming.withinRadius++
		}
	}

	for _, s := range []stats{syndrome, bruteHamming, soft} {
		pct := float64(s.withinRadius) / float64(N) * 100
		avg := s.totalDist / float64(N)
		t.Logf("%-18s  within √2: %4d/%d (%5.1f%%)  avg dist: %.4f", s.name, s.withinRadius, N, pct, avg)
	}

	// The corrected decoder must dramatically outperform the old ones.
	softPct := float64(soft.withinRadius) / float64(N) * 100
	if softPct < 90.0 {
		t.Errorf("Construction A decoder should achieve >=90%% within covering radius, got %.1f%%", softPct)
	}
}

// TestVerifyLeechPoints checks that the decoder always outputs valid Λ₂₄ points.
func TestVerifyLeechPoints(t *testing.T) {
	rng := rand.New(rand.NewSource(123))
	const N = 500
	invalid := 0

	for i := 0; i < N; i++ {
		var v [24]float64
		for j := 0; j < 24; j++ {
			v[j] = rng.Float64()*20 - 10 // range [-10, 10]
		}
		decoded := DecodeLeechTuryn(v)
		ok, reason := VerifyLeechPoint(decoded)
		if !ok {
			invalid++
			if invalid <= 5 {
				t.Logf("Invalid point #%d: %s (input sample: [%.2f, %.2f, ...])", i, reason, v[0], v[1])
			}
		}
	}

	pct := float64(N-invalid) / float64(N) * 100
	t.Logf("Valid Leech points: %d/%d (%.1f%%)", N-invalid, N, pct)
	if invalid > 0 {
		t.Errorf("%d/%d outputs were NOT valid Leech lattice points", invalid, N)
	}
}

func TestDecodeLeech_Determinism(t *testing.T) {
	var v [24]float64
	for i := range v {
		v[i] = float64(i) * 0.37
	}
	r1 := DecodeLeechTuryn(v)
	r2 := DecodeLeechTuryn(v)
	if r1 != r2 {
		t.Error("Decoder must be deterministic")
	}
}

func TestDecodeLeech_Zero(t *testing.T) {
	var v [24]float64
	decoded := DecodeLeechTuryn(v)
	// Zero is in the Leech lattice (even family, all zeros).
	dist := math.Sqrt(distSq24(v, decoded))
	if dist > 1e-10 {
		t.Errorf("Zero should decode to zero, got dist=%f", dist)
	}
}

// --- Old decoders for comparison ---

func decodeLeechWithSyndrome(x [24]float64) [24]float64 {
	candInt := decodeIntCosetSyndrome(x)
	var shifted [24]float64
	for i := 0; i < 24; i++ {
		shifted[i] = x[i] - 0.5
	}
	half := decodeIntCosetSyndrome(shifted)
	for i := 0; i < 24; i++ {
		half[i] += 0.5
	}
	if distSq24(x, half) < distSq24(x, candInt) {
		return half
	}
	return candInt
}

func decodeIntCosetSyndrome(x [24]float64) [24]float64 {
	var u [24]float64
	var p [24]byte
	for i := 0; i < 24; i++ {
		u[i] = math.Round(x[i])
	}
	for i := 0; i < 24; i++ {
		v := int(u[i])
		p[i] = byte(((v % 2) + 2) % 2)
	}
	c := DecodeGolay24(p)
	for i := 0; i < 24; i++ {
		if p[i] != c[i] {
			if x[i]-u[i] >= 0 {
				u[i] += 1
			} else {
				u[i] -= 1
			}
		}
	}
	return u
}

func decodeLeechWithBruteHamming(x [24]float64) [24]float64 {
	candInt := decodeIntCosetBruteHamming(x)
	var shifted [24]float64
	for i := 0; i < 24; i++ {
		shifted[i] = x[i] - 0.5
	}
	half := decodeIntCosetBruteHamming(shifted)
	for i := 0; i < 24; i++ {
		half[i] += 0.5
	}
	if distSq24(x, half) < distSq24(x, candInt) {
		return half
	}
	return candInt
}

func decodeIntCosetBruteHamming(x [24]float64) [24]float64 {
	var u [24]float64
	var p [24]byte
	for i := 0; i < 24; i++ {
		u[i] = math.Round(x[i])
	}
	for i := 0; i < 24; i++ {
		v := int(u[i])
		p[i] = byte(((v % 2) + 2) % 2)
	}
	c := GolayDecodeBruteForce(p)
	for i := 0; i < 24; i++ {
		if p[i] != c[i] {
			if x[i]-u[i] >= 0 {
				u[i] += 1
			} else {
				u[i] -= 1
			}
		}
	}
	return u
}
