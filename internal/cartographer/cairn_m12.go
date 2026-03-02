// cairn_m12.go — Gear 3: M12 / Ternary Golay [12,6,6] lattice quantization.
// Ported from cairn experiment (m12.go).
//
// The Ternary Golay code is a [12,6,6] code over GF(3) with 729 codewords.
// Its automorphism group is 2.M₁₂ (double cover of the Mathieu group M₁₂).

package cartographer

import "math"

// --- GF(3) arithmetic ---

func gf3Mul(a, b int) int { return (a * b) % 3 }

// --- Ternary Golay [12,6,6] code ---

// golayTernaryParity is the parity matrix A (6x6) for G = [I₆ | A].
var golayTernaryParity = [6][6]int{
	{0, 1, 1, 1, 1, 1},
	{1, 1, 1, 2, 0, 2},
	{1, 1, 2, 0, 2, 1},
	{1, 2, 0, 2, 1, 1},
	{1, 0, 2, 1, 1, 2},
	{1, 2, 1, 1, 2, 0},
}

// golayTernaryCodewords holds all 729 codewords of the [12,6,6] ternary Golay code.
var golayTernaryCodewords [729][12]int

func init() {
	for idx := 0; idx < 729; idx++ {
		var msg [6]int
		temp := idx
		for i := 5; i >= 0; i-- {
			msg[i] = temp % 3
			temp /= 3
		}

		var cw [12]int
		for j := 0; j < 6; j++ {
			cw[j] = msg[j]
		}
		for j := 0; j < 6; j++ {
			sum := 0
			for i := 0; i < 6; i++ {
				sum += gf3Mul(msg[i], golayTernaryParity[i][j])
			}
			cw[6+j] = sum % 3
		}
		golayTernaryCodewords[idx] = cw
	}
}

// GolayTernaryDecode finds the nearest ternary Golay codeword by Hamming distance.
func GolayTernaryDecode(v [12]int) [12]int {
	bestDist := 13
	bestIdx := 0
	for idx := 0; idx < 729; idx++ {
		dist := 0
		for j := 0; j < 12; j++ {
			if v[j] != golayTernaryCodewords[idx][j] {
				dist++
			}
		}
		if dist < bestDist {
			bestDist = dist
			bestIdx = idx
			if dist == 0 {
				break
			}
		}
	}
	return golayTernaryCodewords[bestIdx]
}

// GolayTernaryEncode encodes a 6-symbol GF(3) message to a 12-symbol codeword.
func GolayTernaryEncode(msg [6]int) [12]int {
	var cw [12]int
	for j := 0; j < 6; j++ {
		cw[j] = msg[j]
	}
	for j := 0; j < 6; j++ {
		sum := 0
		for i := 0; i < 6; i++ {
			sum += gf3Mul(msg[i], golayTernaryParity[i][j])
		}
		cw[6+j] = sum % 3
	}
	return cw
}

// --- Phase quantization ---

// TernaryPhaseQuantize maps continuous values to ternary {0, 1, 2} via phase angles.
func TernaryPhaseQuantize(x [CairnNumDims]float64) [12]int {
	var result [12]int
	third := 2 * math.Pi / 3
	for i := 0; i < 12; i++ {
		phase := x[i] * 2 * math.Pi
		phase = math.Mod(phase, 2*math.Pi)
		if phase < 0 {
			phase += 2 * math.Pi
		}
		q := math.Round(phase / third)
		result[i] = int(q) % 3
		if result[i] < 0 {
			result[i] += 3
		}
	}
	return result
}

// --- Gear 3 pipeline ---

// Gear3Result holds the output of the M12 Gear 3 pipeline.
type Gear3Result struct {
	Input       [CairnNumDims]float64
	Ternary     [12]int
	Codeword    [12]int
	HammingDist int
}

// ProjectToGear3 runs the M12 pipeline: 12D features → phase quantize → Golay decode.
func ProjectToGear3(features [CairnNumDims]float64) Gear3Result {
	ternary := TernaryPhaseQuantize(features)
	codeword := GolayTernaryDecode(ternary)

	dist := 0
	for i := 0; i < 12; i++ {
		if ternary[i] != codeword[i] {
			dist++
		}
	}

	return Gear3Result{
		Input:       features,
		Ternary:     ternary,
		Codeword:    codeword,
		HammingDist: dist,
	}
}

// Gear3Cluster groups cells by identical Golay codewords.
func Gear3Cluster(results []Gear3Result) map[[12]int][]int {
	clusters := make(map[[12]int][]int)
	for i, r := range results {
		clusters[r.Codeword] = append(clusters[r.Codeword], i)
	}
	return clusters
}
