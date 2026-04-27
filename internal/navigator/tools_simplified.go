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
