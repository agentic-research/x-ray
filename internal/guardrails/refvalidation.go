package guardrails

import (
	"fmt"
	"regexp"
	"strings"
)

var macheIDPattern = regexp.MustCompile(`^mache-\d+$`)

// ValidateActPath checks if a bare mache-N path is being used after reading a
// children file (which uses [N] ordinals). If so, returns an error message
// suggesting the correct _c/N path. Returns "" if the path is valid.
func (gs *GoalState) ValidateActPath(path string) string {
	if !gs.Enabled {
		return ""
	}

	// Only intercept bare mache-N references.
	if !macheIDPattern.MatchString(path) {
		return ""
	}

	// Look back at recent actions (newest first) to see if context was children-based.
	recent := gs.LastActions(3)
	for i := len(recent) - 1; i >= 0; i-- {
		a := recent[i]
		// If most recent relevant tool was grep or cat(text_index), bare mache-N is valid.
		if a.Tool == "grep" || (a.Tool == "cat" && strings.Contains(a.Path, "text_index")) {
			return ""
		}

		// If most recent relevant tool was cat(children), mache-N is wrong.
		if a.Tool == "cat" && strings.HasSuffix(a.Path, "/children") {
			parentPath := strings.TrimSuffix(a.Path, "/children")
			gs.Log("refvalidation", fmt.Sprintf("blocked %s after cat(children), suggested %s/_c/N", path, parentPath))
			return fmt.Sprintf(
				"Error: %s is a mache ID, not a clickable ordinal. "+
					"You read a children file which uses [N] ordinals. "+
					"Use act('%s/_c/N', 'click') where N is the ordinal number from the children listing.",
				path, parentPath,
			)
		}
	}

	return ""
}
