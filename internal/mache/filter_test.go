package mache

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// boundsOverlap
// ---------------------------------------------------------------------------

func TestBoundsOverlap(t *testing.T) {
	t.Run("overlapping", func(t *testing.T) {
		a := [4]float64{0.1, 0.1, 0.3, 0.3} // x=0.1..0.4, y=0.1..0.4
		b := [4]float64{0.2, 0.2, 0.3, 0.3} // x=0.2..0.5, y=0.2..0.5
		if !boundsOverlap(a, b) {
			t.Error("expected overlap")
		}
	})

	t.Run("disjoint_horizontal", func(t *testing.T) {
		a := [4]float64{0.0, 0.0, 0.2, 0.2}
		b := [4]float64{0.5, 0.0, 0.2, 0.2}
		if boundsOverlap(a, b) {
			t.Error("expected no overlap (disjoint horizontal)")
		}
	})

	t.Run("disjoint_vertical", func(t *testing.T) {
		a := [4]float64{0.0, 0.0, 0.2, 0.2}
		b := [4]float64{0.0, 0.5, 0.2, 0.2}
		if boundsOverlap(a, b) {
			t.Error("expected no overlap (disjoint vertical)")
		}
	})

	t.Run("contained", func(t *testing.T) {
		outer := [4]float64{0.0, 0.0, 1.0, 1.0}
		inner := [4]float64{0.2, 0.2, 0.1, 0.1}
		if !boundsOverlap(outer, inner) {
			t.Error("expected overlap (contained)")
		}
		if !boundsOverlap(inner, outer) {
			t.Error("expected overlap (contained, reversed)")
		}
	})

	t.Run("touching_edge_no_overlap", func(t *testing.T) {
		// Two rectangles sharing an edge: a ends at x=0.5, b starts at x=0.5.
		// Strictly, a.x+a.w == b.x so a.x+a.w > b.x is false => no overlap.
		a := [4]float64{0.0, 0.0, 0.5, 0.5}
		b := [4]float64{0.5, 0.0, 0.5, 0.5}
		if boundsOverlap(a, b) {
			t.Error("touching edge should NOT count as overlap")
		}
	})

	t.Run("zero_size", func(t *testing.T) {
		a := [4]float64{0.5, 0.5, 0.0, 0.0}
		b := [4]float64{0.0, 0.0, 1.0, 1.0}
		if boundsOverlap(a, b) {
			t.Error("zero-size rect should not overlap")
		}
	})
}

// ---------------------------------------------------------------------------
// FilterSummaryByBounds
// ---------------------------------------------------------------------------

const filterTestSummary = `Interactive Elements:
ID: mache-1 | Color: BLUE | Bounds: [0.10, 0.10, 0.20, 0.10] | Parent: none | Tag: nav | Text: "Top Nav"
ID: mache-2 | Color: GREEN | Bounds: [0.05, 0.30, 0.40, 0.30] | Parent: none | Tag: div | Text: "Sidebar"
ID: mache-3 | Color: RED | Bounds: [0.50, 0.30, 0.40, 0.30] | Parent: none | Tag: main | Text: "Content"
ID: mache-4 | Color: BLUE | Bounds: [0.10, 0.80, 0.80, 0.10] | Parent: none | Tag: footer | Text: "Footer"
ID: mache-5 | Color: YELLOW | Bounds: [0.60, 0.35, 0.10, 0.10] | Parent: mache-3 | Tag: a | Text: "Link"`

func TestFilterSummaryByBounds_Partial(t *testing.T) {
	// Region covers roughly the center-right area: x=0.45..0.95, y=0.25..0.65
	region := [4]float64{0.45, 0.25, 0.50, 0.40}
	result := FilterSummaryByBounds(filterTestSummary, region, 0.0)

	lines := strings.Split(result, "\n")
	var idLines []string
	for _, l := range lines {
		if strings.HasPrefix(l, "ID: ") {
			idLines = append(idLines, l)
		}
	}

	// mache-3 (Content) and mache-5 (Link) overlap the region.
	// mache-2 (Sidebar) ends at x=0.45 which touches but doesn't overlap.
	// mache-1 (Top Nav) is above. mache-4 (Footer) is below.
	if len(idLines) != 2 {
		t.Fatalf("expected 2 elements in region, got %d:\n%s", len(idLines), result)
	}
	if !strings.Contains(result, "mache-3") {
		t.Error("expected mache-3 (Content) in result")
	}
	if !strings.Contains(result, "mache-5") {
		t.Error("expected mache-5 (Link) in result")
	}
}

func TestFilterSummaryByBounds_NoneInside(t *testing.T) {
	// Region far off to the right: nothing overlaps
	region := [4]float64{0.95, 0.95, 0.05, 0.05}
	result := FilterSummaryByBounds(filterTestSummary, region, 0.0)

	// Should have no ID lines
	for _, l := range strings.Split(result, "\n") {
		if strings.HasPrefix(l, "ID: ") {
			t.Fatalf("expected no elements, but got: %s", l)
		}
	}
}

func TestFilterSummaryByBounds_NoBoundsField(t *testing.T) {
	summary := `Interactive Elements:
ID: mache-1 | Color: BLUE | Parent: none | Tag: nav | Text: "No bounds here"
ID: mache-2 | Color: GREEN | Bounds: [0.10, 0.10, 0.30, 0.30] | Parent: none | Tag: div | Text: "Has bounds"`

	// Region that covers everything
	region := [4]float64{0.0, 0.0, 1.0, 1.0}
	result := FilterSummaryByBounds(summary, region, 0.0)

	// mache-1 has no Bounds field, so it should be excluded
	if strings.Contains(result, "mache-1") {
		t.Error("element without Bounds should be excluded")
	}
	// mache-2 has Bounds and is within region
	if !strings.Contains(result, "mache-2") {
		t.Error("element with Bounds in region should be included")
	}
}

func TestFilterSummaryByBounds_WithMargin(t *testing.T) {
	// mache-2 (Sidebar) has bounds [0.05, 0.30, 0.40, 0.30] => x=0.05..0.45
	// Region starts at x=0.45 => touching edge, no overlap at margin=0.
	// With margin=0.05, region expands to x=0.40..1.00, which overlaps sidebar.
	region := [4]float64{0.45, 0.25, 0.50, 0.40}
	result := FilterSummaryByBounds(filterTestSummary, region, 0.05)

	if !strings.Contains(result, "mache-2") {
		t.Error("margin=0.05 should catch mache-2 (Sidebar) at boundary")
	}
	// mache-3 and mache-5 should still be there
	if !strings.Contains(result, "mache-3") {
		t.Error("mache-3 should still be included with margin")
	}
	if !strings.Contains(result, "mache-5") {
		t.Error("mache-5 should still be included with margin")
	}
}

func TestFilterSummaryByBounds_PreservesFormat(t *testing.T) {
	// The filtered output should be parseable by parseSummary()
	region := [4]float64{0.0, 0.0, 1.0, 1.0} // cover everything
	result := FilterSummaryByBounds(filterTestSummary, region, 0.0)

	elements := parseSummary(result)
	if len(elements) != 5 {
		t.Fatalf("expected 5 elements from parseSummary on filtered output, got %d", len(elements))
	}

	// Verify fields survived round-trip
	for _, el := range elements {
		if el.ID == "" {
			t.Error("element ID empty after round-trip")
		}
		if el.Tag == "" {
			t.Error("element Tag empty after round-trip")
		}
		if el.Bounds == "" {
			t.Error("element Bounds empty after round-trip")
		}
		if el.Color == "" {
			t.Error("element Color empty after round-trip")
		}
	}

	// Check the "Interactive Elements:" header is preserved
	if !strings.HasPrefix(result, "Interactive Elements:") {
		t.Error("header line should be preserved")
	}
}
