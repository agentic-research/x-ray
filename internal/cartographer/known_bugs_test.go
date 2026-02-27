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
		`ID: cv-0 | Color: CYAN | Bounds: [0.050, 0.150, 0.700, 0.600] | Parent: none | Tag: canvas | Text: "[CV detected]" | Path: canvas`,
		`ID: cv-1 | Color: CYAN | Bounds: [0.800, 0.150, 0.200, 0.400] | Parent: none | Tag: canvas | Text: "[CV detected]" | Path: canvas`,
		`ID: cv-2 | Color: CYAN | Bounds: [0.000, 0.900, 1.000, 0.100] | Parent: none | Tag: canvas | Text: "[CV detected]" | Path: canvas`,
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
		`ID: cv-0 | Color: CYAN | Bounds: [0.000, 0.500, 1.000, 0.500] | Parent: none | Tag: canvas | Text: "[CV detected]" | Path: canvas`,
		`ID: cv-1 | Color: CYAN | Bounds: [0.100, 0.600, 0.800, 0.300] | Parent: none | Tag: canvas | Text: "[CV detected]" | Path: canvas`,
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
