package api

import (
	"net/url"
	"strings"
	"sync"
	"time"
)

const schemaCacheMaxSize = 50

// schemaCacheEntry holds one cached schema and metadata for logging.
type schemaCacheEntry struct {
	SchemaJSON string
	CachedAt   time.Time
}

// schemaCache is a bounded in-memory cache keyed by domain+path slug.
type schemaCache struct {
	mu      sync.RWMutex
	entries map[string]*schemaCacheEntry
	order   []string // insertion order for FIFO eviction
}

func newSchemaCache() *schemaCache {
	return &schemaCache{
		entries: make(map[string]*schemaCacheEntry, schemaCacheMaxSize),
	}
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

// Put stores a schema. If the cache is full, the oldest entry is evicted.
func (c *schemaCache) Put(key, schemaJSON string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; exists {
		c.entries[key] = &schemaCacheEntry{
			SchemaJSON: schemaJSON,
			CachedAt:   time.Now(),
		}
		return
	}
	if len(c.entries) >= schemaCacheMaxSize {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
	c.entries[key] = &schemaCacheEntry{
		SchemaJSON: schemaJSON,
		CachedAt:   time.Now(),
	}
	c.order = append(c.order, key)
}
