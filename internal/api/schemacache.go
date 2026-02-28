package api

import (
	"encoding/json"
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
	"github.com/jamesgardner/x-ray/internal/mache"
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
// Checks v2 (per-zone) format first, falls back to v1 (monolithic blob).
func (c *SchemaCache) Get(key string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Check for v2 per-zone format.
	if vnode, err := c.store.GetNode(key + "/meta/version"); err == nil {
		if string(vnode.Data) == "2" {
			return c.getAllZonesLocked(key)
		}
	}

	// Fall back to v1 monolithic blob.
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

// escapeZonePath converts "/header/nav" → "header~nav" for use as graph node IDs.
func escapeZonePath(vpath string) string {
	return strings.ReplaceAll(strings.TrimPrefix(vpath, "/"), "/", "~")
}

// PutZones stores each mount as a separate zone entry in the graph.
// This replaces the monolithic schema_json blob with per-zone sections,
// enabling targeted invalidation (the sheaf cache model).
func (c *SchemaCache) PutZones(key string, mounts []mache.Mount) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	nowStr := strconv.FormatInt(now.Unix(), 10)

	// Ensure root directory exists.
	if _, err := c.store.GetNode(key); err != nil {
		c.store.AddRoot(&graph.Node{
			ID:       key,
			Mode:     fs.ModeDir,
			Children: []string{},
		})
	}

	// Ensure zones/ directory exists.
	zonesDir := key + "/zones"
	if _, err := c.store.GetNode(zonesDir); err != nil {
		c.store.AddNode(&graph.Node{
			ID:       zonesDir,
			Mode:     fs.ModeDir,
			Children: []string{},
		})
		if root, err := c.store.GetNode(key); err == nil {
			root.Children = appendUniqueStr(root.Children, zonesDir)
		}
	}

	// Clear existing zone entries (full replacement).
	if zonesNode, err := c.store.GetNode(zonesDir); err == nil {
		zonesNode.Children = []string{}
	}

	// Write each mount as a zone entry.
	for _, m := range mounts {
		zoneID := zonesDir + "/" + escapeZonePath(m.VirtualPath)
		mountJSON, _ := json.Marshal(m)

		c.store.AddNode(&graph.Node{
			ID:       zoneID,
			Mode:     fs.ModeDir,
			Children: []string{zoneID + "/mount_json", zoneID + "/fingerprint", zoneID + "/cached_at"},
		})
		c.store.AddNode(&graph.Node{ID: zoneID + "/mount_json", Data: mountJSON, ModTime: now})
		c.store.AddNode(&graph.Node{ID: zoneID + "/fingerprint", Data: []byte(m.Fingerprint), ModTime: now})
		c.store.AddNode(&graph.Node{ID: zoneID + "/cached_at", Data: []byte(nowStr), ModTime: now})

		if zonesNode, err := c.store.GetNode(zonesDir); err == nil {
			zonesNode.Children = appendUniqueStr(zonesNode.Children, zoneID)
		}
	}

	// Write version marker.
	metaDir := key + "/meta"
	if _, err := c.store.GetNode(metaDir); err != nil {
		c.store.AddNode(&graph.Node{
			ID:       metaDir,
			Mode:     fs.ModeDir,
			Children: []string{metaDir + "/version", metaDir + "/cached_at"},
		})
		if root, err := c.store.GetNode(key); err == nil {
			root.Children = appendUniqueStr(root.Children, metaDir)
		}
	}
	c.store.AddNode(&graph.Node{ID: metaDir + "/version", Data: []byte("2"), ModTime: now})
	c.store.AddNode(&graph.Node{ID: metaDir + "/cached_at", Data: []byte(nowStr), ModTime: now})

	// Remove old v1 schema_json blob if present.
	// (AddNode with empty data effectively clears it; GetNode will still find it
	// but getAllZonesLocked is checked first due to version marker.)

	c.persistLocked()
}

// GetAllZones reconstructs a full CartographerOutput JSON from per-zone entries.
// Returns ("", false) if no v2 zones are cached.
func (c *SchemaCache) GetAllZones(key string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.getAllZonesLocked(key)
}

func (c *SchemaCache) getAllZonesLocked(key string) (string, bool) {
	zonesDir := key + "/zones"
	zonesNode, err := c.store.GetNode(zonesDir)
	if err != nil || len(zonesNode.Children) == 0 {
		return "", false
	}

	var mounts []mache.Mount
	for _, childID := range zonesNode.Children {
		mountNode, err := c.store.GetNode(childID + "/mount_json")
		if err != nil {
			continue
		}
		var m mache.Mount
		if err := json.Unmarshal(mountNode.Data, &m); err != nil {
			continue
		}
		mounts = append(mounts, m)
	}

	if len(mounts) == 0 {
		return "", false
	}

	out := mache.CartographerOutput{Mounts: mounts}
	data, err := json.Marshal(out)
	if err != nil {
		return "", false
	}
	return string(data), true
}

// InvalidateZone removes a single zone from the cache.
func (c *SchemaCache) InvalidateZone(key, zonePath string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	zonesDir := key + "/zones"
	zoneID := zonesDir + "/" + escapeZonePath(zonePath)

	// Remove from parent's children list.
	if zonesNode, err := c.store.GetNode(zonesDir); err == nil {
		filtered := zonesNode.Children[:0]
		for _, child := range zonesNode.Children {
			if child != zoneID {
				filtered = append(filtered, child)
			}
		}
		zonesNode.Children = filtered
	}

	c.persistLocked()
}

// InvalidateURL removes all cached zones for a URL.
func (c *SchemaCache) InvalidateURL(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Clear the zones directory children.
	zonesDir := key + "/zones"
	if zonesNode, err := c.store.GetNode(zonesDir); err == nil {
		zonesNode.Children = []string{}
	}

	c.persistLocked()
}

// PutZone stores a single zone entry (used during targeted rescan).
func (c *SchemaCache) PutZone(key string, m mache.Mount) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	nowStr := strconv.FormatInt(now.Unix(), 10)

	// Ensure zones/ directory exists.
	zonesDir := key + "/zones"
	if _, err := c.store.GetNode(zonesDir); err != nil {
		// If no zones dir exists, this is a v1 cache — create the structure.
		if _, err := c.store.GetNode(key); err != nil {
			c.store.AddRoot(&graph.Node{ID: key, Mode: fs.ModeDir, Children: []string{}})
		}
		c.store.AddNode(&graph.Node{ID: zonesDir, Mode: fs.ModeDir, Children: []string{}})
		if root, err := c.store.GetNode(key); err == nil {
			root.Children = appendUniqueStr(root.Children, zonesDir)
		}
	}

	zoneID := zonesDir + "/" + escapeZonePath(m.VirtualPath)
	mountJSON, _ := json.Marshal(m)

	c.store.AddNode(&graph.Node{
		ID:       zoneID,
		Mode:     fs.ModeDir,
		Children: []string{zoneID + "/mount_json", zoneID + "/fingerprint", zoneID + "/cached_at"},
	})
	c.store.AddNode(&graph.Node{ID: zoneID + "/mount_json", Data: mountJSON, ModTime: now})
	c.store.AddNode(&graph.Node{ID: zoneID + "/fingerprint", Data: []byte(m.Fingerprint), ModTime: now})
	c.store.AddNode(&graph.Node{ID: zoneID + "/cached_at", Data: []byte(nowStr), ModTime: now})

	if zonesNode, err := c.store.GetNode(zonesDir); err == nil {
		zonesNode.Children = appendUniqueStr(zonesNode.Children, zoneID)
	}

	c.persistLocked()
}

func appendUniqueStr(slice []string, val string) []string {
	for _, v := range slice {
		if v == val {
			return slice
		}
	}
	return append(slice, val)
}

// Close is a no-op — ExportSQLite manages its own DB connections.
func (c *SchemaCache) Close() error {
	return nil
}
