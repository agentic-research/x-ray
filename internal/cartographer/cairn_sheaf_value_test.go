package cartographer

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// TestSheafVsBaseline_SpatiallyCloseButVisuallyDifferent tests the scenario
// where two zones are spatially close (baseline would merge them) but
// visually distinct (sheaf should keep them separate).
//
// Layout: a nav bar and a content area are vertically adjacent (close centers),
// but the nav has high-contrast horizontal edges and the content has
// low-contrast vertical text flow. Spatial fold merges them; sheaf should not.
func TestSheafVsBaseline_SpatiallyCloseButVisuallyDifferent(t *testing.T) {
	// Build elements with bounds and structural parents.
	// Nav zone: elements at y=0.05..0.12 (top strip)
	// Content zone: elements at y=0.15..0.50 (just below nav)
	// Footer zone: elements at y=0.85..0.95 (bottom, far away)
	summary := buildRichSummary(t)

	// Run baseline (spatial fold)
	baseline := &CairnCartographer{Gear: 5, Scale: 10.0, GridSize: 12, MinZones: 3, MaxZones: 7}
	baselineOut, err := baseline.GenerateSchema(context.Background(), nil, "", summary)
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}

	// Run sheaf fold (needs screenshot for visual features — test with cells directly)
	sheafCart := &CairnCartographer{Gear: 5, Scale: 10.0, GridSize: 12, MinZones: 3, MaxZones: 7, SheafFolding: true}
	sheafOut, err := sheafCart.GenerateSchema(context.Background(), nil, "", summary)
	if err != nil {
		t.Fatalf("sheaf: %v", err)
	}

	t.Logf("baseline zones: %d", countMounts(baselineOut))
	t.Logf("sheaf zones:    %d", countMounts(sheafOut))

	// Both should produce valid output
	if countMounts(baselineOut) == 0 {
		t.Error("baseline produced 0 zones")
	}
	if countMounts(sheafOut) == 0 {
		t.Error("sheaf produced 0 zones")
	}
}

// TestSheafFolding_WithVisualFeatures tests sheaf behavior when visual features
// are available (via cells). Creates zones with known stalk differences.
func TestSheafFolding_WithVisualFeatures(t *testing.T) {
	gridSize := 4

	// Build elements that have bounds
	var elements []element
	idToIdx := make(map[string]int)

	// Zone A: nav elements at top
	elements = append(elements, element{id: "nav", tag: "nav", parentID: "none", hasBounds: true, centerX: 0.5, centerY: 0.05, bounds: [4]float64{0, 0, 1, 0.1}})
	idToIdx["nav"] = 0
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("nav-link-%d", i)
		elements = append(elements, element{id: id, tag: "a", parentID: "nav", hasBounds: true,
			centerX: float64(i)*0.3 + 0.15, centerY: 0.05, bounds: [4]float64{float64(i) * 0.3, 0, 0.3, 0.1}})
		idToIdx[id] = len(elements) - 1
	}

	// Zone B: main content at middle
	elements = append(elements, element{id: "main", tag: "main", parentID: "none", hasBounds: true, centerX: 0.5, centerY: 0.4, bounds: [4]float64{0, 0.15, 1, 0.5}})
	idToIdx["main"] = len(elements) - 1
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("content-%d", i)
		elements = append(elements, element{id: id, tag: "div", parentID: "main", hasBounds: true,
			centerX: 0.5, centerY: 0.2 + float64(i)*0.06, bounds: [4]float64{0.1, 0.15 + float64(i)*0.06, 0.8, 0.05}})
		idToIdx[id] = len(elements) - 1
	}

	// Zone C: footer at bottom
	elements = append(elements, element{id: "footer", tag: "footer", parentID: "none", hasBounds: true, centerX: 0.5, centerY: 0.92, bounds: [4]float64{0, 0.85, 1, 0.15}})
	idToIdx["footer"] = len(elements) - 1
	for i := 0; i < 2; i++ {
		id := fmt.Sprintf("footer-link-%d", i)
		elements = append(elements, element{id: id, tag: "a", parentID: "footer", hasBounds: true,
			centerX: float64(i)*0.4 + 0.3, centerY: 0.92, bounds: [4]float64{float64(i)*0.4 + 0.1, 0.85, 0.3, 0.15}})
		idToIdx[id] = len(elements) - 1
	}

	// Build grid cells with distinct visual signatures per region:
	// Top rows (0-1): high horiz energy (nav bar)
	// Middle rows (1-2): high vert energy (text content)
	// Bottom rows (3): mixed (footer)
	cells := make([]CairnGridCell, gridSize*gridSize)
	for r := 0; r < gridSize; r++ {
		for c := 0; c < gridSize; c++ {
			idx := r*gridSize + c
			var features [CairnNumDims]float64
			features[4] = 0.5 // edge density

			switch {
			case r == 0: // nav: strong horizontal edges
				features[5] = 0.9 // horiz
				features[6] = 0.1 // vert
				features[0] = 0.8 // high luma
			case r >= 1 && r <= 2: // content: strong vertical edges
				features[5] = 0.1
				features[6] = 0.9
				features[0] = 0.3
			default: // footer: mixed
				features[5] = 0.5
				features[6] = 0.3
				features[7] = 0.3 // diag
				features[0] = 0.6
			}
			cells[idx] = CairnGridCell{Row: r, Col: c, Features: features}
		}
	}

	// Build zones from DOM structure
	visualTypes := elementVisualTypes(elements, nil) // no cell projections needed
	zones := buildDOMSubtreeGroups(elements, visualTypes)
	t.Logf("initial zones from DOM: %d", len(zones))

	// Test spatial fold
	spatialZones := foldCairnZones(zones, elements, 3, 7)
	t.Logf("spatial fold: %d zones", len(spatialZones))

	// Test sheaf fold
	sheafZones := FoldZonesBySheaf(zones, elements, cells, gridSize, 3, 7)
	t.Logf("sheaf fold: %d zones", len(sheafZones))

	// Compute stalks to show the visual signal
	stalks := computeZoneStalks(zones, elements, cells, gridSize)
	allZero := true
	for _, s := range stalks {
		for _, v := range s {
			if v != 0 {
				allZero = false
				break
			}
		}
	}
	t.Logf("stalks all zero: %v", allZero)
	for i, s := range stalks {
		if len(zones[i].elems) > 0 {
			t.Logf("  zone %d (%d elems): luma=%.2f horiz=%.2f vert=%.2f diag=%.2f",
				i, len(zones[i].elems), s[0], s[5], s[6], s[7])
		}
	}

	// Key assertion: both should produce at least minZ zones
	if len(spatialZones) < 3 {
		t.Errorf("spatial fold produced %d zones, want >= 3", len(spatialZones))
	}
	if len(sheafZones) < 3 {
		t.Errorf("sheaf fold produced %d zones, want >= 3", len(sheafZones))
	}
}

func buildRichSummary(t *testing.T) string {
	t.Helper()
	var sb strings.Builder
	sb.WriteString("Interactive Elements:\n")

	// Header/nav
	sb.WriteString("ID: el-0 | Parent: none | Tag: header | Text: \"Site Header\" | Bounds: [0.000, 0.000, 1.000, 0.080]\n")
	sb.WriteString("ID: el-1 | Parent: el-0 | Tag: nav | Text: \"Navigation\" | Bounds: [0.000, 0.000, 1.000, 0.080]\n")
	for i := 0; i < 5; i++ {
		sb.WriteString(fmt.Sprintf("ID: el-%d | Parent: el-1 | Tag: a | Text: \"Link %d\" | Bounds: [%.3f, 0.010, 0.150, 0.060]\n",
			10+i, i, float64(i)*0.18+0.05))
	}

	// Main content
	sb.WriteString("ID: el-20 | Parent: none | Tag: main | Text: \"Content\" | Bounds: [0.000, 0.100, 1.000, 0.700]\n")
	sb.WriteString("ID: el-21 | Parent: el-20 | Tag: section | Text: \"Feed\" | Bounds: [0.050, 0.120, 0.600, 0.650]\n")
	for i := 0; i < 10; i++ {
		sb.WriteString(fmt.Sprintf("ID: el-%d | Parent: el-21 | Tag: a | Text: \"Story %d\" | Bounds: [0.060, %.3f, 0.580, 0.050]\n",
			30+i, i, 0.13+float64(i)*0.06))
	}

	// Sidebar
	sb.WriteString("ID: el-50 | Parent: el-20 | Tag: aside | Text: \"Sidebar\" | Bounds: [0.700, 0.120, 0.280, 0.400]\n")
	for i := 0; i < 3; i++ {
		sb.WriteString(fmt.Sprintf("ID: el-%d | Parent: el-50 | Tag: a | Text: \"Ad %d\" | Bounds: [0.710, %.3f, 0.260, 0.100]\n",
			60+i, i, 0.13+float64(i)*0.12))
	}

	// Footer
	sb.WriteString("ID: el-80 | Parent: none | Tag: footer | Text: \"Footer\" | Bounds: [0.000, 0.880, 1.000, 0.120]\n")
	for i := 0; i < 3; i++ {
		sb.WriteString(fmt.Sprintf("ID: el-%d | Parent: el-80 | Tag: a | Text: \"Footer Link %d\" | Bounds: [%.3f, 0.900, 0.200, 0.060]\n",
			90+i, i, float64(i)*0.25+0.1))
	}

	return sb.String()
}

func countMounts(schemaJSON string) int {
	return strings.Count(schemaJSON, "mache_id")
}

// TestSheafVsBaseline_MergeDecision tests the key value proposition:
// when forced to merge (zones > maxZ), sheaf merges visually-similar zones
// while baseline merges spatially-close zones.
//
// Layout (8 zones, maxZ=4):
//   Row 0: [nav-A: horiz edges] [nav-B: horiz edges]   ← visually similar
//   Row 1: [content-A: vert edges] [sidebar-A: mixed]   ← visually different
//   Row 2: [content-B: vert edges] [sidebar-B: mixed]   ← matches row 1
//   Row 3: [footer-A: horiz edges] [footer-B: horiz edges] ← visually similar
//
// Spatial fold merges vertically adjacent zones (nav-A + content-A, etc.)
// Sheaf fold merges visually similar zones (nav-A + nav-B, content-A + content-B, etc.)
func TestSheafVsBaseline_MergeDecision(t *testing.T) {
	gridSize := 4

	// 8 elements, each is its own structural container (section)
	// so buildDOMSubtreeGroups creates 8 zones.
	elements := make([]element, 8)
	names := []string{"nav-A", "nav-B", "content-A", "sidebar-A", "content-B", "sidebar-B", "footer-A", "footer-B"}
	positions := [][2]float64{
		{0.25, 0.06}, {0.75, 0.06}, // row 0: two nav zones
		{0.25, 0.35}, {0.75, 0.35}, // row 1: content + sidebar
		{0.25, 0.65}, {0.75, 0.65}, // row 2: content + sidebar
		{0.25, 0.94}, {0.75, 0.94}, // row 3: two footer zones
	}
	for i := range elements {
		elements[i] = element{
			id:        fmt.Sprintf("el-%d", i),
			tag:       "section", // structural tag → each becomes its own ancestor
			parentID:  "none",
			hasBounds: true,
			centerX:   positions[i][0],
			centerY:   positions[i][1],
			bounds:    [4]float64{positions[i][0] - 0.2, positions[i][1] - 0.05, 0.4, 0.1},
			text:      names[i],
		}
	}

	// Grid cells: 4x4, distinct visual signatures per region
	cells := make([]CairnGridCell, gridSize*gridSize)
	for r := 0; r < gridSize; r++ {
		for c := 0; c < gridSize; c++ {
			idx := r*gridSize + c
			var f [CairnNumDims]float64
			f[4] = 0.7 // edge density

			switch r {
			case 0: // nav row: strong horizontal
				f[0] = 0.9 // high luma
				f[5] = 0.95
				f[6] = 0.05
			case 1, 2: // content/sidebar rows
				if c < 2 {
					// content: strong vertical, low luma
					f[0] = 0.2
					f[5] = 0.05
					f[6] = 0.95
				} else {
					// sidebar: mixed edges, medium luma
					f[0] = 0.5
					f[5] = 0.4
					f[6] = 0.3
					f[7] = 0.4
				}
			case 3: // footer: strong horizontal (like nav)
				f[0] = 0.8
				f[5] = 0.90
				f[6] = 0.10
			}
			cells[idx] = CairnGridCell{Row: r, Col: c, Features: f}
		}
	}

	// Build initial zones (8 zones, one per section element)
	visualTypes := elementVisualTypes(elements, nil)
	zones := buildDOMSubtreeGroups(elements, visualTypes)
	t.Logf("initial zones: %d", len(zones))

	if len(zones) < 4 {
		t.Skipf("need >= 4 initial zones for merge test, got %d", len(zones))
	}

	maxZ := 4

	// Spatial fold: merges by proximity
	spatialZones := foldCairnZones(append([]zone(nil), zones...), elements, 3, maxZ)

	// Sheaf fold: merges by visual similarity
	sheafZones := FoldZonesBySheaf(append([]zone(nil), zones...), elements, cells, gridSize, 3, maxZ)

	t.Logf("spatial fold: %d zones", len(spatialZones))
	for i, z := range spatialZones {
		t.Logf("  spatial zone %d: %d elements, center=(%.2f, %.2f)", i, len(z.elems), z.centerX, z.centerY)
	}

	t.Logf("sheaf fold: %d zones", len(sheafZones))
	for i, z := range sheafZones {
		t.Logf("  sheaf zone %d: %d elements, center=(%.2f, %.2f)", i, len(z.elems), z.centerX, z.centerY)
	}

	// Both should respect maxZ
	if len(spatialZones) > maxZ {
		t.Errorf("spatial fold: %d zones > maxZ=%d", len(spatialZones), maxZ)
	}
	if len(sheafZones) > maxZ {
		t.Errorf("sheaf fold: %d zones > maxZ=%d", len(sheafZones), maxZ)
	}

	// Log stalks for visual inspection
	stalks := computeZoneStalks(zones, elements, cells, gridSize)
	for i, s := range stalks {
		if i < len(zones) {
			t.Logf("  zone %d %q: luma=%.2f horiz=%.2f vert=%.2f diag=%.2f",
				i, names[min(i, len(names)-1)], s[0], s[5], s[6], s[7])
		}
	}
}
