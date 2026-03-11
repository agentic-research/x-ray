// cairn.go — CairnCartographer: Leech lattice visual tokenization for zone segmentation.
//
// Unlike TropicalCartographer (NJ tree on tropical distance matrix, O(N³)),
// CairnCartographer uses error-correcting codes to project visual features
// onto lattice points. Elements that share a lattice point + structural
// ancestor are grouped into zones. No NJ, no 500-element cap.
//
// The "Semantic Gearbox" provides multi-resolution quantization:
//   Gear 1: 4D Tetracode [4,2,3] — 9 codewords (coarsest)
//   Gear 3: 12D Ternary Golay [12,6,6] — 729 codewords
//   Gear 5: 24D Leech lattice Λ₂₄ — continuous lattice (default)
//   Gear 6: 32D Barnes-Wall BW₃₂ — finest resolution

package cartographer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"log"
	"os"
	"sort"
)

// CairnCartographer implements api.SchemaGenerator using Leech lattice
// visual tokenization instead of tropical neighbor-joining.
type CairnCartographer struct {
	// Gear selects the quantization resolution (1, 3, 5, 6). Default: 5 (Leech).
	Gear int

	// Scale multiplier for mapping feature magnitudes to lattice spacing.
	// Higher = more zones (finer discrimination). Default: 10.0.
	Scale float64

	// GridSize controls the screenshot sampling grid. Default: 12.
	GridSize int

	// MinZones / MaxZones control zone count bounds. Defaults: 3 / 7.
	MinZones int
	MaxZones int

	// SheafFolding enables H⁰ sheaf-based zone folding (ADR-003).
	// When true, zones are merged based on Čech coboundary consistency
	// instead of pure spatial proximity. Default: false.
	SheafFolding bool

	// CurvatureDetection enables H¹ contour detection (ADR-004).
	// When true, SO(2) transport maps detect visual contours between
	// zones and annotate mounts with boundary strengths. Default: false.
	CurvatureDetection bool
}

// GenerateSchema implements api.SchemaGenerator.
func (cc *CairnCartographer) GenerateSchema(
	ctx context.Context,
	screenshot []byte,
	mimeType, summary string,
) (string, error) {
	gear := cc.Gear
	if gear == 0 {
		gear = 5
	}
	scale := cc.Scale
	if scale == 0 {
		scale = 10.0
	}
	gridSize := cc.GridSize
	if gridSize == 0 {
		gridSize = 12
	}
	minZ := cc.MinZones
	if minZ <= 0 {
		minZ = 3
	}
	maxZ := cc.MaxZones
	if maxZ <= 0 {
		maxZ = 7
	}

	if os.Getenv("XRAY_DEBUG") == "1" {
		log.Printf("CairnCartographer: generating schema (gear=%d, scale=%.1f, grid=%d)", gear, scale, gridSize)
	}

	// Step 1: Parse DOM summary (reuse from tropical.go)
	elements := parseElements(summary)
	if len(elements) == 0 {
		return "", fmt.Errorf("no elements found in summary")
	}

	// Step 2: Prefilter (reuse from tropical.go — still useful for reducing noise)
	if len(elements) > 2000 {
		elements = prefilterElements(elements, 2000)
		log.Printf("CairnCartographer: pre-filtered to %d elements", len(elements))
	}

	// Step 3: Decode screenshot and extract grid features
	var cellProjections map[string]string // gridKey → zoneKey
	var cells []CairnGridCell             // retained for sheaf/curvature
	var curvature *CurvatureResult
	if len(screenshot) > 0 {
		img, _, err := image.Decode(bytes.NewReader(screenshot))
		if err != nil {
			log.Printf("CairnCartographer: screenshot decode failed: %v (falling back to DOM-only)", err)
		} else {
			cells = ExtractFusedFeatures(img, elements, gridSize)
			CairnNormalizeFeatures(cells)
			cellProjections = projectCells(cells, gear, scale, img.Bounds())

			// Step 3b: Compute curvature if enabled (ADR-004)
			if cc.CurvatureDetection {
				cr := ComputeCurvature(cells, gridSize)
				curvature = &cr
				if os.Getenv("XRAY_DEBUG") == "1" {
					log.Printf("CairnCartographer: H¹ curvature: %d contour cells, %d contour edges, H¹=%d",
						len(cr.ContourCells), len(cr.ContourEdges), cr.H1Dim)
				}
			}
		}
	}

	// Step 4: Assign visual zone keys to elements
	visualTypes := elementVisualTypes(elements, cellProjections)

	// Step 5: Build DOM subtree groups
	zones := buildDOMSubtreeGroups(elements, visualTypes)

	if len(zones) == 0 {
		// Fallback: one zone per structural container
		zones = structuralFallbackZones(elements)
	}

	// Step 6: Fold/merge zones to target range
	if cc.SheafFolding && len(cells) > 0 {
		// ADR-003: sheaf-based zone folding via Čech H⁰
		zones = FoldZonesBySheaf(zones, elements, cells, gridSize, minZ, maxZ)
	} else {
		zones = foldCairnZones(zones, elements, minZ, maxZ)
	}

	if len(zones) == 0 {
		return "", fmt.Errorf("no zones produced from %d elements", len(elements))
	}

	// Step 7: Build mounts (reuse from tropical.go)
	layout := layoutThresholds{
		headerMaxY: 0.15,
		footerMinY: 0.85,
		sidebarW:   0.2,
	}
	mounts := buildMounts(zones, elements, layout)

	// Step 7b: Annotate zone boundaries with curvature data (ADR-004)
	if curvature != nil {
		boundaries := AnnotateZoneBoundaries(zones, *curvature, elements, gridSize)
		for zi, b := range boundaries {
			if zi < len(mounts) {
				mounts[zi].Boundaries = b
			}
		}
	}

	// Step 8: Marshal
	output := struct {
		Mounts []tropicalMount `json:"mounts"`
	}{Mounts: mounts}

	data, err := json.Marshal(output)
	if err != nil {
		return "", fmt.Errorf("marshal schema: %w", err)
	}

	if os.Getenv("XRAY_DEBUG") == "1" {
		log.Printf("CairnCartographer: %d zones from %d elements (gear=%d)", len(mounts), len(elements), gear)
	}
	return string(data), nil
}

// projectCells projects grid cells through the selected gear and returns
// a map from "row,col" → zone key string (the lattice point fingerprint).
func projectCells(cells []CairnGridCell, gear int, scale float64, imgBounds image.Rectangle) map[string]string {
	result := make(map[string]string, len(cells))
	for _, c := range cells {
		key := fmt.Sprintf("%d,%d", c.Row, c.Col)
		var zoneKey string

		switch gear {
		case 1:
			proj := ProjectToGear1(c.Features)
			cw := QuantizeTetracode(proj)
			zoneKey = fmt.Sprintf("t%v", cw)
		case 3:
			r := ProjectToGear3(c.Features)
			zoneKey = fmt.Sprintf("m%v", r.Codeword)
		case 6:
			r := ProjectToGear6(c.Features, scale)
			// Use first 8 coords as fingerprint (full 32D is too verbose)
			var fp [8]float64
			for i := 0; i < 8; i++ {
				fp[i] = r.BW32Point[i]
			}
			zoneKey = fmt.Sprintf("b%v", fp)
		default: // gear 5 (Leech)
			r := ProjectToLeech(c.Features, scale)
			// Use first 8 coords as fingerprint
			var fp [8]float64
			for i := 0; i < 8; i++ {
				fp[i] = r.LeechPoint[i]
			}
			zoneKey = fmt.Sprintf("l%v", fp)
		}

		result[key] = zoneKey
	}
	return result
}

// elementVisualTypes maps each element index to a visual zone key
// by finding which grid cells overlap the element's bounds.
func elementVisualTypes(elements []element, cellProjections map[string]string) map[int]string {
	result := make(map[int]string, len(elements))
	if cellProjections == nil {
		return result
	}

	// Determine grid dimensions from the projection keys
	maxDim := 0
	for key := range cellProjections {
		var r, c int
		if _, err := fmt.Sscanf(key, "%d,%d", &r, &c); err != nil {
			continue
		}
		if r > maxDim {
			maxDim = r
		}
		if c > maxDim {
			maxDim = c
		}
	}
	gridSize := maxDim + 1
	if gridSize == 0 {
		return result
	}

	for i, el := range elements {
		if !el.hasBounds {
			continue
		}

		// Map element center to grid cell
		col := int(el.centerX * float64(gridSize))
		row := int(el.centerY * float64(gridSize))
		if col >= gridSize {
			col = gridSize - 1
		}
		if row >= gridSize {
			row = gridSize - 1
		}
		if col < 0 {
			col = 0
		}
		if row < 0 {
			row = 0
		}

		key := fmt.Sprintf("%d,%d", row, col)
		if zk, ok := cellProjections[key]; ok {
			result[i] = zk
		}
	}

	return result
}

// buildDOMSubtreeGroups groups elements by (structural ancestor, visual zone key).
// Each unique combination becomes a zone.
func buildDOMSubtreeGroups(elements []element, visualTypes map[int]string) []zone {
	// Build parent index for walking up the DOM
	idToIdx := make(map[string]int, len(elements))
	for i, el := range elements {
		idToIdx[el.id] = i
	}

	type groupKey struct {
		ancestor string
		visual   string
	}
	groups := make(map[groupKey][]int)

	for i, el := range elements {
		ancestor := findStructuralAncestor(el, elements, idToIdx)
		visual := visualTypes[i]
		if visual == "" {
			visual = "_dom" // no visual data — group by structure alone
		}
		gk := groupKey{ancestor: ancestor, visual: visual}
		groups[gk] = append(groups[gk], i)
	}

	var zones []zone
	for _, indices := range groups {
		if len(indices) == 0 {
			continue
		}
		z := zone{
			rootIdx: indices[0],
			elems:   indices,
		}
		computeZoneFeatures(&z, elements)
		zones = append(zones, z)
	}

	// Sort by position so map iteration order doesn't affect output.
	sortZonesByPosition(zones)

	return zones
}

// findStructuralAncestor walks up the parent chain to find the nearest
// structural container tag (nav, main, section, header, footer, etc.).
func findStructuralAncestor(el element, elements []element, idToIdx map[string]int) string {
	// Check self first
	if structuralTags[el.tag] {
		return el.id
	}

	// Walk up parent chain (max 20 levels to avoid infinite loops)
	current := el.parentID
	for depth := 0; depth < 20; depth++ {
		if current == "" || current == "none" {
			break
		}
		idx, ok := idToIdx[current]
		if !ok {
			break
		}
		parent := elements[idx]
		if structuralTags[parent.tag] {
			return parent.id
		}
		current = parent.parentID
	}

	// No structural ancestor found — use "body" as catch-all
	return "body"
}

// structuralFallbackZones creates one zone per structural container
// when no screenshot is available. Pure DOM grouping.
func structuralFallbackZones(elements []element) []zone {
	idToIdx := make(map[string]int, len(elements))
	for i, el := range elements {
		idToIdx[el.id] = i
	}

	groups := make(map[string][]int)
	for i, el := range elements {
		ancestor := findStructuralAncestor(el, elements, idToIdx)
		groups[ancestor] = append(groups[ancestor], i)
	}

	var zones []zone
	for _, indices := range groups {
		if len(indices) == 0 {
			continue
		}
		z := zone{
			rootIdx: indices[0],
			elems:   indices,
		}
		computeZoneFeatures(&z, elements)
		zones = append(zones, z)
	}

	// Sort by position so map iteration order doesn't affect output.
	sortZonesByPosition(zones)

	return zones
}

// foldCairnZones merges or splits zones to reach the target range [minZ, maxZ].
func foldCairnZones(zones []zone, elements []element, minZ, maxZ int) []zone {
	if len(zones) == 0 {
		return zones
	}

	// Too many? Agglomerative merge by spatial proximity
	for len(zones) > maxZ {
		zones = mergeClosestZones(zones, elements)
	}

	// Too few? Can't really split without more info — accept what we have
	// (the structural grouping provides a natural floor)

	return zones
}

// sortZonesByPosition sorts zones top-to-bottom, left-to-right for deterministic output.
// Uses rootIdx as tiebreaker when centers are identical.
func sortZonesByPosition(zones []zone) {
	sort.Slice(zones, func(i, j int) bool {
		if zones[i].centerY != zones[j].centerY {
			return zones[i].centerY < zones[j].centerY
		}
		if zones[i].centerX != zones[j].centerX {
			return zones[i].centerX < zones[j].centerX
		}
		return zones[i].rootIdx < zones[j].rootIdx
	})
}

// mergeClosestZones merges the two spatially closest zones.
func mergeClosestZones(zones []zone, elements []element) []zone {
	if len(zones) <= 1 {
		return zones
	}

	// Find the pair with smallest center-to-center distance
	bestI, bestJ := 0, 1
	bestDist := 1e18
	for i := 0; i < len(zones); i++ {
		for j := i + 1; j < len(zones); j++ {
			dx := zones[i].centerX - zones[j].centerX
			dy := zones[i].centerY - zones[j].centerY
			d := dx*dx + dy*dy
			if d < bestDist {
				bestDist = d
				bestI = i
				bestJ = j
			}
		}
	}

	// Merge j into i
	merged := zones[bestI]
	merged.elems = append(merged.elems, zones[bestJ].elems...)
	computeZoneFeatures(&merged, elements)

	// Build new slice without j
	var result []zone
	for k, z := range zones {
		if k == bestI {
			result = append(result, merged)
		} else if k != bestJ {
			result = append(result, z)
		}
	}

	sortZonesByPosition(result)
	return result
}
