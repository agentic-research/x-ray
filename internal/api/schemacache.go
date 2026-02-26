package api

import (
	"io/fs"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agentic-research/mache/graph"
)

// SchemaCache stores schema JSON in a mache MemoryStore graph, persisted
// to SQLite via graph.ExportSQLite. This proves that x-ray's cached state
// is a transferrable semantic graph readable by any mache-aware tool.
//
// Graph structure per cached URL:
//
//	{url_key}/              (dir, root node)
//	├── schema_json         (file: raw Cartographer JSON)
//	└── cached_at           (file: unix timestamp)
type SchemaCache struct {
	mu     sync.RWMutex
	store  *graph.MemoryStore
	dbPath string // empty = pure in-memory mode
}

// NewSchemaCache creates a schema cache. If dbPath is empty, the cache is
// purely in-memory (suitable for tests). Otherwise it loads any previously
// persisted graph from SQLite.
func NewSchemaCache(dbPath string) *SchemaCache {
	c := &SchemaCache{
		store:  graph.NewMemoryStore(),
		dbPath: dbPath,
	}

	if dbPath == "" {
		return c
	}

	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		log.Printf("schemacache: mkdir %s: %v (in-memory only)", dbPath, err)
		c.dbPath = ""
		return c
	}

	// Load from existing SQLite graph.
	if _, err := os.Stat(dbPath); err == nil {
		imported, err := graph.ImportSQLite(dbPath)
		if err != nil {
			log.Printf("schemacache: import %s: %v (starting fresh)", dbPath, err)
		} else {
			c.store = imported
			n := len(imported.RootIDs())
			if n > 0 {
				log.Printf("schemacache: loaded %d entries from %s", n, dbPath)
			}
		}
	}

	return c
}

// CacheKey extracts "host/path" from a raw URL, stripping query params
// and fragments. Returns empty string if the URL is unparseable or empty.
func CacheKey(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ""
	}
	p := strings.TrimRight(u.Path, "/")
	if p == "" {
		p = "/"
	}
	return u.Host + p
}

// Get returns the cached schema for the given URL key, or ("", false).
func (c *SchemaCache) Get(key string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	node, err := c.store.GetNode(key + "/schema_json")
	if err != nil {
		return "", false
	}
	return string(node.Data), true
}

// Put stores a schema in the graph and persists to SQLite.
func (c *SchemaCache) Put(key, schemaJSON string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()

	// Create root directory if this is a new key.
	if _, err := c.store.GetNode(key); err != nil {
		c.store.AddRoot(&graph.Node{
			ID:       key,
			Mode:     fs.ModeDir,
			Children: []string{key + "/schema_json", key + "/cached_at"},
		})
	}

	// Add/overwrite child file nodes.
	c.store.AddNode(&graph.Node{
		ID:      key + "/schema_json",
		Data:    []byte(schemaJSON),
		ModTime: now,
	})
	c.store.AddNode(&graph.Node{
		ID:      key + "/cached_at",
		Data:    []byte(strconv.FormatInt(now.Unix(), 10)),
		ModTime: now,
	})

	c.persistLocked()
}

// persistLocked exports the full graph to SQLite. Caller must hold c.mu.
func (c *SchemaCache) persistLocked() {
	if c.dbPath == "" {
		return
	}
	if err := graph.ExportSQLite(c.store, c.dbPath); err != nil {
		log.Printf("schemacache: export %s: %v", c.dbPath, err)
	}
}

// Close is a no-op — ExportSQLite manages its own DB connections.
func (c *SchemaCache) Close() error {
	return nil
}
