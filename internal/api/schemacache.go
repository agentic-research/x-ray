// TODO(post-hackathon): Replace raw MemoryStore + manual persistLocked() with
// graph.GraphCache from mache (merged in agentic-research/mache#79).
//
// Migration plan:
//  1. Replace c.mu/c.store/c.dbPath with *graph.GraphCache
//  2. Simple single-op methods (PutZone, InvalidateZone, InvalidateURL) →
//     use GraphCache.PutDir/PutFile/AppendChild/RemoveChild/ClearChildren
//  3. Complex multi-op methods (PutZones, getSectionsLocked) →
//     use GraphCache.Batch(func(store *graph.MemoryStore) { ... })
//     for atomic multi-step mutations with one SQLite persist
//  4. Remove persistLocked() entirely — GraphCache handles it
//  5. Keep all domain logic here: fingerprint matching, NavSection
//     serialization, goal hash normalization, max-sections eviction
//  6. Validate via existing schemacache_test.go (behavioral equivalence)
//
// See also: INVESTIGATION_LOG.md entry "2026-03-11: Implemented graph.GraphCache"
package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"log"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agentic-research/mache/graph"
	"github.com/agentic-research/x-ray/internal/mache"
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

// NavSection records a navigation action taken within a specific zone,
// keyed by a goal hash so that similar goals map to the same slot.
type NavSection struct {
	GoalHash     string
	ZonePath     string
	Fingerprint  string // content fingerprint (legacy, still stored)
	StructuralFP string // tag-shape fingerprint — used for cross-page matching
	Ordinal      string
	ElementText  string
	Action       string
	Payload      string // for type actions (e.g., search queries)
	Outcome      string
	RecordedAt   int64
}

// urlPattern matches http/https URLs for stripping from goal text.
var urlPattern = regexp.MustCompile(`https?://\S+`)

// digitPattern matches sequences of digits for normalization.
var digitPattern = regexp.MustCompile(`\d+`)

// multiSpace collapses runs of whitespace into a single space.
var multiSpace = regexp.MustCompile(`\s+`)

// NormalizeGoalHash produces a stable 16-hex-char hash from goal text.
// URLs are stripped, digit sequences replaced with "#", and whitespace collapsed
// before hashing, so that "click item 42" and "click item 99" produce the same hash.
func NormalizeGoalHash(goalText string) string {
	s := strings.ToLower(goalText)
	s = urlPattern.ReplaceAllString(s, "")
	s = digitPattern.ReplaceAllString(s, "#")
	s = multiSpace.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:16]
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

// CacheKey extracts "host/path?query" from a raw URL, stripping only fragments.
// Query parameters are preserved because paginated pages (e.g. ?p=2) have
// different DOM content and must not share a cache entry.
// Returns empty string if the URL is unparseable or empty.
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
	key := u.Host + p
	if u.RawQuery != "" {
		key += "?" + u.RawQuery
	}
	return key
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

	// Backup sections from old zones before clearing.
	// Key: escaped zone path, Value: slice of section backups.
	type sectionBackup struct {
		goalHash     string
		ordinal      string
		elementText  string
		action       string
		payload      string
		fingerprint  string
		structuralFP string
		outcome      string
		recordedAt   string
	}
	savedSections := map[string][]sectionBackup{}
	if zonesNode, err := c.store.GetNode(zonesDir); err == nil {
		for _, oldZoneID := range zonesNode.Children {
			escaped := strings.TrimPrefix(oldZoneID, zonesDir+"/")
			sectionsDir := oldZoneID + "/sections"
			sdNode, err := c.store.GetNode(sectionsDir)
			if err != nil {
				continue
			}
			for _, secID := range sdNode.Children {
				parts := strings.Split(secID, "/")
				gh := parts[len(parts)-1]
				sb := sectionBackup{goalHash: gh}
				if n, err := c.store.GetNode(secID + "/ordinal"); err == nil {
					sb.ordinal = string(n.Data)
				}
				if n, err := c.store.GetNode(secID + "/element_text"); err == nil {
					sb.elementText = string(n.Data)
				}
				if n, err := c.store.GetNode(secID + "/action"); err == nil {
					sb.action = string(n.Data)
				}
				if n, err := c.store.GetNode(secID + "/payload"); err == nil {
					sb.payload = string(n.Data)
				}
				if n, err := c.store.GetNode(secID + "/fingerprint"); err == nil {
					sb.fingerprint = string(n.Data)
				}
				if n, err := c.store.GetNode(secID + "/structural_fp"); err == nil {
					sb.structuralFP = string(n.Data)
				}
				if n, err := c.store.GetNode(secID + "/outcome"); err == nil {
					sb.outcome = string(n.Data)
				}
				if n, err := c.store.GetNode(secID + "/recorded_at"); err == nil {
					sb.recordedAt = string(n.Data)
				}
				savedSections[escaped] = append(savedSections[escaped], sb)
			}
		}
		// Clear existing zone entries (full replacement).
		zonesNode.Children = []string{}
	}

	// Write each mount as a zone entry.
	for _, m := range mounts {
		zoneID := zonesDir + "/" + escapeZonePath(m.VirtualPath)
		escaped := escapeZonePath(m.VirtualPath)
		mountJSON, _ := json.Marshal(m)

		c.store.AddNode(&graph.Node{
			ID:       zoneID,
			Mode:     fs.ModeDir,
			Children: []string{zoneID + "/mount_json", zoneID + "/fingerprint", zoneID + "/structural_fp", zoneID + "/cached_at"},
		})
		c.store.AddNode(&graph.Node{ID: zoneID + "/mount_json", Data: mountJSON, ModTime: now})
		c.store.AddNode(&graph.Node{ID: zoneID + "/fingerprint", Data: []byte(m.Fingerprint), ModTime: now})
		c.store.AddNode(&graph.Node{ID: zoneID + "/structural_fp", Data: []byte(m.StructuralFP), ModTime: now})
		c.store.AddNode(&graph.Node{ID: zoneID + "/cached_at", Data: []byte(nowStr), ModTime: now})

		if zonesNode, err := c.store.GetNode(zonesDir); err == nil {
			zonesNode.Children = appendUniqueStr(zonesNode.Children, zoneID)
		}

		// Restore sections whose fingerprint matches the new zone's fingerprint.
		if backups, ok := savedSections[escaped]; ok {
			sectionsDir := zoneID + "/sections"
			c.store.AddNode(&graph.Node{
				ID:       sectionsDir,
				Mode:     fs.ModeDir,
				Children: []string{},
			})
			if zoneNode, err := c.store.GetNode(zoneID); err == nil {
				zoneNode.Children = appendUniqueStr(zoneNode.Children, sectionsDir)
			}
			for _, sb := range backups {
				// Match on structural FP if available, else fall back to content fingerprint.
				matched := false
				if sb.structuralFP != "" && m.StructuralFP != "" {
					matched = sb.structuralFP == m.StructuralFP
				} else {
					matched = sb.fingerprint == m.Fingerprint
				}
				if !matched {
					continue
				}
				secID := sectionsDir + "/" + sb.goalHash
				c.store.AddNode(&graph.Node{
					ID:   secID,
					Mode: fs.ModeDir,
					Children: []string{
						secID + "/ordinal",
						secID + "/element_text",
						secID + "/action",
						secID + "/payload",
						secID + "/fingerprint",
						secID + "/structural_fp",
						secID + "/outcome",
						secID + "/recorded_at",
					},
				})
				c.store.AddNode(&graph.Node{ID: secID + "/ordinal", Data: []byte(sb.ordinal), ModTime: now})
				c.store.AddNode(&graph.Node{ID: secID + "/element_text", Data: []byte(sb.elementText), ModTime: now})
				c.store.AddNode(&graph.Node{ID: secID + "/action", Data: []byte(sb.action), ModTime: now})
				c.store.AddNode(&graph.Node{ID: secID + "/payload", Data: []byte(sb.payload), ModTime: now})
				c.store.AddNode(&graph.Node{ID: secID + "/fingerprint", Data: []byte(sb.fingerprint), ModTime: now})
				c.store.AddNode(&graph.Node{ID: secID + "/structural_fp", Data: []byte(sb.structuralFP), ModTime: now})
				c.store.AddNode(&graph.Node{ID: secID + "/outcome", Data: []byte(sb.outcome), ModTime: now})
				c.store.AddNode(&graph.Node{ID: secID + "/recorded_at", Data: []byte(sb.recordedAt), ModTime: now})
				if sdNode, err := c.store.GetNode(sectionsDir); err == nil {
					sdNode.Children = appendUniqueStr(sdNode.Children, secID)
				}
			}
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
		Children: []string{zoneID + "/mount_json", zoneID + "/fingerprint", zoneID + "/structural_fp", zoneID + "/cached_at"},
	})
	c.store.AddNode(&graph.Node{ID: zoneID + "/mount_json", Data: mountJSON, ModTime: now})
	c.store.AddNode(&graph.Node{ID: zoneID + "/fingerprint", Data: []byte(m.Fingerprint), ModTime: now})
	c.store.AddNode(&graph.Node{ID: zoneID + "/structural_fp", Data: []byte(m.StructuralFP), ModTime: now})
	c.store.AddNode(&graph.Node{ID: zoneID + "/cached_at", Data: []byte(nowStr), ModTime: now})

	if zonesNode, err := c.store.GetNode(zonesDir); err == nil {
		zonesNode.Children = appendUniqueStr(zonesNode.Children, zoneID)
	}

	c.persistLocked()
}

// GetZoneFingerprint returns the fingerprint for a zone, or "" if not found.
func (c *SchemaCache) GetZoneFingerprint(key, zonePath string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	fpID := key + "/zones/" + escapeZonePath(zonePath) + "/fingerprint"
	node, err := c.store.GetNode(fpID)
	if err != nil {
		return ""
	}
	return string(node.Data)
}

// GetZoneStructuralFP returns the structural fingerprint for a zone, or "" if not found.
func (c *SchemaCache) GetZoneStructuralFP(key, zonePath string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	fpID := key + "/zones/" + escapeZonePath(zonePath) + "/structural_fp"
	node, err := c.store.GetNode(fpID)
	if err != nil {
		return ""
	}
	return string(node.Data)
}

// maxSectionsPerZone is the cap on how many nav sections a single zone retains.
const maxSectionsPerZone = 5

// PutSection stores a NavSection under its zone. At most 5 sections are kept
// per zone; the oldest by RecordedAt is evicted when the cap is exceeded.
// Same GoalHash overwrites (idempotent).
func (c *SchemaCache) PutSection(key string, section NavSection) {
	c.mu.Lock()
	defer c.mu.Unlock()

	escaped := escapeZonePath(section.ZonePath)
	zoneID := key + "/zones/" + escaped
	sectionsDir := zoneID + "/sections"

	// Ensure sections/ directory node exists under the zone.
	if _, err := c.store.GetNode(sectionsDir); err != nil {
		c.store.AddNode(&graph.Node{
			ID:       sectionsDir,
			Mode:     fs.ModeDir,
			Children: []string{},
		})
		if zoneNode, err := c.store.GetNode(zoneID); err == nil {
			zoneNode.Children = appendUniqueStr(zoneNode.Children, sectionsDir)
		}
	}

	now := time.Now()
	secID := sectionsDir + "/" + section.GoalHash

	// Create/overwrite the section directory node.
	c.store.AddNode(&graph.Node{
		ID:   secID,
		Mode: fs.ModeDir,
		Children: []string{
			secID + "/ordinal",
			secID + "/element_text",
			secID + "/action",
			secID + "/payload",
			secID + "/fingerprint",
			secID + "/structural_fp",
			secID + "/outcome",
			secID + "/recorded_at",
		},
	})
	c.store.AddNode(&graph.Node{ID: secID + "/ordinal", Data: []byte(section.Ordinal), ModTime: now})
	c.store.AddNode(&graph.Node{ID: secID + "/element_text", Data: []byte(section.ElementText), ModTime: now})
	c.store.AddNode(&graph.Node{ID: secID + "/action", Data: []byte(section.Action), ModTime: now})
	c.store.AddNode(&graph.Node{ID: secID + "/payload", Data: []byte(section.Payload), ModTime: now})
	c.store.AddNode(&graph.Node{ID: secID + "/fingerprint", Data: []byte(section.Fingerprint), ModTime: now})
	c.store.AddNode(&graph.Node{ID: secID + "/structural_fp", Data: []byte(section.StructuralFP), ModTime: now})
	c.store.AddNode(&graph.Node{ID: secID + "/outcome", Data: []byte(section.Outcome), ModTime: now})
	c.store.AddNode(&graph.Node{ID: secID + "/recorded_at", Data: []byte(strconv.FormatInt(section.RecordedAt, 10)), ModTime: now})

	// Add to sections dir children (dedup).
	if sdNode, err := c.store.GetNode(sectionsDir); err == nil {
		sdNode.Children = appendUniqueStr(sdNode.Children, secID)

		// Enforce max 5 sections: evict oldest by RecordedAt.
		if len(sdNode.Children) > maxSectionsPerZone {
			oldestIdx := -1
			var oldestTS int64 = math.MaxInt64
			for i, childID := range sdNode.Children {
				if raNode, err := c.store.GetNode(childID + "/recorded_at"); err == nil {
					ts, _ := strconv.ParseInt(string(raNode.Data), 10, 64)
					if ts < oldestTS {
						oldestTS = ts
						oldestIdx = i
					}
				}
			}
			if oldestIdx >= 0 {
				sdNode.Children = append(sdNode.Children[:oldestIdx], sdNode.Children[oldestIdx+1:]...)
			}
		}
	}

	c.persistLocked()
}

// GetSections returns NavSections for a zone whose structure matches.
// Matches on structural fingerprint (tag-shape) first, falling back to
// content fingerprint for legacy sections that lack a structural FP.
// Sections that match neither are GC'd.
func (c *SchemaCache) GetSections(key, zonePath, currentFingerprint string) []NavSection {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Read the zone's structural FP from the cache.
	currentStructuralFP := ""
	zoneID := key + "/zones/" + escapeZonePath(zonePath)
	if n, err := c.store.GetNode(zoneID + "/structural_fp"); err == nil {
		currentStructuralFP = string(n.Data)
	}

	return c.getSectionsLocked(key, zonePath, currentFingerprint, currentStructuralFP, true)
}

// getSectionsLocked is the inner implementation shared by GetSections and
// GetAllSectionsForURL. If gcPersist is true it calls persistLocked after GC.
func (c *SchemaCache) getSectionsLocked(key, zonePath, currentFingerprint, currentStructuralFP string, gcPersist bool) []NavSection {
	escaped := escapeZonePath(zonePath)
	sectionsDir := key + "/zones/" + escaped + "/sections"
	sdNode, err := c.store.GetNode(sectionsDir)
	if err != nil {
		return nil
	}

	var result []NavSection
	var keep []string
	gcHappened := false

	for _, childID := range sdNode.Children {
		sec := c.readSectionNode(childID)
		if sec == nil {
			continue
		}
		sec.ZonePath = zonePath

		// Match on structural FP if both sides have one (cross-page transfer).
		// Fall back to content fingerprint for legacy sections without structural FP.
		matched := false
		if currentStructuralFP != "" && sec.StructuralFP != "" {
			matched = sec.StructuralFP == currentStructuralFP
		} else {
			matched = sec.Fingerprint == currentFingerprint
		}

		if matched {
			result = append(result, *sec)
			keep = append(keep, childID)
		} else {
			gcHappened = true
		}
	}

	if gcHappened {
		sdNode.Children = keep
		if gcPersist {
			c.persistLocked()
		}
	}

	// Sort newest first.
	sort.Slice(result, func(i, j int) bool {
		return result[i].RecordedAt > result[j].RecordedAt
	})

	return result
}

// readSectionNode reads a single section directory node and returns a NavSection.
func (c *SchemaCache) readSectionNode(secID string) *NavSection {
	secNode, err := c.store.GetNode(secID)
	if err != nil {
		return nil
	}
	_ = secNode // validate existence

	sec := &NavSection{}
	// Extract goal hash from the node ID (last path segment).
	parts := strings.Split(secID, "/")
	sec.GoalHash = parts[len(parts)-1]

	if n, err := c.store.GetNode(secID + "/ordinal"); err == nil {
		sec.Ordinal = string(n.Data)
	}
	if n, err := c.store.GetNode(secID + "/element_text"); err == nil {
		sec.ElementText = string(n.Data)
	}
	if n, err := c.store.GetNode(secID + "/action"); err == nil {
		sec.Action = string(n.Data)
	}
	if n, err := c.store.GetNode(secID + "/payload"); err == nil {
		sec.Payload = string(n.Data)
	}
	if n, err := c.store.GetNode(secID + "/fingerprint"); err == nil {
		sec.Fingerprint = string(n.Data)
	}
	if n, err := c.store.GetNode(secID + "/structural_fp"); err == nil {
		sec.StructuralFP = string(n.Data)
	}
	if n, err := c.store.GetNode(secID + "/outcome"); err == nil {
		sec.Outcome = string(n.Data)
	}
	if n, err := c.store.GetNode(secID + "/recorded_at"); err == nil {
		sec.RecordedAt, _ = strconv.ParseInt(string(n.Data), 10, 64)
	}

	return sec
}

// GetAllSectionsForURL walks all zones under a URL key and returns every
// valid (fingerprint-matching) NavSection. Stale sections are GC'd.
func (c *SchemaCache) GetAllSectionsForURL(key string) []NavSection {
	c.mu.Lock()
	defer c.mu.Unlock()

	zonesDir := key + "/zones"
	zonesNode, err := c.store.GetNode(zonesDir)
	if err != nil {
		return nil
	}

	var all []NavSection
	anyGC := false

	for _, zoneID := range zonesNode.Children {
		// Recover the zone path from the escaped node ID.
		escaped := strings.TrimPrefix(zoneID, zonesDir+"/")
		zonePath := "/" + strings.ReplaceAll(escaped, "~", "/")

		// Read the zone's fingerprints.
		fp := ""
		if fpNode, err := c.store.GetNode(zoneID + "/fingerprint"); err == nil {
			fp = string(fpNode.Data)
		}
		sfp := ""
		if sfpNode, err := c.store.GetNode(zoneID + "/structural_fp"); err == nil {
			sfp = string(sfpNode.Data)
		}

		sectionsDir := zoneID + "/sections"
		sdNode, err := c.store.GetNode(sectionsDir)
		if err != nil {
			continue
		}

		var keep []string
		for _, childID := range sdNode.Children {
			sec := c.readSectionNode(childID)
			if sec == nil {
				continue
			}
			sec.ZonePath = zonePath

			matched := false
			if sfp != "" && sec.StructuralFP != "" {
				matched = sec.StructuralFP == sfp
			} else {
				matched = sec.Fingerprint == fp
			}

			if matched {
				all = append(all, *sec)
				keep = append(keep, childID)
			} else {
				anyGC = true
			}
		}
		if len(keep) != len(sdNode.Children) {
			sdNode.Children = keep
		}
	}

	if anyGC {
		c.persistLocked()
	}

	// Sort newest first.
	sort.Slice(all, func(i, j int) bool {
		return all[i].RecordedAt > all[j].RecordedAt
	})

	return all
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
