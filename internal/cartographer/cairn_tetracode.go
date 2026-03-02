// cairn_tetracode.go — Gear 1: Tetracode/D4 lattice quantization (4D).
// Ported from cairn experiment (tetracode.go).
//
// The Tetracode is a [4,2,3] ternary code over GF(3).
// 12D features → semantic down-project to 4D → quantize to ternary → decode to nearest codeword.

package cartographer

import "math"

// Tetracode codewords: all 9 valid codewords of the [4,2,3] ternary code.
// Generator matrix: G = [[1,0,1,1],[0,1,1,2]] over GF(3)
// Codeword = [a, b, a+b, a+2b] mod 3
var tetracodeWords [][4]int

func init() {
	for a := 0; a < 3; a++ {
		for b := 0; b < 3; b++ {
			tetracodeWords = append(tetracodeWords, [4]int{
				a,
				b,
				(a + b) % 3,
				(a + 2*b) % 3,
			})
		}
	}
}

// ProjectToGear1 performs the semantic 12D→4D down-projection for Gear 1.
// Output: [Luminance, edgeDensity, dirVariance, contrast]
func ProjectToGear1(features [CairnNumDims]float64) [4]float64 {
	return [4]float64{
		features[0], // luminance
		features[4], // edgeDensity
		features[8], // dirVariance
		features[9], // contrast
	}
}

// QuantizeTetracode quantizes a 4D vector to the nearest Tetracode codeword.
func QuantizeTetracode(x [4]float64) [4]int {
	// Quantize to ternary: map continuous values to {0, 1, 2}
	var ternary [4]float64
	for i := 0; i < 4; i++ {
		v := x[i] * 3.0
		if v < 0 {
			v = 0
		}
		ternary[i] = v
	}

	// Find nearest valid codeword (brute force over 9 codewords)
	bestDist := math.Inf(1)
	var bestWord [4]int
	for _, cw := range tetracodeWords {
		var dist float64
		for i := 0; i < 4; i++ {
			d := ternary[i] - float64(cw[i])
			dist += d * d
		}
		if dist < bestDist {
			bestDist = dist
			bestWord = cw
		}
	}
	return bestWord
}

// TetraDistance computes distance from a 4D point to the nearest Tetracode codeword.
func TetraDistance(x [4]float64) float64 {
	cw := QuantizeTetracode(x)
	var dist float64
	v := [4]float64{x[0] * 3, x[1] * 3, x[2] * 3, x[3] * 3}
	for i := 0; i < 4; i++ {
		d := v[i] - float64(cw[i])
		dist += d * d
	}
	return math.Sqrt(dist)
}
