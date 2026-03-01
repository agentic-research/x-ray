package cartographer

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// Regression tests for known bugs in the cartographer package.
// Run with: go test -run TestBug_ ./internal/cartographer/

func TestBug_CVRegionAsZoneRoot(t *testing.T) {
	// Bug: TropicalCartographer picks cv-* IDs (from edge detection) as mount
	// mache_ids. These synthetic IDs are appended to the cartographer summary
	// by websocket.go but don't exist in the original browser DOM summary, so
	// ValidateSchema rejects them as "hallucinated".
	//
	// Live repro (reddit.com, 2026-02-27):
	//   TropicalCartographer: 5 zones from 106 elements
	//   Cartographer hallucinated IDs: [cv-19] — regenerating
	//   Cartographer still hallucinating after retry: [cv-19]
	//
	// Fix: buildMounts skips cv-* when selecting zone root elements.

	// Simulate a summary with real mache-* elements and synthetic cv-* regions
	// (matching the format websocket.go uses to append CV detections).
	lines := []string{
		`ID: mache-0 | Tag: div | Text: "Site Header" | Bounds: [0.000, 0.000, 1.000, 0.100]`,
		`ID: mache-1 | Tag: a | Text: "Home" | Bounds: [0.000, 0.020, 0.100, 0.050]`,
		`ID: mache-2 | Tag: a | Text: "Popular" | Bounds: [0.110, 0.020, 0.100, 0.050]`,
		`ID: mache-10 | Tag: div | Text: "Post Title 1" | Bounds: [0.100, 0.200, 0.600, 0.100]`,
		`ID: mache-11 | Tag: div | Text: "Post Title 2" | Bounds: [0.100, 0.350, 0.600, 0.100]`,
		`ID: mache-12 | Tag: div | Text: "Post Title 3" | Bounds: [0.100, 0.500, 0.600, 0.100]`,
		`ID: mache-20 | Tag: div | Text: "Sidebar Widget" | Bounds: [0.800, 0.200, 0.200, 0.300]`,
		// CV regions appended by edge detection (canvas-detected zones)
		`ID: cv-0 | Color: CV | Bounds: [0.050, 0.150, 0.700, 0.600] | Parent: none | Tag: canvas | Text: "[CV detected]" | Path: canvas`,
		`ID: cv-1 | Color: CV | Bounds: [0.800, 0.150, 0.200, 0.400] | Parent: none | Tag: canvas | Text: "[CV detected]" | Path: canvas`,
		`ID: cv-2 | Color: CV | Bounds: [0.000, 0.900, 1.000, 0.100] | Parent: none | Tag: canvas | Text: "[CV detected]" | Path: canvas`,
	}
	summary := strings.Join(lines, "\n") + "\n"

	cart := &TropicalCartographer{}
	schemaJSON, err := cart.GenerateSchema(context.Background(), nil, "image/jpeg", summary)
	if err != nil {
		t.Fatalf("GenerateSchema failed: %v", err)
	}

	// Parse and check every mache_id — none should be cv-*
	var output struct {
		Mounts []json.RawMessage `json:"mounts"`
	}
	if err := json.Unmarshal([]byte(schemaJSON), &output); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if len(output.Mounts) == 0 {
		t.Fatal("expected at least 1 mount")
	}

	for _, raw := range output.Mounts {
		var m struct {
			MacheID      string   `json:"mache_id"`
			VirtualPath  string   `json:"virtual_path"`
			PrimaryItems []string `json:"primary_items"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal mount: %v", err)
		}
		if strings.HasPrefix(m.MacheID, "cv-") {
			t.Errorf("REGRESSION: mount %s has cv-* mache_id %q — these are synthetic edge-detection IDs that don't exist in the browser DOM",
				m.VirtualPath, m.MacheID)
		}
		for _, item := range m.PrimaryItems {
			if strings.HasPrefix(item, "cv-") {
				t.Errorf("REGRESSION: mount %s has cv-* primary_item %q",
					m.VirtualPath, item)
			}
		}
	}

	t.Logf("Schema (no cv-* IDs): %s", schemaJSON)
}

func TestBug_CVRegionOnlyZone(t *testing.T) {
	// Edge case: a zone contains ONLY cv-* elements (no real DOM elements).
	// The zone should be skipped entirely rather than emitting a mount
	// with no valid mache_id.

	lines := []string{
		`ID: mache-0 | Tag: div | Text: "Header" | Bounds: [0.000, 0.000, 1.000, 0.050]`,
		// Large spatial gap forces these into a separate zone
		`ID: cv-0 | Color: CV | Bounds: [0.000, 0.500, 1.000, 0.500] | Parent: none | Tag: canvas | Text: "[CV detected]" | Path: canvas`,
		`ID: cv-1 | Color: CV | Bounds: [0.100, 0.600, 0.800, 0.300] | Parent: none | Tag: canvas | Text: "[CV detected]" | Path: canvas`,
	}
	summary := strings.Join(lines, "\n") + "\n"

	cart := &TropicalCartographer{}
	schemaJSON, err := cart.GenerateSchema(context.Background(), nil, "image/jpeg", summary)
	if err != nil {
		t.Fatalf("GenerateSchema failed: %v", err)
	}

	var output struct {
		Mounts []json.RawMessage `json:"mounts"`
	}
	if err := json.Unmarshal([]byte(schemaJSON), &output); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	for _, raw := range output.Mounts {
		var m struct {
			MacheID string `json:"mache_id"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal mount: %v", err)
		}
		if strings.HasPrefix(m.MacheID, "cv-") {
			t.Errorf("REGRESSION: all-cv zone should be skipped, but got mount with mache_id %q", m.MacheID)
		}
	}
}

func TestBug_DuplicateHeaderFragmentation(t *testing.T) {
	// Bug: Reddit (and other SPAs) render duplicate DOM subtrees for
	// mobile/desktop navigation. The tropical distance correctly separates
	// them into distinct zones, producing /header/content, /header/content_3,
	// /header/actions_4 etc.
	//
	// Fix: H^0 cohomology folding merges zones with identical fiber
	// signatures (same text/tag distribution + spatial overlap).

	// Simulate two identical nav bars (desktop + mobile) at the same Y position
	// plus a distinct main content area.
	lines := []string{
		// Desktop nav (visible)
		`ID: mache-0 | Tag: nav | Text: "Home" | Bounds: [0.000, 0.000, 0.200, 0.050]`,
		`ID: mache-1 | Tag: a | Text: "Feed" | Bounds: [0.200, 0.000, 0.100, 0.050]`,
		`ID: mache-2 | Tag: a | Text: "Popular" | Bounds: [0.300, 0.000, 0.100, 0.050]`,
		`ID: mache-3 | Tag: a | Text: "Settings" | Bounds: [0.400, 0.000, 0.100, 0.050]`,
		// Mobile nav (hidden via CSS but present in DOM, same text)
		`ID: mache-10 | Tag: nav | Text: "Home" | Bounds: [0.000, 0.010, 0.200, 0.050]`,
		`ID: mache-11 | Tag: a | Text: "Feed" | Bounds: [0.200, 0.010, 0.100, 0.050]`,
		`ID: mache-12 | Tag: a | Text: "Popular" | Bounds: [0.300, 0.010, 0.100, 0.050]`,
		`ID: mache-13 | Tag: a | Text: "Settings" | Bounds: [0.400, 0.010, 0.100, 0.050]`,
		// Main content (clearly different)
		`ID: mache-20 | Tag: div | Text: "Post Title Alpha" | Bounds: [0.100, 0.300, 0.600, 0.100]`,
		`ID: mache-21 | Tag: div | Text: "Post Title Beta" | Bounds: [0.100, 0.450, 0.600, 0.100]`,
		`ID: mache-22 | Tag: div | Text: "Post Title Gamma" | Bounds: [0.100, 0.600, 0.600, 0.100]`,
		`ID: mache-23 | Tag: div | Text: "Post Title Delta" | Bounds: [0.100, 0.750, 0.600, 0.100]`,
	}
	summary := strings.Join(lines, "\n") + "\n"

	cart := &TropicalCartographer{}
	schemaJSON, err := cart.GenerateSchema(context.Background(), nil, "image/jpeg", summary)
	if err != nil {
		t.Fatalf("GenerateSchema failed: %v", err)
	}

	var output struct {
		Mounts []json.RawMessage `json:"mounts"`
	}
	if err := json.Unmarshal([]byte(schemaJSON), &output); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// Count header mounts — should be at most 2 (nav + content), not 4+
	headerCount := 0
	for _, raw := range output.Mounts {
		var m struct {
			VirtualPath string `json:"virtual_path"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if strings.HasPrefix(m.VirtualPath, "/header/") {
			headerCount++
		}
	}

	if headerCount > 2 {
		t.Errorf("REGRESSION: header fragmentation — got %d header mounts, expected ≤2 (H^0 folding should merge duplicate nav bars)", headerCount)
	}

	t.Logf("Schema (%d header mounts): %s", headerCount, schemaJSON)
}

func TestBug_AXOnlyZone(t *testing.T) {
	// Edge case: a zone contains ONLY ax-* elements (from macOS Accessibility).
	// These synthetic IDs don't exist in the browser DOM summary, so the zone
	// should be skipped entirely — same pattern as TestBug_CVRegionOnlyZone.

	lines := []string{
		`ID: mache-0 | Tag: div | Text: "Header" | Bounds: [0.000, 0.000, 1.000, 0.050]`,
		// Large spatial gap forces these into a separate zone
		`ID: ax-0 | Tag: axwindow | Text: "Finder" | Bounds: [0.000, 0.500, 1.000, 0.300] | Path: AXApplication > AXWindow | Parent: none`,
		`ID: ax-1 | Tag: axbutton | Text: "Close" | Bounds: [0.010, 0.510, 0.020, 0.020] | Path: AXApplication > AXWindow > AXButton | Parent: none`,
		`ID: ax-2 | Tag: axbutton | Text: "Minimize" | Bounds: [0.040, 0.510, 0.020, 0.020] | Path: AXApplication > AXWindow > AXButton | Parent: none`,
	}
	summary := strings.Join(lines, "\n") + "\n"

	cart := &TropicalCartographer{}
	schemaJSON, err := cart.GenerateSchema(context.Background(), nil, "image/jpeg", summary)
	if err != nil {
		t.Fatalf("GenerateSchema failed: %v", err)
	}

	var output struct {
		Mounts []json.RawMessage `json:"mounts"`
	}
	if err := json.Unmarshal([]byte(schemaJSON), &output); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	for _, raw := range output.Mounts {
		var m struct {
			MacheID      string   `json:"mache_id"`
			VirtualPath  string   `json:"virtual_path"`
			PrimaryItems []string `json:"primary_items"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal mount: %v", err)
		}
		if strings.HasPrefix(m.MacheID, "ax-") {
			t.Errorf("REGRESSION: mount %s has ax-* mache_id %q — these are macOS Accessibility IDs that don't exist in the browser DOM",
				m.VirtualPath, m.MacheID)
		}
		for _, item := range m.PrimaryItems {
			if strings.HasPrefix(item, "ax-") {
				t.Errorf("REGRESSION: mount %s has ax-* primary_item %q",
					m.VirtualPath, item)
			}
		}
	}

	t.Logf("Schema (no ax-* IDs): %s", schemaJSON)
}

func TestBug_H0FoldingViolatesMinZones(t *testing.T) {
	// Bug: foldCoherentZones runs AFTER cutTree's minZones enforcement.
	// If duplicate zones (e.g., mobile+desktop nav) get folded together,
	// the final zone count can drop below minZones (default 3).
	//
	// Fix: guard in GenerateSchema — if folding would drop below minZones,
	// keep the pre-fold zones.

	// Create elements that produce 3-4 zones with duplicate fiber signatures.
	// Without the guard, folding merges duplicates below minZones.
	lines := []string{
		// Nav bar A (top-left)
		`ID: mache-0 | Tag: nav | Text: "Home" | Bounds: [0.000, 0.000, 0.200, 0.050]`,
		`ID: mache-1 | Tag: a | Text: "Feed" | Bounds: [0.200, 0.000, 0.100, 0.050]`,
		// Nav bar B (duplicate, slightly offset — will have same fiber signature)
		`ID: mache-10 | Tag: nav | Text: "Home" | Bounds: [0.000, 0.050, 0.200, 0.050]`,
		`ID: mache-11 | Tag: a | Text: "Feed" | Bounds: [0.200, 0.050, 0.100, 0.050]`,
		// Main content (distinct)
		`ID: mache-20 | Tag: div | Text: "Article Title" | Bounds: [0.100, 0.300, 0.600, 0.100]`,
		`ID: mache-21 | Tag: p | Text: "Article body text here" | Bounds: [0.100, 0.450, 0.600, 0.100]`,
		`ID: mache-22 | Tag: p | Text: "More article text" | Bounds: [0.100, 0.600, 0.600, 0.100]`,
	}
	summary := strings.Join(lines, "\n") + "\n"

	cart := &TropicalCartographer{}
	schemaJSON, err := cart.GenerateSchema(context.Background(), nil, "image/jpeg", summary)
	if err != nil {
		t.Fatalf("GenerateSchema failed: %v", err)
	}

	var output struct {
		Mounts []json.RawMessage `json:"mounts"`
	}
	if err := json.Unmarshal([]byte(schemaJSON), &output); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// minZones default is 3 — we must never produce fewer.
	if len(output.Mounts) < 3 {
		t.Errorf("BUG NOT FIXED: H^0 folding dropped zone count to %d (below minZones=3). Schema: %s",
			len(output.Mounts), schemaJSON)
	}

	t.Logf("Schema (%d zones): %s", len(output.Mounts), schemaJSON)
}

func TestBug_AXMixedWithDOM(t *testing.T) {
	// When ax-* elements are mixed with mache-* elements in the same zone,
	// the mount should use a mache-* ID as root (never ax-*), and ax-*
	// should be excluded from primary_items.

	lines := []string{
		`ID: mache-0 | Tag: div | Text: "App Header" | Bounds: [0.000, 0.000, 1.000, 0.080]`,
		`ID: mache-1 | Tag: a | Text: "Home" | Bounds: [0.050, 0.020, 0.100, 0.040]`,
		`ID: mache-2 | Tag: a | Text: "Settings" | Bounds: [0.200, 0.020, 0.100, 0.040]`,
		// AX elements spatially interleaved with DOM elements in main area
		`ID: mache-10 | Tag: div | Text: "Content Panel" | Bounds: [0.100, 0.200, 0.600, 0.150] | Path: body > main > div`,
		`ID: ax-0 | Tag: axgroup | Text: "Native Widget" | Bounds: [0.100, 0.400, 0.600, 0.100] | Path: AXApplication > AXWindow > AXGroup | Parent: none`,
		`ID: mache-11 | Tag: div | Text: "Footer Panel" | Bounds: [0.100, 0.550, 0.600, 0.100] | Path: body > main > div`,
	}
	summary := strings.Join(lines, "\n") + "\n"

	cart := &TropicalCartographer{}
	schemaJSON, err := cart.GenerateSchema(context.Background(), nil, "image/jpeg", summary)
	if err != nil {
		t.Fatalf("GenerateSchema failed: %v", err)
	}

	var output struct {
		Mounts []json.RawMessage `json:"mounts"`
	}
	if err := json.Unmarshal([]byte(schemaJSON), &output); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	for _, raw := range output.Mounts {
		var m struct {
			MacheID      string   `json:"mache_id"`
			VirtualPath  string   `json:"virtual_path"`
			PrimaryItems []string `json:"primary_items"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal mount: %v", err)
		}
		if strings.HasPrefix(m.MacheID, "ax-") {
			t.Errorf("REGRESSION: mount %s uses ax-* mache_id %q — should use a DOM element",
				m.VirtualPath, m.MacheID)
		}
		for _, item := range m.PrimaryItems {
			if strings.HasPrefix(item, "ax-") {
				t.Errorf("REGRESSION: mount %s has ax-* primary_item %q",
					m.VirtualPath, item)
			}
		}
	}

	t.Logf("Schema (ax mixed with DOM): %s", schemaJSON)
}
