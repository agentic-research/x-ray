package navigator

import (
	"context"
	"strings"
	"testing"

	"github.com/agentic-research/x-ray/internal/mache"
)

// testProjection builds a SemanticProjection from the standard test fixtures.
func testProjection() *SemanticProjection {
	mounts := []mache.Mount{
		{
			VirtualPath: "/header/nav",
			MacheID:     "mache-0",
			Description: "Top navigation bar",
			Bounds:      [4]float64{0, 0, 1.0, 0.1},
		},
		{
			VirtualPath: "/main/stories",
			MacheID:     "mache-10",
			Description: "Main story listing",
			Bounds:      [4]float64{0.1, 0.2, 0.7, 0.6},
		},
	}

	summary := `Interactive Elements:
ID: mache-0 | Parent: none | Tag: nav | Text: "Navigation"
ID: mache-1 | Parent: mache-0 | Tag: a | Text: "Home"
ID: mache-2 | Parent: mache-0 | Tag: a | Text: "About"
ID: mache-3 | Parent: mache-0 | Tag: input | Text: "Search"
ID: mache-10 | Parent: none | Tag: div | Text: "Stories"
ID: mache-11 | Parent: mache-10 | Tag: a | Text: "First Story Title"
ID: mache-13 | Parent: mache-10 | Tag: a | Text: "Second Story"
`
	return NewSemanticProjection(mounts, summary)
}

func TestFindTool_BasicSearch(t *testing.T) {
	ft := &FindTool{projection: testProjection()}

	result, action := ft.Execute(context.Background(), map[string]any{
		"query": "search",
	})

	if action != nil {
		t.Fatal("find should not return an action")
	}
	if result == "" {
		t.Fatal("expected non-empty result")
	}
	// Should match the search input element.
	if !strings.Contains(strings.ToLower(result), "search") {
		t.Errorf("result should contain 'search': %s", result)
	}
	// Should include a semantic path.
	if !strings.Contains(result, "/browser/") {
		t.Errorf("result should contain a semantic path: %s", result)
	}
	t.Logf("find result:\n%s", result)
}

func TestFindTool_NoMatch(t *testing.T) {
	ft := &FindTool{projection: testProjection()}

	result, _ := ft.Execute(context.Background(), map[string]any{
		"query": "zzzznonexistent",
	})

	if !strings.Contains(result, "No matches") {
		t.Errorf("expected 'No matches' message, got: %s", result)
	}
}

func TestFindTool_EmptyQuery(t *testing.T) {
	ft := &FindTool{projection: testProjection()}

	result, _ := ft.Execute(context.Background(), map[string]any{
		"query": "",
	})

	if !strings.Contains(result, "Error") {
		t.Errorf("expected error for empty query, got: %s", result)
	}
}

func TestFindTool_RoleFilter(t *testing.T) {
	ft := &FindTool{projection: testProjection()}

	// Searching for "home" should find the Home link.
	result, _ := ft.Execute(context.Background(), map[string]any{
		"query": "home",
	})

	if !strings.Contains(result, "link") {
		t.Errorf("expected result to show 'link' role: %s", result)
	}
}

func TestFindTool_TopNLimit(t *testing.T) {
	ft := &FindTool{projection: testProjection()}

	// Broad query that matches many things.
	result, _ := ft.Execute(context.Background(), map[string]any{
		"query": "a",
	})

	// Count result lines (non-empty).
	lines := 0
	for _, line := range strings.Split(result, "\n") {
		if strings.TrimSpace(line) != "" {
			lines++
		}
	}
	// Should not exceed 5 results (1 line each + header possible).
	if lines > 6 {
		t.Errorf("expected at most 5 results, got %d lines:\n%s", lines, result)
	}
}
