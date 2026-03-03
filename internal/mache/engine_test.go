package mache

import (
	"encoding/json"
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

	// Read children file — ordinal format, no mache IDs exposed
	content, err := e.ReadFile("/main/news_feed/children")
	if err != nil {
		t.Fatalf("ReadFile children failed: %v", err)
	}
	if !strings.Contains(content, `[1] "First Story Title"`) {
		t.Errorf("children file missing first story: %q", content)
	}

	// Verify _c/ subdirectory entries (ordinal names)
	cEntries, err := e.ListDir("/main/news_feed/_c")
	if err != nil {
		t.Fatalf("ListDir _c failed: %v", err)
	}
	if len(cEntries) != 3 {
		t.Errorf("expected 3 child dirs in _c/, got %d: %v", len(cEntries), cEntries)
	}

	// Verify ordinal child dir has mache_id, tag, text files
	childEntries, err := e.ListDir("/main/news_feed/_c/1")
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

	// Verify mache_id file inside ordinal dir resolves to actual mache ID
	macheID, err := e.ReadFile("/main/news_feed/_c/1/mache_id")
	if err != nil {
		t.Fatalf("ReadFile mache_id failed: %v", err)
	}
	if macheID != "mache-16" {
		t.Errorf("expected mache-16 in _c/1/mache_id, got %q", macheID)
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

func TestSelectChildItems(t *testing.T) {
	descendants := []SummaryElement{
		{ID: "m-1", Tag: "span", Text: ""},
		{ID: "m-2", Tag: "a", Text: "Story One"},
		{ID: "m-3", Tag: "a", Text: "(example.com)"},
		{ID: "m-4", Tag: "span", Text: ""},
		{ID: "m-5", Tag: "a", Text: "Story Two"},
		{ID: "m-6", Tag: "a", Text: "(other.com)"},
	}

	// With primary items: those listed first, then supplemented with other text-bearing items
	items := selectChildItems(descendants, []string{"m-2", "m-5"})
	// 2 primary + 2 supplemented text items: (example.com), (other.com)
	if len(items) != 4 {
		t.Fatalf("expected 4 items (2 primary + 2 supplemented), got %d", len(items))
	}
	if items[0].ID != "m-2" || items[1].ID != "m-5" {
		t.Errorf("primary items should come first: %v", items)
	}
	// Supplemented items come after primaries
	if items[2].ID != "m-3" || items[3].ID != "m-6" {
		t.Errorf("supplemented items should follow primaries: %v", items)
	}

	// Missing primary items are skipped gracefully
	items = selectChildItems(descendants, []string{"m-2", "m-99"})
	// 1 primary (m-2) + 2 supplemented: (example.com), (other.com), Story Two
	if len(items) < 1 {
		t.Fatalf("expected at least 1 item (m-99 missing), got %d", len(items))
	}

	// Without primary items: all non-empty text descendants
	items = selectChildItems(descendants, nil)
	if len(items) != 4 {
		t.Fatalf("expected 4 non-empty items, got %d", len(items))
	}
	for _, item := range items {
		if item.Text == "" {
			t.Error("empty-text item should be filtered out")
		}
	}

	// Empty primary slice behaves same as nil
	items2 := selectChildItems(descendants, []string{})
	if len(items2) != len(items) {
		t.Errorf("empty slice should behave same as nil: got %d vs %d", len(items2), len(items))
	}
}

func TestFormatOrdinalChildren(t *testing.T) {
	items := []SummaryElement{
		{ID: "m-2", Tag: "a", Text: "Story One"},
		{ID: "m-5", Tag: "a", Text: "Story Two"},
	}
	got := formatOrdinalChildren(items)
	want := "[1] \"Story One\"\n[2] \"Story Two\""
	if got != want {
		t.Errorf("expected:\n%s\ngot:\n%s", want, got)
	}

	// No mache IDs should appear in output
	if strings.Contains(got, "m-2") || strings.Contains(got, "m-5") {
		t.Error("ordinal format should not expose mache IDs")
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

	// 2 primary items in ordinal format
	if !strings.Contains(content, `[1] "First Story"`) {
		t.Errorf("missing [1] First Story in output:\n%s", content)
	}
	if !strings.Contains(content, `[2] "Second Story"`) {
		t.Errorf("missing [2] Second Story in output:\n%s", content)
	}
	// 2 primary items + 2 supplemented text-bearing descendants (Stories, example.com, other.com)
	// "Stories" is the zone root text, "(example.com)" and "(other.com)" are non-primary text items.
	if strings.Count(content, "] \"") < 2 {
		t.Errorf("expected at least 2 items, got:\n%s", content)
	}
	// No mache IDs in children file
	if strings.Contains(content, "mache-") {
		t.Errorf("children file should not expose mache IDs:\n%s", content)
	}
}

func TestResolveMacheIDChild(t *testing.T) {
	e := NewEngine()
	if err := e.ApplySchema(sampleSchema); err != nil {
		t.Fatal(err)
	}
	e.LoadChildren(sampleSummary, nil)

	// Ordinal path _c/1 should resolve to the actual mache ID
	id, err := e.ResolveMacheID("/main/news_feed/_c/1")
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

	// All 4 resolved items should appear in ordinal format
	if !strings.Contains(content, `[1] "First Story"`) {
		t.Errorf("missing [1] First Story:\n%s", content)
	}
	if !strings.Contains(content, `[3] "Third Story (new after scroll)"`) {
		t.Errorf("missing [3] Third Story (resolved item):\n%s", content)
	}
	if !strings.Contains(content, `[4] "Fourth Story (new after scroll)"`) {
		t.Errorf("missing [4] Fourth Story (resolved item):\n%s", content)
	}
	// 4 resolved items + supplemented text-bearing descendants (Stories, (example.com))
	if strings.Count(content, "] \"") < 4 {
		t.Errorf("expected at least 4 items from resolved items, got:\n%s", content)
	}
	// No mache IDs exposed
	if strings.Contains(content, "mache-") {
		t.Errorf("children file should not expose mache IDs:\n%s", content)
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

// TestConcurrentReadWrite verifies the Engine is safe under concurrent access.
// Run with -race to catch data races: go test -race ./internal/mache/
func TestConcurrentReadWrite(t *testing.T) {
	e := NewEngine()
	if err := e.ApplySchema(sampleSchema); err != nil {
		t.Fatal(err)
	}
	e.LoadChildren(sampleSummary, nil)

	// Hammer the engine with concurrent reads and writes.
	done := make(chan struct{})
	for i := 0; i < 5; i++ {
		// Readers
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 100; j++ {
				_, _ = e.ListDir("/")
				_, _ = e.ReadFile("/main/news_feed/description")
				_, _ = e.ResolveMacheID("/main/news_feed")
				_ = e.HasSchema()
				_ = e.ZoneSelectors()
				_ = e.ToTopology()
			}
		}()
		// Writer: re-apply schema + reload children
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 20; j++ {
				_ = e.ApplySchema(sampleSchema)
				e.LoadChildren(sampleSummary, nil)
			}
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
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

	// All 3 primary items should appear in ordinal format
	if !strings.Contains(content, `[1] "First Story"`) {
		t.Errorf("missing [1] First Story:\n%s", content)
	}
	if !strings.Contains(content, `[2] "Second Story"`) {
		t.Errorf("missing [2] Second Story:\n%s", content)
	}
	if !strings.Contains(content, `[3] "Third Story"`) {
		t.Errorf("missing [3] Third Story:\n%s", content)
	}
	if strings.Count(content, "] \"") != 3 {
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

func TestLoadChildrenSpatialContainment(t *testing.T) {
	// Simulates the orphaned Reviews tab bug: element is registered and
	// visible on screen but its parent chain goes to body (not through the
	// zone root), so both collectZoneMembers and collectDescendants miss it.
	// The spatial containment fallback should claim it based on bounds overlap.
	schema := `{
  "mounts": [
    {
      "virtual_path": "/main/product-tabs",
      "mache_id": "mache-32",
      "description": "Product tab navigation",
      "primary_items": [],
      "bounds": [0.05, 0.40, 0.90, 0.10]
    }
  ]
}`
	// mache-32 is the zone root. mache-99 (Reviews tab) has parent=body (none),
	// NOT under mache-32 — so parent-chain walk and BFS both fail.
	// But its bounds [0.10, 0.42, 0.08, 0.03] are inside the zone.
	// mache-200 is far outside the zone bounds — should NOT be claimed.
	summary := `Interactive Elements:
ID: mache-32 | Parent: none | Tag: div | Text: "" | Bounds: [0.05, 0.40, 0.90, 0.10]
ID: mache-99 | Parent: none | Tag: a | Text: "Reviews" | Bounds: [0.10, 0.42, 0.08, 0.03]
ID: mache-100 | Parent: none | Tag: a | Text: "Description" | Bounds: [0.20, 0.42, 0.10, 0.03]
ID: mache-200 | Parent: none | Tag: a | Text: "Unrelated" | Bounds: [0.50, 0.80, 0.10, 0.05]
`
	e := NewEngine()
	if err := e.ApplySchema(schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	e.LoadChildren(summary, nil)

	content, err := e.ReadFile("/main/product-tabs/children")
	if err != nil {
		t.Fatalf("children file missing (spatial containment fallback failed): %v", err)
	}

	if !strings.Contains(content, "Reviews") {
		t.Errorf("spatial containment should have claimed Reviews tab:\n%s", content)
	}
	if !strings.Contains(content, "Description") {
		t.Errorf("spatial containment should have claimed Description tab:\n%s", content)
	}
	if strings.Contains(content, "Unrelated") {
		t.Errorf("spatial containment should NOT claim element outside zone bounds:\n%s", content)
	}
}

func TestLoadChildrenSpatialSkipsLargeZones(t *testing.T) {
	// Zones covering >80% of the viewport should not use spatial containment,
	// to prevent every element being claimed by a full-page zone.
	schema := `{
  "mounts": [
    {
      "virtual_path": "/main/fullpage",
      "mache_id": "mache-1",
      "description": "Full page zone",
      "primary_items": [],
      "bounds": [0.0, 0.0, 1.0, 1.0]
    }
  ]
}`
	summary := `Interactive Elements:
ID: mache-1 | Parent: none | Tag: div | Text: "" | Bounds: [0.0, 0.0, 1.0, 1.0]
ID: mache-50 | Parent: none | Tag: a | Text: "Orphan" | Bounds: [0.10, 0.10, 0.05, 0.03]
`
	e := NewEngine()
	if err := e.ApplySchema(schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	e.LoadChildren(summary, nil)

	_, err := e.ReadFile("/main/fullpage/children")
	if err == nil {
		t.Error("full-page zone should NOT claim orphans via spatial containment")
	}
}

// ---------------------------------------------------------------------------
// MergeSchema tests (Stream C — Phase 6)
// ---------------------------------------------------------------------------

func TestMergeSchemaBasic(t *testing.T) {
	e := NewEngine()
	if err := e.ApplySchema(sampleSchema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	// Merge a new zone under /main
	mergeJSON := `{"mounts":[{"virtual_path":"/main/sidebar","mache_id":"mache-50","description":"Sidebar widget"}]}`
	if err := e.MergeSchema(mergeJSON); err != nil {
		t.Fatalf("MergeSchema failed: %v", err)
	}

	// Root should still have 3 top-level dirs: header, main, footer
	entries, err := e.ListDir("/")
	if err != nil {
		t.Fatalf("ListDir / failed: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 root entries, got %d: %v", len(entries), entries)
	}

	// /main should contain both news_feed and sidebar
	mainEntries, err := e.ListDir("/main")
	if err != nil {
		t.Fatalf("ListDir /main failed: %v", err)
	}
	found := map[string]bool{}
	for _, name := range mainEntries {
		found[name] = true
	}
	if !found["news_feed/"] {
		t.Error("missing news_feed/ in /main after merge")
	}
	if !found["sidebar/"] {
		t.Error("missing sidebar/ in /main after merge")
	}

	// Verify the new zone is readable
	desc, err := e.ReadFile("/main/sidebar/description")
	if err != nil {
		t.Fatalf("ReadFile sidebar description failed: %v", err)
	}
	if desc != "Sidebar widget" {
		t.Errorf("unexpected sidebar description: %q", desc)
	}
}

func TestMergeSchemaParentEviction(t *testing.T) {
	// Start with /main/player as a mount
	baseSchema := `{"mounts":[
		{"virtual_path":"/main/player","mache_id":"mache-10","description":"Video player"}
	]}`
	e := NewEngine()
	if err := e.ApplySchema(baseSchema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	// Verify /main/player exists
	id, err := e.ResolveMacheID("/main/player")
	if err != nil {
		t.Fatalf("ResolveMacheID /main/player failed before merge: %v", err)
	}
	if id != "mache-10" {
		t.Errorf("expected mache-10 before merge, got %q", id)
	}

	// Merge a child /main/player/controls — should evict /main/player from mounts
	mergeJSON := `{"mounts":[{"virtual_path":"/main/player/controls","mache_id":"mache-11","description":"Playback controls"}]}`
	if err := e.MergeSchema(mergeJSON); err != nil {
		t.Fatalf("MergeSchema failed: %v", err)
	}

	// The new child zone should be accessible
	id, err = e.ResolveMacheID("/main/player/controls")
	if err != nil {
		t.Fatalf("ResolveMacheID /main/player/controls failed: %v", err)
	}
	if id != "mache-11" {
		t.Errorf("expected mache-11, got %q", id)
	}

	// ToTopology should NOT include /main/player (evicted)
	topo := e.ToTopology()
	for _, node := range topo.Nodes {
		if node.Name == "/main/player" {
			t.Error("/main/player should have been evicted from mounts by child /main/player/controls")
		}
	}
}

func TestMergeSchemaNoEvictionForSiblings(t *testing.T) {
	baseSchema := `{"mounts":[
		{"virtual_path":"/main/player","mache_id":"mache-10","description":"Video player"}
	]}`
	e := NewEngine()
	if err := e.ApplySchema(baseSchema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	// Merge a sibling /main/search — should NOT evict /main/player
	mergeJSON := `{"mounts":[{"virtual_path":"/main/search","mache_id":"mache-20","description":"Search bar"}]}`
	if err := e.MergeSchema(mergeJSON); err != nil {
		t.Fatalf("MergeSchema failed: %v", err)
	}

	// Both should still exist in topology
	topo := e.ToTopology()
	found := map[string]bool{}
	for _, node := range topo.Nodes {
		found[node.Name] = true
	}
	if !found["/main/player"] {
		t.Error("/main/player should NOT be evicted by sibling /main/search")
	}
	if !found["/main/search"] {
		t.Error("/main/search should exist after merge")
	}
}

func TestMergeSchemaNoEvictionForPrefixNames(t *testing.T) {
	baseSchema := `{"mounts":[
		{"virtual_path":"/main/player","mache_id":"mache-10","description":"Video player"}
	]}`
	e := NewEngine()
	if err := e.ApplySchema(baseSchema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	// Merge /main/player2 — NOT a child of /main/player (prefix-safe: requires "/" separator)
	mergeJSON := `{"mounts":[{"virtual_path":"/main/player2","mache_id":"mache-20","description":"Second player"}]}`
	if err := e.MergeSchema(mergeJSON); err != nil {
		t.Fatalf("MergeSchema failed: %v", err)
	}

	// /main/player should still exist
	topo := e.ToTopology()
	found := map[string]bool{}
	for _, node := range topo.Nodes {
		found[node.Name] = true
	}
	if !found["/main/player"] {
		t.Error("/main/player should NOT be evicted by /main/player2 (prefix-safe)")
	}
	if !found["/main/player2"] {
		t.Error("/main/player2 should exist after merge")
	}
}

func TestMergeSchemaPreservesChildren(t *testing.T) {
	e := NewEngine()
	if err := e.ApplySchema(sampleSchema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	// Load children for existing zones
	e.LoadChildren(sampleSummary, nil)

	// Verify news_feed has children before merge
	content, err := e.ReadFile("/main/news_feed/children")
	if err != nil {
		t.Fatalf("ReadFile children before merge failed: %v", err)
	}
	if !strings.Contains(content, `[1] "First Story Title"`) {
		t.Errorf("missing children content before merge: %q", content)
	}

	// Merge a new unrelated zone
	mergeJSON := `{"mounts":[{"virtual_path":"/sidebar/trending","mache_id":"mache-99","description":"Trending topics"}]}`
	if err := e.MergeSchema(mergeJSON); err != nil {
		t.Fatalf("MergeSchema failed: %v", err)
	}

	// Old zone's children should still be readable
	content, err = e.ReadFile("/main/news_feed/children")
	if err != nil {
		t.Fatalf("ReadFile children after merge failed: %v", err)
	}
	if !strings.Contains(content, `[1] "First Story Title"`) {
		t.Errorf("children lost after merge: %q", content)
	}
}

func TestMergeSchemaOverwritesExisting(t *testing.T) {
	e := NewEngine()
	baseSchema := `{"mounts":[
		{"virtual_path":"/main/feed","mache_id":"mache-10","description":"Old feed"}
	]}`
	if err := e.ApplySchema(baseSchema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	// Merge at the same path with new data — both mounts exist (append behavior)
	mergeJSON := `{"mounts":[{"virtual_path":"/main/feed","mache_id":"mache-11","description":"New feed"}]}`
	if err := e.MergeSchema(mergeJSON); err != nil {
		t.Fatalf("MergeSchema failed: %v", err)
	}

	// The description file should reflect the latest mount (graph AddNode overwrites)
	desc, err := e.ReadFile("/main/feed/description")
	if err != nil {
		t.Fatalf("ReadFile description failed: %v", err)
	}
	if desc != "New feed" {
		t.Errorf("expected 'New feed' after overwrite merge, got %q", desc)
	}
}

func TestMergeSchemaEmpty(t *testing.T) {
	e := NewEngine()
	if err := e.ApplySchema(sampleSchema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	// Merge empty mounts — should be a no-op
	if err := e.MergeSchema(`{"mounts":[]}`); err != nil {
		t.Fatalf("MergeSchema empty failed: %v", err)
	}

	// All original mounts should still exist
	topo := e.ToTopology()
	if len(topo.Nodes) != 3 {
		t.Errorf("expected 3 nodes after empty merge, got %d", len(topo.Nodes))
	}

	entries, err := e.ListDir("/")
	if err != nil {
		t.Fatalf("ListDir / failed: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("expected 3 root entries after empty merge, got %d: %v", len(entries), entries)
	}
}

func TestMergeSchemaConcurrent(t *testing.T) {
	e := NewEngine()
	if err := e.ApplySchema(sampleSchema); err != nil {
		t.Fatal(err)
	}

	mergeJSON := `{"mounts":[{"virtual_path":"/sidebar/widget","mache_id":"mache-77","description":"Widget"}]}`

	done := make(chan struct{})
	// Concurrent merges
	for i := 0; i < 5; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 50; j++ {
				_ = e.MergeSchema(mergeJSON)
			}
		}()
	}
	// Concurrent reads
	for i := 0; i < 5; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 100; j++ {
				_, _ = e.ListDir("/")
				_, _ = e.ReadFile("/main/news_feed/description")
				_, _ = e.ResolveMacheID("/header/global_nav")
				_ = e.ToTopology()
			}
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestMergeSchemaResolveMacheID(t *testing.T) {
	e := NewEngine()
	if err := e.ApplySchema(sampleSchema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	// Merge a new zone
	mergeJSON := `{"mounts":[{"virtual_path":"/aside/ads","mache_id":"mache-300","description":"Ad banner"}]}`
	if err := e.MergeSchema(mergeJSON); err != nil {
		t.Fatalf("MergeSchema failed: %v", err)
	}

	// Resolve old zone
	id, err := e.ResolveMacheID("/header/global_nav")
	if err != nil {
		t.Fatalf("ResolveMacheID old zone failed: %v", err)
	}
	if id != "mache-0" {
		t.Errorf("expected mache-0 for old zone, got %q", id)
	}

	// Resolve new zone
	id, err = e.ResolveMacheID("/aside/ads")
	if err != nil {
		t.Fatalf("ResolveMacheID new zone failed: %v", err)
	}
	if id != "mache-300" {
		t.Errorf("expected mache-300 for new zone, got %q", id)
	}
}

// --- ValidateSchema / ValidateSchemaZones tests ---

func TestValidateSchema_AllValid(t *testing.T) {
	bad := ValidateSchema(sampleSchema, sampleSummary)
	if len(bad) != 0 {
		t.Errorf("expected all valid, got bad IDs: %v", bad)
	}
}

func TestValidateSchema_Hallucinated(t *testing.T) {
	// mache-385 doesn't exist in sampleSummary (max is mache-200).
	schema := `{"mounts":[
		{"virtual_path":"/header/nav","mache_id":"mache-0","description":"Nav"},
		{"virtual_path":"/main/comments","mache_id":"mache-385","description":"Comments"}
	]}`
	bad := ValidateSchema(schema, sampleSummary)
	if len(bad) != 1 {
		t.Fatalf("expected 1 hallucinated ID, got %d: %v", len(bad), bad)
	}
	if bad[0] != "mache-385" {
		t.Errorf("expected mache-385 in bad list, got %q", bad[0])
	}
}

func TestValidateSchema_InvalidJSON(t *testing.T) {
	bad := ValidateSchema("not json", sampleSummary)
	if len(bad) != 0 {
		t.Errorf("invalid JSON should return empty, got: %v", bad)
	}
}

func TestValidateSchemaZones_AllValid(t *testing.T) {
	stale := ValidateSchemaZones(sampleSchema, sampleSummary)
	if len(stale) != 0 {
		t.Errorf("expected all valid, got stale: %v", stale)
	}
}

func TestValidateSchemaZones_ReturnsPaths(t *testing.T) {
	schema := `{"mounts":[
		{"virtual_path":"/header/nav","mache_id":"mache-0","description":"Nav"},
		{"virtual_path":"/main/comments","mache_id":"mache-999","description":"Comments"}
	]}`
	stale := ValidateSchemaZones(schema, sampleSummary)
	if len(stale) != 1 {
		t.Fatalf("expected 1 stale zone, got %d: %v", len(stale), stale)
	}
	if stale["/main/comments"] != "mache-999" {
		t.Errorf("expected /main/comments → mache-999, got: %v", stale)
	}
}

func TestValidateSchemaZones_InvalidJSON(t *testing.T) {
	stale := ValidateSchemaZones("{bad", sampleSummary)
	if stale != nil {
		t.Errorf("invalid JSON should return nil, got: %v", stale)
	}
}

func TestValidateSchemaZones_AllHallucinated(t *testing.T) {
	schema := `{"mounts":[
		{"virtual_path":"/a","mache_id":"mache-500","description":"A"},
		{"virtual_path":"/b","mache_id":"mache-600","description":"B"}
	]}`
	stale := ValidateSchemaZones(schema, sampleSummary)
	if len(stale) != 2 {
		t.Fatalf("expected 2 stale zones, got %d: %v", len(stale), stale)
	}
}

// --- ValidateSchemaBounds tests ---

// --- RepairSchema tests ---

func TestRepairSchema_SwapsHallucinatedAnchor(t *testing.T) {
	// Schema with a hallucinated zone anchor (mache-385 doesn't exist in DOM)
	// but valid primary_items (mache-51, mache-56 exist).
	schema := `{"mounts":[{
		"virtual_path":"/main/comments",
		"mache_id":"mache-385",
		"description":"Comment tree",
		"primary_items":["mache-51","mache-56"]
	}]}`
	summary := `Interactive Elements:
ID: mache-10 | Parent: none | Tag: div | Text: "Page"
ID: mache-51 | Parent: mache-10 | Tag: div | Text: "First comment"
ID: mache-56 | Parent: mache-10 | Tag: div | Text: "Second comment"
`
	repaired, count := RepairSchema(schema, summary)
	if count != 1 {
		t.Fatalf("expected 1 repair, got %d", count)
	}

	// Parse repaired JSON and verify anchor was swapped to first valid child.
	var output CartographerOutput
	if err := json.Unmarshal([]byte(repaired), &output); err != nil {
		t.Fatalf("failed to parse repaired JSON: %v", err)
	}
	if len(output.Mounts) != 1 {
		t.Fatalf("expected 1 mount, got %d", len(output.Mounts))
	}
	if output.Mounts[0].MacheID != "mache-51" {
		t.Errorf("expected anchor swapped to mache-51, got %q", output.Mounts[0].MacheID)
	}
}

func TestRepairSchema_NoRepairNeeded(t *testing.T) {
	// All anchors are valid — no repair.
	schema := `{"mounts":[{
		"virtual_path":"/main/feed",
		"mache_id":"mache-10",
		"description":"Feed",
		"primary_items":["mache-11","mache-12"]
	}]}`
	summary := "ID: mache-10 | Parent: none | Tag: div | Text: \"Feed\"\nID: mache-11 | Parent: mache-10 | Tag: a | Text: \"Story\"\n"

	repaired, count := RepairSchema(schema, summary)
	if count != 0 {
		t.Fatalf("expected 0 repairs, got %d", count)
	}
	if repaired != schema {
		t.Error("expected original JSON returned when no repairs needed")
	}
}

func TestRepairSchema_NoPrimaryItems(t *testing.T) {
	// Hallucinated anchor but no primary_items — unrepairable.
	schema := `{"mounts":[{
		"virtual_path":"/main/feed",
		"mache_id":"mache-999",
		"description":"Feed"
	}]}`
	summary := "ID: mache-10 | Parent: none | Tag: div | Text: \"Feed\"\n"

	repaired, count := RepairSchema(schema, summary)
	if count != 0 {
		t.Fatalf("expected 0 repairs (no primary_items), got %d", count)
	}
	if repaired != schema {
		t.Error("expected original JSON returned when unrepairable")
	}
}

func TestRepairSchema_MultipleZones(t *testing.T) {
	// Two zones: one hallucinated (repairable), one valid.
	schema := `{"mounts":[
		{"virtual_path":"/header/nav","mache_id":"mache-0","description":"Nav"},
		{"virtual_path":"/main/comments","mache_id":"mache-500","description":"Comments","primary_items":["mache-20","mache-25"]}
	]}`
	summary := `Interactive Elements:
ID: mache-0 | Parent: none | Tag: nav | Text: "Nav"
ID: mache-20 | Parent: none | Tag: div | Text: "Comment 1"
ID: mache-25 | Parent: mache-20 | Tag: div | Text: "Comment 2"
`
	repaired, count := RepairSchema(schema, summary)
	if count != 1 {
		t.Fatalf("expected 1 repair, got %d", count)
	}

	var output CartographerOutput
	if err := json.Unmarshal([]byte(repaired), &output); err != nil {
		t.Fatalf("failed to parse repaired JSON: %v", err)
	}
	// First zone untouched
	if output.Mounts[0].MacheID != "mache-0" {
		t.Errorf("valid zone should be untouched, got %q", output.Mounts[0].MacheID)
	}
	// Second zone repaired
	if output.Mounts[1].MacheID != "mache-20" {
		t.Errorf("expected anchor swapped to mache-20, got %q", output.Mounts[1].MacheID)
	}
}

func TestValidateSchemaBounds_Displaced(t *testing.T) {
	// Cached schema: mache-3 at [0.1, 0.3, 0.8, 0.5] (center ≈ 0.5, 0.55)
	// Summary:        mache-3 at [0.8, 0.1, 0.15, 0.08] (center ≈ 0.875, 0.14)
	// Distance ≈ 0.56 — far above 0.10 threshold.
	cachedSchema := `{"mounts":[{
		"virtual_path":"/main/feed",
		"mache_id":"mache-3",
		"description":"Feed",
		"bounds":[0.1, 0.3, 0.8, 0.5]
	}]}`
	summary := "ID: mache-3 | Parent: none | Tag: a | Text: \"Sidebar\" | Bounds: [0.8, 0.1, 0.15, 0.08]\n"

	stale := ValidateSchemaBounds(cachedSchema, summary, 0.10)
	if len(stale) == 0 {
		t.Error("expected bounds mismatch to be caught (center moved ~56%)")
	}
	if _, ok := stale["/main/feed"]; !ok {
		t.Errorf("expected /main/feed in stale map, got: %v", stale)
	}
}

func TestValidateSchemaBounds_MinorReflow(t *testing.T) {
	// Cached: mache-3 at [0.1, 0.3, 0.8, 0.5] (center ≈ 0.5, 0.55)
	// Summary: mache-3 at [0.1, 0.32, 0.8, 0.5] (center ≈ 0.5, 0.57)
	// Distance ≈ 0.02 — within 0.10 threshold.
	cachedSchema := `{"mounts":[{
		"virtual_path":"/main/feed",
		"mache_id":"mache-3",
		"description":"Feed",
		"bounds":[0.1, 0.3, 0.8, 0.5]
	}]}`
	summary := "ID: mache-3 | Parent: none | Tag: div | Text: \"Feed\" | Bounds: [0.1, 0.32, 0.8, 0.5]\n"

	stale := ValidateSchemaBounds(cachedSchema, summary, 0.10)
	if len(stale) != 0 {
		t.Errorf("minor reflow (2%%) should not trigger staleness, got stale: %v", stale)
	}
}

func TestValidateSchemaBounds_ZeroBounds(t *testing.T) {
	// Cached mount has no bounds (older format). Should be skipped.
	cachedSchema := `{"mounts":[{
		"virtual_path":"/main/feed",
		"mache_id":"mache-3",
		"description":"Feed"
	}]}`
	summary := "ID: mache-3 | Parent: none | Tag: div | Text: \"Feed\" | Bounds: [0.8, 0.8, 0.1, 0.1]\n"

	stale := ValidateSchemaBounds(cachedSchema, summary, 0.10)
	if len(stale) != 0 {
		t.Errorf("zones without stored bounds should be skipped, got stale: %v", stale)
	}
}
