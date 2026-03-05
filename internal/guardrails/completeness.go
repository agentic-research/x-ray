package guardrails

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Item count patterns found in typical web pages.
var itemCountPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(\d+)\s+[Rr]eviews?`),                  // "12 Reviews"
	regexp.MustCompile(`[Rr]eviews?\s*\((\d+)\)`),              // "Reviews (12)"
	regexp.MustCompile(`[Rr]eviews?\s*:\s*(\d+)`),              // "Reviews: 12"
	regexp.MustCompile(`[Ss]howing\s+\d+[-–]\d+\s+of\s+(\d+)`), // "Showing 1-10 of 47"
	regexp.MustCompile(`(\d+)\s+results?`),                     // "47 results"
	regexp.MustCompile(`(\d+)\s+items?`),                       // "12 items"
	regexp.MustCompile(`(\d+)\s+comments?`),                    // "8 comments"
	regexp.MustCompile(`(\d+)\s+posts?`),                       // "15 posts"
}

// ExtractItemCount scans text for patterns like "12 Reviews" or "Showing 1-10 of 47"
// and returns the expected total count. Returns -1 if no pattern matches.
func ExtractItemCount(text string) int {
	for _, pat := range itemCountPatterns {
		if m := pat.FindStringSubmatch(text); len(m) > 1 {
			if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
				return n
			}
		}
	}
	return -1
}

// UpdateItemCount updates the expected item count if the text contains a count
// pattern and no count has been set yet (or the new count is higher).
func (gs *GoalState) UpdateItemCount(text string) {
	if !gs.Enabled {
		return
	}
	n := ExtractItemCount(text)
	if n < 0 {
		return
	}

	gs.mu.Lock()
	defer gs.mu.Unlock()
	if gs.PageItemCount < 0 || n > gs.PageItemCount {
		gs.PageItemCount = n
		gs.Log("completeness", fmt.Sprintf("detected expected item count: %d", n))
	}
}

// CountScratchItems counts structured finding lines in the scratchpad.
// Only lines starting with "Found:", "- ", or "* " are counted to avoid
// inflating the count with Navigator working notes.
func CountScratchItems(scratchpad string) int {
	if strings.TrimSpace(scratchpad) == "" {
		return 0
	}
	count := 0
	for _, line := range strings.Split(scratchpad, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Found:") ||
			strings.HasPrefix(line, "- ") ||
			strings.HasPrefix(line, "* ") {
			count++
		}
	}
	return count
}

// CheckCompleteness compares found items against expected count.
// Returns a warning string if incomplete, or "" if complete/unknown.
func (gs *GoalState) CheckCompleteness(scratchpad string) string {
	if !gs.Enabled {
		return ""
	}

	gs.mu.RLock()
	expected := gs.PageItemCount
	gs.mu.RUnlock()

	if expected < 0 {
		return ""
	}

	found := CountScratchItems(scratchpad)
	if found >= expected {
		return ""
	}

	warning := fmt.Sprintf("WARNING: Found %d of %d items. Keep looking — check remaining pages/sections.", found, expected)
	gs.Log("completeness", fmt.Sprintf("found %d/%d items, injecting retry warning", found, expected))
	return warning
}
