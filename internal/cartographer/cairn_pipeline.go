// cairn_pipeline.go — 24D native Leech lattice projection.
// Ported from cairn experiment (pipeline.go).

package cartographer

import "math"

// Construct24D builds a 24D vector from an 8D point using the zero-sum construction.
// Used by legacy components (e.g. BW32).
func Construct24D(e8 [8]float64) [24]float64 {
	var leech [24]float64
	for i := 0; i < 8; i++ {
		leech[i] = e8[i]
		leech[i+8] = e8[i]
		leech[i+16] = -2 * e8[i]
	}
	return leech
}

// VerifyZeroSum checks the zero-sum property: sum of all 24 coordinates should be 0.
func VerifyZeroSum(v [24]float64) float64 {
	var sum float64
	for _, x := range v {
		sum += x
	}
	return math.Abs(sum)
}

// ProjectToGear2 performs a semantic 24D→8D down-projection for Gear 2 (E8).
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

// ProjectToLeech scales the native 24D vector and decodes it using the Leech lattice.
func ProjectToLeech(features [CairnNumDims]float64, scale float64) LeechResult {
	// Step 1: Scale features to Leech lattice range
	var scaled [24]float64
	for i := 0; i < 24; i++ {
		scaled[i] = features[i] * scale
	}

	// Step 2: Turyn error correction (closest point in Lambda 24)
	leech := DecodeLeechTuryn(scaled)
	leechDist := math.Sqrt(distSq24(scaled, leech))

	return LeechResult{
		Input:       features,
		Raw24D:      scaled,
		LeechPoint:  leech,
		LeechDist:   leechDist,
		SchismLeech: leechDist > LeechCoveringRadius,
	}
}

// LeechResult holds the output of the full projection pipeline.
type LeechResult struct {
	Input       [CairnNumDims]float64
	Raw24D      [24]float64
	LeechPoint  [24]float64
	LeechDist   float64 // distance from scaled input to Leech point
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
