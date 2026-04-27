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

func TestLookTool_TopLevel(t *testing.T) {
	lt := &LookTool{projection: testProjection()}

	// Omit zone -> show top-level zones.
	result, action := lt.Execute(context.Background(), map[string]any{})

	if action != nil {
		t.Fatal("look should not return an action")
	}
	if result == "" {
		t.Fatal("expected non-empty result")
	}
	// Should contain the regions from our test data.
	if !strings.Contains(result, "header") {
		t.Errorf("expected 'header' zone in result: %s", result)
	}
	if !strings.Contains(result, "main") {
		t.Errorf("expected 'main' zone in result: %s", result)
	}
	t.Logf("look top-level:\n%s", result)
}

func TestLookTool_ZoneChildren(t *testing.T) {
	sp := testProjection()
	lt := &LookTool{projection: sp}

	// Find a zone path that exists (the header zone).
	var headerZone string
	for _, pi := range sp.AllPaths() {
		if strings.Contains(pi.Path, "header") && !strings.Contains(pi.Path[len("/browser/header/"):], "/") {
			headerZone = pi.Path
			break
		}
	}
	if headerZone == "" {
		t.Skip("no header zone found in projection")
	}

	result, _ := lt.Execute(context.Background(), map[string]any{
		"zone": headerZone,
	})

	// Should list children of the header zone.
	if !strings.Contains(result, "Home") || !strings.Contains(result, "About") {
		t.Errorf("expected header children (Home, About) in result: %s", result)
	}
	t.Logf("look zone:\n%s", result)
}

func TestLookTool_UnknownZone(t *testing.T) {
	lt := &LookTool{projection: testProjection()}

	result, _ := lt.Execute(context.Background(), map[string]any{
		"zone": "/browser/nonexistent/zone",
	})

	// Should return a helpful message, not crash.
	if !strings.Contains(result, "No elements found") && !strings.Contains(result, "not found") {
		t.Errorf("expected error-ish message for unknown zone: %s", result)
	}
}
