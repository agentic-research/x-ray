package navigator

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"google.golang.org/genai"
)

// FindTool searches the semantic projection for elements matching a query.
// Returns top 5 ranked matches with path, role, action, and text.
type FindTool struct {
	projection *SemanticProjection
}

func (f *FindTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "find",
		Description: "Search for page elements by text or role. Returns the top 5 matches with their semantic paths, roles, and actions. Use the returned path with act() to interact.",
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"query": {Type: genai.TypeString, Description: "Text to search for (case-insensitive). Matches against element text, path segments, and roles."},
			},
			Required: []string{"query"},
		},
	}
}

// scored pairs a PathInfo with a relevance score for ranking.
type scored struct {
	pi    PathInfo
	score int
}

func (f *FindTool) Execute(_ context.Context, args map[string]any) (string, *ActionResult) {
	query, _ := args["query"].(string)
	if query == "" {
		return "Error: query is required", nil
	}

	lower := strings.ToLower(query)
	var results []scored

	for _, pi := range f.projection.AllPaths() {
		score := 0
		// Text match (strongest signal).
		if strings.Contains(strings.ToLower(pi.Text), lower) {
			score += 10
		}
		// Path match.
		if strings.Contains(strings.ToLower(pi.Path), lower) {
			score += 5
		}
		// Role match.
		if strings.Contains(strings.ToLower(pi.Role), lower) {
			score += 3
		}
		// Action match.
		if strings.Contains(strings.ToLower(pi.Action), lower) {
			score += 2
		}
		if score > 0 {
			results = append(results, scored{pi: pi, score: score})
		}
	}

	if len(results) == 0 {
		return fmt.Sprintf("No matches for %q", query), nil
	}

	// Sort by score descending, then path ascending for stability.
	sort.Slice(results, func(i, j int) bool {
		if results[i].score != results[j].score {
			return results[i].score > results[j].score
		}
		return results[i].pi.Path < results[j].pi.Path
	})

	// Cap at 5 results.
	if len(results) > 5 {
		results = results[:5]
	}

	var sb strings.Builder
	for _, r := range results {
		fmt.Fprintf(&sb, "%s  [%s, %s]", r.pi.Path, r.pi.Role, r.pi.Action)
		if r.pi.Text != "" {
			fmt.Fprintf(&sb, " %q", r.pi.Text)
		}
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

// LookTool shows the children of a zone (or top-level zones if omitted).
// Replaces ls + cat + stat with a single call that returns semantic paths,
// roles, and element text.
type LookTool struct {
	projection *SemanticProjection
}

func (l *LookTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "look",
		Description: "Show page zones and their elements. Without a zone path, shows all top-level zones. With a zone path, shows all elements inside that zone with their roles and text.",
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"zone": {Type: genai.TypeString, Description: "Optional: semantic zone path (e.g., '/browser/header/nav'). Omit to see all zones."},
			},
		},
	}
}

func (l *LookTool) Execute(_ context.Context, args map[string]any) (string, *ActionResult) {
	zone, _ := args["zone"].(string)

	allPaths := l.projection.AllPaths()
	if len(allPaths) == 0 {
		return "No page elements available. Use browser.goto() to load a page first.", nil
	}

	if zone == "" {
		return l.showTopLevel(allPaths), nil
	}

	return l.showZoneChildren(zone, allPaths), nil
}

// showTopLevel groups paths by their zone (first two path segments after /browser/)
// and shows a summary of each zone.
func (l *LookTool) showTopLevel(allPaths []PathInfo) string {
	// Group by zone: /browser/<region>/<zone-slug>
	zones := make(map[string][]PathInfo)
	var zoneOrder []string
	for _, pi := range allPaths {
		parts := strings.Split(pi.Path, "/")
		// /browser/region/zone -> parts = ["", "browser", "region", "zone", ...]
		if len(parts) < 4 {
			continue
		}
		zoneKey := strings.Join(parts[:4], "/")
		if _, exists := zones[zoneKey]; !exists {
			zoneOrder = append(zoneOrder, zoneKey)
		}
		zones[zoneKey] = append(zones[zoneKey], pi)
	}

	var sb strings.Builder
	for _, zk := range zoneOrder {
		children := zones[zk]
		// Count interactive elements.
		interactive := 0
		for _, pi := range children {
			if pi.Action != "none" {
				interactive++
			}
		}
		fmt.Fprintf(&sb, "%s  (%d elements, %d interactive)\n", zk, len(children), interactive)
	}

	if sb.Len() == 0 {
		return "No zones found."
	}
	return strings.TrimRight(sb.String(), "\n")
}

// showZoneChildren lists all elements whose path starts with the given zone prefix.
func (l *LookTool) showZoneChildren(zone string, allPaths []PathInfo) string {
	// Normalize: ensure zone doesn't end with /
	zone = strings.TrimRight(zone, "/")

	var sb strings.Builder
	count := 0
	for _, pi := range allPaths {
		if pi.Path == zone || strings.HasPrefix(pi.Path, zone+"/") {
			fmt.Fprintf(&sb, "%s  [%s, %s]", pi.Path, pi.Role, pi.Action)
			if pi.Text != "" {
				fmt.Fprintf(&sb, " %q", pi.Text)
			}
			sb.WriteByte('\n')
			count++
		}
	}

	if count == 0 {
		return fmt.Sprintf("No elements found under %q. Use look() without arguments to see available zones.", zone)
	}
	return strings.TrimRight(sb.String(), "\n")
}
