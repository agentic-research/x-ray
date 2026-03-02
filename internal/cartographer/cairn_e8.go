// cairn_e8.go — D8 and E8 lattice quantization.
// Ported from cairn experiment (e8.go). Originally from ley-line (Rust) leech.rs.
//
// E8 = D8 ∪ (D8 + ½⁸)
// D8 = { x ∈ Z⁸ | Σxᵢ is even }
//
// E8 has 240 kissing neighbors and covering radius √2/2 ≈ 0.707.

package cartographer

import "math"

// QuantizeD8 snaps an 8D continuous vector to the nearest D8 lattice point.
// D8 requires all coordinates to be integers with even sum.
func QuantizeD8(x [8]float64) [8]float64 {
	var g [8]float64
	var sum float64
	for i := 0; i < 8; i++ {
		g[i] = math.Round(x[i])
		sum += g[i]
	}

	// Check parity: D8 requires even sum
	parity := int(math.Round(sum)) % 2
	if parity < 0 {
		parity = -parity
	}
	if parity == 0 {
		return g
	}

	// Fix: find the coordinate with largest rounding error and flip it
	maxErr := 0.0
	maxIdx := 0
	for i := 0; i < 8; i++ {
		err := math.Abs(x[i] - g[i])
		if err > maxErr {
			maxErr = err
			maxIdx = i
		}
	}

	// Flip in the direction of the original value
	if x[maxIdx] >= g[maxIdx] {
		g[maxIdx] += 1
	} else {
		g[maxIdx] -= 1
	}
	return g
}

// QuantizeE8 snaps an 8D continuous vector to the nearest E8 lattice point.
// E8 = D8 ∪ (D8 + (½,½,...,½)). We try both cosets and pick the closest.
func QuantizeE8(x [8]float64) [8]float64 {
	// Candidate 1: D8 coset (integer coordinates, even sum)
	y1 := QuantizeD8(x)
	d1 := distSq8(x, y1)

	// Candidate 2: D8 + ½ coset (half-integer coordinates, even sum)
	var xShifted [8]float64
	for i := 0; i < 8; i++ {
		xShifted[i] = x[i] - 0.5
	}
	y2Shifted := QuantizeD8(xShifted)
	var y2 [8]float64
	for i := 0; i < 8; i++ {
		y2[i] = y2Shifted[i] + 0.5
	}
	d2 := distSq8(x, y2)

	if d2 < d1 {
		return y2
	}
	return y1
}

// E8Distance computes the Euclidean distance from a vector to its nearest E8 point.
func E8Distance(x [8]float64) float64 {
	e8 := QuantizeE8(x)
	return math.Sqrt(distSq8(x, e8))
}

// E8CoveringRadius is √2/2 ≈ 0.707
const E8CoveringRadius = 0.7071067811865476 // math.Sqrt(2) / 2

func distSq8(a, b [8]float64) float64 {
	var sum float64
	for i := 0; i < 8; i++ {
		d := a[i] - b[i]
		sum += d * d
	}
	return sum
}

// IsEvenSum checks if the sum of coordinates rounds to an even integer.
func IsEvenSum(x [8]float64) bool {
	var sum float64
	for i := 0; i < 8; i++ {
		sum += x[i]
	}
	p := int(math.Round(sum)) % 2
	if p < 0 {
		p = -p
	}
	return p == 0
}
