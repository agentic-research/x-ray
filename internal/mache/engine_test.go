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
	e.LoadChildren(sampleSummary, nil)

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

	e.LoadChildren(bigSummary, nil)

	cEntries, err := e.ListDir("/main/news_feed/_c")
	if err != nil {
		t.Fatalf("ListDir _c failed: %v", err)
	}
	if len(cEntries) > 200 {
		t.Errorf("expected capped children, got %d", len(cEntries))
	}
}

func TestFormatByPrimaryItems(t *testing.T) {
	tests := []struct {
		name         string
		descendants  []SummaryElement
		primaryItems []string
		wantItems    int    // number of "Item N:" entries
		wantContains string // substring that must appear
	}{
		{
			name: "only primary items listed",
			descendants: []SummaryElement{
				{ID: "m-1", Tag: "span", Text: ""},       // not primary
				{ID: "m-2", Tag: "a", Text: "Story One"}, // primary
				{ID: "m-3", Tag: "a", Text: "(example.com)"},
				{ID: "m-4", Tag: "span", Text: ""},       // not primary
				{ID: "m-5", Tag: "a", Text: "Story Two"}, // primary
				{ID: "m-6", Tag: "a", Text: "(other.com)"},
			},
			primaryItems: []string{"m-2", "m-5"},
			wantItems:    2, // only primary items
			wantContains: "Item 1:",
		},
		{
			name: "non-primary descendants excluded",
			descendants: []SummaryElement{
				{ID: "m-10", Tag: "a", Text: "First"},
				{ID: "m-11", Tag: "span", Text: "meta"},
				{ID: "m-12", Tag: "a", Text: "Second"},
				{ID: "m-13", Tag: "a", Text: "Extra Link"},
			},
			primaryItems: []string{"m-10", "m-12"},
			wantItems:    2, // only primary, m-13 excluded
			wantContains: "Item 1:",
		},
		{
			name: "missing primary items skipped gracefully",
			descendants: []SummaryElement{
				{ID: "m-20", Tag: "a", Text: "Title"},
			},
			primaryItems: []string{"m-20", "m-99"},
			wantItems:    1, // m-99 not in descendants
			wantContains: `m-20 | a | "Title"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatByPrimaryItems(tt.descendants, tt.primaryItems)
			gotItems := strings.Count(got, "Item ")
			if gotItems != tt.wantItems {
				t.Errorf("expected %d items, got %d\noutput:\n%s", tt.wantItems, gotItems, got)
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

	// With primary items: should use compact primary-item listing (only primaries)
	withPrimary := formatGroupedChildren(descendants, []string{"m-2", "m-5"})
	if !strings.Contains(withPrimary, "Item 1:") {
		t.Errorf("expected item listing with primary items, got:\n%s", withPrimary)
	}
	if !strings.Contains(withPrimary, `"Story"`) {
		t.Errorf("primary item text missing:\n%s", withPrimary)
	}
	if strings.Count(withPrimary, "Item ") != 2 {
		t.Errorf("expected only 2 primary items, got:\n%s", withPrimary)
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
	e.LoadChildren(summary, nil)

	content, err := e.ReadFile("/main/stories/children")
	if err != nil {
		t.Fatalf("ReadFile children failed: %v", err)
	}

	// 2 primary items; non-primary elements are <span> so not included
	if strings.Count(content, "Item ") != 2 {
		t.Errorf("expected 2 items, got:\n%s", content)
	}

	// Primary items should appear first
	if !strings.Contains(content, `"First Story"`) {
		t.Errorf("missing First Story in output:\n%s", content)
	}
	if !strings.Contains(content, `"Second Story"`) {
		t.Errorf("missing Second Story in output:\n%s", content)
	}
}

func TestResolveMacheIDChild(t *testing.T) {
	e := NewEngine()
	if err := e.ApplySchema(sampleSchema); err != nil {
		t.Fatal(err)
	}
	e.LoadChildren(sampleSummary, nil)

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

func TestParseSummaryWithPath(t *testing.T) {
	summary := `Interactive Elements:
ID: mache-0 | Parent: none | Tag: nav | Text: "Site Nav" | Path: body > nav.main-nav
ID: mache-1 | Parent: mache-0 | Tag: a | Text: "Home" | Path: nav.main-nav > ul.links > a
ID: mache-2 | Parent: mache-0 | Tag: a | Text: "About" | Path: nav.main-nav > ul.links > a | AXRole: link
`
	elements := parseSummary(summary)
	if len(elements) != 3 {
		t.Fatalf("expected 3 elements, got %d", len(elements))
	}

	// Path parsed correctly
	if elements[0].Path != "body > nav.main-nav" {
		t.Errorf("expected Path 'body > nav.main-nav', got %q", elements[0].Path)
	}
	if elements[1].Path != "nav.main-nav > ul.links > a" {
		t.Errorf("expected Path 'nav.main-nav > ul.links > a', got %q", elements[1].Path)
	}

	// Path + AXRole on same line
	if elements[2].Path != "nav.main-nav > ul.links > a" {
		t.Errorf("expected Path with AXRole present, got %q", elements[2].Path)
	}
	if elements[2].AXRole != "link" {
		t.Errorf("expected AXRole 'link' alongside Path, got %q", elements[2].AXRole)
	}

	// Core fields still correct
	if elements[0].ID != "mache-0" || elements[0].Tag != "nav" || elements[0].Text != "Site Nav" {
		t.Errorf("core fields wrong: %+v", elements[0])
	}
}

func TestParseSummaryBackwardCompatPath(t *testing.T) {
	// Old format without Path — must still parse with empty Path
	elements := parseSummary(sampleSummary)
	for i, el := range elements {
		if el.Path != "" {
			t.Errorf("element %d: expected empty Path for old format, got %q", i, el.Path)
		}
	}
}

func TestLoadChildrenWithResolvedItems(t *testing.T) {
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
ID: mache-14 | Parent: mache-10 | Tag: a | Text: "Third Story (new after scroll)"
ID: mache-15 | Parent: mache-10 | Tag: a | Text: "Fourth Story (new after scroll)"
`
	e := NewEngine()
	if err := e.ApplySchema(schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	// Pass resolved items that override the schema's primary_items
	resolved := map[string][]string{
		"mache-10": {"mache-11", "mache-13", "mache-14", "mache-15"},
	}
	e.LoadChildren(summary, resolved)

	content, err := e.ReadFile("/main/stories/children")
	if err != nil {
		t.Fatalf("ReadFile children failed: %v", err)
	}

	// All 4 resolved items should appear as primary items
	if !strings.Contains(content, `"First Story"`) {
		t.Errorf("missing First Story:\n%s", content)
	}
	if !strings.Contains(content, `"Third Story (new after scroll)"`) {
		t.Errorf("missing Third Story (resolved item):\n%s", content)
	}
	if !strings.Contains(content, `"Fourth Story (new after scroll)"`) {
		t.Errorf("missing Fourth Story (resolved item):\n%s", content)
	}
	// Should have 4 primary items (resolved), mache-12 is <span> so not appended
	if strings.Count(content, "Item ") != 4 {
		t.Errorf("expected 4 items from resolved items, got:\n%s", content)
	}
}

func TestZoneSelectors(t *testing.T) {
	schema := `{
  "mounts": [
    {
      "virtual_path": "/header/nav",
      "mache_id": "mache-0",
      "description": "Top navigation"
    },
    {
      "virtual_path": "/main/stories",
      "mache_id": "mache-10",
      "description": "Story list",
      "primary_items": ["mache-11"],
      "item_selector": "div.Post > h3.title > a[data-mache-id]"
    },
    {
      "virtual_path": "/footer/links",
      "mache_id": "mache-50",
      "description": "Footer",
      "item_selector": "footer > a[data-mache-id]"
    }
  ]
}`
	e := NewEngine()
	if err := e.ApplySchema(schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	selectors := e.ZoneSelectors()

	// Only zones with non-empty ItemSelector should be returned
	if len(selectors) != 2 {
		t.Fatalf("expected 2 selectors, got %d: %v", len(selectors), selectors)
	}

	if selectors["mache-10"] != "div.Post > h3.title > a[data-mache-id]" {
		t.Errorf("wrong selector for mache-10: %q", selectors["mache-10"])
	}
	if selectors["mache-50"] != "footer > a[data-mache-id]" {
		t.Errorf("wrong selector for mache-50: %q", selectors["mache-50"])
	}

	// mache-0 (no selector) should not appear
	if _, ok := selectors["mache-0"]; ok {
		t.Error("mache-0 should not have a selector")
	}
}

func TestLoadChildrenLeafZoneRoot(t *testing.T) {
	// Simulates HN: the Cartographer picks the first story link (mache-13)
	// as the zone root. Stories are siblings in a table, not descendants
	// of mache-13. The engine should fall back to collecting primary items
	// directly from the parsed summary.
	schema := `{
  "mounts": [
    {
      "virtual_path": "/main/stories",
      "mache_id": "mache-13",
      "description": "News stories",
      "primary_items": ["mache-13", "mache-20", "mache-27"]
    }
  ]
}`
	// Stories are siblings under different parents (table rows),
	// NOT descendants of mache-13.
	summary := `Interactive Elements:
ID: mache-10 | Parent: none | Tag: table | Text: ""
ID: mache-11 | Parent: mache-10 | Tag: tr | Text: ""
ID: mache-13 | Parent: mache-11 | Tag: a | Text: "First Story"
ID: mache-18 | Parent: mache-10 | Tag: tr | Text: ""
ID: mache-20 | Parent: mache-18 | Tag: a | Text: "Second Story"
ID: mache-25 | Parent: mache-10 | Tag: tr | Text: ""
ID: mache-27 | Parent: mache-25 | Tag: a | Text: "Third Story"
`
	e := NewEngine()
	if err := e.ApplySchema(schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	e.LoadChildren(summary, nil)

	// children file should exist (primary item fallback kicks in)
	content, err := e.ReadFile("/main/stories/children")
	if err != nil {
		t.Fatalf("children file missing (leaf zone root fallback failed): %v", err)
	}

	// All 3 primary items should appear
	if !strings.Contains(content, `"First Story"`) {
		t.Errorf("missing First Story:\n%s", content)
	}
	if !strings.Contains(content, `"Second Story"`) {
		t.Errorf("missing Second Story:\n%s", content)
	}
	if !strings.Contains(content, `"Third Story"`) {
		t.Errorf("missing Third Story:\n%s", content)
	}
	if strings.Count(content, "Item ") != 3 {
		t.Errorf("expected 3 items, got:\n%s", content)
	}

	// _c/ should also be populated
	cEntries, err := e.ListDir("/main/stories/_c")
	if err != nil {
		t.Fatalf("_c/ dir missing: %v", err)
	}
	if len(cEntries) != 3 {
		t.Errorf("expected 3 child dirs, got %d: %v", len(cEntries), cEntries)
	}
}
