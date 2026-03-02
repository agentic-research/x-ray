// cairn_leech.go — Golay [24,12,8] error correction and hexacode adelic quantization.
// Ported from cairn experiment (leech.go). Originally from hotsheaf/pkg/geometry/leech.go.

package cartographer

import (
	"math"
	"math/bits"
	"sync"
)

var (
	aRaw = [12][12]int{
		{0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1},
		{1, 1, 0, 1, 0, 0, 0, 1, 1, 1, 0, 1},
		{1, 1, 1, 0, 1, 0, 0, 0, 1, 1, 1, 0},
		{1, 0, 1, 1, 0, 1, 0, 0, 0, 1, 1, 1},
		{1, 1, 0, 1, 1, 0, 1, 0, 0, 0, 1, 1},
		{1, 1, 1, 0, 1, 1, 0, 1, 0, 0, 0, 1},
		{1, 1, 1, 1, 0, 1, 1, 0, 1, 0, 0, 0},
		{1, 0, 1, 1, 1, 0, 1, 1, 0, 1, 0, 0},
		{1, 0, 0, 1, 1, 1, 0, 1, 1, 0, 1, 0},
		{1, 0, 0, 0, 1, 1, 1, 0, 1, 1, 0, 1},
		{1, 1, 0, 0, 0, 1, 1, 1, 0, 1, 1, 0},
		{1, 0, 1, 0, 0, 0, 1, 1, 1, 0, 1, 1},
	}

	aPacked [12]uint16
	aCols   [12]uint16
	golayMu sync.Once
)

func initGolay() {
	golayMu.Do(func() {
		for i := 0; i < 12; i++ {
			var row, col uint16
			for j := 0; j < 12; j++ {
				if aRaw[i][j] == 1 {
					row |= (1 << j)
				}
				if aRaw[j][i] == 1 {
					col |= (1 << j)
				}
			}
			aPacked[i] = row
			aCols[i] = col
		}
	})
}

func matVecMul(v uint16) uint16 {
	var result uint16
	for i := 0; i < 12; i++ {
		if (v>>i)&1 == 1 {
			result ^= aPacked[i]
		}
	}
	return result
}

func matVecMulCols(v uint16) uint16 {
	var result uint16
	for i := 0; i < 12; i++ {
		if (v>>i)&1 == 1 {
			result ^= aCols[i]
		}
	}
	return result
}

// DecodeGolay24 takes a 24-bit noisy vector and snaps it to the nearest valid Golay codeword.
// Corrects up to 3 bit errors. Uses syndrome decoding (fast path, not brute-force).
func DecodeGolay24(input [24]byte) [24]byte {
	initGolay()

	var x, y uint16
	for i := 0; i < 12; i++ {
		if input[i] == 1 {
			x |= (1 << i)
		}
		if input[i+12] == 1 {
			y |= (1 << i)
		}
	}

	var ex, ey uint16
	found := false

	s := matVecMul(x) ^ y

	if bits.OnesCount16(s) <= 3 {
		ex = 0
		ey = s
		found = true
	} else {
		for i := 0; i < 12; i++ {
			if bits.OnesCount16(s^aPacked[i]) <= 2 {
				ex = (1 << i)
				ey = s ^ aPacked[i]
				found = true
				break
			}
		}
	}

	if !found {
		sPrime := matVecMulCols(y) ^ x

		if bits.OnesCount16(sPrime) <= 3 {
			ex = sPrime
			ey = 0
		} else {
			for i := 0; i < 12; i++ {
				if bits.OnesCount16(sPrime^aCols[i]) <= 2 {
					ex = sPrime ^ aCols[i]
					ey = (1 << i)
					break
				}
			}
		}
	}

	cx := x ^ ex
	cy := y ^ ey

	var output [24]byte
	for i := 0; i < 12; i++ {
		output[i] = byte((cx >> i) & 1)
		output[i+12] = byte((cy >> i) & 1)
	}

	return output
}

// --- Hexacode Adelic Quantization ---

var (
	hexMulTable = [4][4]int{
		{0, 0, 0, 0},
		{0, 1, 2, 3},
		{0, 2, 3, 1},
		{0, 3, 1, 2},
	}

	allHexCodewords [64][6]uint8
	rootToGf4       = [6]int{1, 3, 2, 1, 3, 2}
	gf4ToRoots      = [4][2]int{
		{-1, -1},
		{0, 3},
		{2, 5},
		{1, 4},
	}

	hexMu sync.Once
)

func gf4Add(a, b int) int { return a ^ b }
func gf4Mul(a, b int) int { return hexMulTable[a][b] }

func initHexacode() {
	hexMu.Do(func() {
		idx := 0
		for m0 := 0; m0 < 4; m0++ {
			for m1 := 0; m1 < 4; m1++ {
				for m2 := 0; m2 < 4; m2++ {
					allHexCodewords[idx][0] = uint8(m0)
					allHexCodewords[idx][1] = uint8(m1)
					allHexCodewords[idx][2] = uint8(m2)
					allHexCodewords[idx][3] = uint8(gf4Add(gf4Add(m0, m1), m2))
					allHexCodewords[idx][4] = uint8(gf4Add(gf4Add(m0, gf4Mul(2, m1)), gf4Mul(3, m2)))
					allHexCodewords[idx][5] = uint8(gf4Add(gf4Add(m0, gf4Mul(3, m1)), gf4Mul(2, m2)))
					idx++
				}
			}
		}
	})
}

func circularDist(a, b int) int {
	d := a - b
	if d < 0 {
		d = -d
	}
	if 6-d < d {
		return 6 - d
	}
	return d
}

func decodeRootIndex(inputK, targetClass int) uint8 {
	inputClass := rootToGf4[inputK]
	if targetClass == inputClass || targetClass == 0 {
		return uint8(inputK)
	}
	r1 := gf4ToRoots[targetClass][0]
	r2 := gf4ToRoots[targetClass][1]
	if circularDist(inputK, r1) <= circularDist(inputK, r2) {
		return uint8(r1)
	}
	return uint8(r2)
}

func findNearestCodeword(u [6]uint8) int {
	minDist := 7
	bestCodeIdx := 0
	for cIdx := 0; cIdx < 64; cIdx++ {
		dist := 0
		for i := 0; i < 6; i++ {
			if allHexCodewords[cIdx][i] != u[i] {
				dist++
			}
		}
		if dist < minDist {
			minDist = dist
			bestCodeIdx = cIdx
			if dist == 0 {
				break
			}
		}
	}
	return bestCodeIdx
}

// AdelicPhaseQuantizeInt16 snaps 6 root indices (values 0-5) to nearest valid hexacode.
func AdelicPhaseQuantizeInt16(input [6]int16) [6]uint8 {
	initHexacode()
	var u [6]uint8
	var inputK [6]int
	for i := 0; i < 6; i++ {
		k := int(input[i])
		k = ((k % 6) + 6) % 6
		inputK[i] = k
		u[i] = uint8(rootToGf4[k])
	}
	bestCodeIdx := findNearestCodeword(u)
	var output [6]uint8
	for i := 0; i < 6; i++ {
		output[i] = decodeRootIndex(inputK[i], int(allHexCodewords[bestCodeIdx][i]))
	}
	return output
}

// AdelicPhaseQuantizeFloat snaps 6 continuous float phases via the hexacode lattice.
func AdelicPhaseQuantizeFloat(phases [6]float64) [6]float64 {
	initHexacode()
	pi3 := math.Pi / 3.0
	var u [6]uint8
	var bestK [6]int
	for i := 0; i < 6; i++ {
		pNorm := phases[i] / pi3
		k := int(math.Round(pNorm))
		k = ((k % 6) + 6) % 6
		bestK[i] = k
		u[i] = uint8(rootToGf4[k])
	}
	bestCodeIdx := findNearestCodeword(u)
	var output [6]float64
	for i := 0; i < 6; i++ {
		outK := decodeRootIndex(bestK[i], int(allHexCodewords[bestCodeIdx][i]))
		output[i] = float64(outK) * pi3
	}
	return output
}
