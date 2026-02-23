package mache

import (
	"fmt"
	"strings"
	"testing"
)

const sampleSchema = `{
  "mounts": [
    {
      "virtual_path": "/header/global_nav",
      "mache_id": "mache-0",
      "description": "Top navigation bar with logo and search"
    },
    {
      "virtual_path": "/main/news_feed",
      "mache_id": "mache-15",
      "description": "Main news article listing"
    },
    {
      "virtual_path": "/footer/links",
      "mache_id": "mache-200",
      "description": "Footer with legal and help links"
    }
  ]
}`

func TestApplySchema(t *testing.T) {
	e := NewEngine()
	if err := e.ApplySchema(sampleSchema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	// Root should have 3 top-level dirs
	entries, err := e.ListDir("/")
	if err != nil {
		t.Fatalf("ListDir / failed: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 root entries, got %d: %v", len(entries), entries)
	}
}

func TestListDir(t *testing.T) {
	e := NewEngine()
	if err := e.ApplySchema(sampleSchema); err != nil {
		t.Fatal(err)
	}

	entries, err := e.ListDir("/header/global_nav")
	if err != nil {
		t.Fatalf("ListDir failed: %v", err)
	}

	// Should contain mache_id and description files
	found := map[string]bool{}
	for _, name := range entries {
		found[name] = true
	}
	if !found["mache_id"] {
		t.Error("missing mache_id file")
	}
	if !found["description"] {
		t.Error("missing description file")
	}
}

func TestReadFile(t *testing.T) {
	e := NewEngine()
	if err := e.ApplySchema(sampleSchema); err != nil {
		t.Fatal(err)
	}

	content, err := e.ReadFile("/main/news_feed/description")
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if content != "Main news article listing" {
		t.Errorf("unexpected description: %q", content)
	}

	id, err := e.ReadFile("/main/news_feed/mache_id")
	if err != nil {
		t.Fatalf("ReadFile mache_id failed: %v", err)
	}
	if id != "mache-15" {
		t.Errorf("unexpected mache_id: %q", id)
	}
}

func TestResolveMacheID(t *testing.T) {
	e := NewEngine()
	if err := e.ApplySchema(sampleSchema); err != nil {
		t.Fatal(err)
	}

	id, err := e.ResolveMacheID("/header/global_nav")
	if err != nil {
		t.Fatalf("ResolveMacheID failed: %v", err)
	}
	if id != "mache-0" {
		t.Errorf("expected mache-0, got %q", id)
	}

	id, err = e.ResolveMacheID("/footer/links")
	if err != nil {
		t.Fatalf("ResolveMacheID footer failed: %v", err)
	}
	if id != "mache-200" {
		t.Errorf("expected mache-200, got %q", id)
	}
}

func TestResolveNotFound(t *testing.T) {
	e := NewEngine()
	if err := e.ApplySchema(sampleSchema); err != nil {
		t.Fatal(err)
	}

	_, err := e.ListDir("/nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent path")
	}

	_, err = e.ReadFile("/header/global_nav")
	if err == nil {
		t.Error("expected error reading a directory")
	}
}

func TestToTopology(t *testing.T) {
	e := NewEngine()
	if err := e.ApplySchema(sampleSchema); err != nil {
		t.Fatal(err)
	}

	topo := e.ToTopology()
	if topo.Version != "v1" {
		t.Errorf("expected v1, got %q", topo.Version)
	}
	if len(topo.Nodes) != 3 {
		t.Errorf("expected 3 nodes, got %d", len(topo.Nodes))
	}
}

const sampleSummary = `Interactive Elements:
ID: mache-0 | Parent: none | Tag: nav | Text: "Site Navigation"
ID: mache-1 | Parent: mache-0 | Tag: a | Text: "Home"
ID: mache-2 | Parent: mache-0 | Tag: a | Text: "About"
ID: mache-15 | Parent: none | Tag: div | Text: "News Feed"
ID: mache-16 | Parent: mache-15 | Tag: a | Text: "First Story Title"
ID: mache-17 | Parent: mache-15 | Tag: a | Text: "Second Story"
ID: mache-18 | Parent: mache-15 | Tag: a | Text: "Third Story"
ID: mache-200 | Parent: none | Tag: footer | Text: "Footer Links"
`

func TestParseSummary(t *testing.T) {
	elements := parseSummary(sampleSummary)
	if len(elements) != 8 {
		t.Fatalf("expected 8 elements, got %d", len(elements))
	}
	// Spot-check first and a child element
	if elements[0].ID != "mache-0" || elements[0].ParentID != "none" || elements[0].Tag != "nav" {
		t.Errorf("unexpected first element: %+v", elements[0])
	}
	if elements[4].ID != "mache-16" || elements[4].ParentID != "mache-15" || elements[4].Text != "First Story Title" {
		t.Errorf("unexpected mache-16 element: %+v", elements[4])
	}
}

func TestCollectDescendants(t *testing.T) {
	elements := parseSummary(sampleSummary)
	pm := buildParentMap(elements)

	// Direct children of mache-15
	desc := collectDescendants(pm, "mache-15", 1)
	if len(desc) != 3 {
		t.Fatalf("expected 3 direct children of mache-15, got %d", len(desc))
	}

	// Depth-2 BFS from mache-0 (has direct children only, no grandchildren in sample)
	desc = collectDescendants(pm, "mache-0", 2)
	if len(desc) != 2 {
		t.Fatalf("expected 2 descendants of mache-0, got %d", len(desc))
	}
}

func TestLoadChildren(t *testing.T) {
	e := NewEngine()
	if err := e.ApplySchema(sampleSchema); err != nil {
		t.Fatal(err)
	}
	e.LoadChildren(sampleSummary)

	// news_feed (mache-15) should have children file and _c/ dir
	entries, err := e.ListDir("/main/news_feed")
	if err != nil {
		t.Fatalf("ListDir failed: %v", err)
	}
	found := map[string]bool{}
	for _, name := range entries {
		found[name] = true
	}
	if !found["children"] {
		t.Error("missing children file in news_feed")
	}
	if !found["_c/"] {
		t.Error("missing _c/ directory in news_feed")
	}

	// Read children file
	content, err := e.ReadFile("/main/news_feed/children")
	if err != nil {
		t.Fatalf("ReadFile children failed: %v", err)
	}
	if !strings.Contains(content, "mache-16") {
		t.Errorf("children file missing mache-16: %q", content)
	}

	// Verify _c/ subdirectory entries
	cEntries, err := e.ListDir("/main/news_feed/_c")
	if err != nil {
		t.Fatalf("ListDir _c failed: %v", err)
	}
	if len(cEntries) != 3 {
		t.Errorf("expected 3 child dirs in _c/, got %d: %v", len(cEntries), cEntries)
	}

	// Verify child has mache_id, tag, text files
	childEntries, err := e.ListDir("/main/news_feed/_c/mache-16")
	if err != nil {
		t.Fatalf("ListDir child failed: %v", err)
	}
	childFound := map[string]bool{}
	for _, name := range childEntries {
		childFound[name] = true
	}
	for _, f := range []string{"mache_id", "tag", "text"} {
		if !childFound[f] {
			t.Errorf("missing %s file in child dir", f)
		}
	}
}

func TestLoadChildrenCap(t *testing.T) {
	e := NewEngine()
	if err := e.ApplySchema(sampleSchema); err != nil {
		t.Fatal(err)
	}

	// Build a summary with 40 children under mache-15
	var lines []string
	lines = append(lines, "Interactive Elements:")
	lines = append(lines, `ID: mache-15 | Parent: none | Tag: div | Text: "Feed"`)
	for i := range 40 {
		lines = append(lines, fmt.Sprintf(`ID: mache-%d | Parent: mache-15 | Tag: a | Text: "Story %d"`, 100+i, i))
	}
	bigSummary := strings.Join(lines, "\n")

	e.LoadChildren(bigSummary)

	cEntries, err := e.ListDir("/main/news_feed/_c")
	if err != nil {
		t.Fatalf("ListDir _c failed: %v", err)
	}
	if len(cEntries) != 30 {
		t.Errorf("expected 30 children (capped), got %d", len(cEntries))
	}
}

func TestFormatByPrimaryItems(t *testing.T) {
	tests := []struct {
		name         string
		descendants  []SummaryElement
		primaryItems []string
		wantItems    int    // number of "--- Item N ---" headers
		wantContains string // substring that must appear
	}{
		{
			name: "HN-like: story titles as primary items",
			descendants: []SummaryElement{
				{ID: "m-1", Tag: "span", Text: ""},       // upvote arrow
				{ID: "m-2", Tag: "a", Text: "Story One"}, // primary
				{ID: "m-3", Tag: "a", Text: "(example.com)"},
				{ID: "m-4", Tag: "span", Text: ""},       // upvote arrow
				{ID: "m-5", Tag: "a", Text: "Story Two"}, // primary
				{ID: "m-6", Tag: "a", Text: "(other.com)"},
			},
			primaryItems: []string{"m-2", "m-5"},
			wantItems:    2, // Item 1 (m-2,m-3) + Item 2 (m-5,m-6); preamble (m-1) skipped
			wantContains: "--- Item 1 ---",
		},
		{
			name: "no preamble: first element is primary",
			descendants: []SummaryElement{
				{ID: "m-10", Tag: "a", Text: "First"},
				{ID: "m-11", Tag: "span", Text: "meta"},
				{ID: "m-12", Tag: "a", Text: "Second"},
			},
			primaryItems: []string{"m-10", "m-12"},
			wantItems:    2,
			wantContains: "--- Item 1 ---",
		},
		{
			name: "skips structural tags",
			descendants: []SummaryElement{
				{ID: "m-20", Tag: "a", Text: "Title"},
				{ID: "m-21", Tag: "tbody", Text: ""},
				{ID: "m-22", Tag: "a", Text: "Link"},
			},
			primaryItems: []string{"m-20"},
			wantItems:    1,
			wantContains: `m-22 | a | "Link"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatByPrimaryItems(tt.descendants, tt.primaryItems)
			gotItems := strings.Count(got, "--- Item ")
			if gotItems != tt.wantItems {
				t.Errorf("expected %d item headers, got %d\noutput:\n%s", tt.wantItems, gotItems, got)
			}
			if !strings.Contains(got, tt.wantContains) {
				t.Errorf("output missing %q\noutput:\n%s", tt.wantContains, got)
			}
		})
	}
}

func TestFormatGroupedChildrenDispatch(t *testing.T) {
	// Need enough non-empty elements so emptyCount (2) < len/2 (4) triggers heuristic
	descendants := []SummaryElement{
		{ID: "m-1", Tag: "span", Text: ""},
		{ID: "m-2", Tag: "a", Text: "Story"},
		{ID: "m-3", Tag: "a", Text: "meta1"},
		{ID: "m-4", Tag: "span", Text: ""},
		{ID: "m-5", Tag: "a", Text: "Another"},
		{ID: "m-6", Tag: "a", Text: "meta2"},
	}

	// With primary items: should use primary-item grouping
	withPrimary := formatGroupedChildren(descendants, []string{"m-2", "m-5"})
	if !strings.Contains(withPrimary, "--- Item ") {
		t.Errorf("expected item headers with primary items, got:\n%s", withPrimary)
	}
	if !strings.Contains(withPrimary, `"Story"`) {
		t.Errorf("primary item text missing:\n%s", withPrimary)
	}

	// With nil primary items: should fall back to empty-separator heuristic
	withHeuristic := formatGroupedChildren(descendants, nil)
	if !strings.Contains(withHeuristic, "--- Item ") {
		t.Errorf("expected item headers from heuristic fallback, got:\n%s", withHeuristic)
	}

	// With empty slice: same fallback
	withEmpty := formatGroupedChildren(descendants, []string{})
	if withEmpty != withHeuristic {
		t.Errorf("empty slice should behave same as nil\nempty: %s\nnil: %s", withEmpty, withHeuristic)
	}
}

func TestLoadChildrenWithPrimaryItems(t *testing.T) {
	schema := `{
  "mounts": [
    {
      "virtual_path": "/main/stories",
      "mache_id": "mache-10",
      "description": "Story list",
      "primary_items": ["mache-11", "mache-13"]
    }
  ]
}`
	summary := `Interactive Elements:
ID: mache-10 | Parent: none | Tag: div | Text: "Stories"
ID: mache-11 | Parent: mache-10 | Tag: a | Text: "First Story"
ID: mache-12 | Parent: mache-10 | Tag: span | Text: "(example.com)"
ID: mache-13 | Parent: mache-10 | Tag: a | Text: "Second Story"
ID: mache-14 | Parent: mache-10 | Tag: span | Text: "(other.com)"
`

	e := NewEngine()
	if err := e.ApplySchema(schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	e.LoadChildren(summary)

	content, err := e.ReadFile("/main/stories/children")
	if err != nil {
		t.Fatalf("ReadFile children failed: %v", err)
	}

	// Should have 2 items grouped by primary items
	if strings.Count(content, "--- Item ") != 2 {
		t.Errorf("expected 2 item groups, got:\n%s", content)
	}

	// First item should contain the story title
	if !strings.Contains(content, `"First Story"`) {
		t.Errorf("missing First Story in output:\n%s", content)
	}

	// Metadata should be indented under the item
	if !strings.Contains(content, `"(example.com)"`) {
		t.Errorf("missing metadata in output:\n%s", content)
	}
}

func TestResolveMacheIDChild(t *testing.T) {
	e := NewEngine()
	if err := e.ApplySchema(sampleSchema); err != nil {
		t.Fatal(err)
	}
	e.LoadChildren(sampleSummary)

	id, err := e.ResolveMacheID("/main/news_feed/_c/mache-16")
	if err != nil {
		t.Fatalf("ResolveMacheID child failed: %v", err)
	}
	if id != "mache-16" {
		t.Errorf("expected mache-16, got %q", id)
	}
}

func TestParseSummaryWithAXFields(t *testing.T) {
	summary := `Interactive Elements:
ID: mache-0 | Parent: none | Tag: nav | Text: "Site Navigation" | AXRole: navigation | AXName: "Primary navigation"
ID: mache-1 | Parent: mache-0 | Tag: a | Text: "Home" | AXRole: link | AXName: "Home"
ID: mache-2 | Parent: mache-0 | Tag: a | Text: "About" | AXRole: link
`
	elements := parseSummary(summary)
	if len(elements) != 3 {
		t.Fatalf("expected 3 elements, got %d", len(elements))
	}

	// First element: full AX data
	if elements[0].AXRole != "navigation" {
		t.Errorf("expected AXRole 'navigation', got %q", elements[0].AXRole)
	}
	if elements[0].AXName != "Primary navigation" {
		t.Errorf("expected AXName 'Primary navigation', got %q", elements[0].AXName)
	}

	// Second element: both AX fields
	if elements[1].AXRole != "link" {
		t.Errorf("expected AXRole 'link', got %q", elements[1].AXRole)
	}
	if elements[1].AXName != "Home" {
		t.Errorf("expected AXName 'Home', got %q", elements[1].AXName)
	}

	// Third element: AXRole only, no AXName
	if elements[2].AXRole != "link" {
		t.Errorf("expected AXRole 'link', got %q", elements[2].AXRole)
	}
	if elements[2].AXName != "" {
		t.Errorf("expected empty AXName, got %q", elements[2].AXName)
	}

	// Core fields still parsed correctly
	if elements[0].ID != "mache-0" || elements[0].Tag != "nav" || elements[0].Text != "Site Navigation" {
		t.Errorf("core fields wrong: %+v", elements[0])
	}
}

func TestParseSummaryBackwardCompat(t *testing.T) {
	// Old format without AX fields — must still work
	elements := parseSummary(sampleSummary)
	if len(elements) != 8 {
		t.Fatalf("expected 8 elements, got %d", len(elements))
	}
	// All AX fields should be empty
	for i, el := range elements {
		if el.AXRole != "" {
			t.Errorf("element %d: expected empty AXRole, got %q", i, el.AXRole)
		}
		if el.AXName != "" {
			t.Errorf("element %d: expected empty AXName, got %q", i, el.AXName)
		}
	}
	// Spot check core parsing still works
	if elements[0].ID != "mache-0" || elements[0].Tag != "nav" {
		t.Errorf("element 0 core fields wrong: %+v", elements[0])
	}
	if elements[4].Text != "First Story Title" {
		t.Errorf("element 4 text wrong: %q", elements[4].Text)
	}
}
