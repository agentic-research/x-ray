package api

import (
	"strings"
	"testing"

	"github.com/agentic-research/x-ray/internal/cdp"
)

func TestFilterSummaryByClip(t *testing.T) {
	// Page is 1000x2000 pixels. Clip covers the top-left quadrant (0-500, 0-1000).
	clip := cdp.ScreenshotClip{X: 0, Y: 0, Width: 500, Height: 1000, Scale: 1}
	pageW, pageH := 1000.0, 2000.0

	summary := strings.Join([]string{
		`ID: mache-1 | Color: YELLOW | Bounds: [0.100, 0.100, 0.200, 0.200] | Tag: div | Text: "inside"`,
		`ID: mache-2 | Color: YELLOW | Bounds: [0.800, 0.800, 0.100, 0.100] | Tag: div | Text: "outside"`,
		`ID: mache-3 | Color: YELLOW | Bounds: [0.400, 0.400, 0.200, 0.200] | Tag: div | Text: "overlapping"`,
		`ID: mache-4 | Color: YELLOW | Bounds: [0.600, 0.100, 0.100, 0.100] | Tag: div | Text: "just outside"`,
		`Some header line without bounds`,
	}, "\n")

	result := filterSummaryByClip(summary, clip, pageW, pageH)

	// mache-1 (0.1-0.3, 0.1-0.3): fully inside clip (0-0.5, 0-0.5) → kept
	if !strings.Contains(result, "mache-1") {
		t.Error("expected mache-1 (inside) to be kept")
	}
	// mache-2 (0.8-0.9, 0.8-0.9): fully outside → filtered
	if strings.Contains(result, "mache-2") {
		t.Error("expected mache-2 (outside) to be filtered")
	}
	// mache-3 (0.4-0.6, 0.4-0.6): overlaps clip edge → kept
	if !strings.Contains(result, "mache-3") {
		t.Error("expected mache-3 (overlapping) to be kept")
	}
	// mache-4 (0.6-0.7, 0.1-0.2): starts at x=0.6 which is > clip right=0.5 → filtered
	if strings.Contains(result, "mache-4") {
		t.Error("expected mache-4 (just outside) to be filtered")
	}
	// Header line without bounds → kept
	if !strings.Contains(result, "header line") {
		t.Error("expected non-bounds line to be kept")
	}
}

func TestFilterSummaryByClip_FullPage(t *testing.T) {
	// Full-page clip should keep everything.
	clip := cdp.ScreenshotClip{X: 0, Y: 0, Width: 1000, Height: 2000, Scale: 1}
	pageW, pageH := 1000.0, 2000.0

	summary := `ID: mache-1 | Bounds: [0.900, 0.900, 0.100, 0.100] | Tag: div`
	result := filterSummaryByClip(summary, clip, pageW, pageH)

	if !strings.Contains(result, "mache-1") {
		t.Error("full-page clip should keep all elements")
	}
}
