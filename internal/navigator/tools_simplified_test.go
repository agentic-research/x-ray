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

func TestSemanticActTool_ResolvesPath(t *testing.T) {
	sp := testProjection()

	// Build a minimal ActTool backed by the standard test agent's NavFS.
	agent := newTestAgent()
	inner := agent.actTool

	sat := &SemanticActTool{
		inner:      inner,
		projection: sp,
	}

	// Find the semantic path for the "Home" link (mache-1).
	homePath := sp.SemanticPath("mache-1")
	if homePath == "" {
		t.Fatal("mache-1 should have a semantic path")
	}

	result, action := sat.Execute(context.Background(), map[string]any{
		"path":   homePath,
		"action": "click",
	})

	// Should resolve to mache-1 and attempt the click action.
	t.Logf("result=%q action=%+v", result, action)

	if action == nil {
		// Browser MemoryStore returns ErrActNotSupported, so ActTool falls through
		// to ActionResult dispatch. That path calls ResolveMacheID which needs
		// the mache-ID, not the semantic path. Our wrapper should have resolved it.
		t.Fatal("expected an ActionResult for browser element click")
	}
	if action.MacheID != "mache-1" {
		t.Errorf("expected mache-1, got %q", action.MacheID)
	}
}

func TestSemanticActTool_FallsBackToBareID(t *testing.T) {
	sp := testProjection()
	agent := newTestAgent()

	sat := &SemanticActTool{
		inner:      agent.actTool,
		projection: sp,
	}

	// Bare mache-ID should still work (passthrough).
	result, action := sat.Execute(context.Background(), map[string]any{
		"path":   "mache-10",
		"action": "click",
	})

	t.Logf("result=%q action=%+v", result, action)

	if action == nil {
		t.Fatal("expected ActionResult for bare mache-ID")
	}
	if action.MacheID != "mache-10" {
		t.Errorf("expected mache-10, got %q", action.MacheID)
	}
}

func TestSemanticActTool_UnknownPath(t *testing.T) {
	sp := testProjection()
	agent := newTestAgent()

	sat := &SemanticActTool{
		inner:      agent.actTool,
		projection: sp,
	}

	result, action := sat.Execute(context.Background(), map[string]any{
		"path":   "/browser/nonexistent/element",
		"action": "click",
	})

	// Should return an error, not panic.
	if action != nil {
		t.Fatal("expected nil action for unknown path")
	}
	if !strings.Contains(result, "Error") && !strings.Contains(result, "not found") {
		t.Errorf("expected error message, got: %s", result)
	}
}

func TestSemanticActTool_TypeAction(t *testing.T) {
	sp := testProjection()
	agent := newTestAgent()

	sat := &SemanticActTool{
		inner:      agent.actTool,
		projection: sp,
	}

	// Find the search input.
	searchPath := sp.SemanticPath("mache-3")
	if searchPath == "" {
		t.Skip("mache-3 not projected (no search input in test data)")
	}

	result, action := sat.Execute(context.Background(), map[string]any{
		"path":    searchPath,
		"action":  "type",
		"payload": "hello world",
	})

	t.Logf("result=%q action=%+v", result, action)
	if action == nil {
		t.Fatal("expected ActionResult for type action")
	}
	if action.Payload != "hello world" {
		t.Errorf("expected payload 'hello world', got %q", action.Payload)
	}
}
