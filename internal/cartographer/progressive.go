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
	"time"
)

// ProgressResult is emitted on the Progress channel after each pipeline stage.
type ProgressResult struct {
	Stage   int           // 0=DOM, 1=Gear1, 2=Gear3, 3=Gear5, 4=Tropical, 5=Sheaf
	Schema  string        // valid CartographerOutput JSON at this stage's resolution
	IsFinal bool          // true for the last emission
	Latency time.Duration // cumulative wall time from GenerateSchema entry
}

// ProgressiveCartographer implements api.SchemaGenerator with staged emission.
// Each gearbox stage produces a valid zone tree. Intermediate results are sent
// on the Progress channel (if non-nil) so the navigator can start working
// before the full-precision result is ready.
type ProgressiveCartographer struct {
	// Scale multiplier for lattice spacing. Default: 10.0.
	Scale float64

	// GridSize controls screenshot sampling grid. Default: 12.
	GridSize int

	// MinZones / MaxZones control zone count bounds. Defaults: 3 / 7.
	MinZones int
	MaxZones int

	// Progress receives intermediate results. If nil, only the final
	// result is returned from GenerateSchema (backward compatible).
	Progress chan<- ProgressResult
}

// StageName returns a human-readable label for a stage number.
func StageName(stage int) string {
	switch stage {
	case 0:
		return "DOM"
	case 1:
		return "Gear1-Tetracode"
	case 2:
		return "Gear3-TernaryGolay"
	case 3:
		return "Gear5-Leech"
	case 4:
		return "Tropical-Centroid"
	case 5:
		return "Sheaf-H0"
	default:
		return fmt.Sprintf("Stage%d", stage)
	}
}

// GenerateSchema implements api.SchemaGenerator.
func (pc *ProgressiveCartographer) GenerateSchema(
	ctx context.Context,
	screenshot []byte,
	mimeType, summary string,
) (string, error) {
	start := time.Now()

	scale := pc.Scale
	if scale == 0 {
		scale = 10.0
	}
	gridSize := pc.GridSize
	if gridSize == 0 {
		gridSize = 12
	}
	minZ := pc.MinZones
	if minZ <= 0 {
		minZ = 3
	}
	maxZ := pc.MaxZones
	if maxZ <= 0 {
		maxZ = 7
	}

	debug := os.Getenv("XRAY_DEBUG") == "1"

	// emit sends a ProgressResult if the channel is set.
	emit := func(stage int, schema string, isFinal bool) {
		if pc.Progress != nil {
			pc.Progress <- ProgressResult{
				Stage:   stage,
				Schema:  schema,
				IsFinal: isFinal,
				Latency: time.Since(start),
			}
		}
		if debug {
			log.Printf("ProgressiveCartographer: stage %d (%s) in %s",
				stage, StageName(stage), time.Since(start))
		}
	}

	// marshalZones converts zones + elements into a CartographerOutput JSON string.
	marshalZones := func(zones []zone, elements []element) (string, error) {
		layout := layoutThresholds{headerMaxY: 0.15, footerMinY: 0.85, sidebarW: 0.2}
		mounts := buildMounts(zones, elements, layout)
		output := struct {
			Mounts []tropicalMount `json:"mounts"`
		}{Mounts: mounts}
		data, err := json.Marshal(output)
		if err != nil {
			return "", fmt.Errorf("marshal schema: %w", err)
		}
		return string(data), nil
	}

	// --- Stage 0: DOM parse + structural grouping ---
	elements := parseElements(summary)
	if len(elements) == 0 {
		return "", fmt.Errorf("no elements found in summary")
	}
	if len(elements) > 2000 {
		elements = prefilterElements(elements, 2000)
	}

	domZones := structuralFallbackZones(elements)
	domZones = foldCairnZones(domZones, elements, minZ, maxZ)
	if len(domZones) == 0 {
		return "", fmt.Errorf("no zones produced from %d elements", len(elements))
	}

	domSchema, err := marshalZones(domZones, elements)
	if err != nil {
		return "", err
	}

	// If no screenshot, DOM is all we can do.
	if len(screenshot) == 0 {
		emit(0, domSchema, true)
		return domSchema, nil
	}
	emit(0, domSchema, false)

	// Decode screenshot for visual stages
	img, _, err := image.Decode(bytes.NewReader(screenshot))
	if err != nil {
		log.Printf("ProgressiveCartographer: screenshot decode failed: %v (returning DOM-only)", err)
		emit(0, domSchema, true) // re-emit as final
		return domSchema, nil
	}

	// Extract fused features (shared across gears 1-5)
	cells := ExtractFusedFeatures(img, elements, gridSize)
	CairnNormalizeFeatures(cells)

	// --- Stage 1: Gear 1 (Tetracode 4D) ---
	g1Projections := projectCells(cells, 1, scale, img.Bounds())
	g1Visual := elementVisualTypes(elements, g1Projections)
	g1Zones := buildDOMSubtreeGroups(elements, g1Visual)
	g1Zones = foldCairnZones(g1Zones, elements, minZ, maxZ)

	var lastSchema string
	if len(g1Zones) > 0 {
		lastSchema, err = marshalZones(g1Zones, elements)
		if err != nil {
			return "", err
		}
		emit(1, lastSchema, false)
	} else {
		lastSchema = domSchema
		emit(1, lastSchema, false)
	}

	// --- Stage 2: Gear 3 (Ternary Golay 12D) ---
	g3Projections := projectCells(cells, 3, scale, img.Bounds())
	g3Visual := elementVisualTypes(elements, g3Projections)
	g3Zones := buildDOMSubtreeGroups(elements, g3Visual)
	g3Zones = foldCairnZones(g3Zones, elements, minZ, maxZ)

	if len(g3Zones) > 0 {
		lastSchema, err = marshalZones(g3Zones, elements)
		if err != nil {
			return "", err
		}
		emit(2, lastSchema, false)
	} else {
		emit(2, lastSchema, false)
	}

	// --- Stage 3: Gear 5 (Leech 24D) ---
	g5Projections := projectCells(cells, 5, scale, img.Bounds())
	g5Visual := elementVisualTypes(elements, g5Projections)
	g5Zones := buildDOMSubtreeGroups(elements, g5Visual)
	g5Zones = foldCairnZones(g5Zones, elements, minZ, maxZ)

	if len(g5Zones) > 0 {
		lastSchema, err = marshalZones(g5Zones, elements)
		if err != nil {
			return "", err
		}
		emit(3, lastSchema, false)
	} else {
		emit(3, lastSchema, false)
	}

	// --- Stage 4: Tropical NJ on zone centroids ---
	// Run tropical NJ on zone centroids, not raw elements.
	// K = number of zones (5-20), so O(K^3) is fast.
	tropicalZones := g5Zones
	if len(tropicalZones) > 0 {
		refined := tropicalRefineZones(tropicalZones, elements, cells, gridSize)
		if len(refined) > 0 {
			tropicalZones = refined
			tropicalZones = foldCairnZones(tropicalZones, elements, minZ, maxZ)
		}
	}

	if len(tropicalZones) > 0 {
		lastSchema, err = marshalZones(tropicalZones, elements)
		if err != nil {
			return "", err
		}
		emit(4, lastSchema, false)
	} else {
		emit(4, lastSchema, false)
	}

	// --- Stage 5: Sheaf H^0 folding ---
	finalZones := tropicalZones
	if len(finalZones) > 0 && len(cells) > 0 {
		sheafZones := FoldZonesBySheaf(finalZones, elements, cells, gridSize, minZ, maxZ)
		if len(sheafZones) > 0 {
			finalZones = sheafZones
		}
	}

	if len(finalZones) == 0 {
		// Fall back to last good result
		emit(5, lastSchema, true)
		return lastSchema, nil
	}

	finalSchema, err := marshalZones(finalZones, elements)
	if err != nil {
		return "", err
	}
	emit(5, finalSchema, true)

	if debug {
		log.Printf("ProgressiveCartographer: total %s, %d final zones from %d elements",
			time.Since(start), len(finalZones), len(elements))
	}
	return finalSchema, nil
}
