package api

import (
	"database/sql"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const schemaCacheMaxSize = 50

// schemaCacheEntry holds one cached schema and metadata for logging.
type schemaCacheEntry struct {
	SchemaJSON string
	CachedAt   time.Time
}

// schemaCache is a bounded in-memory cache keyed by domain+path slug.
// When a dbPath is provided, entries are persisted to SQLite and reloaded
// on startup so schemas survive process restarts.
type schemaCache struct {
	mu      sync.RWMutex
	entries map[string]*schemaCacheEntry
	order   []string // insertion order for FIFO eviction
	db      *sql.DB  // nil when running in pure in-memory mode
}

// newSchemaCache creates a schema cache. If dbPath is empty, the cache is
// purely in-memory (suitable for tests). Otherwise it opens (or creates) a
// SQLite database at dbPath, creates the schemas table, and pre-loads any
// previously persisted entries into the in-memory map.
func newSchemaCache(dbPath string) *schemaCache {
	c := &schemaCache{
		entries: make(map[string]*schemaCacheEntry, schemaCacheMaxSize),
	}

	if dbPath == "" {
		return c
	}

	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		log.Printf("schemacache: failed to create dir for %s: %v (falling back to in-memory)", dbPath, err)
		return c
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Printf("schemacache: failed to open %s: %v (falling back to in-memory)", dbPath, err)
		return c
	}

	// Create table if it doesn't exist.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schemas (
		key TEXT PRIMARY KEY,
		schema_json TEXT,
		cached_at INTEGER
	)`); err != nil {
		log.Printf("schemacache: failed to create table: %v (falling back to in-memory)", err)
		_ = db.Close()
		return c
	}

	c.db = db

	// Load persisted entries into the in-memory map.
	rows, err := db.Query(`SELECT key, schema_json, cached_at FROM schemas ORDER BY cached_at ASC`)
	if err != nil {
		log.Printf("schemacache: failed to load entries: %v", err)
		return c
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var key, schemaJSON string
		var cachedAtUnix int64
		if err := rows.Scan(&key, &schemaJSON, &cachedAtUnix); err != nil {
			log.Printf("schemacache: failed to scan row: %v", err)
			continue
		}
		c.entries[key] = &schemaCacheEntry{
			SchemaJSON: schemaJSON,
			CachedAt:   time.Unix(cachedAtUnix, 0),
		}
		c.order = append(c.order, key)
	}

	if len(c.entries) > 0 {
		log.Printf("schemacache: loaded %d entries from %s", len(c.entries), dbPath)
	}

	return c
}

// cacheKey extracts "host/path" from a raw URL, stripping query params
// and fragments. Returns empty string if the URL is unparseable or empty.
func cacheKey(rawURL string) string {
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

// Get returns the cached schema for the given key, or ("", false).
func (c *schemaCache) Get(key string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[key]
	if !ok {
		return "", false
	}
	return entry.SchemaJSON, true
}

// Put stores a schema. If the in-memory cache is full, the oldest entry is
// evicted. When backed by SQLite, the entry is also written through to disk.
func (c *schemaCache) Put(key, schemaJSON string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()

	if _, exists := c.entries[key]; exists {
		c.entries[key] = &schemaCacheEntry{
			SchemaJSON: schemaJSON,
			CachedAt:   now,
		}
		c.persistLocked(key, schemaJSON, now)
		return
	}
	if len(c.entries) >= schemaCacheMaxSize {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
		// Note: we do NOT delete from SQLite — it stores everything.
	}
	c.entries[key] = &schemaCacheEntry{
		SchemaJSON: schemaJSON,
		CachedAt:   now,
	}
	c.order = append(c.order, key)
	c.persistLocked(key, schemaJSON, now)
}

// persistLocked writes an entry to SQLite. Caller must hold c.mu.
func (c *schemaCache) persistLocked(key, schemaJSON string, cachedAt time.Time) {
	if c.db == nil {
		return
	}
	_, err := c.db.Exec(
		`INSERT OR REPLACE INTO schemas (key, schema_json, cached_at) VALUES (?, ?, ?)`,
		key, schemaJSON, cachedAt.Unix(),
	)
	if err != nil {
		log.Printf("schemacache: failed to persist %s: %v", key, err)
	}
}

// Close closes the underlying SQLite connection, if any.
func (c *schemaCache) Close() error {
	if c.db != nil {
		return c.db.Close()
	}
	return nil
}
