package mache

import (
	"regexp"
	"strconv"
	"strings"
)

var boundsRe = regexp.MustCompile(`Bounds: \[([\d.]+), ([\d.]+), ([\d.]+), ([\d.]+)\]`)

// boundsOverlap checks if two [x, y, w, h] normalized bounding boxes overlap.
// Touching edges (shared boundary) do not count as overlap.
// Zero-area rectangles (w==0 or h==0) never overlap.
func boundsOverlap(a, b [4]float64) bool {
	// Degenerate (zero-area) rectangles cannot overlap.
	if a[2] <= 0 || a[3] <= 0 || b[2] <= 0 || b[3] <= 0 {
		return false
	}
	// Overlap requires strict inequality on all four sides.
	return a[0] < b[0]+b[2] &&
		a[0]+a[2] > b[0] &&
		a[1] < b[1]+b[3] &&
		a[1]+a[3] > b[1]
}

// FilterSummaryByBounds filters DOM summary lines to only those elements whose
// Bounds overlap the given region (expanded by margin in all directions, clamped
// to [0,1]).
//
// Lines that do not start with "ID: " (e.g. "Interactive Elements:") are
// preserved as-is. Lines starting with "ID: " that lack a Bounds field are
// excluded.
func FilterSummaryByBounds(summary string, region [4]float64, margin float64) string {
	// Expand region by margin, clamped to [0,1].
	expanded := [4]float64{
		clamp01(region[0] - margin),
		clamp01(region[1] - margin),
		0, 0, // w and h computed below
	}
	// Right and bottom edges of the original region, expanded outward.
	right := clamp01(region[0] + region[2] + margin)
	bottom := clamp01(region[1] + region[3] + margin)
	expanded[2] = right - expanded[0]
	expanded[3] = bottom - expanded[1]

	var kept []string
	for _, line := range strings.Split(summary, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "ID: ") {
			// Preserve non-element lines (headers, blank lines).
			kept = append(kept, line)
			continue
		}

		// Parse bounds from this line.
		m := boundsRe.FindStringSubmatch(line)
		if m == nil {
			// No Bounds field — exclude.
			continue
		}

		x, _ := strconv.ParseFloat(m[1], 64)
		y, _ := strconv.ParseFloat(m[2], 64)
		w, _ := strconv.ParseFloat(m[3], 64)
		h, _ := strconv.ParseFloat(m[4], 64)
		elemBounds := [4]float64{x, y, w, h}

		if boundsOverlap(expanded, elemBounds) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// clamp01 clamps v to the range [0, 1].
func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
