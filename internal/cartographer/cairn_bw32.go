// cairn_bw32.go — Gear 6: Barnes-Wall BW₃₂ lattice quantization (32D).
// Ported from cairn experiment (bw32.go).
//
// BW₃₂ is decoded via Reed-Muller RM(1,5) parity correction.
// RM(1,5) is a [32,6,16] binary code with 2⁶ = 64 codewords.
// BW₃₂ has covering radius √8 ≈ 2.828.

package cartographer

import "math"

// --- Reed-Muller RM(1,5) [32,6,16] ---

var (
	rm15Generator [6][32]byte
	rm15Codewords [64][32]byte
)

func init() {
	// Build generator matrix
	for j := 0; j < 32; j++ {
		rm15Generator[0][j] = 1 // all-ones row
		for i := 1; i <= 5; i++ {
			rm15Generator[i][j] = byte((j >> (i - 1)) & 1)
		}
	}

	// Generate all 64 = 2⁶ codewords
	for msg := 0; msg < 64; msg++ {
		var cw [32]byte
		for i := 0; i < 6; i++ {
			if (msg>>i)&1 == 1 {
				for j := 0; j < 32; j++ {
					cw[j] ^= rm15Generator[i][j]
				}
			}
		}
		rm15Codewords[msg] = cw
	}
}

// RM15Decode finds the nearest RM(1,5) codeword by Hamming distance.
func RM15Decode(bits [32]byte) [32]byte {
	bestDist := 33
	bestIdx := 0
	for idx := 0; idx < 64; idx++ {
		dist := 0
		for j := 0; j < 32; j++ {
			if bits[j] != rm15Codewords[idx][j] {
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
	return rm15Codewords[bestIdx]
}

// --- BW₃₂ lattice decoder ---

// DecodeBW32 decodes a 32D continuous vector to the nearest BW₃₂ lattice point.
func DecodeBW32(x [32]float64) [32]float64 {
	var u [32]float64
	var p [32]byte

	for i := 0; i < 32; i++ {
		u[i] = math.Round(x[i])
	}

	for i := 0; i < 32; i++ {
		v := int(u[i])
		p[i] = byte(((v % 2) + 2) % 2)
	}

	c := RM15Decode(p)

	for i := 0; i < 32; i++ {
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

// BW32CoveringRadius is √8 ≈ 2.828
const BW32CoveringRadius = 2.8284271247461903

// BW32Distance computes the Euclidean distance from a vector to the nearest BW₃₂ point.
func BW32Distance(x [32]float64) float64 {
	bw := DecodeBW32(x)
	var sum float64
	for i := 0; i < 32; i++ {
		d := x[i] - bw[i]
		sum += d * d
	}
	return math.Sqrt(sum)
}

// --- Gear 6 pipeline ---

// Construct32D builds a 32D vector: [v24 | v8].
func Construct32D(v24 [24]float64, v8 [8]float64) [32]float64 {
	var v [32]float64
	for i := 0; i < 24; i++ {
		v[i] = v24[i]
	}
	for i := 0; i < 8; i++ {
		v[24+i] = v8[i]
	}
	return v
}

// Gear6Result holds the output of the BW₃₂ Gear 6 pipeline.
type Gear6Result struct {
	Input32   [32]float64
	BW32Point [32]float64
	Distance  float64
}

// ProjectToGear6 runs the BW₃₂ pipeline:
// 12D features → Gear 2 (8D) → scale → [x,x,-2x] (24D) → concat scaled 8D → BW₃₂ decode (32D)
func ProjectToGear6(features [CairnNumDims]float64, scale float64) Gear6Result {
	projected := ProjectToGear2(features)
	var scaled8 [8]float64
	for i := 0; i < 8; i++ {
		scaled8[i] = projected[i] * scale
	}

	raw24 := Construct24D(scaled8)
	raw32 := Construct32D(raw24, scaled8)

	bw32 := DecodeBW32(raw32)
	dist := 0.0
	for i := 0; i < 32; i++ {
		d := raw32[i] - bw32[i]
		dist += d * d
	}

	return Gear6Result{
		Input32:   raw32,
		BW32Point: bw32,
		Distance:  math.Sqrt(dist),
	}
}

// Gear6Cluster groups cells by identical BW₃₂ lattice points.
func Gear6Cluster(results []Gear6Result) map[[32]float64][]int {
	clusters := make(map[[32]float64][]int)
	for i, r := range results {
		clusters[r.BW32Point] = append(clusters[r.BW32Point], i)
	}
	return clusters
}
