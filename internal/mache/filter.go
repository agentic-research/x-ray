package mache

import (
	"regexp"
	"strconv"
	"strings"
)

// BoundsRe matches "Bounds: [x, y, w, h]" in a DOM summary line.
// Exported so callers (api/capture, api/websocket) don't duplicate the regex.
var BoundsRe = regexp.MustCompile(`Bounds: \[([\d.]+), ([\d.]+), ([\d.]+), ([\d.]+)\]`)

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
		m := BoundsRe.FindStringSubmatch(line)
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

// parseBoundsString parses a bounds string like "[0.1, 0.2, 0.3, 0.4]" into [4]float64.
// Returns zero value and false if parsing fails.
func parseBoundsString(s string) ([4]float64, bool) {
	return ParseBoundsFromLine("Bounds: " + s)
}

// ParseBoundsFromLine extracts [x, y, w, h] from a line containing "Bounds: [...]".
// Returns zero value and false if the line has no parseable bounds.
func ParseBoundsFromLine(line string) ([4]float64, bool) {
	m := BoundsRe.FindStringSubmatch(line)
	if m == nil {
		return [4]float64{}, false
	}
	x, _ := strconv.ParseFloat(m[1], 64)
	y, _ := strconv.ParseFloat(m[2], 64)
	w, _ := strconv.ParseFloat(m[3], 64)
	h, _ := strconv.ParseFloat(m[4], 64)
	return [4]float64{x, y, w, h}, true
}

// ParseAllBounds extracts all [x, y, w, h] bounds from a multi-line DOM summary.
func ParseAllBounds(summary string) [][4]float64 {
	matches := BoundsRe.FindAllStringSubmatch(summary, -1)
	bounds := make([][4]float64, 0, len(matches))
	for _, m := range matches {
		x, _ := strconv.ParseFloat(m[1], 64)
		y, _ := strconv.ParseFloat(m[2], 64)
		w, _ := strconv.ParseFloat(m[3], 64)
		h, _ := strconv.ParseFloat(m[4], 64)
		bounds = append(bounds, [4]float64{x, y, w, h})
	}
	return bounds
}

// BoundsOverlap checks if two [x, y, w, h] normalized bounding boxes overlap.
// Exported for use by api/capture clip filtering.
func BoundsOverlap(a, b [4]float64) bool {
	return boundsOverlap(a, b)
}

// boundsContains checks if the center of inner falls within outer.
// Used for spatial containment: an element "belongs" to a zone if its center
// is inside the zone's bounding box.
func boundsContains(outer, inner [4]float64) bool {
	if inner[2] <= 0 || inner[3] <= 0 || outer[2] <= 0 || outer[3] <= 0 {
		return false
	}
	cx := inner[0] + inner[2]/2
	cy := inner[1] + inner[3]/2
	return cx >= outer[0] && cx <= outer[0]+outer[2] &&
		cy >= outer[1] && cy <= outer[1]+outer[3]
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
