// cairn_turyn.go — Turyn construction for Leech lattice decoding.
// Ported from cairn experiment (turyn.go). Originally from ley-line (Rust) leech.rs.
//
// The Leech lattice Λ₂₄ is decoded via two cosets:
//   - Integer coset: v ∈ Z²⁴ where (v mod 2) is a Golay codeword
//   - Half-integer coset: v ∈ (Z+½)²⁴ where ((v-½) mod 2) is a Golay codeword
//
// The decoder tries both and picks the closest.

package cartographer

import "math"

// golayGenerator is the [I₁₂ | A] systematic generator matrix for G24.
// Row i encodes message bit i. The codeword is (message, parity).
// A is the same matrix used in cairn_leech.go's syndrome decoder.
var golayGenerator [12][24]byte

// golayCodewords stores all 4096 codewords for brute-force decoding.
var golayCodewords [4096][24]byte

func init() {
	// Build generator matrix G = [I₁₂ | A]
	for i := 0; i < 12; i++ {
		// Identity part
		golayGenerator[i][i] = 1
		// Parity part from aRaw
		for j := 0; j < 12; j++ {
			if aRaw[i][j] == 1 {
				golayGenerator[i][12+j] = 1
			}
		}
	}

	// Generate all 4096 codewords
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
// This is the ley-line approach: simple, correct, O(4096 × 24).
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

// DecodeIntegerCoset decodes a 24D continuous vector to the nearest
// Leech lattice point in the integer coset.
func DecodeIntegerCoset(x [24]float64) [24]float64 {
	var u [24]float64
	var p [24]byte

	// Step 1: Round
	for i := 0; i < 24; i++ {
		u[i] = math.Round(x[i])
	}

	// Step 2: Extract parity bits
	for i := 0; i < 24; i++ {
		v := int(u[i])
		p[i] = byte(((v % 2) + 2) % 2) // ensure non-negative mod
	}

	// Step 3: Correct parity via Golay
	c := DecodeGolay24(p)

	// Step 4: Where parity differs, adjust the integer by ±1
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

// DecodeHalfCoset decodes a 24D continuous vector to the nearest
// Leech lattice point in the half-integer coset.
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

// DecodeLeechTuryn decodes a 24D continuous vector to the nearest
// Leech lattice point using the Turyn construction.
// Tries both integer and half-integer cosets, picks the closer one.
func DecodeLeechTuryn(x [24]float64) [24]float64 {
	candInt := DecodeIntegerCoset(x)
	candHalf := DecodeHalfCoset(x)

	distInt := distSq24(x, candInt)
	distHalf := distSq24(x, candHalf)

	if distHalf < distInt {
		return candHalf
	}
	return candInt
}

func distSq24(a, b [24]float64) float64 {
	var sum float64
	for i := 0; i < 24; i++ {
		d := a[i] - b[i]
		sum += d * d
	}
	return sum
}

// LeechCoveringRadius is √2 ≈ 1.414
const LeechCoveringRadius = 1.4142135623730951
