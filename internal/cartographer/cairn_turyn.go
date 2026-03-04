// cairn_turyn.go — Leech lattice CVP decoder via Construction A.
//
// The Leech lattice Λ₂₄ at standard normalization (min norm 2, covering radius √2)
// is (1/√8) · L, where L ⊂ Z²⁴ satisfies three constraints:
//
//   (C1) Parity homogeneity: all coordinates same parity (all even or all odd).
//   (C2) Golay constraint: the mod-2 "half-parity" bits form a C₂₄ codeword.
//        Even family: bit_i = (w_i/2) mod 2       (is w_i ≡ 2 mod 4?)
//        Odd family:  bit_i = ((w_i-1)/2) mod 2   (is w_i ≡ 3 mod 4?)
//   (C3) Sum constraint:
//        Even family: Σw_i ≡ 0 (mod 8)
//        Odd family:  Σw_i ≡ 4 (mod 8)
//
// Decoder: scale input by √8, try both parity families with soft-decision
// Golay decoding (±2 corrections) + sum enforcement, pick closest, unscale.

package cartographer

import "math"

// golayGenerator is the [I₁₂ | A] systematic generator matrix for G24.
var golayGenerator [12][24]byte

// golayCodewords stores all 4096 codewords for brute-force decoding.
var golayCodewords [4096][24]byte

func init() {
	for i := 0; i < 12; i++ {
		golayGenerator[i][i] = 1
		for j := 0; j < 12; j++ {
			if aRaw[i][j] == 1 {
				golayGenerator[i][12+j] = 1
			}
		}
	}
	for msg := uint16(0); msg < 4096; msg++ {
		var cw [24]byte
		for i := 0; i < 12; i++ {
			if (msg>>i)&1 == 1 {
				for j := 0; j < 24; j++ {
					cw[j] ^= golayGenerator[i][j]
				}
			}
		}
		golayCodewords[msg] = cw
	}
}

// GolayDecodeBruteForce finds the nearest Golay codeword by Hamming distance.
// O(4096 × 24). Retained for tests.
func GolayDecodeBruteForce(bits [24]byte) [24]byte {
	bestDist := 25
	bestIdx := 0
	for idx := 0; idx < 4096; idx++ {
		dist := 0
		for j := 0; j < 24; j++ {
			if bits[j] != golayCodewords[idx][j] {
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
	return golayCodewords[bestIdx]
}

// SoftDecodeGolay24 finds the Golay codeword minimizing total Euclidean flip
// penalty. For each codeword c, penalty = Σ penalties[i] where c[i] ≠ parities[i].
// O(4096 × 24).
func SoftDecodeGolay24(parities [24]byte, penalties [24]float64) [24]byte {
	bestPenalty := math.Inf(1)
	bestIdx := 0
	for idx := 0; idx < 4096; idx++ {
		var penalty float64
		for j := 0; j < 24; j++ {
			if parities[j] != golayCodewords[idx][j] {
				penalty += penalties[j]
			}
		}
		if penalty < bestPenalty {
			bestPenalty = penalty
			bestIdx = idx
			if penalty == 0 {
				break
			}
		}
	}
	return golayCodewords[bestIdx]
}

var sqrt8 = math.Sqrt(8.0)

// DecodeLeechConstructionA decodes a 24D vector to the nearest Leech lattice
// point using the mathematically correct Construction A algorithm.
// Scale by √8, decode both parity families, enforce Golay + sum constraints.
// NOTE: produces different lattice points than the legacy decoder. Use for
// verification and future work; the pipeline currently uses DecodeLeechTuryn.
func DecodeLeechConstructionA(x [24]float64) [24]float64 {
	// Scale to integer lattice.
	var t [24]float64
	for i := 0; i < 24; i++ {
		t[i] = x[i] * sqrt8
	}

	// Try both parity families.
	evenResult, evenDist := decodeFamily(t, true)
	oddResult, oddDist := decodeFamily(t, false)

	var best [24]float64
	if evenDist <= oddDist {
		best = evenResult
	} else {
		best = oddResult
	}

	// Unscale back to standard normalization.
	var result [24]float64
	for i := 0; i < 24; i++ {
		result[i] = best[i] / sqrt8
	}
	return result
}

// decodeFamily decodes within the all-even or all-odd parity family of L.
// Returns the decoded point (scaled coordinates) and squared distance from t.
func decodeFamily(t [24]float64, even bool) ([24]float64, float64) {
	var f [24]float64     // rounded coordinates (all same parity)
	var delta [24]float64 // residuals: t[i] - f[i], in range (-1, 1]

	// Step A: Round to nearest same-parity integer.
	if even {
		for i := 0; i < 24; i++ {
			f[i] = 2 * math.Round(t[i]/2) // nearest even
			delta[i] = t[i] - f[i]
		}
	} else {
		for i := 0; i < 24; i++ {
			f[i] = 2*math.Round((t[i]-1)/2) + 1 // nearest odd
			delta[i] = t[i] - f[i]
		}
	}

	// Step B: Extract Golay bits.
	// Even: bit_i = (f_i/2) mod 2  — is f_i ≡ 2 (mod 4)?
	// Odd:  bit_i = ((f_i-1)/2) mod 2 — is f_i ≡ 3 (mod 4)?
	var bits [24]byte
	for i := 0; i < 24; i++ {
		fi := int(math.Round(f[i]))
		var half int
		if even {
			half = fi / 2
		} else {
			half = (fi - 1) / 2
		}
		bits[i] = byte(((half % 2) + 2) % 2)
	}

	// Step C: Compute soft-decision penalties.
	// Flipping bit_i means changing f[i] by ±2 (toward t[i]).
	// |delta[i]| ≤ 1, so the flip overshoots: new |delta| = 2 - |delta|.
	var penalties [24]float64
	for i := 0; i < 24; i++ {
		var newDelta float64
		if delta[i] >= 0 {
			newDelta = delta[i] - 2
		} else {
			newDelta = delta[i] + 2
		}
		penalties[i] = newDelta*newDelta - delta[i]*delta[i]
		// = 4 - 4·|delta[i]|, always ≥ 0
	}

	// Step D: Soft-decision Golay decode.
	c := SoftDecodeGolay24(bits, penalties)

	// Step E: Apply Golay corrections (±2).
	for i := 0; i < 24; i++ {
		if bits[i] != c[i] {
			if delta[i] >= 0 {
				f[i] += 2
			} else {
				f[i] -= 2
			}
			delta[i] = t[i] - f[i]
		}
	}

	// Step F: Enforce sum constraint.
	// Even family: sum(f) ≡ 0 (mod 8). Odd family: sum(f) ≡ 4 (mod 8).
	targetMod8 := 0
	if !even {
		targetMod8 = 4
	}

	sumF := 0
	for i := 0; i < 24; i++ {
		sumF += int(math.Round(f[i]))
	}
	residue := ((sumF % 8) + 8) % 8

	if residue != targetMod8 {
		// Difference is 4 (Golay code is doubly-even → sum changes by multiples of 4).
		// Fix by adjusting one coordinate by ±4, choosing cheapest.
		bestCost := math.Inf(1)
		bestIdx := 0
		bestAdj := 0.0

		for i := 0; i < 24; i++ {
			costPlus := (delta[i]-4)*(delta[i]-4) - delta[i]*delta[i]
			costMinus := (delta[i]+4)*(delta[i]+4) - delta[i]*delta[i]
			if costPlus < bestCost {
				bestCost = costPlus
				bestIdx = i
				bestAdj = 4.0
			}
			if costMinus < bestCost {
				bestCost = costMinus
				bestIdx = i
				bestAdj = -4.0
			}
		}
		f[bestIdx] += bestAdj
	}

	// Step G: Compute squared distance in scaled coordinates.
	distSq := 0.0
	for i := 0; i < 24; i++ {
		d := t[i] - f[i]
		distSq += d * d
	}

	return f, distSq
}

// DecodeLeechTuryn decodes via integer + half-integer cosets, picking closest.
// This is the original decoder used by the pipeline. Not mathematically perfect
// (see DecodeLeechConstructionA) but zone clustering is calibrated to it.
func DecodeLeechTuryn(x [24]float64) [24]float64 {
	candInt := DecodeIntegerCoset(x)
	candHalf := DecodeHalfCoset(x)
	if distSq24(x, candHalf) < distSq24(x, candInt) {
		return candHalf
	}
	return candInt
}

// DecodeIntegerCoset decodes to nearest integer-coset Leech point.
func DecodeIntegerCoset(x [24]float64) [24]float64 {
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

// DecodeHalfCoset is retained for legacy callers.
func DecodeHalfCoset(x [24]float64) [24]float64 {
	var shifted [24]float64
	for i := 0; i < 24; i++ {
		shifted[i] = x[i] - 0.5
	}
	decoded := DecodeIntegerCoset(shifted)
	for i := 0; i < 24; i++ {
		decoded[i] += 0.5
	}
	return decoded
}

func distSq24(a, b [24]float64) float64 {
	var sum float64
	for i := 0; i < 24; i++ {
		d := a[i] - b[i]
		sum += d * d
	}
	return sum
}

// LeechCoveringRadius is √2 ≈ 1.414 (standard normalization).
const LeechCoveringRadius = 1.4142135623730951

// VerifyLeechPoint checks that v is a valid Leech lattice point by verifying
// all three constraints (C1, C2, C3) on the scaled vector w = √8·v.
func VerifyLeechPoint(v [24]float64) (valid bool, reason string) {
	var w [24]int
	for i := 0; i < 24; i++ {
		w[i] = int(math.Round(v[i] * sqrt8))
		// Check that scaling produces near-integer values.
		if math.Abs(v[i]*sqrt8-float64(w[i])) > 0.01 {
			return false, "non-integer in scaled system"
		}
	}

	// C1: All same parity.
	p0 := ((w[0] % 2) + 2) % 2
	for i := 1; i < 24; i++ {
		pi := ((w[i] % 2) + 2) % 2
		if pi != p0 {
			return false, "mixed parity"
		}
	}

	even := p0 == 0

	// C2: Golay constraint.
	var bits [24]byte
	for i := 0; i < 24; i++ {
		var half int
		if even {
			half = w[i] / 2
		} else {
			half = (w[i] - 1) / 2
		}
		bits[i] = byte(((half % 2) + 2) % 2)
	}
	// Check if bits is a valid Golay codeword.
	decoded := GolayDecodeBruteForce(bits)
	if decoded != bits {
		return false, "Golay violation"
	}

	// C3: Sum constraint.
	sumW := 0
	for i := 0; i < 24; i++ {
		sumW += w[i]
	}
	if even {
		if ((sumW%8)+8)%8 != 0 {
			return false, "sum ≢ 0 mod 8 (even family)"
		}
	} else {
		if ((sumW%8)+8)%8 != 4 {
			return false, "sum ≢ 4 mod 8 (odd family)"
		}
	}

	return true, ""
}
