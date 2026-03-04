package cartographer

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Parser tests
// ---------------------------------------------------------------------------

func TestParseElementsOldFormat(t *testing.T) {
	summary := `Interactive Elements:
ID: mache-0 | Tag: a | Text: ""
ID: mache-1 | Tag: a | Text: "Hacker News"
ID: mache-2 | Tag: a | Text: "new"
ID: mache-3 | Tag: button | Text: "submit"`

	elems := parseElements(summary)
	if len(elems) != 4 {
		t.Fatalf("expected 4 elements, got %d", len(elems))
	}

	// Old format should get sequential Y positions
	if elems[0].centerY != 0.0 {
		t.Errorf("first element centerY = %f, want 0.0", elems[0].centerY)
	}
	if elems[3].centerY != 1.0 {
		t.Errorf("last element centerY = %f, want 1.0", elems[3].centerY)
	}

	if elems[1].text != "Hacker News" {
		t.Errorf("element 1 text = %q, want %q", elems[1].text, "Hacker News")
	}
	if elems[3].tag != "button" {
		t.Errorf("element 3 tag = %q, want %q", elems[3].tag, "button")
	}
}

func TestParseElementsEnhancedFormat(t *testing.T) {
	summary := `ID: mache-0 | Color: BLUE | Bounds: [0.1, 0.2, 0.3, 0.05] | Parent: none | Tag: a | Text: "Click here" | Path: div.main > a.link`

	elems := parseElements(summary)
	if len(elems) != 1 {
		t.Fatalf("expected 1 element, got %d", len(elems))
	}

	el := elems[0]
	if el.id != "mache-0" {
		t.Errorf("id = %q, want mache-0", el.id)
	}
	if el.color != "BLUE" {
		t.Errorf("color = %q, want BLUE", el.color)
	}
	if !el.hasBounds {
		t.Fatal("expected hasBounds = true")
	}
	if math.Abs(el.centerX-0.25) > 0.01 {
		t.Errorf("centerX = %f, want ~0.25", el.centerX)
	}
	if math.Abs(el.centerY-0.225) > 0.01 {
		t.Errorf("centerY = %f, want ~0.225", el.centerY)
	}
	if el.parentID != "none" {
		t.Errorf("parentID = %q, want none", el.parentID)
	}
	if len(el.pathParts) != 2 {
		t.Errorf("pathParts len = %d, want 2", len(el.pathParts))
	}
	if el.text != "Click here" {
		t.Errorf("text = %q, want %q", el.text, "Click here")
	}
}

func TestParseBounds(t *testing.T) {
	b, ok := parseBounds("[0.1, 0.2, 0.3, 0.4]")
	if !ok {
		t.Fatal("parseBounds failed")
	}
	if b != [4]float64{0.1, 0.2, 0.3, 0.4} {
		t.Errorf("bounds = %v, want [0.1 0.2 0.3 0.4]", b)
	}

	_, ok = parseBounds("[bad]")
	if ok {
		t.Error("expected parseBounds to fail on bad input")
	}
}

// ---------------------------------------------------------------------------
// Distance tests
// ---------------------------------------------------------------------------

func TestSpatialDistance(t *testing.T) {
	a := &element{hasBounds: true, centerX: 0.0, centerY: 0.0}
	b := &element{hasBounds: true, centerX: 1.0, centerY: 1.0}
	d := spatialDistance(a, b)
	// Diagonal of unit square / sqrt(2) = 1.0
	if math.Abs(d-1.0) > 0.01 {
		t.Errorf("diagonal distance = %f, want ~1.0", d)
	}

	c := &element{hasBounds: true, centerX: 0.5, centerY: 0.5}
	d2 := spatialDistance(a, c)
	if d2 >= d {
		t.Errorf("center distance %f should be less than diagonal %f", d2, d)
	}
}

func TestVisualDistance(t *testing.T) {
	a := &element{hasRGB: true, rgb: [3]float64{0, 0, 0}}
	b := &element{hasRGB: true, rgb: [3]float64{255, 255, 255}}
	d := visualDistance(a, b)
	if math.Abs(d-1.0) > 0.01 {
		t.Errorf("black-to-white distance = %f, want ~1.0", d)
	}

	same := visualDistance(a, a)
	if same != 0.0 {
		t.Errorf("same-to-same distance = %f, want 0.0", same)
	}

	noRGB := &element{hasRGB: false}
	neutral := visualDistance(a, noRGB)
	if neutral != 0.5 {
		t.Errorf("neutral distance = %f, want 0.5", neutral)
	}
}

func TestStructuralDistance(t *testing.T) {
	a := &element{pathParts: []string{"div.main", "ul.list", "li.item"}}
	b := &element{pathParts: []string{"div.main", "ul.list", "li.other"}}
	d := structuralDistance(a, b)
	// 2/3 common prefix → 1 - 2/3 = 0.333
	if math.Abs(d-0.333) > 0.01 {
		t.Errorf("structural distance = %f, want ~0.333", d)
	}

	c := &element{pathParts: []string{"nav.top", "a.link"}}
	d2 := structuralDistance(a, c)
	// 0 common prefix → 1.0
	if math.Abs(d2-1.0) > 0.01 {
		t.Errorf("divergent paths distance = %f, want ~1.0", d2)
	}

	// Old format fallback
	noPath1 := &element{tag: "a"}
	noPath2 := &element{tag: "a"}
	noPath3 := &element{tag: "button"}
	if structuralDistance(noPath1, noPath2) != 0.2 {
		t.Error("same tag should give 0.2")
	}
	if structuralDistance(noPath1, noPath3) != 0.8 {
		t.Error("different tag should give 0.8")
	}
}

func TestTropicalDistanceIsMax(t *testing.T) {
	a := &element{
		hasBounds: true, centerX: 0.0, centerY: 0.0,
		hasRGB: true, rgb: [3]float64{0, 0, 0},
		pathParts: []string{"div.a"},
	}
	b := &element{
		hasBounds: true, centerX: 1.0, centerY: 1.0,
		hasRGB: true, rgb: [3]float64{0, 0, 0}, // same color
		pathParts: []string{"div.a"}, // same path
	}

	d := tropicalDistance(a, b)
	ds := spatialDistance(a, b)
	dv := visualDistance(a, b)
	dt := structuralDistance(a, b)
	expected := math.Max(ds, math.Max(dv, dt))

	if math.Abs(d-expected) > 0.001 {
		t.Errorf("tropical d=%f, expected max(%f,%f,%f)=%f", d, ds, dv, dt, expected)
	}

	// Spatial dominates here (color same, path same, but far apart)
	if d != ds {
		t.Errorf("expected spatial to dominate: d=%f, ds=%f", d, ds)
	}
}

// ---------------------------------------------------------------------------
// Neighbor-joining tests
// ---------------------------------------------------------------------------

func TestNeighborJoiningTrivial(t *testing.T) {
	// 1 element
	tree := neighborJoining([][]float64{{0}}, 1)
	if len(tree.elements) != 1 {
		t.Errorf("1-element tree should have 1 element, got %d", len(tree.elements))
	}

	// 2 elements
	dist := [][]float64{{0, 0.5}, {0.5, 0}}
	tree2 := neighborJoining(dist, 2)
	if len(tree2.elements) != 2 {
		t.Errorf("2-element tree should have 2 elements, got %d", len(tree2.elements))
	}
	if len(tree2.children) != 2 {
		t.Errorf("2-element tree root should have 2 children, got %d", len(tree2.children))
	}
}

func TestNeighborJoiningFourElements(t *testing.T) {
	// 4 elements with clear 2+2 clustering:
	// (0,1) are close, (2,3) are close, clusters are far apart
	dist := [][]float64{
		{0.0, 0.1, 0.9, 0.9},
		{0.1, 0.0, 0.9, 0.9},
		{0.9, 0.9, 0.0, 0.1},
		{0.9, 0.9, 0.1, 0.0},
	}

	tree := neighborJoining(dist, 4)
	if len(tree.elements) != 4 {
		t.Fatalf("expected 4 elements in tree, got %d", len(tree.elements))
	}

	// The tree should group {0,1} and {2,3} together
	if len(tree.children) != 2 {
		t.Fatalf("expected 2 children at root, got %d", len(tree.children))
	}

	c0 := tree.children[0].elements
	c1 := tree.children[1].elements
	// One child should contain {0,1}, the other {2,3}
	cluster0 := containsAll(c0, []int{0, 1}) && containsAll(c1, []int{2, 3})
	cluster1 := containsAll(c0, []int{2, 3}) && containsAll(c1, []int{0, 1})
	if !cluster0 && !cluster1 {
		t.Errorf("expected {0,1} and {2,3} clusters, got %v and %v", c0, c1)
	}
}

func containsAll(haystack, needles []int) bool {
	set := map[int]bool{}
	for _, v := range haystack {
		set[v] = true
	}
	for _, n := range needles {
		if !set[n] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Zone cutting tests
// ---------------------------------------------------------------------------

func TestCutTreeProducesValidZones(t *testing.T) {
	// 8 elements, spread along Y axis
	elements := make([]element, 8)
	for i := range elements {
		elements[i] = element{
			id:      fmt.Sprintf("mache-%d", i),
			tag:     "a",
			centerX: 0.5,
			centerY: float64(i) / 7.0,
		}
	}

	dist := buildDistanceMatrix(elements)
	tree := neighborJoining(dist, len(elements))
	zones := cutTree(tree, elements, 3, 5)

	if len(zones) < 3 || len(zones) > 5 {
		t.Errorf("expected 3-5 zones, got %d", len(zones))
	}

	// Every element should appear in exactly one zone
	seen := map[int]bool{}
	for _, z := range zones {
		for _, idx := range z.elems {
			if seen[idx] {
				t.Errorf("element %d appears in multiple zones", idx)
			}
			seen[idx] = true
		}
	}
	if len(seen) != len(elements) {
		t.Errorf("expected %d unique elements across zones, got %d", len(elements), len(seen))
	}
}

// ---------------------------------------------------------------------------
// Split by spatial gap test
// ---------------------------------------------------------------------------

func TestSplitZoneBySpatialGap(t *testing.T) {
	elements := []element{
		{centerX: 0.5, centerY: 0.1},
		{centerX: 0.5, centerY: 0.2},
		// big gap here
		{centerX: 0.5, centerY: 0.8},
		{centerX: 0.5, centerY: 0.9},
	}
	z := zone{elems: []int{0, 1, 2, 3}}
	z1, z2 := splitZoneBySpatialGap(z, elements)

	if len(z1.elems) != 2 || len(z2.elems) != 2 {
		t.Errorf("expected 2+2 split, got %d+%d", len(z1.elems), len(z2.elems))
	}
}

// ---------------------------------------------------------------------------
// List zone detection
// ---------------------------------------------------------------------------

func TestDetectListZoneByTag(t *testing.T) {
	elements := []element{
		{tag: "a", text: "Story 1"},
		{tag: "a", text: "Story 2"},
		{tag: "a", text: "Story 3"},
		{tag: "a", text: "Story 4"},
		{tag: "button", text: "More"},
	}
	elems := []int{0, 1, 2, 3, 4}

	isList, items, _ := detectListZone(elems, elements)
	if !isList {
		t.Fatal("expected list zone detection")
	}
	if len(items) < 3 {
		t.Errorf("expected at least 3 primary items, got %d", len(items))
	}
}

func TestDetectListZoneByPath(t *testing.T) {
	elements := []element{
		{tag: "a", text: "S1", pathParts: []string{"div.feed", "article.item", "a.title"}},
		{tag: "a", text: "S2", pathParts: []string{"div.feed", "article.item", "a.title"}},
		{tag: "a", text: "S3", pathParts: []string{"div.feed", "article.item", "a.title"}},
	}
	elems := []int{0, 1, 2}

	isList, items, selector := detectListZone(elems, elements)
	if !isList {
		t.Fatal("expected list zone detection")
	}
	if len(items) != 3 {
		t.Errorf("expected 3 items, got %d", len(items))
	}
	if selector == "" {
		t.Error("expected a CSS selector")
	}
}

// ---------------------------------------------------------------------------
// Mount generation
// ---------------------------------------------------------------------------

func TestInferCategory(t *testing.T) {
	lt := layoutThresholds{headerMaxY: 0.15, footerMinY: 0.85, sidebarW: 0.2}

	// Spatial heuristic tests (rootIdx=-1 means no DOM tag, pure position).
	spatialTests := []struct {
		y, x float64
		want string
	}{
		{0.05, 0.5, "header"},
		{0.90, 0.5, "footer"},
		{0.50, 0.1, "sidebar"},
		{0.50, 0.9, "sidebar"},
		{0.50, 0.5, "main"},
		{0.90, 0.1, "sidebar"},
		{0.90, 0.95, "sidebar"},
	}
	for _, tt := range spatialTests {
		z := zone{centerY: tt.y, centerX: tt.x, rootIdx: -1}
		got := inferCategory(z, nil, lt)
		if got != tt.want {
			t.Errorf("inferCategory(y=%f, x=%f) = %q, want %q", tt.y, tt.x, got, tt.want)
		}
	}

	// DOM tag tests: structural tag overrides position.
	elements := []element{
		{tag: "nav"},
		{tag: "aside"},
		{tag: "footer"},
		{tag: "main"},
		{tag: "div"}, // non-structural, falls back to spatial
	}
	tagTests := []struct {
		rootIdx int
		y, x    float64
		want    string
	}{
		{0, 0.5, 0.5, "sidebar"}, // <nav> at center → sidebar
		{0, 0.05, 0.5, "header"}, // <nav> at top → header (horizontal navbar)
		{1, 0.5, 0.5, "sidebar"}, // <aside> at center → sidebar
		{2, 0.1, 0.5, "footer"},  // <footer> at top → still footer
		{3, 0.9, 0.1, "main"},    // <main> in sidebar position → still main
		{4, 0.5, 0.5, "main"},    // <div> at center → spatial fallback → main
	}
	for _, tt := range tagTests {
		z := zone{centerY: tt.y, centerX: tt.x, rootIdx: tt.rootIdx}
		got := inferCategory(z, elements, lt)
		if got != tt.want {
			t.Errorf("inferCategory(rootIdx=%d, tag=%s) = %q, want %q", tt.rootIdx, elements[tt.rootIdx].tag, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Prefilter
// ---------------------------------------------------------------------------

func TestPrefilterElements(t *testing.T) {
	elements := make([]element, 100)
	for i := range elements {
		elements[i].id = fmt.Sprintf("mache-%d", i)
		if i < 30 {
			elements[i].text = "has text"
		} else if i < 50 {
			elements[i].color = "BLUE"
		}
	}

	filtered := prefilterElements(elements, 40)
	if len(filtered) != 40 {
		t.Errorf("expected 40 elements, got %d", len(filtered))
	}

	// All text elements should survive
	textCount := 0
	for _, el := range filtered {
		if el.text == "has text" {
			textCount++
		}
	}
	if textCount != 30 {
		t.Errorf("expected 30 text elements preserved, got %d", textCount)
	}
}

func TestPrefilterElements_StructuralContainersSurvive(t *testing.T) {
	// Bug: structural containers (nav, main, section, etc.) have no text
	// and no color, so they landed in the lowest-priority "rest" bucket
	// and were the first to be dropped. This destroys zone hierarchy.
	//
	// Fix: structural tags get a reserved budget and are filled first.

	var elements []element

	// 5 structural containers (no text, no color)
	for i, tag := range []string{"nav", "main", "section", "footer", "header"} {
		elements = append(elements, element{
			id:  fmt.Sprintf("struct-%d", i),
			tag: tag,
		})
	}

	// 500 text elements — enough to fill the entire budget alone
	for i := 0; i < 500; i++ {
		elements = append(elements, element{
			id:   fmt.Sprintf("text-%d", i),
			tag:  "div",
			text: fmt.Sprintf("item %d", i),
		})
	}

	filtered := prefilterElements(elements, 100)
	if len(filtered) != 100 {
		t.Fatalf("expected 100 elements, got %d", len(filtered))
	}

	// Every structural container must survive
	structIDs := map[string]bool{}
	for _, el := range filtered {
		if structuralTags[el.tag] {
			structIDs[el.id] = true
		}
	}
	if len(structIDs) != 5 {
		t.Errorf("BUG NOT FIXED: only %d/5 structural containers survived prefilter (got: %v)",
			len(structIDs), structIDs)
	}

	// Remaining 95 slots should be text elements
	textCount := 0
	for _, el := range filtered {
		if el.text != "" {
			textCount++
		}
	}
	if textCount != 95 {
		t.Errorf("expected 95 text elements, got %d", textCount)
	}
}

// ---------------------------------------------------------------------------
// Integration: GenerateSchema against testdata
// ---------------------------------------------------------------------------

func TestGenerateSchemaHackerNews(t *testing.T) {
	summary, err := os.ReadFile("../../testdata/hackernews/page_summary.txt")
	if err != nil {
		t.Skipf("testdata not found: %v", err)
	}

	cart := &TropicalCartographer{}
	schemaJSON, err := cart.GenerateSchema(context.Background(), nil, "image/jpeg", string(summary))
	if err != nil {
		t.Fatalf("GenerateSchema failed: %v", err)
	}

	// Must be valid JSON
	var output struct {
		Mounts []json.RawMessage `json:"mounts"`
	}
	if err := json.Unmarshal([]byte(schemaJSON), &output); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, schemaJSON)
	}

	// 3-7 zones
	if len(output.Mounts) < 3 || len(output.Mounts) > 7 {
		t.Errorf("expected 3-7 mounts, got %d", len(output.Mounts))
	}

	// Every mache_id must exist in summary (same check as mache.ValidateSchema)
	for _, raw := range output.Mounts {
		var m struct {
			MacheID string `json:"mache_id"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal mount: %v", err)
		}
		if !strings.Contains(string(summary), "ID: "+m.MacheID+" ") {
			t.Errorf("hallucinated mache_id: %s", m.MacheID)
		}
	}

	t.Logf("Generated schema:\n%s", schemaJSON)
}

func TestGenerateSchemaAllTestdata(t *testing.T) {
	sites := []string{"hackernews", "lobsters", "wikipedia", "github", "ecommerce"}
	cart := &TropicalCartographer{}

	for _, site := range sites {
		t.Run(site, func(t *testing.T) {
			summaryPath := "../../testdata/" + site + "/page_summary.txt"
			summary, err := os.ReadFile(summaryPath)
			if err != nil {
				t.Skipf("testdata not found: %v", err)
			}

			schemaJSON, err := cart.GenerateSchema(context.Background(), nil, "image/jpeg", string(summary))
			if err != nil {
				t.Fatalf("GenerateSchema failed: %v", err)
			}

			var output struct {
				Mounts []json.RawMessage `json:"mounts"`
			}
			if err := json.Unmarshal([]byte(schemaJSON), &output); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}

			if len(output.Mounts) < 3 || len(output.Mounts) > 7 {
				t.Errorf("expected 3-7 mounts, got %d", len(output.Mounts))
			}

			// Validate all mache_ids
			for _, raw := range output.Mounts {
				var m struct {
					MacheID string `json:"mache_id"`
				}
				if err := json.Unmarshal(raw, &m); err != nil {
					t.Fatalf("unmarshal mount: %v", err)
				}
				if !strings.Contains(string(summary), "ID: "+m.MacheID+" ") {
					t.Errorf("hallucinated mache_id: %s", m.MacheID)
				}
			}

			t.Logf("%s: %d mounts, schema:\n%s", site, len(output.Mounts), schemaJSON)
		})
	}
}

func TestGenerateSchemaEmpty(t *testing.T) {
	cart := &TropicalCartographer{}
	_, err := cart.GenerateSchema(context.Background(), nil, "image/jpeg", "")
	if err == nil {
		t.Error("expected error on empty summary")
	}
}

func TestGenerateSchemaDeterministic(t *testing.T) {
	summary := `Interactive Elements:
ID: mache-0 | Tag: a | Text: "Home"
ID: mache-1 | Tag: a | Text: "About"
ID: mache-2 | Tag: a | Text: "Story 1"
ID: mache-3 | Tag: a | Text: "Story 2"
ID: mache-4 | Tag: a | Text: "Story 3"
ID: mache-5 | Tag: a | Text: "Contact"`

	cart := &TropicalCartographer{}
	s1, _ := cart.GenerateSchema(context.Background(), nil, "", summary)
	s2, _ := cart.GenerateSchema(context.Background(), nil, "", summary)

	if s1 != s2 {
		t.Errorf("non-deterministic output:\nrun1: %s\nrun2: %s", s1, s2)
	}
}
