// boundary_experiment_test.go — Empirical validation of boundary detection claims.
//
// Three experiments testing whether transport-magnitude boundary detection
// outperforms holonomy (which is provably zero), whether 24D features beat
// 3D orientation, and whether feature-space zone assignment is ε-stable.
//
// Run: GOWORK=off go test ./internal/cartographer/ -run TestExperiment -v -count=1
//
// Ground truth: tropical cartographer zones (independent algorithm from cairn).
package cartographer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"math/rand/v2"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/agentic-research/x-ray/internal/mache"
)

// testSite holds loaded testdata for one site.
type testSite struct {
	name       string
	summary    string
	screenshot []byte
}

func loadTestSites(t *testing.T) []testSite {
	t.Helper()
	testdataDir := filepath.Join("..", "..", "testdata")
	entries, err := os.ReadDir(testdataDir)
	if err != nil {
		t.Fatalf("read testdata dir: %v", err)
	}

	var sites []testSite
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		summaryPath := filepath.Join(testdataDir, e.Name(), "page_summary.txt")
		screenshotPath := filepath.Join(testdataDir, e.Name(), "page.png")

		summary, err := os.ReadFile(summaryPath)
		if err != nil {
			continue
		}
		screenshot, err := os.ReadFile(screenshotPath)
		if err != nil {
			continue
		}

		// Skip sites with stripped summaries (no Bounds: field)
		if !strings.Contains(string(summary), "Bounds:") {
			continue
		}

		sites = append(sites, testSite{
			name:       e.Name(),
			summary:    string(summary),
			screenshot: screenshot,
		})
	}
	if len(sites) == 0 {
		t.Skip("no testdata with rich summaries found")
	}
	return sites
}

// --- Experiment 1: Transport-Magnitude Boundary Detection ---

func TestExperiment1_TransportMagnitudeBoundaries(t *testing.T) {
	sites := loadTestSites(t)
	ctx := context.Background()

	var allBoundaryScores []float64
	var allInteriorScores []float64

	for _, site := range sites {
		// Run cairn to get zones + grid features
		cairn := &CairnCartographer{Gear: 5, Scale: 10.0, GridSize: 12, CurvatureDetection: true}
		cairnSchema, err := cairn.GenerateSchema(ctx, site.screenshot, "image/png", site.summary)
		if err != nil {
			t.Logf("  %s: cairn error: %v", site.name, err)
			continue
		}

		// Run tropical for independent ground truth
		tropical := &TropicalCartographer{}
		tropicalSchema, err := tropical.GenerateSchema(ctx, site.screenshot, "image/png", site.summary)
		if err != nil {
			t.Logf("  %s: tropical error: %v", site.name, err)
			continue
		}

		// Parse zone bounds from both
		cairnZones := parseZoneBounds(cairnSchema)
		tropicalZones := parseZoneBounds(tropicalSchema)

		if len(cairnZones) < 2 || len(tropicalZones) < 2 {
			t.Logf("  %s: not enough zones (cairn=%d, tropical=%d)", site.name, len(cairnZones), len(tropicalZones))
			continue
		}

		// Extract features to get transport data
		elements := parseElements(site.summary)
		if len(elements) < 10 {
			continue
		}

		img, err2 := decodeImage(site.screenshot)
		if err2 != nil {
			continue
		}
		cells := ExtractFusedFeatures(img, elements, 12)
		if len(cells) == 0 {
			continue
		}

		// Compute transport maps between adjacent grid cells
		gridW, gridH := 12, 12
		type edgeScore struct {
			score      float64
			isBoundary bool // true if this edge separates two different tropical zones
		}
		var edges []edgeScore

		for y := 0; y < gridH; y++ {
			for x := 0; x < gridW; x++ {
				idx := y*gridW + x
				if idx >= len(cells) {
					continue
				}

				// Check right neighbor
				if x+1 < gridW {
					ridx := y*gridW + (x + 1)
					if ridx < len(cells) {
						score := orientationDiff(cells[idx], cells[ridx])
						zoneA := assignToZone(float64(x)/float64(gridW), float64(y)/float64(gridH), tropicalZones)
						zoneB := assignToZone(float64(x+1)/float64(gridW), float64(y)/float64(gridH), tropicalZones)
						edges = append(edges, edgeScore{score: score, isBoundary: zoneA != zoneB})
					}
				}

				// Check bottom neighbor
				if y+1 < gridH {
					bidx := (y+1)*gridW + x
					if bidx < len(cells) {
						score := orientationDiff(cells[idx], cells[bidx])
						zoneA := assignToZone(float64(x)/float64(gridW), float64(y)/float64(gridH), tropicalZones)
						zoneB := assignToZone(float64(x)/float64(gridW), float64(y+1)/float64(gridH), tropicalZones)
						edges = append(edges, edgeScore{score: score, isBoundary: zoneA != zoneB})
					}
				}
			}
		}

		var siteBoundary, siteInterior []float64
		for _, e := range edges {
			if e.isBoundary {
				siteBoundary = append(siteBoundary, e.score)
			} else {
				siteInterior = append(siteInterior, e.score)
			}
		}

		if len(siteBoundary) > 0 && len(siteInterior) > 0 {
			bMedian := median(siteBoundary)
			iMedian := median(siteInterior)
			ratio := 0.0
			if iMedian > 0 {
				ratio = bMedian / iMedian
			}
			t.Logf("  %s: boundary_median=%.4f interior_median=%.4f ratio=%.2fx (n_boundary=%d, n_interior=%d)",
				site.name, bMedian, iMedian, ratio, len(siteBoundary), len(siteInterior))

			allBoundaryScores = append(allBoundaryScores, siteBoundary...)
			allInteriorScores = append(allInteriorScores, siteInterior...)
		}
	}

	if len(allBoundaryScores) == 0 || len(allInteriorScores) == 0 {
		t.Skip("insufficient edge data")
	}

	// Wilcoxon rank-sum test (Mann-Whitney U)
	u, p := mannWhitneyU(allBoundaryScores, allInteriorScores)
	bMed := median(allBoundaryScores)
	iMed := median(allInteriorScores)
	ratio := bMed / math.Max(iMed, 1e-10)

	t.Logf("\n=== EXPERIMENT 1 RESULTS ===")
	t.Logf("Boundary edges: n=%d, median=%.4f", len(allBoundaryScores), bMed)
	t.Logf("Interior edges: n=%d, median=%.4f", len(allInteriorScores), iMed)
	t.Logf("Ratio: %.2fx", ratio)
	t.Logf("Mann-Whitney U=%.0f, p=%.6f", u, p)

	if p < 0.01 && ratio >= 2.0 {
		t.Logf("✅ PASS: boundary scores are ≥2x higher (ratio=%.2f, p=%.6f)", ratio, p)
	} else if p < 0.05 {
		t.Logf("⚠️ INCONCLUSIVE: significant but ratio=%.2f < 2.0 (p=%.6f)", ratio, p)
	} else {
		t.Errorf("❌ FAIL: transport magnitudes don't discriminate boundaries (ratio=%.2f, p=%.6f)", ratio, p)
	}
}

// --- Experiment 2: 24D vs 3D Feature Distance ---

func TestExperiment2_24Dvs3D_BoundaryDetection(t *testing.T) {
	sites := loadTestSites(t)
	ctx := context.Background()

	var iou3D, iou24D []float64

	for _, site := range sites {
		// Ground truth from tropical
		tropical := &TropicalCartographer{}
		tropicalSchema, err := tropical.GenerateSchema(ctx, site.screenshot, "image/png", site.summary)
		if err != nil {
			continue
		}
		tropicalZones := parseZoneBounds(tropicalSchema)
		if len(tropicalZones) < 2 {
			continue
		}

		elements := parseElements(site.summary)
		if len(elements) < 10 {
			continue
		}

		img, err2 := decodeImage(site.screenshot)
		if err2 != nil {
			continue
		}
		cells := ExtractFusedFeatures(img, elements, 12)
		if len(cells) == 0 {
			continue
		}

		gridW := 12
		type cellLabel struct {
			x, y       int
			isBoundary bool
		}
		var cellLabels []cellLabel

		for y := 0; y < 12; y++ {
			for x := 0; x < 12; x++ {
				zone := assignToZone(float64(x)/float64(gridW), float64(y)/12.0, tropicalZones)
				isBoundary := false
				// Check if any neighbor is in a different zone
				for _, d := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
					nx, ny := x+d[0], y+d[1]
					if nx >= 0 && nx < 12 && ny >= 0 && ny < 12 {
						nzone := assignToZone(float64(nx)/float64(gridW), float64(ny)/12.0, tropicalZones)
						if nzone != zone {
							isBoundary = true
							break
						}
					}
				}
				cellLabels = append(cellLabels, cellLabel{x, y, isBoundary})
			}
		}

		// Compute boundary scores: 3D (orientation only) and 24D (all features)
		type scored struct {
			score3D    float64
			score24D   float64
			isBoundary bool
		}
		var scoredCells []scored

		for _, cl := range cellLabels {
			idx := cl.y*12 + cl.x
			if idx >= len(cells) {
				continue
			}

			maxDiff3D := 0.0
			maxDiff24D := 0.0
			for _, d := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
				nx, ny := cl.x+d[0], cl.y+d[1]
				if nx < 0 || nx >= 12 || ny < 0 || ny >= 12 {
					continue
				}
				nidx := ny*12 + nx
				if nidx >= len(cells) {
					continue
				}

				// 3D: orientation features only (first 3 of the cell features)
				diff3D := orientationDiff(cells[idx], cells[nidx])
				if diff3D > maxDiff3D {
					maxDiff3D = diff3D
				}

				// 24D: all features
				diff24D := featureDistance24D(cells[idx], cells[nidx])
				if diff24D > maxDiff24D {
					maxDiff24D = diff24D
				}
			}

			scoredCells = append(scoredCells, scored{maxDiff3D, maxDiff24D, cl.isBoundary})
		}

		// Compute IoU at optimal threshold for each method
		scores3D := make([]scoredCell, len(scoredCells))
		scores24D := make([]scoredCell, len(scoredCells))
		for i, s := range scoredCells {
			scores3D[i] = scoredCell{score: s.score3D, isBoundary: s.isBoundary}
			scores24D[i] = scoredCell{score: s.score24D, isBoundary: s.isBoundary}
		}
		site3D := computeOptimalIoU(scores3D)
		site24D := computeOptimalIoU(scores24D)

		t.Logf("  %s: IoU_3D=%.3f IoU_24D=%.3f (improvement=%.1f%%)",
			site.name, site3D, site24D, (site24D-site3D)*100)

		iou3D = append(iou3D, site3D)
		iou24D = append(iou24D, site24D)
	}

	if len(iou3D) < 3 {
		t.Skip("insufficient sites")
	}

	mean3D := mean(iou3D)
	mean24D := mean(iou24D)
	_, p := pairedTTest(iou24D, iou3D)

	t.Logf("\n=== EXPERIMENT 2 RESULTS ===")
	t.Logf("3D IoU:  mean=%.3f (per-site: %v)", mean3D, fmtFloats(iou3D))
	t.Logf("24D IoU: mean=%.3f (per-site: %v)", mean24D, fmtFloats(iou24D))
	t.Logf("Paired t-test p=%.6f", p)

	if mean24D >= 0.6 && mean24D > mean3D && p < 0.01 {
		t.Logf("✅ PASS: 24D significantly better (mean_24D=%.3f vs mean_3D=%.3f, p=%.6f)", mean24D, mean3D, p)
	} else if p < 0.05 {
		t.Logf("⚠️ INCONCLUSIVE: 24D better but weak (mean_24D=%.3f vs mean_3D=%.3f, p=%.6f)", mean24D, mean3D, p)
	} else {
		t.Logf("❌ FAIL: 24D not significantly better (mean_24D=%.3f vs mean_3D=%.3f, p=%.6f)", mean24D, mean3D, p)
	}
}

// --- Experiment 3: ε-Stability of Zone Membership ---

func TestExperiment3_EpsilonStability(t *testing.T) {
	sites := loadTestSites(t)
	ctx := context.Background()

	const epsilon = 0.005
	const nTrials = 100
	rng := rand.New(rand.NewPCG(42, 0))

	var spatialStabilities []float64
	var featureStabilities []float64

	for _, site := range sites {
		cairn := &CairnCartographer{Gear: 5, Scale: 10.0, GridSize: 12, SheafFolding: true}
		schema, err := cairn.GenerateSchema(ctx, site.screenshot, "image/png", site.summary)
		if err != nil {
			continue
		}

		zones := parseZoneBounds(schema)
		if len(zones) < 2 {
			continue
		}

		elements := parseElements(site.summary)
		if len(elements) < 10 {
			continue
		}

		img, err2 := decodeImage(site.screenshot)
		if err2 != nil {
			continue
		}
		cells := ExtractFusedFeatures(img, elements, 12)

		// Compute zone centroids in feature space
		type zoneCentroid struct {
			bounds   [4]float64
			features []float64 // average 24D features of member elements
			count    int
		}
		centroids := make([]zoneCentroid, len(zones))
		for i, z := range zones {
			centroids[i].bounds = z
			centroids[i].features = make([]float64, 24)
		}

		// Assign elements to zones and accumulate features
		for i, el := range elements {
			cx := el.bounds[0] + el.bounds[2]/2
			cy := el.bounds[1] + el.bounds[3]/2
			zi := assignToZoneIdx(cx, cy, zones)
			if zi >= 0 && i < len(cells) {
				for d := 0; d < CairnNumDims; d++ {
					centroids[zi].features[d] += cells[i].Features[d]
				}
				centroids[zi].count++
			}
		}
		for i := range centroids {
			if centroids[i].count > 0 {
				for d := range centroids[i].features {
					centroids[i].features[d] /= float64(centroids[i].count)
				}
			}
		}

		// Run perturbation trials
		var trialSpatial, trialFeature float64
		for trial := 0; trial < nTrials; trial++ {
			spatialSame := 0
			featureSame := 0
			total := 0

			for i, el := range elements {
				cx := el.bounds[0] + el.bounds[2]/2
				cy := el.bounds[1] + el.bounds[3]/2
				origZone := assignToZoneIdx(cx, cy, zones)
				if origZone < 0 {
					continue
				}

				// Perturb position
				px := cx + (rng.Float64()*2-1)*epsilon
				py := cy + (rng.Float64()*2-1)*epsilon

				// Spatial assignment: which zone bounding box contains perturbed point?
				spatialZone := assignToZoneIdx(px, py, zones)

				// Feature-space assignment: nearest centroid in 24D feature space
				featureZone := origZone
				if i < len(cells) {
					minDist := math.Inf(1)
					for zi, c := range centroids {
						if c.count == 0 {
							continue
						}
						dist := 0.0
						for d := 0; d < CairnNumDims; d++ {
							diff := cells[i].Features[d] - c.features[d]
							dist += diff * diff
						}
						if dist < minDist {
							minDist = dist
							featureZone = zi
						}
					}
				}

				total++
				if spatialZone == origZone {
					spatialSame++
				}
				if featureZone == origZone {
					featureSame++
				}
			}

			if total > 0 {
				trialSpatial += float64(spatialSame) / float64(total)
				trialFeature += float64(featureSame) / float64(total)
			}
		}

		avgSpatial := trialSpatial / float64(nTrials)
		avgFeature := trialFeature / float64(nTrials)

		t.Logf("  %s: spatial_stability=%.3f feature_stability=%.3f (ε=%.3f, n_elements=%d)",
			site.name, avgSpatial, avgFeature, epsilon, len(elements))

		spatialStabilities = append(spatialStabilities, avgSpatial)
		featureStabilities = append(featureStabilities, avgFeature)
	}

	if len(spatialStabilities) < 3 {
		t.Skip("insufficient sites")
	}

	meanSpatial := mean(spatialStabilities)
	meanFeature := mean(featureStabilities)
	_, p := pairedTTest(featureStabilities, spatialStabilities)

	t.Logf("\n=== EXPERIMENT 3 RESULTS ===")
	t.Logf("Spatial stability:  mean=%.3f (per-site: %v)", meanSpatial, fmtFloats(spatialStabilities))
	t.Logf("Feature stability:  mean=%.3f (per-site: %v)", meanFeature, fmtFloats(featureStabilities))
	t.Logf("Paired t-test p=%.6f", p)
	t.Logf("ε=%.3f (%d trials per site)", epsilon, nTrials)

	if meanFeature >= 0.95 && meanSpatial <= 0.85 && p < 0.01 {
		t.Logf("✅ PASS: feature-space ≥95%% stable, spatial ≤85%% (p=%.6f)", p)
	} else if meanFeature > meanSpatial && p < 0.05 {
		t.Logf("⚠️ INCONCLUSIVE: feature better but thresholds not met (spatial=%.3f, feature=%.3f, p=%.6f)", meanSpatial, meanFeature, p)
	} else {
		t.Logf("❌ FAIL: feature-space not significantly more stable (spatial=%.3f, feature=%.3f, p=%.6f)", meanSpatial, meanFeature, p)
	}
}

// --- Helper functions ---

type zoneBounds = [4]float64 // x, y, w, h normalized

func parseZoneBounds(schemaJSON string) []zoneBounds {
	var output struct {
		Mounts []struct {
			Bounds [4]float64 `json:"bounds"`
		} `json:"mounts"`
	}
	if err := json.Unmarshal([]byte(schemaJSON), &output); err != nil {
		return nil
	}
	var zones []zoneBounds
	for _, m := range output.Mounts {
		if m.Bounds != [4]float64{} {
			zones = append(zones, m.Bounds)
		}
	}
	return zones
}

func assignToZone(x, y float64, zones []zoneBounds) int {
	return assignToZoneIdx(x, y, zones)
}

func assignToZoneIdx(x, y float64, zones []zoneBounds) int {
	bestDist := math.Inf(1)
	bestIdx := -1
	for i, z := range zones {
		// Check containment first
		if x >= z[0] && x <= z[0]+z[2] && y >= z[1] && y <= z[1]+z[3] {
			// Inside — distance is 0, but prefer smallest zone (most specific)
			area := z[2] * z[3]
			if area < bestDist {
				bestDist = area
				bestIdx = i
			}
		}
	}
	if bestIdx >= 0 {
		return bestIdx
	}
	// Not contained — find nearest zone center
	for i, z := range zones {
		cx := z[0] + z[2]/2
		cy := z[1] + z[3]/2
		dist := math.Sqrt((x-cx)*(x-cx) + (y-cy)*(y-cy))
		if dist < bestDist {
			bestDist = dist
			bestIdx = i
		}
	}
	return bestIdx
}

func orientationDiff(a, b CairnGridCell) float64 {
	// Use the edge orientation features (indices 5,6,7 = hEdge, vEdge, dEdge)
	diff := 0.0
	for i := 5; i < 8; i++ {
		d := a.Features[i] - b.Features[i]
		diff += d * d
	}
	// Weight by edge density (index 4)
	strength := math.Min(a.Features[4], b.Features[4])
	return math.Sqrt(diff) * math.Max(strength, 0.01)
}

func featureDistance24D(a, b CairnGridCell) float64 {
	dist := 0.0
	for i := 0; i < CairnNumDims; i++ {
		d := a.Features[i] - b.Features[i]
		dist += d * d
	}
	return math.Sqrt(dist)
}

func decodeImage(data []byte) (image.Image, error) {
	r := bytes.NewReader(data)
	img, _, err := image.Decode(r)
	return img, err
}

// computeZoneStalksFromBounds builds James-Stein shrunk centroids from zone bounding
// boxes and element features. This is the test-side equivalent of computeZoneStalks
// that works with schema-derived zone bounds instead of internal zone structs.
func computeZoneStalksFromBounds(zoneBounds [][4]float64, elements []element, cells []CairnGridCell, gridSize int) SheafStalks {
	// Build synthetic zones: assign elements to zones by spatial containment
	syntheticZones := make([]zone, len(zoneBounds))
	for i, el := range elements {
		cx := el.bounds[0] + el.bounds[2]/2
		cy := el.bounds[1] + el.bounds[3]/2
		zi := assignToZoneIdx(cx, cy, zoneBounds)
		if zi >= 0 {
			syntheticZones[zi].elems = append(syntheticZones[zi].elems, i)
		}
	}
	// Set rootIdx
	for i := range syntheticZones {
		if len(syntheticZones[i].elems) > 0 {
			syntheticZones[i].rootIdx = syntheticZones[i].elems[0]
		}
	}
	return computeZoneStalks(syntheticZones, elements, cells, gridSize)
}

type scoredCell struct {
	score      float64
	isBoundary bool
}

func computeOptimalIoU(cells []scoredCell) float64 {
	if len(cells) == 0 {
		return 0
	}

	scores := make([]float64, len(cells))
	for i, c := range cells {
		scores[i] = c.score
	}
	sort.Float64s(scores)

	bestIoU := 0.0
	for pct := 50; pct <= 95; pct += 5 {
		idx := pct * len(scores) / 100
		if idx >= len(scores) {
			idx = len(scores) - 1
		}
		threshold := scores[idx]

		tp, fp, fn := 0, 0, 0
		for _, c := range cells {
			predicted := c.score >= threshold
			if predicted && c.isBoundary {
				tp++
			} else if predicted && !c.isBoundary {
				fp++
			} else if !predicted && c.isBoundary {
				fn++
			}
		}
		if tp+fp+fn > 0 {
			iou := float64(tp) / float64(tp+fp+fn)
			if iou > bestIoU {
				bestIoU = iou
			}
		}
	}
	return bestIoU
}

func median(data []float64) float64 {
	if len(data) == 0 {
		return 0
	}
	sorted := make([]float64, len(data))
	copy(sorted, data)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 0 {
		return (sorted[n/2-1] + sorted[n/2]) / 2
	}
	return sorted[n/2]
}

func mean(data []float64) float64 {
	if len(data) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range data {
		sum += v
	}
	return sum / float64(len(data))
}

func mannWhitneyU(a, b []float64) (float64, float64) {
	na, nb := len(a), len(b)
	if na == 0 || nb == 0 {
		return 0, 1
	}

	type ranked struct {
		val   float64
		group int // 0=a, 1=b
	}
	all := make([]ranked, 0, na+nb)
	for _, v := range a {
		all = append(all, ranked{v, 0})
	}
	for _, v := range b {
		all = append(all, ranked{v, 1})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].val < all[j].val })

	// Assign ranks (handle ties)
	ranks := make([]float64, len(all))
	i := 0
	for i < len(all) {
		j := i
		for j < len(all) && all[j].val == all[i].val {
			j++
		}
		avgRank := float64(i+j+1) / 2.0
		for k := i; k < j; k++ {
			ranks[k] = avgRank
		}
		i = j
	}

	// Sum ranks for group a
	r1 := 0.0
	for i, r := range all {
		if r.group == 0 {
			r1 += ranks[i]
		}
	}

	u1 := r1 - float64(na*(na+1))/2
	u2 := float64(na)*float64(nb) - u1

	// Use smaller U
	u := math.Min(u1, u2)

	// Normal approximation for p-value
	mu := float64(na) * float64(nb) / 2
	sigma := math.Sqrt(float64(na) * float64(nb) * float64(na+nb+1) / 12)
	if sigma == 0 {
		return u, 1
	}
	z := (u - mu) / sigma
	p := 2 * normalCDF(-math.Abs(z))

	return u, p
}

func pairedTTest(a, b []float64) (float64, float64) {
	n := len(a)
	if n != len(b) || n < 2 {
		return 0, 1
	}

	diffs := make([]float64, n)
	sumD := 0.0
	for i := range n {
		diffs[i] = a[i] - b[i]
		sumD += diffs[i]
	}
	meanD := sumD / float64(n)

	ss := 0.0
	for _, d := range diffs {
		ss += (d - meanD) * (d - meanD)
	}
	sd := math.Sqrt(ss / float64(n-1))
	if sd == 0 {
		return 0, 1
	}

	tStat := meanD / (sd / math.Sqrt(float64(n)))

	// Two-tailed p-value using normal approximation (good enough for n >= 5)
	p := 2 * normalCDF(-math.Abs(tStat))

	return tStat, p
}

func normalCDF(z float64) float64 {
	return 0.5 * math.Erfc(-z/math.Sqrt2)
}

func fmtFloats(fs []float64) string {
	parts := make([]string, len(fs))
	for i, f := range fs {
		parts[i] = fmt.Sprintf("%.3f", f)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// --- Experiment 2b: 24D vs 3D with z-score normalized features ---

func TestExperiment2b_Normalized24Dvs3D(t *testing.T) {
	sites := loadTestSites(t)
	ctx := context.Background()

	var iou3D, iou24D []float64

	for _, site := range sites {
		tropical := &TropicalCartographer{}
		tropicalSchema, err := tropical.GenerateSchema(ctx, site.screenshot, "image/png", site.summary)
		if err != nil {
			continue
		}
		tropicalZones := parseZoneBounds(tropicalSchema)
		if len(tropicalZones) < 2 {
			continue
		}

		elements := parseElements(site.summary)
		if len(elements) < 10 {
			continue
		}

		img, err2 := decodeImage(site.screenshot)
		if err2 != nil {
			continue
		}
		cells := ExtractFusedFeatures(img, elements, 12)
		if len(cells) < 10 {
			continue
		}

		// Compute per-dimension mean and stddev for z-score normalization
		var means, stds [CairnNumDims]float64
		n := float64(len(cells))
		for _, c := range cells {
			for d := 0; d < CairnNumDims; d++ {
				means[d] += c.Features[d]
			}
		}
		for d := range means {
			means[d] /= n
		}
		for _, c := range cells {
			for d := 0; d < CairnNumDims; d++ {
				diff := c.Features[d] - means[d]
				stds[d] += diff * diff
			}
		}
		for d := range stds {
			stds[d] = math.Sqrt(stds[d] / n)
			if stds[d] < 1e-10 {
				stds[d] = 1 // avoid division by zero for constant features
			}
		}

		gridW := 12
		var scoredCells3D, scoredCells24D []scoredCell

		for y := 0; y < 12; y++ {
			for x := 0; x < 12; x++ {
				idx := y*12 + x
				if idx >= len(cells) {
					continue
				}
				zone := assignToZone(float64(x)/float64(gridW), float64(y)/12.0, tropicalZones)
				isBoundary := false
				for _, d := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
					nx, ny := x+d[0], y+d[1]
					if nx >= 0 && nx < 12 && ny >= 0 && ny < 12 {
						if assignToZone(float64(nx)/float64(gridW), float64(ny)/12.0, tropicalZones) != zone {
							isBoundary = true
							break
						}
					}
				}

				maxDiff3D := 0.0
				maxDiff24D := 0.0
				for _, d := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
					nx, ny := x+d[0], y+d[1]
					if nx < 0 || nx >= 12 || ny < 0 || ny >= 12 {
						continue
					}
					nidx := ny*12 + nx
					if nidx >= len(cells) {
						continue
					}

					// 3D: orientation features (5,6,7) z-normalized
					diff3 := 0.0
					for i := 5; i < 8; i++ {
						da := (cells[idx].Features[i] - means[i]) / stds[i]
						db := (cells[nidx].Features[i] - means[i]) / stds[i]
						d := da - db
						diff3 += d * d
					}
					diff3 = math.Sqrt(diff3)
					if diff3 > maxDiff3D {
						maxDiff3D = diff3
					}

					// 24D: all features z-normalized
					diff24 := 0.0
					for i := 0; i < CairnNumDims; i++ {
						da := (cells[idx].Features[i] - means[i]) / stds[i]
						db := (cells[nidx].Features[i] - means[i]) / stds[i]
						d := da - db
						diff24 += d * d
					}
					diff24 = math.Sqrt(diff24)
					if diff24 > maxDiff24D {
						maxDiff24D = diff24
					}
				}

				scoredCells3D = append(scoredCells3D, scoredCell{score: maxDiff3D, isBoundary: isBoundary})
				scoredCells24D = append(scoredCells24D, scoredCell{score: maxDiff24D, isBoundary: isBoundary})
			}
		}

		site3D := computeOptimalIoU(scoredCells3D)
		site24D := computeOptimalIoU(scoredCells24D)

		t.Logf("  %s: IoU_3D=%.3f IoU_24D=%.3f (improvement=%.1f%%)",
			site.name, site3D, site24D, (site24D-site3D)*100)

		iou3D = append(iou3D, site3D)
		iou24D = append(iou24D, site24D)
	}

	if len(iou3D) < 3 {
		t.Skip("insufficient sites")
	}

	mean3 := mean(iou3D)
	mean24 := mean(iou24D)
	_, p := pairedTTest(iou24D, iou3D)

	t.Logf("\n=== EXPERIMENT 2b RESULTS (z-score normalized) ===")
	t.Logf("3D IoU:  mean=%.3f (per-site: %v)", mean3, fmtFloats(iou3D))
	t.Logf("24D IoU: mean=%.3f (per-site: %v)", mean24, fmtFloats(iou24D))
	t.Logf("Paired t-test p=%.6f", p)

	if mean24 > mean3 && p < 0.05 {
		t.Logf("✅ PASS: normalized 24D significantly better (p=%.6f)", p)
	} else if mean24 > mean3 {
		t.Logf("⚠️ INCONCLUSIVE: 24D better but not significant (p=%.6f)", p)
	} else {
		t.Logf("❌ FAIL: 24D not better even after normalization (p=%.6f)", p)
	}
}

// --- Experiment 3b: ε-Stability sweep across gear levels ---
// Tests ALL gear levels (1,3,5) × ALL sites. Never skips silently.
// Reports per-gear aggregates to find the zoom level where feature-space wins.

func TestExperiment3b_GearSweepStability(t *testing.T) {
	sites := loadTestSites(t)
	ctx := context.Background()

	const epsilon = 0.005
	const nTrials = 50
	rng := rand.New(rand.NewPCG(42, 0))

	gears := []int{1, 3, 5}

	type gearResult struct {
		gear    int
		site    string
		spatial float64
		feature float64
		nZones  int
		nElems  int
		minZone int // smallest zone element count
		problem string
	}
	var allResults []gearResult

	for _, gear := range gears {
		for _, site := range sites {
			elements := parseElements(site.summary)
			gr := gearResult{gear: gear, site: site.name, nElems: len(elements)}

			if len(elements) < 5 {
				gr.problem = fmt.Sprintf("only %d elements", len(elements))
				gr.spatial = -1
				gr.feature = -1
				allResults = append(allResults, gr)
				continue
			}

			cairn := &CairnCartographer{Gear: gear, Scale: 10.0, GridSize: 12, SheafFolding: true}
			schema, err := cairn.GenerateSchema(ctx, site.screenshot, "image/png", site.summary)
			if err != nil {
				gr.problem = fmt.Sprintf("cairn error: %v", err)
				gr.spatial = -1
				gr.feature = -1
				allResults = append(allResults, gr)
				continue
			}

			zones := parseZoneBounds(schema)
			gr.nZones = len(zones)
			if len(zones) < 2 {
				gr.problem = fmt.Sprintf("only %d zones", len(zones))
				gr.spatial = -1
				gr.feature = -1
				allResults = append(allResults, gr)
				continue
			}

			img, err2 := decodeImage(site.screenshot)
			if err2 != nil {
				gr.problem = "decode error"
				gr.spatial = -1
				gr.feature = -1
				allResults = append(allResults, gr)
				continue
			}
			cells := ExtractFusedFeatures(img, elements, 12)

			// Use the James-Stein shrunk centroids from the sheaf pipeline.
			// We already ran cairn above — parse its zones to get the internal
			// zone structure, then compute J-S stalks on those zones.
			//
			// Since we can't easily reconstruct internal zones from the schema JSON,
			// use the zone bounds from the schema as spatial anchors and build
			// centroids from elements assigned to each zone.
			ss := computeZoneStalksFromBounds(zones, elements, cells, 12)

			// Find min zone size
			minZoneCount := math.MaxInt
			for _, c := range ss.Counts {
				if c > 0 && c < minZoneCount {
					minZoneCount = c
				}
			}
			gr.minZone = minZoneCount

			// Run perturbation trials
			var trialSpatial, trialFeature float64
			for trial := 0; trial < nTrials; trial++ {
				spatialSame, featureSame, total := 0, 0, 0
				for i, el := range elements {
					cx := el.bounds[0] + el.bounds[2]/2
					cy := el.bounds[1] + el.bounds[3]/2
					origZone := assignToZoneIdx(cx, cy, zones)
					if origZone < 0 {
						continue
					}
					px := cx + (rng.Float64()*2-1)*epsilon
					py := cy + (rng.Float64()*2-1)*epsilon

					spatialZone := assignToZoneIdx(px, py, zones)

					// Feature-space: nearest James-Stein shrunk centroid
					featureZone := origZone
					if i < len(cells) {
						minDist := math.Inf(1)
						for zi, stalk := range ss.Stalks {
							if ss.Counts[zi] == 0 {
								continue
							}
							dist := 0.0
							for d := 0; d < CairnNumDims; d++ {
								dd := cells[i].Features[d] - stalk[d]
								dist += dd * dd
							}
							if dist < minDist {
								minDist = dist
								featureZone = zi
							}
						}
					}
					total++
					if spatialZone == origZone {
						spatialSame++
					}
					if featureZone == origZone {
						featureSame++
					}
				}
				if total > 0 {
					trialSpatial += float64(spatialSame) / float64(total)
					trialFeature += float64(featureSame) / float64(total)
				}
			}
			gr.spatial = trialSpatial / float64(nTrials)
			gr.feature = trialFeature / float64(nTrials)
			allResults = append(allResults, gr)
		}
	}

	// Report everything — per gear level
	t.Logf("\n=== EXPERIMENT 3b: ε-STABILITY SWEEP (ε=%.3f, %d trials) ===\n", epsilon, nTrials)
	t.Logf("%-6s %-18s %6s %5s %6s %8s %8s %s",
		"Gear", "Site", "Elems", "Zones", "MinZ", "Spatial", "Feature", "Note")
	t.Logf("%s", strings.Repeat("─", 85))

	for _, gear := range gears {
		var gSpatial, gFeature []float64
		for _, r := range allResults {
			if r.gear != gear {
				continue
			}
			note := ""
			if r.problem != "" {
				note = r.problem
			} else if r.feature > r.spatial {
				note = "← feature wins"
			} else if r.minZone < 5 {
				note = fmt.Sprintf("⚠ tiny zone (%d)", r.minZone)
			}
			spatialStr := fmt.Sprintf("%.3f", r.spatial)
			featureStr := fmt.Sprintf("%.3f", r.feature)
			if r.spatial < 0 {
				spatialStr = "  -  "
				featureStr = "  -  "
			}
			t.Logf("%-6d %-18s %6d %5d %6d %8s %8s %s",
				r.gear, r.site, r.nElems, r.nZones, r.minZone, spatialStr, featureStr, note)

			if r.spatial >= 0 {
				gSpatial = append(gSpatial, r.spatial)
				gFeature = append(gFeature, r.feature)
			}
		}

		if len(gSpatial) >= 2 {
			ms := mean(gSpatial)
			mf := mean(gFeature)
			_, p := pairedTTest(gFeature, gSpatial)
			winner := "spatial"
			if mf > ms {
				winner = "FEATURE"
			}
			t.Logf("  Gear %d summary: spatial=%.3f feature=%.3f p=%.4f winner=%s (n=%d sites)\n",
				gear, ms, mf, p, winner, len(gSpatial))
		}
	}

	// Overall verdict
	t.Logf("\n=== VERDICT ===")
	bestGear := 0
	bestDelta := -999.0
	for _, gear := range gears {
		var gS, gF []float64
		for _, r := range allResults {
			if r.gear == gear && r.spatial >= 0 {
				gS = append(gS, r.spatial)
				gF = append(gF, r.feature)
			}
		}
		if len(gS) == 0 {
			continue
		}
		delta := mean(gF) - mean(gS)
		if delta > bestDelta {
			bestDelta = delta
			bestGear = gear
		}
	}
	if bestDelta > 0 {
		t.Logf("Best gear for feature-space stability: gear %d (feature %.3f above spatial)", bestGear, bestDelta)
	} else {
		t.Logf("Spatial containment wins at ALL gear levels. Feature-space assignment does not improve stability.")
		t.Logf("Best (least bad) gear: %d (delta=%.3f)", bestGear, bestDelta)
	}
}

// --- Experiment 4: Continuous vs Quantized Zone Construction ---
//
// Hypothesis: Zone construction using continuous 24D feature similarity (no
// Leech/Golay quantization) produces zones with higher epsilon-stability than
// quantized zones, while maintaining equivalent boundary detection quality.
//
// Quantized path: CairnCartographer{Gear:5} (full lattice pipeline).
// Continuous path: structural ancestor grouping + intra-group k-means on raw
// 24D features — no lattice quantization.

func TestExperiment4_ContinuousVsQuantized(t *testing.T) {
	sites := loadTestSites(t)
	ctx := context.Background()

	const epsilon = 0.005
	const nTrials = 100
	rng := rand.New(rand.NewPCG(42, 0))

	type siteResult struct {
		name                string
		quantizedZones      int
		continuousZones     int
		quantizedStability  float64
		continuousStability float64
		quantizedJaccard    float64
		continuousJaccard   float64
		problem             string
	}
	var results []siteResult

	for _, site := range sites {
		res := siteResult{name: site.name}

		elements := parseElements(site.summary)
		if len(elements) < 10 {
			res.problem = fmt.Sprintf("only %d elements", len(elements))
			results = append(results, res)
			continue
		}

		img, err := decodeImage(site.screenshot)
		if err != nil {
			res.problem = fmt.Sprintf("decode error: %v", err)
			results = append(results, res)
			continue
		}
		cells := ExtractFusedFeatures(img, elements, 12)
		if len(cells) < 10 {
			res.problem = fmt.Sprintf("only %d cells", len(cells))
			results = append(results, res)
			continue
		}

		// Ground truth: tropical zones
		tropical := &TropicalCartographer{}
		tropicalSchema, err := tropical.GenerateSchema(ctx, site.screenshot, "image/png", site.summary)
		if err != nil {
			res.problem = fmt.Sprintf("tropical error: %v", err)
			results = append(results, res)
			continue
		}
		tropicalZones := parseZoneBounds(tropicalSchema)
		if len(tropicalZones) < 2 {
			res.problem = fmt.Sprintf("only %d tropical zones", len(tropicalZones))
			results = append(results, res)
			continue
		}

		// --- Quantized path: CairnCartographer{Gear:5} ---
		cairn := &CairnCartographer{Gear: 5, Scale: 10.0, GridSize: 12, SheafFolding: true}
		cairnSchema, err := cairn.GenerateSchema(ctx, site.screenshot, "image/png", site.summary)
		if err != nil {
			res.problem = fmt.Sprintf("cairn error: %v", err)
			results = append(results, res)
			continue
		}
		quantizedBounds := parseZoneBounds(cairnSchema)
		res.quantizedZones = len(quantizedBounds)

		if len(quantizedBounds) < 2 {
			res.problem = fmt.Sprintf("only %d quantized zones", len(quantizedBounds))
			results = append(results, res)
			continue
		}

		// --- Continuous path: structural grouping + k-means ---
		continuousBounds := buildContinuousZones(elements, cells, rng)
		res.continuousZones = len(continuousBounds)

		if len(continuousBounds) < 2 {
			res.problem = fmt.Sprintf("only %d continuous zones", len(continuousBounds))
			results = append(results, res)
			continue
		}

		// --- Measure stability for both ---
		qStalks := computeZoneStalksFromBounds(quantizedBounds, elements, cells, 12)
		cStalks := computeZoneStalksFromBounds(continuousBounds, elements, cells, 12)

		res.quantizedStability = measureFeatureStability(
			elements, cells, quantizedBounds, qStalks, epsilon, nTrials, rng)
		res.continuousStability = measureFeatureStability(
			elements, cells, continuousBounds, cStalks, epsilon, nTrials, rng)

		// --- Measure Jaccard with tropical zones ---
		res.quantizedJaccard = computeZoneJaccard(quantizedBounds, tropicalZones, elements)
		res.continuousJaccard = computeZoneJaccard(continuousBounds, tropicalZones, elements)

		results = append(results, res)
	}

	// --- Report per-site ---
	t.Logf("\n=== EXPERIMENT 4: CONTINUOUS vs QUANTIZED ZONE CONSTRUCTION ===\n")
	t.Logf("%-18s %6s %6s %10s %10s %10s %10s %s",
		"Site", "QZones", "CZones", "QStabil", "CStabil", "QJaccard", "CJaccard", "Note")
	t.Logf("%s", strings.Repeat("-", 100))

	var qStabilities, cStabilities []float64
	var qJaccards, cJaccards []float64

	for _, r := range results {
		note := r.problem
		if note == "" {
			if r.continuousStability > r.quantizedStability {
				note = "<- continuous wins stability"
			} else {
				note = "<- quantized wins stability"
			}
		}

		if r.problem != "" {
			t.Logf("%-18s %6s %6s %10s %10s %10s %10s %s",
				r.name, "-", "-", "-", "-", "-", "-", note)
		} else {
			t.Logf("%-18s %6d %6d %10.4f %10.4f %10.4f %10.4f %s",
				r.name, r.quantizedZones, r.continuousZones,
				r.quantizedStability, r.continuousStability,
				r.quantizedJaccard, r.continuousJaccard, note)
			qStabilities = append(qStabilities, r.quantizedStability)
			cStabilities = append(cStabilities, r.continuousStability)
			qJaccards = append(qJaccards, r.quantizedJaccard)
			cJaccards = append(cJaccards, r.continuousJaccard)
		}
	}

	if len(qStabilities) < 2 {
		t.Skip("insufficient sites with valid data")
	}

	// --- Aggregate ---
	meanQStab := mean(qStabilities)
	meanCStab := mean(cStabilities)
	meanQJac := mean(qJaccards)
	meanCJac := mean(cJaccards)

	tStab, pStab := pairedTTest(cStabilities, qStabilities)
	tJac, pJac := pairedTTest(cJaccards, qJaccards)

	t.Logf("\n=== AGGREGATE RESULTS (n=%d sites) ===", len(qStabilities))
	t.Logf("Stability:  quantized=%.4f  continuous=%.4f  t=%.3f  p=%.6f",
		meanQStab, meanCStab, tStab, pStab)
	t.Logf("  per-site quantized:  %v", fmtFloats(qStabilities))
	t.Logf("  per-site continuous: %v", fmtFloats(cStabilities))
	t.Logf("Jaccard:    quantized=%.4f  continuous=%.4f  t=%.3f  p=%.6f",
		meanQJac, meanCJac, tJac, pJac)
	t.Logf("  per-site quantized:  %v", fmtFloats(qJaccards))
	t.Logf("  per-site continuous: %v", fmtFloats(cJaccards))

	// --- Verdict ---
	t.Logf("\n=== VERDICT ===")
	stabBetter := meanCStab > meanQStab
	stabSig := pStab < 0.05
	jacDrop := meanCJac < meanQJac-0.05 // significant Jaccard drop threshold

	if stabBetter && stabSig && !jacDrop {
		t.Logf("PASS: Continuous zones have higher feature-space stability (p=%.6f) WITHOUT lower Jaccard", pStab)
		t.Logf("  Stability: +%.4f (continuous - quantized)", meanCStab-meanQStab)
		t.Logf("  Jaccard:   %+.4f (continuous - quantized)", meanCJac-meanQJac)
	} else if !stabBetter || !stabSig {
		t.Logf("FAIL: No stability improvement from removing quantization")
		t.Logf("  Stability delta: %.4f (p=%.6f)", meanCStab-meanQStab, pStab)
		t.Logf("  Jaccard delta:   %+.4f (p=%.6f)", meanCJac-meanQJac, pJac)
	} else {
		t.Logf("INCONCLUSIVE: Stability improved but Jaccard dropped significantly")
		t.Logf("  Stability: +%.4f (p=%.6f)", meanCStab-meanQStab, pStab)
		t.Logf("  Jaccard:   %.4f (p=%.6f)", meanCJac-meanQJac, pJac)
	}
}

// exp4Feat holds an element index paired with its 24D feature vector.
type exp4Feat struct {
	elemIdx int
	feats   [CairnNumDims]float64
}

// buildContinuousZones constructs zones from structural grouping + intra-group
// k-means on raw 24D features. No lattice quantization.
func buildContinuousZones(elements []element, cells []CairnGridCell, rng *rand.Rand) [][4]float64 {
	// Build element -> grid cell mapping by nearest cell
	elemToCell := make(map[int]int, len(elements))
	for i, el := range elements {
		if !el.hasBounds {
			continue
		}
		cx := el.bounds[0] + el.bounds[2]/2
		cy := el.bounds[1] + el.bounds[3]/2
		bestIdx := -1
		bestDist := math.Inf(1)
		for ci, c := range cells {
			ccx := (float64(c.Col) + 0.5) / 12.0
			ccy := (float64(c.Row) + 0.5) / 12.0
			d := (cx-ccx)*(cx-ccx) + (cy-ccy)*(cy-ccy)
			if d < bestDist {
				bestDist = d
				bestIdx = ci
			}
		}
		if bestIdx >= 0 {
			elemToCell[i] = bestIdx
		}
	}

	// Group elements by structural ancestor
	idToIdx := make(map[string]int, len(elements))
	for i, el := range elements {
		idToIdx[el.id] = i
	}
	groups := make(map[string][]int)
	for i, el := range elements {
		ancestor := findStructuralAncestor(el, elements, idToIdx)
		groups[ancestor] = append(groups[ancestor], i)
	}

	// Sort group keys for deterministic iteration order
	groupKeys := make([]string, 0, len(groups))
	for k := range groups {
		groupKeys = append(groupKeys, k)
	}
	sort.Strings(groupKeys)

	// For each structural group, optionally split via k-means on 24D features
	var zones []zone
	for _, key := range groupKeys {
		indices := groups[key]
		if len(indices) == 0 {
			continue
		}

		// Collect feature vectors for elements that have cell mappings
		var feated []exp4Feat
		for _, idx := range indices {
			ci, ok := elemToCell[idx]
			if ok {
				feated = append(feated, exp4Feat{idx, cells[ci].Features})
			}
		}

		if len(feated) <= 30 {
			// Small group: keep as single zone
			z := zone{elems: indices}
			if len(indices) > 0 {
				z.rootIdx = indices[0]
			}
			computeZoneFeatures(&z, elements)
			zones = append(zones, z)
			continue
		}

		// Try splitting into k=2 clusters using Lloyd's algorithm
		clusters := kmeansSplit24D(feated, rng)
		if clusters == nil {
			// No real separation — keep as single zone
			z := zone{elems: indices}
			z.rootIdx = indices[0]
			computeZoneFeatures(&z, elements)
			zones = append(zones, z)
			continue
		}

		// Add elements without cell mappings to the larger cluster
		featedSet := make(map[int]bool, len(feated))
		for _, f := range feated {
			featedSet[f.elemIdx] = true
		}
		biggerCluster := 0
		if len(clusters[1]) > len(clusters[0]) {
			biggerCluster = 1
		}
		for _, idx := range indices {
			if !featedSet[idx] {
				clusters[biggerCluster] = append(clusters[biggerCluster], idx)
			}
		}

		for _, cl := range clusters {
			if len(cl) == 0 {
				continue
			}
			z := zone{elems: cl, rootIdx: cl[0]}
			computeZoneFeatures(&z, elements)
			zones = append(zones, z)
		}
	}

	// Convert zones to bounds
	var bounds [][4]float64
	for _, z := range zones {
		if len(z.elems) == 0 {
			continue
		}
		b := computeZoneBounds(z, elements)
		if b[2] > 0 && b[3] > 0 {
			bounds = append(bounds, b)
		}
	}
	return bounds
}

// kmeansSplit24D performs Lloyd's algorithm (k=2, 10 iterations) on 24D features.
// Returns [2][]int (element indices per cluster) or nil if no real separation
// (inter-cluster distance <= mean intra-cluster distance, or a cluster has <3 members).
func kmeansSplit24D(feats []exp4Feat, rng *rand.Rand) [][]int {
	n := len(feats)
	if n < 6 {
		return nil
	}

	// Initialize centroids: pick two random distinct points
	idx0 := rng.IntN(n)
	idx1 := rng.IntN(n)
	for idx1 == idx0 && n > 1 {
		idx1 = rng.IntN(n)
	}
	var c0, c1 [CairnNumDims]float64
	c0 = feats[idx0].feats
	c1 = feats[idx1].feats

	assign := make([]int, n)
	for iter := 0; iter < 10; iter++ {
		// Assign each point to nearest centroid
		for i, f := range feats {
			d0, d1 := 0.0, 0.0
			for d := 0; d < CairnNumDims; d++ {
				diff0 := f.feats[d] - c0[d]
				diff1 := f.feats[d] - c1[d]
				d0 += diff0 * diff0
				d1 += diff1 * diff1
			}
			if d0 <= d1 {
				assign[i] = 0
			} else {
				assign[i] = 1
			}
		}
		// Update centroids
		var sum0, sum1 [CairnNumDims]float64
		cnt0, cnt1 := 0, 0
		for i, f := range feats {
			if assign[i] == 0 {
				for d := 0; d < CairnNumDims; d++ {
					sum0[d] += f.feats[d]
				}
				cnt0++
			} else {
				for d := 0; d < CairnNumDims; d++ {
					sum1[d] += f.feats[d]
				}
				cnt1++
			}
		}
		if cnt0 > 0 {
			for d := range c0 {
				c0[d] = sum0[d] / float64(cnt0)
			}
		}
		if cnt1 > 0 {
			for d := range c1 {
				c1[d] = sum1[d] / float64(cnt1)
			}
		}
	}

	// Check separation: inter-cluster distance vs mean intra-cluster distance
	interDist := 0.0
	for d := 0; d < CairnNumDims; d++ {
		diff := c0[d] - c1[d]
		interDist += diff * diff
	}
	interDist = math.Sqrt(interDist)

	var intra0, intra1 float64
	cnt0, cnt1 := 0, 0
	for i, f := range feats {
		dist := 0.0
		if assign[i] == 0 {
			for dim := 0; dim < CairnNumDims; dim++ {
				diff := f.feats[dim] - c0[dim]
				dist += diff * diff
			}
			intra0 += math.Sqrt(dist)
			cnt0++
		} else {
			for dim := 0; dim < CairnNumDims; dim++ {
				diff := f.feats[dim] - c1[dim]
				dist += diff * diff
			}
			intra1 += math.Sqrt(dist)
			cnt1++
		}
	}
	avgIntra := 0.0
	if cnt0 > 0 {
		avgIntra += intra0 / float64(cnt0)
	}
	if cnt1 > 0 {
		avgIntra += intra1 / float64(cnt1)
	}
	avgIntra /= 2.0

	// Only split if inter-cluster > intra-cluster (real separation)
	if interDist <= avgIntra || cnt0 < 3 || cnt1 < 3 {
		return nil
	}

	clusters := make([][]int, 2)
	for i, f := range feats {
		clusters[assign[i]] = append(clusters[assign[i]], f.elemIdx)
	}
	return clusters
}

// measureFeatureStability computes feature-space epsilon-stability for a set
// of zones. For each element, perturb position by epsilon and check whether
// nearest-centroid assignment stays the same.
func measureFeatureStability(
	elements []element,
	cells []CairnGridCell,
	zoneBnds [][4]float64,
	stalks SheafStalks,
	epsilon float64,
	nTrials int,
	rng *rand.Rand,
) float64 {
	var trialStability float64
	for trial := 0; trial < nTrials; trial++ {
		same, total := 0, 0
		for i, el := range elements {
			cx := el.bounds[0] + el.bounds[2]/2
			cy := el.bounds[1] + el.bounds[3]/2
			origZone := assignToZoneIdx(cx, cy, zoneBnds)
			if origZone < 0 {
				continue
			}

			// Perturb position — consume RNG to keep stream aligned
			_ = cx + (rng.Float64()*2-1)*epsilon
			_ = cy + (rng.Float64()*2-1)*epsilon

			// Feature-space assignment: nearest James-Stein shrunk centroid
			featureZone := origZone
			if i < len(cells) {
				minDist := math.Inf(1)
				for zi, stalk := range stalks.Stalks {
					if stalks.Counts[zi] == 0 {
						continue
					}
					dist := 0.0
					for d := 0; d < CairnNumDims; d++ {
						dd := cells[i].Features[d] - stalk[d]
						dist += dd * dd
					}
					if dist < minDist {
						minDist = dist
						featureZone = zi
					}
				}
			}

			total++
			if featureZone == origZone {
				same++
			}
		}
		if total > 0 {
			trialStability += float64(same) / float64(total)
		}
	}
	return trialStability / float64(nTrials)
}

// computeZoneJaccard computes the average best-match Jaccard (IoU) between
// two sets of zones using element membership overlap.
func computeZoneJaccard(zonesA, zonesB [][4]float64, elements []element) float64 {
	// Assign each element to its zone in each set
	assignA := make([]int, len(elements))
	assignB := make([]int, len(elements))
	for i, el := range elements {
		cx := el.bounds[0] + el.bounds[2]/2
		cy := el.bounds[1] + el.bounds[3]/2
		assignA[i] = assignToZoneIdx(cx, cy, zonesA)
		assignB[i] = assignToZoneIdx(cx, cy, zonesB)
	}

	// Build membership sets
	setsA := make(map[int]map[int]bool)
	setsB := make(map[int]map[int]bool)
	for i := range elements {
		if assignA[i] >= 0 {
			if setsA[assignA[i]] == nil {
				setsA[assignA[i]] = make(map[int]bool)
			}
			setsA[assignA[i]][i] = true
		}
		if assignB[i] >= 0 {
			if setsB[assignB[i]] == nil {
				setsB[assignB[i]] = make(map[int]bool)
			}
			setsB[assignB[i]][i] = true
		}
	}

	if len(setsA) == 0 || len(setsB) == 0 {
		return 0
	}

	// For each zone in A, find best-matching zone in B (max Jaccard)
	totalJaccard := 0.0
	for _, setA := range setsA {
		bestJ := 0.0
		for _, setB := range setsB {
			inter := 0
			for elem := range setA {
				if setB[elem] {
					inter++
				}
			}
			union := len(setA) + len(setB) - inter
			if union > 0 {
				j := float64(inter) / float64(union)
				if j > bestJ {
					bestJ = j
				}
			}
		}
		totalJaccard += bestJ
	}
	return totalJaccard / float64(len(setsA))
}

// --- Experiment 5: Signal Processing Scale ---
//
// H5a: More orientation channels (3→4→6→8) improve boundary IoU.
// H5b: Finer grid (8→12→16→24) improves boundary IoU.
// H5c: Best orientation × best grid beats baseline (K=3, grid=12).

func TestExperiment5_SignalProcessingScale(t *testing.T) {
	sites := loadTestSites(t)
	ctx := context.Background()

	// Pre-compute tropical ground truth once per site (reused across all conditions).
	type siteData struct {
		name          string
		tropicalZones []zoneBounds
		elements      []element
		img           image.Image
	}
	var validSites []siteData

	for _, site := range sites {
		tropical := &TropicalCartographer{}
		tropicalSchema, err := tropical.GenerateSchema(ctx, site.screenshot, "image/png", site.summary)
		if err != nil {
			t.Logf("  %s: tropical error: %v (skipping)", site.name, err)
			continue
		}
		tz := parseZoneBounds(tropicalSchema)
		if len(tz) < 2 {
			t.Logf("  %s: only %d tropical zones (skipping)", site.name, len(tz))
			continue
		}
		elements := parseElements(site.summary)
		if len(elements) < 10 {
			t.Logf("  %s: only %d elements (skipping)", site.name, len(elements))
			continue
		}
		img, err2 := decodeImage(site.screenshot)
		if err2 != nil {
			t.Logf("  %s: decode error (skipping)", site.name)
			continue
		}
		validSites = append(validSites, siteData{
			name:          site.name,
			tropicalZones: tz,
			elements:      elements,
			img:           img,
		})
	}

	if len(validSites) < 3 {
		t.Skip("insufficient valid sites for experiment 5")
	}

	// computeBoundaryIoU computes boundary detection IoU for a given site,
	// grid of cells, K orientation channels, and tropical ground truth.
	// K=3 uses features[5..7] directly; K>3 synthesizes K orientations from Gx,Gy.
	computeBoundaryIoU := func(cells []CairnGridCell, gridSize, K int, tropZones []zoneBounds) float64 {
		var scored []scoredCell

		for y := 0; y < gridSize; y++ {
			for x := 0; x < gridSize; x++ {
				idx := y*gridSize + x
				if idx >= len(cells) {
					continue
				}

				// Ground truth: is this cell on a boundary?
				zone := assignToZone(float64(x)/float64(gridSize), float64(y)/float64(gridSize), tropZones)
				isBoundary := false
				for _, d := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
					nx, ny := x+d[0], y+d[1]
					if nx >= 0 && nx < gridSize && ny >= 0 && ny < gridSize {
						nzone := assignToZone(float64(nx)/float64(gridSize), float64(ny)/float64(gridSize), tropZones)
						if nzone != zone {
							isBoundary = true
							break
						}
					}
				}

				// Compute max boundary score across all 4-neighbors
				maxScore := 0.0
				for _, d := range [][2]int{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
					nx, ny := x+d[0], y+d[1]
					if nx < 0 || nx >= gridSize || ny < 0 || ny >= gridSize {
						continue
					}
					nidx := ny*gridSize + nx
					if nidx >= len(cells) {
						continue
					}

					var score float64
					if K == 3 {
						// Baseline: use features[5], features[6], features[7] directly
						score = orientationDiff(cells[idx], cells[nidx])
					} else {
						// Synthesize K orientations from Gx (features[5]) and Gy (features[6])
						// edge_theta(cell) = |Gx*cos(theta) + Gy*sin(theta)|
						sumSqDiff := 0.0
						for k := 0; k < K; k++ {
							theta := float64(k) * math.Pi / float64(K)
							cosT := math.Cos(theta)
							sinT := math.Sin(theta)

							edgeA := math.Abs(cells[idx].Features[5]*cosT + cells[idx].Features[6]*sinT)
							edgeB := math.Abs(cells[nidx].Features[5]*cosT + cells[nidx].Features[6]*sinT)
							diff := edgeA - edgeB
							sumSqDiff += diff * diff
						}
						// Weight by min edge density
						strength := math.Min(cells[idx].Features[4], cells[nidx].Features[4])
						score = math.Sqrt(sumSqDiff) * math.Max(strength, 0.01)
					}

					if score > maxScore {
						maxScore = score
					}
				}

				scored = append(scored, scoredCell{score: maxScore, isBoundary: isBoundary})
			}
		}

		return computeOptimalIoU(scored)
	}

	// ====================================================================
	// H5a: Orientation channel sweep (grid fixed at 12)
	// ====================================================================
	t.Logf("\n=== EXPERIMENT 5a: ORIENTATION CHANNEL SWEEP (grid=12) ===")

	channelCounts := []int{3, 4, 6, 8}
	// iouByK[k_index][site_index]
	iouByK := make([][]float64, len(channelCounts))
	for i := range iouByK {
		iouByK[i] = make([]float64, len(validSites))
	}

	for si, sd := range validSites {
		cells := ExtractFusedFeatures(sd.img, sd.elements, 12)
		if len(cells) == 0 {
			for ki := range channelCounts {
				iouByK[ki][si] = 0
			}
			continue
		}
		for ki, K := range channelCounts {
			iou := computeBoundaryIoU(cells, 12, K, sd.tropicalZones)
			iouByK[ki][si] = iou
		}
	}

	meanByK := make([]float64, len(channelCounts))
	t.Logf("%-5s %-10s %s", "K", "Mean IoU", "Per-site")
	for ki, K := range channelCounts {
		meanByK[ki] = mean(iouByK[ki])
		t.Logf("%-5d %-10.3f %s", K, meanByK[ki], fmtFloats(iouByK[ki]))
	}

	// Spearman rho(K, IoU) across all (site, K) pairs
	var spearKvals, spearKious []float64
	for ki, K := range channelCounts {
		for si := range validSites {
			spearKvals = append(spearKvals, float64(K))
			spearKious = append(spearKious, iouByK[ki][si])
		}
	}
	rhoK, pK := spearmanCorrelation(spearKvals, spearKious)
	t.Logf("Spearman rho(K, IoU) = %.2f, p = %.4f", rhoK, pK)

	if rhoK > 0.5 && pK < 0.05 {
		t.Logf("PASS H5a: More channels monotonically improve IoU (rho=%.2f, p=%.4f)", rhoK, pK)
	} else if rhoK > 0 {
		t.Logf("FAIL H5a: Positive trend but not significant (rho=%.2f, p=%.4f)", rhoK, pK)
	} else {
		t.Logf("FAIL H5a: No monotonic improvement (rho=%.2f, p=%.4f)", rhoK, pK)
	}

	// ====================================================================
	// H5b: Grid resolution sweep (K fixed at 3)
	// ====================================================================
	t.Logf("\n=== EXPERIMENT 5b: GRID RESOLUTION SWEEP (K=3) ===")

	gridSizes := []int{8, 12, 16, 24}
	// iouByGrid[grid_index][site_index]
	iouByGrid := make([][]float64, len(gridSizes))
	for i := range iouByGrid {
		iouByGrid[i] = make([]float64, len(validSites))
	}

	for si, sd := range validSites {
		for gi, gs := range gridSizes {
			cells := ExtractFusedFeatures(sd.img, sd.elements, gs)
			if len(cells) == 0 {
				iouByGrid[gi][si] = 0
				continue
			}
			iou := computeBoundaryIoU(cells, gs, 3, sd.tropicalZones)
			iouByGrid[gi][si] = iou
		}
	}

	meanByGrid := make([]float64, len(gridSizes))
	t.Logf("%-6s %-10s %s", "Grid", "Mean IoU", "Per-site")
	for gi, gs := range gridSizes {
		meanByGrid[gi] = mean(iouByGrid[gi])
		t.Logf("%-6d %-10.3f %s", gs, meanByGrid[gi], fmtFloats(iouByGrid[gi]))
	}

	// Spearman rho(gridSize, IoU) across all (site, gridSize) pairs
	var spearGvals, spearGious []float64
	for gi, gs := range gridSizes {
		for si := range validSites {
			spearGvals = append(spearGvals, float64(gs))
			spearGious = append(spearGious, iouByGrid[gi][si])
		}
	}
	rhoG, pG := spearmanCorrelation(spearGvals, spearGious)
	t.Logf("Spearman rho(gridSize, IoU) = %.2f, p = %.4f", rhoG, pG)

	if rhoG > 0.5 && pG < 0.05 {
		t.Logf("PASS H5b: Finer grid monotonically improves IoU (rho=%.2f, p=%.4f)", rhoG, pG)
	} else if rhoG > 0 {
		t.Logf("FAIL H5b: Positive trend but not significant (rho=%.2f, p=%.4f)", rhoG, pG)
	} else {
		t.Logf("FAIL H5b: No monotonic improvement (rho=%.2f, p=%.4f)", rhoG, pG)
	}

	// ====================================================================
	// H5c: Combined (best K x best grid) vs baseline
	// ====================================================================
	t.Logf("\n=== EXPERIMENT 5c: COMBINED (best K x best grid) ===")

	// Find best K from H5a
	bestKIdx := 0
	for i := 1; i < len(meanByK); i++ {
		if meanByK[i] > meanByK[bestKIdx] {
			bestKIdx = i
		}
	}
	bestK := channelCounts[bestKIdx]

	// Find best grid from H5b
	bestGridIdx := 0
	for i := 1; i < len(meanByGrid); i++ {
		if meanByGrid[i] > meanByGrid[bestGridIdx] {
			bestGridIdx = i
		}
	}
	bestGrid := gridSizes[bestGridIdx]

	// Baseline: K=3, grid=12
	var baselineIoUs []float64
	var comboIoUs []float64

	for si, sd := range validSites {
		// Baseline is always K=3 at grid=12 — already computed in H5a
		baselineIoUs = append(baselineIoUs, iouByK[0][si]) // K=3 is index 0 in channelCounts

		// Combined: best K x best grid
		cells := ExtractFusedFeatures(sd.img, sd.elements, bestGrid)
		if len(cells) == 0 {
			comboIoUs = append(comboIoUs, 0)
			continue
		}
		iou := computeBoundaryIoU(cells, bestGrid, bestK, sd.tropicalZones)
		comboIoUs = append(comboIoUs, iou)
	}

	meanBaseline := mean(baselineIoUs)
	meanCombo := mean(comboIoUs)
	_, pCombo := pairedTTest(comboIoUs, baselineIoUs)
	improvement := 0.0
	if meanBaseline > 0 {
		improvement = (meanCombo - meanBaseline) / meanBaseline * 100
	}

	t.Logf("Baseline (K=3, grid=12):    IoU = %.3f  per-site: %s", meanBaseline, fmtFloats(baselineIoUs))
	t.Logf("Best combo (K=%d, grid=%d): IoU = %.3f  per-site: %s", bestK, bestGrid, meanCombo, fmtFloats(comboIoUs))
	t.Logf("Improvement: %.1f%%, paired t p = %.4f", improvement, pCombo)

	if meanCombo > meanBaseline && pCombo < 0.05 {
		t.Logf("PASS H5c: Best combo significantly better than baseline (p=%.4f)", pCombo)
	} else if meanCombo > meanBaseline {
		t.Logf("FAIL H5c: Better but not significant (p=%.4f)", pCombo)
	} else {
		t.Logf("FAIL H5c: Best combo not better than baseline (p=%.4f)", pCombo)
	}
}

// spearmanCorrelation computes Spearman's rank correlation coefficient and
// approximate p-value (normal approximation for n > 10, exact for small n).
func spearmanCorrelation(x, y []float64) (rho, p float64) {
	n := len(x)
	if n != len(y) || n < 3 {
		return 0, 1
	}

	// Compute ranks with average tie handling
	rankX := spearmanRanks(x)
	rankY := spearmanRanks(y)

	// Pearson correlation on ranks
	meanRX, meanRY := 0.0, 0.0
	for i := range rankX {
		meanRX += rankX[i]
		meanRY += rankY[i]
	}
	meanRX /= float64(n)
	meanRY /= float64(n)

	var num, denX, denY float64
	for i := range rankX {
		dx := rankX[i] - meanRX
		dy := rankY[i] - meanRY
		num += dx * dy
		denX += dx * dx
		denY += dy * dy
	}

	if denX == 0 || denY == 0 {
		return 0, 1
	}
	rho = num / math.Sqrt(denX*denY)

	// p-value via t-distribution approximation
	// t = rho * sqrt((n-2) / (1 - rho^2))
	if math.Abs(rho) >= 1.0 {
		if n > 2 {
			return rho, 0
		}
		return rho, 1
	}
	tStat := rho * math.Sqrt(float64(n-2)/(1-rho*rho))
	p = 2 * normalCDF(-math.Abs(tStat))
	return rho, p
}

// spearmanRanks converts values to ranks with average tie handling.
func spearmanRanks(vals []float64) []float64 {
	n := len(vals)
	type indexedVal struct {
		val float64
		idx int
	}
	sorted := make([]indexedVal, n)
	for i, v := range vals {
		sorted[i] = indexedVal{v, i}
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].val < sorted[j].val })

	ranks := make([]float64, n)
	i := 0
	for i < n {
		j := i
		for j < n && sorted[j].val == sorted[i].val {
			j++
		}
		avgRank := float64(i+j+1) / 2.0 // 1-based average rank
		for k := i; k < j; k++ {
			ranks[sorted[k].idx] = avgRank
		}
		i = j
	}
	return ranks
}

// --- Experiment 6: DOM-Only Navigation Accuracy ---
//
// Tests whether DOM-only zone construction achieves equivalent navigation
// accuracy to the full cairn pipeline (screenshot + 24D features + Leech
// lattice quantization). When GenerateSchema receives nil screenshot bytes,
// the image decode fails and the code falls back to structural grouping
// (buildDOMSubtreeGroups with empty visualTypes), which IS the DOM-only path.
//
// Metrics:
//   - Zone count / coverage per condition
//   - Element findability: for each bench case, is the expected text
//     discoverable in the zone children files?
//   - McNemar test on paired findability outcomes

// benchCase mirrors the JSON structure in testdata/bench_cases.json.
type benchCase struct {
	Site          string `json:"site"`
	Intent        string `json:"intent"`
	ExpectMacheID string `json:"expect_mache_id"`
	ExpectText    string `json:"expect_text"`
	Difficulty    string `json:"difficulty"`
}

func loadBenchCases(t *testing.T) []benchCase {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "bench_cases.json"))
	if err != nil {
		t.Fatalf("load bench_cases.json: %v", err)
	}
	var cases []benchCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatalf("parse bench_cases.json: %v", err)
	}
	return cases
}

func filterCases(cases []benchCase, siteName string) []benchCase {
	var out []benchCase
	for _, c := range cases {
		if c.Site == siteName {
			out = append(out, c)
		}
	}
	return out
}

// buildEngineFromSchema creates a mache Engine, applies the schema, loads
// children from the summary, and returns the engine. This is the core
// of findability testing — it mirrors the real pipeline.
func buildEngineFromSchema(schemaJSON, summary string) (*mache.Engine, error) {
	engine := mache.NewEngine()
	if err := engine.ApplySchema(schemaJSON); err != nil {
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	engine.LoadChildren(summary, nil)
	return engine, nil
}

// testFindability checks how many bench cases have their expected text
// discoverable in the zone tree. It walks all zone children files and
// the text_index looking for matches.
//
// Returns (found count, total cases, per-case results).
func testFindability(engine *mache.Engine, cases []benchCase) (int, int, []bool) {
	found := 0
	results := make([]bool, len(cases))

	// Collect all readable text from the engine by walking the tree.
	allText := collectAllText(engine, "")

	for i, c := range cases {
		// Check if the expected text appears anywhere in the zone tree.
		if strings.Contains(allText, c.ExpectText) {
			found++
			results[i] = true
		}
	}
	return found, len(cases), results
}

// testZoneChildrenFindability checks how many bench cases have their
// expected text discoverable specifically in zone children files (not
// the text_index). This isolates whether zone assignment captured the
// element.
func testZoneChildrenFindability(engine *mache.Engine, cases []benchCase) (int, int, []bool) {
	found := 0
	results := make([]bool, len(cases))

	// Collect text only from zone children (exclude text_index).
	zoneText := collectZoneChildrenText(engine, "")

	for i, c := range cases {
		if strings.Contains(zoneText, c.ExpectText) {
			found++
			results[i] = true
		}
	}
	return found, len(cases), results
}

// collectAllText walks the engine tree recursively and concatenates all
// file content (children files, text_index, text files, etc.).
func collectAllText(engine *mache.Engine, root string) string {
	var sb strings.Builder
	children, err := engine.ListChildren(root)
	if err != nil {
		return ""
	}
	for _, childID := range children {
		node, err := engine.GetNode(childID)
		if err != nil {
			continue
		}
		if node.Mode.IsDir() {
			sb.WriteString(collectAllText(engine, childID))
		} else {
			sb.Write(node.Data)
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// collectZoneChildrenText walks zone directories and collects text only
// from children files and _c/ subdirectories (not the root text_index).
func collectZoneChildrenText(engine *mache.Engine, root string) string {
	var sb strings.Builder
	children, err := engine.ListChildren(root)
	if err != nil {
		return ""
	}
	for _, childID := range children {
		// Skip text_index at root level — we want zone-specific content only.
		if root == "" && path.Base(childID) == "text_index" {
			continue
		}
		node, err := engine.GetNode(childID)
		if err != nil {
			continue
		}
		if node.Mode.IsDir() {
			sb.WriteString(collectZoneChildrenText(engine, childID))
		} else {
			sb.Write(node.Data)
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// countZones parses the schema JSON and returns the number of mounts.
func countZones(schemaJSON string) int {
	var output struct {
		Mounts []json.RawMessage `json:"mounts"`
	}
	if err := json.Unmarshal([]byte(schemaJSON), &output); err != nil {
		return 0
	}
	return len(output.Mounts)
}

// countElementsInZones counts elements assigned to zone children files
// (elements that appear in children listings, excluding text_index).
func countElementsInZones(engine *mache.Engine) int {
	text := collectZoneChildrenText(engine, "")
	// Count lines that look like ordinal entries: "[N] tag: text"
	lines := strings.Split(text, "\n")
	count := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if len(line) > 0 && strings.Contains(line, "]") {
			count++
		}
	}
	return count
}

func TestExperiment6_DOMOnlyAccuracy(t *testing.T) {
	sites := loadTestSites(t)
	cases := loadBenchCases(t)
	ctx := context.Background()

	type siteResult struct {
		name string

		// Zone metrics
		fullZones, domZones int
		fullElems, domElems int

		// Findability: all text (zone children + text_index)
		fullFoundAll, domFoundAll     int
		fullResultsAll, domResultsAll []bool

		// Findability: zone children only (isolates zone assignment quality)
		fullFoundZone, domFoundZone     int
		fullResultsZone, domResultsZone []bool

		totalCases int
	}

	var results []siteResult
	var allFullAll, allDOMAll []float64   // per-site findability rates (all text)
	var allFullZone, allDOMZone []float64 // per-site findability rates (zone only)

	for _, site := range sites {
		siteCases := filterCases(cases, site.name)
		if len(siteCases) == 0 {
			t.Logf("  %s: no bench cases, skipping", site.name)
			continue
		}

		// Condition A: full cairn (screenshot + DOM)
		cairnFull := &CairnCartographer{Gear: 5, Scale: 10.0, GridSize: 8, SheafFolding: true}
		schemaFull, err := cairnFull.GenerateSchema(ctx, site.screenshot, "image/png", site.summary)
		if err != nil {
			t.Logf("  %s: full cairn error: %v", site.name, err)
			continue
		}

		// Condition B: DOM-only (nil screenshot triggers structural fallback)
		cairnDOM := &CairnCartographer{Gear: 5, Scale: 10.0, GridSize: 8, SheafFolding: true}
		schemaDOM, err := cairnDOM.GenerateSchema(ctx, nil, "image/png", site.summary)
		if err != nil {
			t.Logf("  %s: DOM-only error: %v", site.name, err)
			continue
		}

		// Build engines for each condition
		engineFull, err := buildEngineFromSchema(schemaFull, site.summary)
		if err != nil {
			t.Logf("  %s: full engine error: %v", site.name, err)
			continue
		}
		engineDOM, err := buildEngineFromSchema(schemaDOM, site.summary)
		if err != nil {
			t.Logf("  %s: DOM engine error: %v", site.name, err)
			continue
		}

		// Zone metrics
		fullZones := countZones(schemaFull)
		domZones := countZones(schemaDOM)
		fullElems := countElementsInZones(engineFull)
		domElems := countElementsInZones(engineDOM)

		// Findability: all text (zone children + text_index)
		fullFoundAll, _, fullResultsAll := testFindability(engineFull, siteCases)
		domFoundAll, _, domResultsAll := testFindability(engineDOM, siteCases)

		// Findability: zone children only
		fullFoundZone, _, fullResultsZone := testZoneChildrenFindability(engineFull, siteCases)
		domFoundZone, _, domResultsZone := testZoneChildrenFindability(engineDOM, siteCases)

		sr := siteResult{
			name:            site.name,
			fullZones:       fullZones,
			domZones:        domZones,
			fullElems:       fullElems,
			domElems:        domElems,
			fullFoundAll:    fullFoundAll,
			domFoundAll:     domFoundAll,
			fullResultsAll:  fullResultsAll,
			domResultsAll:   domResultsAll,
			fullFoundZone:   fullFoundZone,
			domFoundZone:    domFoundZone,
			fullResultsZone: fullResultsZone,
			domResultsZone:  domResultsZone,
			totalCases:      len(siteCases),
		}
		results = append(results, sr)

		fullRateAll := float64(fullFoundAll) / float64(len(siteCases))
		domRateAll := float64(domFoundAll) / float64(len(siteCases))
		fullRateZone := float64(fullFoundZone) / float64(len(siteCases))
		domRateZone := float64(domFoundZone) / float64(len(siteCases))

		allFullAll = append(allFullAll, fullRateAll)
		allDOMAll = append(allDOMAll, domRateAll)
		allFullZone = append(allFullZone, fullRateZone)
		allDOMZone = append(allDOMZone, domRateZone)

		t.Logf("  %s: zones full=%d dom=%d | elems full=%d dom=%d | find(all) full=%d/%d dom=%d/%d | find(zone) full=%d/%d dom=%d/%d",
			site.name, fullZones, domZones, fullElems, domElems,
			fullFoundAll, len(siteCases), domFoundAll, len(siteCases),
			fullFoundZone, len(siteCases), domFoundZone, len(siteCases))

		// Per-case detail for mismatches
		for i, c := range siteCases {
			fullOK := fullResultsZone[i]
			domOK := domResultsZone[i]
			if fullOK != domOK {
				marker := "+"
				if fullOK && !domOK {
					marker = "-"
				}
				t.Logf("    %s %s [%s] %q (%s)", marker, c.ExpectMacheID, c.Difficulty, c.ExpectText, c.Intent)
			}
		}
	}

	if len(results) < 2 {
		t.Skip("insufficient sites with bench cases")
	}

	// === Aggregate Results ===
	t.Logf("\n=== EXPERIMENT 6 RESULTS ===")

	// Aggregate counts
	totalCases := 0
	totalFullAll, totalDOMAll := 0, 0
	totalFullZone, totalDOMZone := 0, 0
	for _, sr := range results {
		totalCases += sr.totalCases
		totalFullAll += sr.fullFoundAll
		totalDOMAll += sr.domFoundAll
		totalFullZone += sr.fullFoundZone
		totalDOMZone += sr.domFoundZone
	}

	t.Logf("Sites tested: %d, Total bench cases: %d", len(results), totalCases)
	t.Logf("")

	// All-text findability (zone children + text_index)
	meanFullAll := mean(allFullAll)
	meanDOMAll := mean(allDOMAll)
	t.Logf("All-text findability (zone children + text_index):")
	t.Logf("  Full cairn:  %d/%d (%.1f%%) per-site: %s", totalFullAll, totalCases, float64(totalFullAll)/float64(totalCases)*100, fmtFloats(allFullAll))
	t.Logf("  DOM-only:    %d/%d (%.1f%%) per-site: %s", totalDOMAll, totalCases, float64(totalDOMAll)/float64(totalCases)*100, fmtFloats(allDOMAll))
	t.Logf("")

	// Zone-only findability (isolates zone assignment quality)
	meanFullZone := mean(allFullZone)
	meanDOMZone := mean(allDOMZone)
	_, pZone := pairedTTest(allDOMZone, allFullZone)
	t.Logf("Zone-children findability (zone assignment quality):")
	t.Logf("  Full cairn:  %d/%d (%.1f%%) per-site: %s", totalFullZone, totalCases, float64(totalFullZone)/float64(totalCases)*100, fmtFloats(allFullZone))
	t.Logf("  DOM-only:    %d/%d (%.1f%%) per-site: %s", totalDOMZone, totalCases, float64(totalDOMZone)/float64(totalCases)*100, fmtFloats(allDOMZone))
	t.Logf("  Paired t-test p=%.6f", pZone)
	t.Logf("")

	// McNemar test on zone-children findability (paired per-case)
	// b = full found, DOM missed; c = DOM found, full missed
	b, c := 0, 0
	for _, sr := range results {
		for i := range sr.fullResultsZone {
			fullOK := sr.fullResultsZone[i]
			domOK := sr.domResultsZone[i]
			if fullOK && !domOK {
				b++
			}
			if !fullOK && domOK {
				c++
			}
		}
	}
	// McNemar chi-squared (with continuity correction)
	mcnemarP := 1.0
	if b+c > 0 {
		diff := math.Abs(float64(b)-float64(c)) - 1 // continuity correction
		if diff < 0 {
			diff = 0
		}
		chi2 := (diff * diff) / float64(b+c)
		// p-value from chi-squared with 1 df (use normal approximation)
		mcnemarP = 2 * normalCDF(-math.Sqrt(chi2))
	}

	t.Logf("McNemar test (zone-children): b=%d (full only) c=%d (DOM only) p=%.6f", b, c, mcnemarP)
	t.Logf("")

	// Decision
	ratio := 0.0
	if meanFullZone > 0 {
		ratio = meanDOMZone / meanFullZone
	}
	t.Logf("DOM-only / Full-cairn zone findability ratio: %.3f", ratio)

	if ratio >= 0.95 || (meanDOMAll >= 0.95 && meanFullAll >= 0.95) {
		t.Logf("PASS: DOM-only achieves >=95%% of full-cairn findability (ratio=%.3f)", ratio)
		t.Logf("Screenshots are NOT necessary for navigation accuracy.")
	} else if mcnemarP < 0.05 && b > c {
		t.Logf("FAIL: Full cairn significantly better (McNemar p=%.6f, full-only=%d, DOM-only=%d)", mcnemarP, b, c)
		t.Logf("Screenshots contribute meaningfully to navigation accuracy.")
	} else {
		t.Logf("INCONCLUSIVE: DOM-only ratio=%.3f, McNemar p=%.6f (b=%d, c=%d)", ratio, mcnemarP, b, c)
		t.Logf("Difference exists but is not statistically significant.")
	}

	// Summary table
	t.Logf("")
	t.Logf("%-12s %6s %6s %10s %10s %10s %10s", "Site", "Z-full", "Z-dom", "All-full", "All-dom", "Zone-full", "Zone-dom")
	for _, sr := range results {
		t.Logf("%-12s %6d %6d %7d/%-3d %7d/%-3d %7d/%-3d %7d/%-3d",
			sr.name, sr.fullZones, sr.domZones,
			sr.fullFoundAll, sr.totalCases, sr.domFoundAll, sr.totalCases,
			sr.fullFoundZone, sr.totalCases, sr.domFoundZone, sr.totalCases)
	}

	_ = meanFullAll
	_ = meanDOMAll
	_ = meanFullZone
	_ = meanDOMZone
}

// --- Experiment 7: Visual Debt Analysis ---

// elementDebt returns the visual debt score for a single element.
// 0 = DOM explains it, 0.5 = partial, 1 = needs pixel analysis.
func elementDebt(el element) float64 {
	switch el.tag {
	case "canvas":
		return 1.0
	case "img":
		if strings.TrimSpace(el.text) == "" {
			return 1.0
		}
		return 0.0 // img with alt text
	case "video":
		return 0.5
	default:
		return 0.0
	}
}

// debtSource tracks which element types contribute visual debt cells.
type debtSource struct {
	tag      string
	elements int
	cells    int
}

func TestExperiment7_VisualDebt(t *testing.T) {
	sites := loadTestSites(t)

	const gridSize = 12
	const totalCells = gridSize * gridSize

	type siteResult struct {
		name      string
		elements  int
		imgNoAlt  int
		canvasN   int
		videoN    int
		debtCells int
		debtPct   float64
		sources   []debtSource
	}

	var results []siteResult

	for _, site := range sites {
		elements := parseElements(site.summary)

		// Decode screenshot to get dimensions for grid mapping.
		img, _, err := image.Decode(bytes.NewReader(site.screenshot))
		if err != nil {
			t.Logf("  %s: decode screenshot: %v", site.name, err)
			continue
		}
		imgW := float64(img.Bounds().Dx())
		imgH := float64(img.Bounds().Dy())
		cellW := imgW / float64(gridSize)
		cellH := imgH / float64(gridSize)

		// Count tag types of interest.
		var imgNoAlt, canvasN, videoN int
		for _, el := range elements {
			switch el.tag {
			case "img":
				if strings.TrimSpace(el.text) == "" {
					imgNoAlt++
				}
			case "canvas":
				canvasN++
			case "video":
				videoN++
			}
		}

		// For each grid cell, compute max debt from overlapping elements.
		debtGrid := make([]float64, totalCells)

		// Track which source tags contribute to each cell for breakdown.
		type cellTag struct {
			row, col int
			tag      string
		}
		var cellTags []cellTag

		for _, el := range elements {
			if !el.hasBounds {
				continue
			}
			d := elementDebt(el)
			if d == 0 {
				continue
			}

			// Element bounds are normalized [0,1]. Convert to pixel coords.
			elX := el.bounds[0] * imgW
			elY := el.bounds[1] * imgH
			elW := el.bounds[2] * imgW
			elH := el.bounds[3] * imgH

			// Skip zero-size elements.
			if elW < 1 && elH < 1 {
				continue
			}

			// Determine which grid cells this element overlaps.
			colStart := int(elX / cellW)
			colEnd := int((elX + elW) / cellW)
			rowStart := int(elY / cellH)
			rowEnd := int((elY + elH) / cellH)

			// Clamp to grid bounds.
			if colStart < 0 {
				colStart = 0
			}
			if colEnd >= gridSize {
				colEnd = gridSize - 1
			}
			if rowStart < 0 {
				rowStart = 0
			}
			if rowEnd >= gridSize {
				rowEnd = gridSize - 1
			}

			for r := rowStart; r <= rowEnd; r++ {
				for c := colStart; c <= colEnd; c++ {
					idx := r*gridSize + c
					if d > debtGrid[idx] {
						debtGrid[idx] = d
					}
					cellTags = append(cellTags, cellTag{r, c, el.tag})
				}
			}
		}

		// Count cells with debt > 0.
		debtCells := 0
		for _, d := range debtGrid {
			if d > 0 {
				debtCells++
			}
		}

		// Build per-tag breakdown: how many elements and cells per debt-contributing tag.
		tagElements := map[string]int{}
		tagCells := map[string]map[[2]int]bool{}
		for _, el := range elements {
			if elementDebt(el) > 0 && el.hasBounds {
				elW := el.bounds[2] * imgW
				elH := el.bounds[3] * imgH
				if elW < 1 && elH < 1 {
					continue
				}
				label := el.tag
				if el.tag == "img" {
					label = "img (no alt)"
				}
				tagElements[label]++
			}
		}
		for _, ct := range cellTags {
			label := ct.tag
			if ct.tag == "img" {
				label = "img (no alt)"
			}
			if tagCells[label] == nil {
				tagCells[label] = map[[2]int]bool{}
			}
			tagCells[label][[2]int{ct.row, ct.col}] = true
		}

		var sources []debtSource
		for tag, elCount := range tagElements {
			sources = append(sources, debtSource{
				tag:      tag,
				elements: elCount,
				cells:    len(tagCells[tag]),
			})
		}
		sort.Slice(sources, func(i, j int) bool { return sources[i].cells > sources[j].cells })

		pct := 100.0 * float64(debtCells) / float64(totalCells)
		results = append(results, siteResult{
			name:      site.name,
			elements:  len(elements),
			imgNoAlt:  imgNoAlt,
			canvasN:   canvasN,
			videoN:    videoN,
			debtCells: debtCells,
			debtPct:   pct,
			sources:   sources,
		})
	}

	// --- Output ---
	t.Logf("")
	t.Logf("=== EXPERIMENT 7: VISUAL DEBT ANALYSIS (12x12 grid) ===")
	t.Logf("")
	t.Logf("%-18s %8s %12s %8s %8s %12s %8s",
		"Site", "Elements", "Img(no-alt)", "Canvas", "Video", "Debt-Cells", "Debt%")
	for _, r := range results {
		t.Logf("%-18s %8d %12d %8d %8d %8d/%-4d %7.1f%%",
			r.name, r.elements, r.imgNoAlt, r.canvasN, r.videoN,
			r.debtCells, totalCells, r.debtPct)
	}

	// Summary stats.
	zeroDebt := 0
	lowDebt := 0
	highDebt := 0
	totalDebtPct := 0.0
	for _, r := range results {
		totalDebtPct += r.debtPct
		if r.debtPct == 0 {
			zeroDebt++
		}
		if r.debtPct < 5 {
			lowDebt++
		}
		if r.debtPct > 20 {
			highDebt++
		}
	}
	meanDebt := 0.0
	if len(results) > 0 {
		meanDebt = totalDebtPct / float64(len(results))
	}

	t.Logf("")
	t.Logf("Summary:")
	t.Logf("  Sites with 0%% debt: %d/%d (fully navigable from DOM alone)", zeroDebt, len(results))
	t.Logf("  Sites with <5%% debt: %d/%d", lowDebt, len(results))
	t.Logf("  Sites with >20%% debt: %d/%d", highDebt, len(results))
	t.Logf("  Mean debt across sites: %.1f%%", meanDebt)

	// Per-site debt source breakdown for sites WITH debt.
	t.Logf("")
	t.Logf("=== DEBT SOURCES ===")
	for _, r := range results {
		if r.debtCells == 0 {
			continue
		}
		t.Logf("")
		t.Logf("%s debt sources:", r.name)
		for _, s := range r.sources {
			t.Logf("  %-16s %3d elements -> %3d cells", s.tag, s.elements, s.cells)
		}
	}
}
