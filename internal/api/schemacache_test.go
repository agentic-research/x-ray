package api

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/agentic-research/mache/graph"
	"github.com/agentic-research/x-ray/internal/mache"
)

func TestCacheKey(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		// Query params are preserved — different pages must not share cache.
		{"https://news.ycombinator.com/news?p=2", "news.ycombinator.com/news?p=2"},
		{"https://news.ycombinator.com/news?p=3", "news.ycombinator.com/news?p=3"},
		{"https://news.ycombinator.com/news", "news.ycombinator.com/news"},
		{"https://www.reddit.com/r/programming", "www.reddit.com/r/programming"},
		{"https://www.reddit.com/r/programming?sort=new", "www.reddit.com/r/programming?sort=new"},
		{"https://example.com", "example.com/"},
		{"https://example.com/", "example.com/"},
		// Fragments are still stripped (same DOM).
		{"https://example.com/path/to/page#section", "example.com/path/to/page"},
		{"", ""},
		{"not-a-url", ""},
	}
	for _, tt := range tests {
		got := CacheKey(tt.url)
		if got != tt.want {
			t.Errorf("CacheKey(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestSchemaCacheHitMiss(t *testing.T) {
	c := NewSchemaCache("")

	// Miss on empty cache.
	if _, ok := c.Get("example.com/"); ok {
		t.Fatal("expected miss on empty cache")
	}

	// Put and hit.
	c.Put("example.com/", `{"mounts":[]}`)
	got, ok := c.Get("example.com/")
	if !ok {
		t.Fatal("expected hit after Put")
	}
	if got != `{"mounts":[]}` {
		t.Errorf("unexpected schema: %s", got)
	}

	// Overwrite same key.
	c.Put("example.com/", `{"mounts":[{"virtual_path":"/a"}]}`)
	got, _ = c.Get("example.com/")
	if got != `{"mounts":[{"virtual_path":"/a"}]}` {
		t.Errorf("overwrite failed: %s", got)
	}
}

func TestSchemaCacheSQLitePersistence(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test-schemas.db")

	// Create a cache backed by SQLite, put an entry.
	c1 := NewSchemaCache(dbPath)
	c1.Put("example.com/page", `{"mounts":[{"virtual_path":"/main"}]}`)

	// Create a new cache with the same path — entry should be loaded from disk.
	c2 := NewSchemaCache(dbPath)

	got, ok := c2.Get("example.com/page")
	if !ok {
		t.Fatal("expected hit after reopening SQLite-backed cache")
	}
	if got != `{"mounts":[{"virtual_path":"/main"}]}` {
		t.Errorf("unexpected schema after reload: %s", got)
	}
}

func TestSchemaCacheSQLiteOverwrite(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test-overwrite.db")

	c1 := NewSchemaCache(dbPath)
	c1.Put("example.com/", `{"v":1}`)
	c1.Put("example.com/", `{"v":2}`)

	c2 := NewSchemaCache(dbPath)

	got, ok := c2.Get("example.com/")
	if !ok {
		t.Fatal("expected hit")
	}
	if got != `{"v":2}` {
		t.Errorf("expected overwritten value, got %s", got)
	}
}

// TestSchemaCacheMacheGraphIntegrity proves the thesis: the SQLite file
// produced by x-ray's schema cache IS a mache graph, readable by any
// mache-aware tool via graph.ImportSQLite.
func TestSchemaCacheMacheGraphIntegrity(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test-graph.db")

	c := NewSchemaCache(dbPath)
	c.Put("example.com/page", `{"mounts":[{"virtual_path":"/main"}]}`)
	c.Put("news.ycombinator.com/news", `{"mounts":[{"virtual_path":"/feed"}]}`)

	// The SQLite file should be a valid mache graph.
	store, err := graph.ImportSQLite(dbPath)
	if err != nil {
		t.Fatalf("ImportSQLite: %v", err)
	}

	// Verify roots correspond to cached URLs.
	roots := store.RootIDs()
	if len(roots) != 2 {
		t.Fatalf("expected 2 roots, got %d: %v", len(roots), roots)
	}

	// Verify graph node data matches what was cached.
	node, err := store.GetNode("example.com/page/schema_json")
	if err != nil {
		t.Fatalf("GetNode(schema_json): %v", err)
	}
	if string(node.Data) != `{"mounts":[{"virtual_path":"/main"}]}` {
		t.Errorf("schema data = %q", node.Data)
	}

	// Verify the directory structure.
	dir, err := store.GetNode("example.com/page")
	if err != nil {
		t.Fatalf("GetNode(dir): %v", err)
	}
	if !dir.Mode.IsDir() {
		t.Error("expected directory node")
	}
	if len(dir.Children) != 2 {
		t.Errorf("expected 2 children, got %v", dir.Children)
	}
}

// ---------------------------------------------------------------------------
// SchemaCache zone operations tests (Stream D — Phase 6)
// ---------------------------------------------------------------------------

func testMounts() []mache.Mount {
	return []mache.Mount{
		{VirtualPath: "/header/nav", MacheID: "mache-0", Description: "Top navigation", Fingerprint: "fp-0"},
		{VirtualPath: "/main/feed", MacheID: "mache-10", Description: "News feed", Fingerprint: "fp-10"},
		{VirtualPath: "/footer/links", MacheID: "mache-20", Description: "Footer links", Fingerprint: "fp-20"},
	}
}

func TestPutZonesAndGetAllZones(t *testing.T) {
	c := NewSchemaCache("")
	key := "example.com/page"

	c.PutZones(key, testMounts())

	got, ok := c.GetAllZones(key)
	if !ok {
		t.Fatal("expected GetAllZones to return true after PutZones")
	}

	var output mache.CartographerOutput
	if err := json.Unmarshal([]byte(got), &output); err != nil {
		t.Fatalf("unmarshal GetAllZones result: %v", err)
	}
	if len(output.Mounts) != 3 {
		t.Fatalf("expected 3 mounts, got %d", len(output.Mounts))
	}

	// Verify mount data roundtrips correctly.
	found := map[string]bool{}
	for _, m := range output.Mounts {
		found[m.VirtualPath] = true
	}
	for _, vp := range []string{"/header/nav", "/main/feed", "/footer/links"} {
		if !found[vp] {
			t.Errorf("missing mount %s in GetAllZones result", vp)
		}
	}
}

func TestPutZonesReplacesExisting(t *testing.T) {
	c := NewSchemaCache("")
	key := "example.com/page"

	// First PutZones with 3 mounts.
	c.PutZones(key, testMounts())

	// Second PutZones with different data.
	newMounts := []mache.Mount{
		{VirtualPath: "/main/sidebar", MacheID: "mache-99", Description: "Sidebar", Fingerprint: "fp-99"},
	}
	c.PutZones(key, newMounts)

	got, ok := c.GetAllZones(key)
	if !ok {
		t.Fatal("expected GetAllZones to return true after second PutZones")
	}

	var output mache.CartographerOutput
	if err := json.Unmarshal([]byte(got), &output); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(output.Mounts) != 1 {
		t.Fatalf("expected 1 mount after replacement, got %d", len(output.Mounts))
	}
	if output.Mounts[0].VirtualPath != "/main/sidebar" {
		t.Errorf("expected /main/sidebar, got %s", output.Mounts[0].VirtualPath)
	}
}

func TestInvalidateZoneRemovesOne(t *testing.T) {
	c := NewSchemaCache("")
	key := "example.com/page"

	c.PutZones(key, testMounts())

	// Invalidate one zone.
	c.InvalidateZone(key, "/main/feed")

	got, ok := c.GetAllZones(key)
	if !ok {
		t.Fatal("expected GetAllZones to return true after invalidating one of three zones")
	}

	var output mache.CartographerOutput
	if err := json.Unmarshal([]byte(got), &output); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(output.Mounts) != 2 {
		t.Fatalf("expected 2 mounts after invalidating one, got %d", len(output.Mounts))
	}

	for _, m := range output.Mounts {
		if m.VirtualPath == "/main/feed" {
			t.Error("/main/feed should have been invalidated")
		}
	}
}

func TestInvalidateZoneNonExistent(t *testing.T) {
	c := NewSchemaCache("")
	key := "example.com/page"

	c.PutZones(key, testMounts())

	// Invalidating a non-existent zone should not panic.
	c.InvalidateZone(key, "/nonexistent/zone")

	// Original zones should be unaffected.
	got, ok := c.GetAllZones(key)
	if !ok {
		t.Fatal("expected GetAllZones to return true")
	}

	var output mache.CartographerOutput
	if err := json.Unmarshal([]byte(got), &output); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(output.Mounts) != 3 {
		t.Fatalf("expected 3 mounts unchanged after invalidating non-existent zone, got %d", len(output.Mounts))
	}
}

func TestPutZoneSingleAdd(t *testing.T) {
	c := NewSchemaCache("")
	key := "example.com/page"

	// Start with 2 zones via PutZones.
	initial := []mache.Mount{
		{VirtualPath: "/header/nav", MacheID: "mache-0", Description: "Nav"},
		{VirtualPath: "/main/feed", MacheID: "mache-10", Description: "Feed"},
	}
	c.PutZones(key, initial)

	// Add a 3rd zone via PutZone.
	c.PutZone(key, mache.Mount{
		VirtualPath: "/footer/links",
		MacheID:     "mache-20",
		Description: "Footer",
	})

	got, ok := c.GetAllZones(key)
	if !ok {
		t.Fatal("expected GetAllZones to return true")
	}

	var output mache.CartographerOutput
	if err := json.Unmarshal([]byte(got), &output); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(output.Mounts) != 3 {
		t.Fatalf("expected 3 mounts after PutZone, got %d", len(output.Mounts))
	}

	found := map[string]bool{}
	for _, m := range output.Mounts {
		found[m.VirtualPath] = true
	}
	if !found["/footer/links"] {
		t.Error("PutZone'd /footer/links not found in GetAllZones result")
	}
}

func TestPutZoneThenInvalidate(t *testing.T) {
	c := NewSchemaCache("")
	key := "example.com/page"

	// Add a single zone via PutZone.
	c.PutZone(key, mache.Mount{
		VirtualPath: "/main/feed",
		MacheID:     "mache-10",
		Description: "Feed",
	})

	// Verify it exists.
	_, ok := c.GetAllZones(key)
	if !ok {
		t.Fatal("expected zone to exist after PutZone")
	}

	// Invalidate it.
	c.InvalidateZone(key, "/main/feed")

	// GetAllZones should return false (no zones left).
	_, ok = c.GetAllZones(key)
	if ok {
		t.Error("expected GetAllZones to return false after invalidating only zone")
	}
}

func TestGetReturnsV2WhenAvailable(t *testing.T) {
	c := NewSchemaCache("")
	key := "example.com/page"

	c.PutZones(key, testMounts())

	// Get() should detect v2 format and return reconstructed JSON.
	got, ok := c.Get(key)
	if !ok {
		t.Fatal("expected Get to return true when v2 zones exist")
	}

	var output mache.CartographerOutput
	if err := json.Unmarshal([]byte(got), &output); err != nil {
		t.Fatalf("unmarshal Get result: %v", err)
	}
	if len(output.Mounts) != 3 {
		t.Fatalf("expected 3 mounts from Get() with v2 zones, got %d", len(output.Mounts))
	}
}

func TestInvalidateURLClearsAllZones(t *testing.T) {
	c := NewSchemaCache("")
	key := "example.com/page"

	c.PutZones(key, testMounts())

	// Verify zones exist.
	_, ok := c.GetAllZones(key)
	if !ok {
		t.Fatal("expected zones to exist before InvalidateURL")
	}

	// InvalidateURL should clear all zones.
	c.InvalidateURL(key)

	_, ok = c.GetAllZones(key)
	if ok {
		t.Error("expected GetAllZones to return false after InvalidateURL")
	}

	// Get() should also return false.
	_, ok = c.Get(key)
	if ok {
		t.Error("expected Get to return false after InvalidateURL")
	}
}

func TestPutZonesPersistsToSQLite(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test-zones-persist.db")

	// Create cache, store zones, let it persist.
	c1 := NewSchemaCache(dbPath)
	c1.PutZones("example.com/page", testMounts())

	// Create a new cache from the same dbPath — zones should survive.
	c2 := NewSchemaCache(dbPath)

	got, ok := c2.Get("example.com/page")
	if !ok {
		t.Fatal("expected zones to persist across cache instances via SQLite")
	}

	var output mache.CartographerOutput
	if err := json.Unmarshal([]byte(got), &output); err != nil {
		t.Fatalf("unmarshal persisted zones: %v", err)
	}
	if len(output.Mounts) != 3 {
		t.Fatalf("expected 3 persisted mounts, got %d", len(output.Mounts))
	}
}

// ---------------------------------------------------------------------------
// NavSection tests
// ---------------------------------------------------------------------------

func TestNormalizeGoalHash(t *testing.T) {
	// URL stripping: same hash with and without URL.
	h1 := NormalizeGoalHash("find reviews on https://example.com/page")
	h2 := NormalizeGoalHash("find reviews on")
	if h1 != h2 {
		t.Errorf("URL stripping failed: %q != %q", h1, h2)
	}

	// Number replacement: different numbers → same hash.
	h3 := NormalizeGoalHash("click item 42")
	h4 := NormalizeGoalHash("click item 99")
	if h3 != h4 {
		t.Errorf("number normalization failed: %q != %q", h3, h4)
	}

	// Stability: same input → same output.
	h5 := NormalizeGoalHash("find the reviews tab")
	h6 := NormalizeGoalHash("find the reviews tab")
	if h5 != h6 {
		t.Errorf("stability failed: %q != %q", h5, h6)
	}

	// Length is 16 hex chars.
	if len(h5) != 16 {
		t.Errorf("expected 16-char hash, got %d: %q", len(h5), h5)
	}
}

func TestPutSectionAndGetSections(t *testing.T) {
	c := NewSchemaCache("")
	key := "example.com/product"
	c.PutZones(key, []mache.Mount{
		{VirtualPath: "/main/feed_4", MacheID: "m-10", Fingerprint: "fp-A"},
	})

	section := NavSection{
		GoalHash:    NormalizeGoalHash("find the reviews"),
		ZonePath:    "/main/feed_4",
		Fingerprint: "fp-A",
		Ordinal:     "4",
		ElementText: "Reviews 12",
		Action:      "click",
		RecordedAt:  1000,
	}
	c.PutSection(key, section)

	got := c.GetSections(key, "/main/feed_4", "fp-A")
	if len(got) != 1 {
		t.Fatalf("expected 1 section, got %d", len(got))
	}
	if got[0].Ordinal != "4" {
		t.Errorf("ordinal = %q, want 4", got[0].Ordinal)
	}
	if got[0].ElementText != "Reviews 12" {
		t.Errorf("element_text = %q, want 'Reviews 12'", got[0].ElementText)
	}
	if got[0].Action != "click" {
		t.Errorf("action = %q, want 'click'", got[0].Action)
	}
}

func TestGetSectionsStaleGC(t *testing.T) {
	c := NewSchemaCache("")
	key := "example.com/product"
	c.PutZones(key, []mache.Mount{
		{VirtualPath: "/main/feed", MacheID: "m-10", Fingerprint: "fp-A"},
	})
	c.PutSection(key, NavSection{
		GoalHash:    "hash1",
		ZonePath:    "/main/feed",
		Fingerprint: "fp-A",
		Ordinal:     "1",
		Action:      "click",
		RecordedAt:  1000,
	})

	// Query with different fingerprint → should return empty and GC.
	got := c.GetSections(key, "/main/feed", "fp-B")
	if len(got) != 0 {
		t.Fatalf("expected 0 sections with mismatched fingerprint, got %d", len(got))
	}

	// Re-query with original fingerprint → should also be empty (GC'd).
	got = c.GetSections(key, "/main/feed", "fp-A")
	if len(got) != 0 {
		t.Fatalf("expected 0 sections after GC, got %d", len(got))
	}
}

func TestPutSectionMaxFive(t *testing.T) {
	c := NewSchemaCache("")
	key := "example.com/page"
	c.PutZones(key, []mache.Mount{
		{VirtualPath: "/main/content", MacheID: "m-1", Fingerprint: "fp-X"},
	})

	// Insert 6 sections with different goal hashes.
	for i := 0; i < 6; i++ {
		c.PutSection(key, NavSection{
			GoalHash:    fmt.Sprintf("goal-%d", i),
			ZonePath:    "/main/content",
			Fingerprint: "fp-X",
			Ordinal:     fmt.Sprintf("%d", i),
			Action:      "click",
			RecordedAt:  int64(1000 + i),
		})
	}

	got := c.GetSections(key, "/main/content", "fp-X")
	if len(got) != 5 {
		t.Fatalf("expected 5 sections (max cap), got %d", len(got))
	}

	// The oldest (RecordedAt=1000, goal-0) should have been evicted.
	for _, s := range got {
		if s.GoalHash == "goal-0" {
			t.Error("goal-0 (oldest) should have been evicted")
		}
	}
}

func TestPutSectionIdempotent(t *testing.T) {
	c := NewSchemaCache("")
	key := "example.com/page"
	c.PutZones(key, []mache.Mount{
		{VirtualPath: "/main/feed", MacheID: "m-1", Fingerprint: "fp-A"},
	})

	c.PutSection(key, NavSection{
		GoalHash:    "same-hash",
		ZonePath:    "/main/feed",
		Fingerprint: "fp-A",
		Ordinal:     "3",
		ElementText: "Old Text",
		Action:      "click",
		RecordedAt:  1000,
	})
	c.PutSection(key, NavSection{
		GoalHash:    "same-hash",
		ZonePath:    "/main/feed",
		Fingerprint: "fp-A",
		Ordinal:     "3",
		ElementText: "New Text",
		Action:      "click",
		RecordedAt:  2000,
	})

	got := c.GetSections(key, "/main/feed", "fp-A")
	if len(got) != 1 {
		t.Fatalf("expected 1 section (idempotent overwrite), got %d", len(got))
	}
	if got[0].ElementText != "New Text" {
		t.Errorf("expected updated text 'New Text', got %q", got[0].ElementText)
	}
}

func TestGetAllSectionsForURL(t *testing.T) {
	c := NewSchemaCache("")
	key := "example.com/page"
	c.PutZones(key, []mache.Mount{
		{VirtualPath: "/header/nav", MacheID: "m-0", Fingerprint: "fp-0"},
		{VirtualPath: "/main/feed", MacheID: "m-10", Fingerprint: "fp-10"},
	})

	c.PutSection(key, NavSection{
		GoalHash: "h1", ZonePath: "/header/nav", Fingerprint: "fp-0",
		Ordinal: "1", Action: "click", RecordedAt: 1000,
	})
	c.PutSection(key, NavSection{
		GoalHash: "h2", ZonePath: "/main/feed", Fingerprint: "fp-10",
		Ordinal: "5", Action: "click", RecordedAt: 2000,
	})

	got := c.GetAllSectionsForURL(key)
	if len(got) != 2 {
		t.Fatalf("expected 2 sections across zones, got %d", len(got))
	}
}

func TestPutZonesPreservesSections(t *testing.T) {
	c := NewSchemaCache("")
	key := "example.com/product"
	mounts := []mache.Mount{
		{VirtualPath: "/main/feed", MacheID: "m-10", Fingerprint: "fp-A"},
	}
	c.PutZones(key, mounts)

	c.PutSection(key, NavSection{
		GoalHash: "h1", ZonePath: "/main/feed", Fingerprint: "fp-A",
		Ordinal: "4", ElementText: "Reviews", Action: "click", RecordedAt: 1000,
	})

	// Re-PutZones with same fingerprint — section should survive.
	c.PutZones(key, mounts)

	got := c.GetSections(key, "/main/feed", "fp-A")
	if len(got) != 1 {
		t.Fatalf("expected section to survive PutZones with same fingerprint, got %d", len(got))
	}
	if got[0].ElementText != "Reviews" {
		t.Errorf("unexpected element text: %q", got[0].ElementText)
	}
}

func TestPutZonesEvictsStaleSections(t *testing.T) {
	c := NewSchemaCache("")
	key := "example.com/product"
	c.PutZones(key, []mache.Mount{
		{VirtualPath: "/main/feed", MacheID: "m-10", Fingerprint: "fp-A"},
	})

	c.PutSection(key, NavSection{
		GoalHash: "h1", ZonePath: "/main/feed", Fingerprint: "fp-A",
		Ordinal: "4", Action: "click", RecordedAt: 1000,
	})

	// Re-PutZones with different fingerprint — section should be evicted.
	c.PutZones(key, []mache.Mount{
		{VirtualPath: "/main/feed", MacheID: "m-10", Fingerprint: "fp-B"},
	})

	got := c.GetSections(key, "/main/feed", "fp-B")
	if len(got) != 0 {
		t.Fatalf("expected stale section to be evicted after fingerprint change, got %d", len(got))
	}
}

func TestSectionsPersistToSQLite(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test-sections.db")

	c1 := NewSchemaCache(dbPath)
	c1.PutZones("example.com/page", []mache.Mount{
		{VirtualPath: "/main/feed", MacheID: "m-10", Fingerprint: "fp-A"},
	})
	c1.PutSection("example.com/page", NavSection{
		GoalHash: "h1", ZonePath: "/main/feed", Fingerprint: "fp-A",
		Ordinal: "4", ElementText: "Reviews", Action: "click", RecordedAt: 1000,
	})

	// Reload from disk.
	c2 := NewSchemaCache(dbPath)
	got := c2.GetSections("example.com/page", "/main/feed", "fp-A")
	if len(got) != 1 {
		t.Fatalf("expected section to persist to SQLite, got %d", len(got))
	}
	if got[0].ElementText != "Reviews" {
		t.Errorf("unexpected element text after reload: %q", got[0].ElementText)
	}
}
