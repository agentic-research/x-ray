package guardrails

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

var (
	andSplitPattern     = regexp.MustCompile(`\band\b`)
	bulletListPattern   = regexp.MustCompile(`(?m)^[*\-]\s+`)
	bulletPrefixPattern = regexp.MustCompile(`^[*\-]\s+`)
)

// GuardrailsEnabled returns true if XRAY_GUARDRAILS=1. Intended for callers
// outside the GoalState lifecycle (e.g., the Planner) who need to check once.
func GuardrailsEnabled() bool {
	return os.Getenv("XRAY_GUARDRAILS") == "1"
}

// NormalizeAnswer cleans up an LLM answer into a consistent format.
// Handles: JSON arrays, Python-style lists, comma-separated, "and"-separated,
// bullet lists, newline-separated, trailing periods.
// Pass enabled=true to activate; when false, returns raw unchanged.
func NormalizeAnswer(raw string, enabled bool) string {
	if !enabled {
		return raw
	}

	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw
	}

	// Try to parse as a list and re-serialize as clean JSON array.
	if items := parseAsList(raw); len(items) > 1 {
		normalized, err := json.Marshal(items)
		if err == nil {
			if os.Getenv("XRAY_DEBUG") == "1" {
				fmt.Printf("Guardrail [outputnorm]: normalized %q → %s\n", raw, string(normalized))
			}
			return string(normalized)
		}
	}

	// Single value — strip trailing period.
	return strings.TrimRight(raw, ".")
}

// parseAsList tries to extract a list of strings from various formats.
func parseAsList(raw string) []string {
	// JSON array: ["Alice", "Bob"]
	if strings.HasPrefix(raw, "[") {
		var items []any
		if err := json.Unmarshal([]byte(raw), &items); err == nil {
			return anyToStrings(items)
		}

		// Python-style single-quote: ['Alice', 'Bob']
		fixed := strings.ReplaceAll(raw, "'", "\"")
		if err := json.Unmarshal([]byte(fixed), &items); err == nil {
			return anyToStrings(items)
		}
	}

	// Markdown bullet list: "* Alice\n* Bob" or "- Alice\n- Bob"
	if bulletListPattern.MatchString(raw) {
		var items []string
		for _, line := range strings.Split(raw, "\n") {
			line = bulletPrefixPattern.ReplaceAllString(line, "")
			line = strings.TrimSpace(line)
			if line != "" {
				items = append(items, strings.TrimRight(line, "."))
			}
		}
		if len(items) > 0 {
			return items
		}
	}

	// Comma-separated: "Alice, Bob, Charlie"
	// Guard against splitting suffixes like "Smith, Jr." — reject if any
	// part is <= 3 characters.
	if strings.Contains(raw, ",") {
		var items []string
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			part = strings.TrimRight(part, ".")
			if part != "" {
				items = append(items, part)
			}
		}
		allSubstantial := true
		for _, item := range items {
			if len(item) <= 2 {
				allSubstantial = false
				break
			}
		}
		if len(items) > 1 && allSubstantial {
			return items
		}
	}

	// Newline-separated (multiple lines without bullets) — checked BEFORE
	// "and"-split so "Rachel and T. Gannon\nJohn Smith" splits on newlines.
	if strings.Contains(raw, "\n") {
		var items []string
		for _, line := range strings.Split(raw, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				items = append(items, strings.TrimRight(line, "."))
			}
		}
		if len(items) > 1 {
			return items
		}
	}

	// "and"-separated: "Rachel and T. Gannon" — only on single-line inputs
	// to avoid corrupting multi-line answers.
	if !strings.Contains(raw, "\n") && andSplitPattern.MatchString(raw) {
		parts := andSplitPattern.Split(raw, -1)
		var items []string
		for _, part := range parts {
			part = strings.TrimSpace(part)
			part = strings.TrimRight(part, ".")
			if part != "" {
				items = append(items, part)
			}
		}
		if len(items) > 1 {
			return items
		}
	}

	return nil
}

func anyToStrings(items []any) []string {
	var out []string
	for _, item := range items {
		s := fmt.Sprintf("%v", item)
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
