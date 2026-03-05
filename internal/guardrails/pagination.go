package guardrails

import (
	"fmt"
	"sort"
	"strings"
)

// PageVisit records a URL + viewport range seen during navigation.
type PageVisit struct {
	URL      string
	StartPct int // viewport start percentage (0-100)
	EndPct   int // viewport end percentage (0-100)
}

// RecordPageVisit tracks that the agent has seen the given URL at the given
// scroll position. startPct/endPct are viewport percentages (0-100).
func (gs *GoalState) RecordPageVisit(url string, startPct, endPct int) {
	if !gs.Enabled || url == "" {
		return
	}

	key := fmt.Sprintf("%s#%d-%d", url, startPct, endPct)

	gs.mu.Lock()
	gs.VisitedPages[key]++
	count := gs.VisitedPages[key]
	gs.mu.Unlock()

	if count > 1 {
		gs.Log("pagination", fmt.Sprintf("revisit #%d to %s (%d-%d%%)", count, url, startPct, endPct))
	}
}

// VisitedSummary returns a compact one-line summary of visited pages, or ""
// if no pages have been recorded.
//
// Example: "PAGES VISITED: example.com (0-50%, 50-100%), other.com (0-100%)"
func (gs *GoalState) VisitedSummary() string {
	gs.mu.RLock()
	defer gs.mu.RUnlock()

	if len(gs.VisitedPages) == 0 {
		return ""
	}

	// Group by URL, collect unique ranges.
	type urlRanges struct {
		url    string
		ranges []string
	}
	seen := make(map[string]*urlRanges)
	var order []string

	for key := range gs.VisitedPages {
		// Parse "url#start-end" format.
		idx := strings.LastIndex(key, "#")
		if idx < 0 {
			continue
		}
		url := key[:idx]
		rangePart := key[idx+1:]

		if _, ok := seen[url]; !ok {
			seen[url] = &urlRanges{url: url}
			order = append(order, url)
		}
		seen[url].ranges = append(seen[url].ranges, rangePart)
	}

	var parts []string
	for _, url := range order {
		ur := seen[url]
		sort.Strings(ur.ranges) // deterministic ordering for reproducible prompts
		short := shortenURL(url)
		parts = append(parts, fmt.Sprintf("%s (%s)", short, strings.Join(ur.ranges, ", ")))
	}

	return "PAGES VISITED: " + strings.Join(parts, "; ")
}

// shortenURL strips the protocol prefix for compact display.
func shortenURL(url string) string {
	for _, prefix := range []string{"https://", "http://"} {
		url = strings.TrimPrefix(url, prefix)
	}
	if len(url) > 60 {
		url = url[:57] + "..."
	}
	return url
}
