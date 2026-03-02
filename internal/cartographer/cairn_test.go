package cartographer

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// Minimal DOM summary for testing CairnCartographer.
const cairnTestSummary = `ID: el-1 | Tag: body | Parent: none | Path: body | Bounds: [0, 0, 1, 1]
ID: el-2 | Tag: header | Parent: el-1 | Path: body>header | Bounds: [0, 0, 1, 0.1] | Text: Site Title
ID: el-3 | Tag: nav | Parent: el-2 | Path: body>header>nav | Bounds: [0, 0, 1, 0.05] | Text: Home About Contact
ID: el-4 | Tag: main | Parent: el-1 | Path: body>main | Bounds: [0, 0.1, 1, 0.75]
ID: el-5 | Tag: section | Parent: el-4 | Path: body>main>section | Bounds: [0, 0.1, 1, 0.3] | Text: Welcome to our site
ID: el-6 | Tag: div | Parent: el-4 | Path: body>main>div | Bounds: [0, 0.4, 0.5, 0.3] | Text: Article one about cats
ID: el-7 | Tag: div | Parent: el-4 | Path: body>main>div | Bounds: [0.5, 0.4, 0.5, 0.3] | Text: Article two about dogs
ID: el-8 | Tag: div | Parent: el-4 | Path: body>main>div | Bounds: [0, 0.7, 1, 0.15] | Text: Sidebar links and ads
ID: el-9 | Tag: footer | Parent: el-1 | Path: body>footer | Bounds: [0, 0.85, 1, 0.15] | Text: Copyright 2026
ID: el-10 | Tag: a | Parent: el-9 | Path: body>footer>a | Bounds: [0, 0.9, 0.5, 0.05] | Text: Privacy Policy
ID: el-11 | Tag: a | Parent: el-9 | Path: body>footer>a | Bounds: [0.5, 0.9, 0.5, 0.05] | Text: Terms of Service`

func TestCairnCartographer_GenerateSchema_DOMOnly(t *testing.T) {
	cc := &CairnCartographer{
		Gear:     5,
		Scale:    10.0,
		MinZones: 2,
		MaxZones: 7,
	}

	schema, err := cc.GenerateSchema(context.Background(), nil, "", cairnTestSummary)
	if err != nil {
		t.Fatalf("GenerateSchema failed: %v", err)
	}

	// Verify valid JSON
	var result struct {
		Mounts []tropicalMount `json:"mounts"`
	}
	if err := json.Unmarshal([]byte(schema), &result); err != nil {
		t.Fatalf("Schema is not valid JSON: %v\nSchema: %s", err, schema)
	}

	if len(result.Mounts) == 0 {
		t.Fatal("Expected at least 1 mount")
	}

	t.Logf("DOM-only: %d mounts produced", len(result.Mounts))
	for _, m := range result.Mounts {
		t.Logf("  %s → %s (%s)", m.VirtualPath, m.MacheID, m.Description)
	}

	// Basic sanity: should have virtual paths
	for _, m := range result.Mounts {
		if m.VirtualPath == "" {
			t.Error("Mount has empty virtual path")
		}
		if m.MacheID == "" {
			t.Error("Mount has empty mache_id")
		}
	}
}

func TestCairnCartographer_GenerateSchema_EmptySummary(t *testing.T) {
	cc := &CairnCartographer{}
	_, err := cc.GenerateSchema(context.Background(), nil, "", "")
	if err == nil {
		t.Error("Expected error for empty summary")
	}
}

func TestCairnCartographer_AllGears(t *testing.T) {
	for _, gear := range []int{1, 3, 5, 6} {
		t.Run(strings.ReplaceAll(string(rune('0'+gear)), "\x00", ""), func(t *testing.T) {
			cc := &CairnCartographer{
				Gear:     gear,
				Scale:    10.0,
				MinZones: 2,
				MaxZones: 7,
			}

			schema, err := cc.GenerateSchema(context.Background(), nil, "", cairnTestSummary)
			if err != nil {
				t.Fatalf("Gear %d failed: %v", gear, err)
			}

			var result struct {
				Mounts []tropicalMount `json:"mounts"`
			}
			if err := json.Unmarshal([]byte(schema), &result); err != nil {
				t.Fatalf("Gear %d: invalid JSON: %v", gear, err)
			}

			if len(result.Mounts) == 0 {
				t.Fatalf("Gear %d: expected at least 1 mount", gear)
			}
			t.Logf("Gear %d: %d mounts", gear, len(result.Mounts))
		})
	}
}

func TestCairnCartographer_DefaultValues(t *testing.T) {
	// Zero-value struct should use sensible defaults
	cc := &CairnCartographer{}

	schema, err := cc.GenerateSchema(context.Background(), nil, "", cairnTestSummary)
	if err != nil {
		t.Fatalf("Default CairnCartographer failed: %v", err)
	}

	var result struct {
		Mounts []tropicalMount `json:"mounts"`
	}
	if err := json.Unmarshal([]byte(schema), &result); err != nil {
		t.Fatalf("Invalid JSON: %v", err)
	}
	if len(result.Mounts) == 0 {
		t.Fatal("Expected at least 1 mount with defaults")
	}
	t.Logf("Defaults: %d mounts", len(result.Mounts))
}

func TestCairnCartographer_Determinism(t *testing.T) {
	cc := &CairnCartographer{Gear: 5, Scale: 10.0}

	s1, err := cc.GenerateSchema(context.Background(), nil, "", cairnTestSummary)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := cc.GenerateSchema(context.Background(), nil, "", cairnTestSummary)
	if err != nil {
		t.Fatal(err)
	}
	if s1 != s2 {
		t.Errorf("CairnCartographer should be deterministic\n  s1=%s\n  s2=%s", s1, s2)
	}
}

func TestBuildDOMSubtreeGroups(t *testing.T) {
	elements := parseElements(cairnTestSummary)
	if len(elements) == 0 {
		t.Fatal("No elements parsed")
	}

	visualTypes := make(map[int]string)
	zones := buildDOMSubtreeGroups(elements, visualTypes)
	if len(zones) == 0 {
		t.Fatal("Expected at least 1 zone from DOM grouping")
	}
	t.Logf("DOM subtree groups: %d zones from %d elements", len(zones), len(elements))
}

func TestStructuralFallbackZones(t *testing.T) {
	elements := parseElements(cairnTestSummary)
	zones := structuralFallbackZones(elements)
	if len(zones) == 0 {
		t.Fatal("Expected at least 1 fallback zone")
	}
	t.Logf("Structural fallback: %d zones", len(zones))
}

func TestFoldCairnZones(t *testing.T) {
	elements := parseElements(cairnTestSummary)
	visualTypes := make(map[int]string)
	zones := buildDOMSubtreeGroups(elements, visualTypes)

	folded := foldCairnZones(zones, elements, 3, 7)
	if len(folded) > 7 {
		t.Errorf("Expected <= 7 zones after folding, got %d", len(folded))
	}
	t.Logf("Folded: %d → %d zones", len(zones), len(folded))
}

func TestMergeClosestZones(t *testing.T) {
	elements := parseElements(cairnTestSummary)
	visualTypes := make(map[int]string)
	zones := buildDOMSubtreeGroups(elements, visualTypes)

	if len(zones) > 1 {
		merged := mergeClosestZones(zones, elements)
		if len(merged) != len(zones)-1 {
			t.Errorf("Expected %d zones after merge, got %d", len(zones)-1, len(merged))
		}
	}
}
