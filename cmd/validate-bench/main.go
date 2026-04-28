// cmd/validate-bench/main.go — Validate bench_cases.json against current testdata.
//
// For each bench case, checks that:
// 1. The site's page_summary.txt exists
// 2. The expect_mache_id exists in the summary
// 3. The element at expect_mache_id contains expect_text
//
// Outputs: which cases are valid, stale (wrong mache-ID), or broken (site missing).
// Also suggests corrected mache-IDs when possible.
//
// Usage: GOWORK=off go run ./cmd/validate-bench
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type benchCase struct {
	Site          string `json:"site"`
	Intent        string `json:"intent"`
	ExpectMacheID string `json:"expect_mache_id"`
	ExpectText    string `json:"expect_text"`
	Difficulty    string `json:"difficulty"`
}

func main() {
	data, err := os.ReadFile("testdata/bench_cases.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading bench_cases.json: %v\n", err)
		os.Exit(1)
	}

	var cases []benchCase
	if err := json.Unmarshal(data, &cases); err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing bench_cases.json: %v\n", err)
		os.Exit(1)
	}

	valid, stale, broken, missing := 0, 0, 0, 0
	var fixes []benchCase

	fmt.Printf("%-15s %-35s %-12s %-12s %s\n", "Site", "Intent", "Expected", "Status", "Details")
	fmt.Println(strings.Repeat("─", 100))

	for _, c := range cases {
		summaryPath := fmt.Sprintf("testdata/%s/page_summary.txt", c.Site)
		summary, err := os.ReadFile(summaryPath)
		if err != nil {
			fmt.Printf("%-15s %-35s %-12s %-12s %s\n", c.Site, trunc(c.Intent, 35), c.ExpectMacheID, "BROKEN", "no page_summary.txt")
			broken++
			continue
		}

		lines := strings.Split(string(summary), "\n")

		// Check if expected mache-ID exists
		expectedFound := false
		expectedText := ""
		for _, line := range lines {
			if strings.Contains(line, "ID: "+c.ExpectMacheID+" ") {
				expectedFound = true
				// Extract text
				if ti := strings.Index(line, "Text: \""); ti >= 0 {
					rest := line[ti+7:]
					if ei := strings.Index(rest, "\""); ei > 0 {
						expectedText = rest[:ei]
					}
				}
				break
			}
		}

		if !expectedFound {
			fmt.Printf("%-15s %-35s %-12s %-12s %s\n", c.Site, trunc(c.Intent, 35), c.ExpectMacheID, "MISSING", "mache-ID not in summary")
			missing++
			// Try to find correct ID
			if suggestion := findByText(lines, c.ExpectText); suggestion != "" {
				fmt.Printf("  → SUGGEST: %s (text match)\n", suggestion)
				fixed := c
				fixed.ExpectMacheID = suggestion
				fixes = append(fixes, fixed)
			}
			continue
		}

		// Check if text matches
		if c.ExpectText != "" && !strings.Contains(strings.ToLower(expectedText), strings.ToLower(c.ExpectText)) {
			fmt.Printf("%-15s %-35s %-12s %-12s got %q, want %q\n", c.Site, trunc(c.Intent, 35), c.ExpectMacheID, "STALE", trunc(expectedText, 30), trunc(c.ExpectText, 30))
			stale++
			if suggestion := findByText(lines, c.ExpectText); suggestion != "" {
				fmt.Printf("  → SUGGEST: %s (text match)\n", suggestion)
				fixed := c
				fixed.ExpectMacheID = suggestion
				fixes = append(fixes, fixed)
			}
			continue
		}

		fmt.Printf("%-15s %-35s %-12s %-12s %q\n", c.Site, trunc(c.Intent, 35), c.ExpectMacheID, "VALID", trunc(expectedText, 40))
		valid++
		fixes = append(fixes, c)
	}

	fmt.Println(strings.Repeat("─", 100))
	fmt.Printf("Total: %d  Valid: %d  Stale: %d  Missing: %d  Broken: %d\n", len(cases), valid, stale, missing, broken)

	if stale+missing > 0 && len(fixes) > 0 {
		fmt.Printf("\nWriting suggested fixes to testdata/bench_cases_fixed.json...\n")
		fixedJSON, _ := json.MarshalIndent(fixes, "", "  ")
		os.WriteFile("testdata/bench_cases_fixed.json", fixedJSON, 0o644)
		fmt.Printf("Review and rename to bench_cases.json if correct.\n")
	}
}

func findByText(lines []string, text string) string {
	if text == "" {
		return ""
	}
	lower := strings.ToLower(text)
	// Prefer interactive elements (a, button, input) over containers (div, section).
	// Prefer visible elements (non-zero bounds) over hidden ones.
	type candidate struct {
		id          string
		tag         string
		hasNonZero  bool
		interactive bool
	}
	var candidates []candidate
	for _, line := range lines {
		if !strings.Contains(line, "ID: ") {
			continue
		}
		lineText := ""
		if ti := strings.Index(line, "Text: \""); ti >= 0 {
			rest := line[ti+7:]
			if ei := strings.Index(rest, "\""); ei > 0 {
				lineText = rest[:ei]
			}
		}
		if lineText == "" || !strings.Contains(strings.ToLower(lineText), lower) {
			continue
		}
		// Extract ID
		idStart := strings.Index(line, "ID: ") + 4
		idEnd := strings.Index(line[idStart:], " ")
		if idEnd <= 0 {
			continue
		}
		id := line[idStart : idStart+idEnd]
		// Extract tag
		tag := ""
		if ti := strings.Index(line, "Tag: "); ti >= 0 {
			rest := line[ti+5:]
			if si := strings.Index(rest, " "); si > 0 {
				tag = rest[:si]
			}
		}
		// Check bounds
		hasNonZero := !strings.Contains(line, "Bounds: 0.0000,0.0000,0.0000,0.0000")
		interactive := tag == "a" || tag == "button" || tag == "input" || tag == "select"

		candidates = append(candidates, candidate{id, tag, hasNonZero, interactive})
	}
	// Sort: interactive+visible > interactive+hidden > container+visible > container+hidden
	for _, c := range candidates {
		if c.interactive && c.hasNonZero {
			return c.id
		}
	}
	for _, c := range candidates {
		if c.interactive {
			return c.id
		}
	}
	for _, c := range candidates {
		if c.hasNonZero {
			return c.id
		}
	}
	if len(candidates) > 0 {
		return candidates[0].id
	}
	return ""
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
