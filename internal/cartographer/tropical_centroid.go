package cartographer

import (
	"fmt"
	"math"
)

// BuildCentroidDistanceMatrix computes a 5-fiber max-plus distance matrix
// between zone centroids. Uses the same tropical distance fibers as
// TropicalCartographer (spatial, visual, structural, semantic, frequency)
// but adapted to work on feature vectors + bounding boxes instead of elements.
//
// centroids: [K][24]float64 feature vectors (one per zone)
// bounds: [K][4]float64 zone bounding boxes [x, y, w, h] normalized
func BuildCentroidDistanceMatrix(centroids [][CairnNumDims]float64, bounds [][4]float64) [][]float64 {
	k := len(centroids)
	dist := make([][]float64, k)
	for i := range dist {
		dist[i] = make([]float64, k)
	}
	for i := 0; i < k; i++ {
		for j := i + 1; j < k; j++ {
			d := centroidTropicalDistance(centroids[i], centroids[j], bounds[i], bounds[j])
			dist[i][j] = d
			dist[j][i] = d
		}
	}
	return dist
}

// centroidTropicalDistance computes tropical distance between two zone centroids.
// d = max(d_spatial, d_visual, d_structural, d_semantic, d_frequency)
func centroidTropicalDistance(a, b [CairnNumDims]float64, boundsA, boundsB [4]float64) float64 {
	// Spatial: Euclidean distance of zone centers
	cxA := boundsA[0] + boundsA[2]/2
	cyA := boundsA[1] + boundsA[3]/2
	cxB := boundsB[0] + boundsB[2]/2
	cyB := boundsB[1] + boundsB[3]/2
	dx := cxA - cxB
	dy := cyA - cyB
	dSpatial := math.Sqrt(dx*dx+dy*dy) / math.Sqrt(2)

	// Visual: L2 on color features (dims 0-3: luminance, rgOpponent, byOpponent, sat)
	var dVisual float64
	for i := 0; i < 4; i++ {
		d := a[i] - b[i]
		dVisual += d * d
	}
	dVisual = math.Sqrt(dVisual) / 2 // normalize to ~[0,1]

	// Structural: L2 on semantic features (dims 12-18: area, depth, interact, etc.)
	var dStructural float64
	for i := 12; i < 19; i++ {
		d := a[i] - b[i]
		dStructural += d * d
	}
	dStructural = math.Sqrt(dStructural) / math.Sqrt(7)

	// Semantic: L2 on position + density features (dims 19-23)
	var dSemantic float64
	for i := 19; i < CairnNumDims; i++ {
		d := a[i] - b[i]
		dSemantic += d * d
	}
	dSemantic = math.Sqrt(dSemantic) / math.Sqrt(5)

	// Frequency: L2 on edge/spectral features (dims 4-11)
	var dFrequency float64
	for i := 4; i < 12; i++ {
		d := a[i] - b[i]
		dFrequency += d * d
	}
	dFrequency = math.Sqrt(dFrequency) / math.Sqrt(8)

	return math.Max(dSpatial, math.Max(dVisual, math.Max(dStructural, math.Max(dSemantic, dFrequency))))
}

// tropicalRefineZones runs tropical NJ on zone centroids and re-groups
// elements based on the NJ tree structure. Returns refined zones.
//
// zones: current zones from Gear 5
// elements: all parsed DOM elements
// cells: grid cells with feature vectors
// gridSize: grid dimension
func tropicalRefineZones(zones []zone, elements []element, cells []CairnGridCell, gridSize int) []zone {
	if len(zones) <= 2 {
		return zones // NJ needs at least 3 nodes
	}

	// Compute zone centroids from grid cell features
	stalks := computeZoneStalks(zones, elements, cells, gridSize)
	centroids := make([][CairnNumDims]float64, len(zones))
	bounds := make([][4]float64, len(zones))

	for i, stalk := range stalks {
		if len(stalk) == CairnNumDims {
			for d := 0; d < CairnNumDims; d++ {
				centroids[i][d] = stalk[d]
			}
		}
		// Compute zone bounds from element bounds
		bounds[i] = computeZoneBounds(zones[i], elements)
	}

	// Build tropical distance matrix on centroids (O(K^2), K = number of zones)
	dist := BuildCentroidDistanceMatrix(centroids, bounds)

	// Run NJ on centroids (O(K^3), K typically 5-20 => very fast)
	tree := neighborJoining(dist, len(zones))

	// Cut the NJ tree into new clusters
	minZ, maxZ := 3, 7
	njZones := cutTree(tree, convertZonesToPseudoElements(zones, elements), minZ, maxZ)

	if len(njZones) == 0 {
		return zones
	}

	// Map NJ clusters back to original elements
	return remapNJZonesToElements(njZones, zones, elements)
}

// convertZonesToPseudoElements creates pseudo-elements representing zone centroids
// for the NJ tree cutter. Each "element" carries the zone's center and bounds.
func convertZonesToPseudoElements(zones []zone, elements []element) []element {
	pseudo := make([]element, len(zones))
	for i, z := range zones {
		b := computeZoneBounds(z, elements)
		pseudo[i] = element{
			id:        fmt.Sprintf("zone-%d", i),
			hasBounds: true,
			bounds:    b,
			centerX:   b[0] + b[2]/2,
			centerY:   b[1] + b[3]/2,
		}
	}
	return pseudo
}

// remapNJZonesToElements maps NJ clusters (of zone indices) back to element indices.
func remapNJZonesToElements(njZones, origZones []zone, elements []element) []zone {
	result := make([]zone, len(njZones))
	for i, nj := range njZones {
		var allElems []int
		for _, zoneIdx := range nj.elems {
			if zoneIdx < len(origZones) {
				allElems = append(allElems, origZones[zoneIdx].elems...)
			}
		}
		result[i] = zone{
			elems: allElems,
		}
		if len(allElems) > 0 {
			result[i].rootIdx = allElems[0]
		}
		computeZoneFeatures(&result[i], elements)
	}

	sortZonesByPosition(result)
	return result
}
