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
