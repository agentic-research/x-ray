// cairn_pipeline.go — Full 8D → E8 → [x,x,-2x] → 24D → Turyn pipeline.
// Ported from cairn experiment (pipeline.go).

package cartographer

import "math"

// Construct24D builds a 24D vector from an 8D E8 point using the zero-sum construction.
// leech[0:8] = x, leech[8:16] = x, leech[16:24] = -2x
// Property: sum of all 24 coordinates = 0 (zero-sum).
func Construct24D(e8 [8]float64) [24]float64 {
	var leech [24]float64
	for i := 0; i < 8; i++ {
		leech[i] = e8[i]
		leech[i+8] = e8[i]
		leech[i+16] = -2 * e8[i]
	}
	return leech
}

// ProjectToGear2 performs the semantic 12D→8D down-projection for Gear 2 (E8).
// Selects the 8 dimensions most relevant for E8 lattice quantization:
//
//	[luminance, rgOpponent, byOpponent, saturation, edgeDensity, dirVariance, contrast, entropy]
func ProjectToGear2(features [CairnNumDims]float64) [8]float64 {
	return [8]float64{
		features[0],  // luminance
		features[1],  // rgOpponent
		features[2],  // byOpponent
		features[3],  // saturation
		features[4],  // edgeDensity
		features[8],  // dirVariance
		features[9],  // contrast
		features[11], // entropy
	}
}

// ProjectToLeech runs the full pipeline: 12D features → 8D projection → E8 snap → 24D → Turyn decode.
func ProjectToLeech(features [CairnNumDims]float64, scale float64) LeechResult {
	// Step 0: Project 12D → 8D for E8
	projected := ProjectToGear2(features)

	// Step 1: Scale features to E8 lattice range
	var scaled [8]float64
	for i := 0; i < 8; i++ {
		scaled[i] = projected[i] * scale
	}

	// Step 2: Snap to E8 lattice
	e8 := QuantizeE8(scaled)
	e8Dist := math.Sqrt(distSq8(scaled, e8))

	// Step 3: Zero-sum construction → 24D
	raw24 := Construct24D(e8)

	// Step 4: Turyn error correction
	leech := DecodeLeechTuryn(raw24)
	leechDist := math.Sqrt(distSq24(raw24, leech))

	return LeechResult{
		Input:       features,
		E8Point:     e8,
		Raw24D:      raw24,
		LeechPoint:  leech,
		E8Dist:      e8Dist,
		LeechDist:   leechDist,
		SchismE8:    e8Dist > E8CoveringRadius,
		SchismLeech: leechDist > LeechCoveringRadius,
	}
}

// LeechResult holds the output of the full projection pipeline.
type LeechResult struct {
	Input       [CairnNumDims]float64
	E8Point     [8]float64
	Raw24D      [24]float64
	LeechPoint  [24]float64
	E8Dist      float64 // distance from scaled input to E8 point
	LeechDist   float64 // distance from raw24D to Leech point
	SchismE8    bool    // true if outside E8 covering radius
	SchismLeech bool    // true if outside Leech covering radius
}

// LeechDistance computes Euclidean distance between two Leech lattice points.
func LeechDistance(a, b [24]float64) float64 {
	return math.Sqrt(distSq24(a, b))
}

// LeechCluster groups cells by identical Leech lattice points.
func LeechCluster(results []LeechResult) map[[24]float64][]int {
	clusters := make(map[[24]float64][]int)
	for i, r := range results {
		clusters[r.LeechPoint] = append(clusters[r.LeechPoint], i)
	}
	return clusters
}

// VerifyZeroSum checks the zero-sum property: sum of all 24 coordinates should be 0.
func VerifyZeroSum(v [24]float64) float64 {
	var sum float64
	for _, x := range v {
		sum += x
	}
	return math.Abs(sum)
}
