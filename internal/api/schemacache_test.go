package api

import (
	"path/filepath"
	"testing"

	"github.com/agentic-research/mache/graph"
)

func TestCacheKey(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://news.ycombinator.com/news?p=2", "news.ycombinator.com/news"},
		{"https://news.ycombinator.com/news?p=3", "news.ycombinator.com/news"},
		{"https://news.ycombinator.com/news", "news.ycombinator.com/news"},
		{"https://www.reddit.com/r/programming", "www.reddit.com/r/programming"},
		{"https://www.reddit.com/r/programming?sort=new", "www.reddit.com/r/programming"},
		{"https://example.com", "example.com/"},
		{"https://example.com/", "example.com/"},
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
