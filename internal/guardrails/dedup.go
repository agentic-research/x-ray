package guardrails

import (
	"fmt"
	"strings"
)

// IsDuplicate checks if text is already present in the scratchpad (normalized).
// Returns true if the text should be blocked as a duplicate.
func (gs *GoalState) IsDuplicate(scratchpad, text string) bool {
	if !gs.Enabled {
		return false
	}

	norm := normalize(text)
	if norm == "" {
		return false
	}

	for _, line := range strings.Split(scratchpad, "\n") {
		if normalize(line) == norm {
			gs.Log("dedup", fmt.Sprintf("blocked duplicate %q", text))
			return true
		}
	}

	// Atomic check+append on FoundItems to prevent TOCTOU race.
	gs.mu.Lock()
	for _, item := range gs.FoundItems {
		if item == norm {
			gs.mu.Unlock()
			gs.Log("dedup", fmt.Sprintf("blocked duplicate %q (from history)", text))
			return true
		}
	}
	gs.FoundItems = append(gs.FoundItems, norm)
	gs.mu.Unlock()

	return false
}

// normalize strips common prefixes and normalizes whitespace/case.
func normalize(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ToLower(s)

	// Strip common prefixes the Navigator uses.
	for _, prefix := range []string{"found: ", "- ", "* ", "• "} {
		s = strings.TrimPrefix(s, prefix)
	}
	s = strings.TrimSpace(s)

	// Collapse internal whitespace.
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}
