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

	// With primary items: only those are selected
	items := selectChildItems(descendants, []string{"m-2", "m-5"})
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].ID != "m-2" || items[1].ID != "m-5" {
		t.Errorf("wrong items: %v", items)
	}

	// Missing primary items are skipped gracefully
	items = selectChildItems(descendants, []string{"m-2", "m-99"})
	if len(items) != 1 {
		t.Fatalf("expected 1 item (m-99 missing), got %d", len(items))
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
	if strings.Count(content, "] \"") != 2 {
		t.Errorf("expected 2 items, got:\n%s", content)
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
	if strings.Count(content, "] \"") != 4 {
		t.Errorf("expected 4 items from resolved items, got:\n%s", content)
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
