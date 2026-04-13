package mache

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/agentic-research/mache/api"
	"github.com/agentic-research/mache/graph"
)

// Mount represents one entry from the Cartographer's output.
type Mount struct {
	VirtualPath  string     `json:"virtual_path"`
	MacheID      string     `json:"mache_id"`
	Description  string     `json:"description"`
	CSSSelector  string     `json:"css_selector,omitempty"`
	PrimaryItems []string   `json:"primary_items"`
	ItemSelector string     `json:"item_selector,omitempty"`
	Bounds       [4]float64 `json:"bounds,omitempty"`        // zone AABB [x,y,w,h] normalized
	Fingerprint  string     `json:"fingerprint,omitempty"`   // content hash for sheaf cache staleness
	StructuralFP string     `json:"structural_fp,omitempty"` // tag-shape hash — stable across same-layout pages
}

// CartographerOutput is the top-level JSON from the Cartographer.
type CartographerOutput struct {
	Mounts []Mount `json:"mounts"`
}

// Engine holds the virtual semantic filesystem backed by a mache MemoryStore.
// All public methods are safe for concurrent use.
type Engine struct {
	mu     sync.RWMutex
	store  *graph.MemoryStore
	mounts []Mount
}

func NewEngine() *Engine {
	return &Engine{
		store: graph.NewMemoryStore(),
	}
}

// cleanPath normalizes a virtual path to a MemoryStore node ID.
// Strips leading slash and cleans ".." etc. Root becomes "".
func cleanPath(p string) string {
	p = path.Clean(p)
	p = strings.TrimPrefix(p, "/")
	if p == "." {
		return ""
	}
	return p
}

// HasSchema reports whether at least one mount has been applied.
func (e *Engine) HasSchema() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.mounts) > 0
}

// ValidateSchema checks that every mache_id in the schema actually exists in
// the DOM summary. Returns a list of hallucinated IDs (empty = all valid).
func ValidateSchema(schemaJSON, summary string) []string {
	stale := ValidateSchemaZones(schemaJSON, summary)
	bad := make([]string, 0, len(stale))
	for _, reason := range stale {
		bad = append(bad, reason)
	}
	return bad
}

// ValidateSchemaZones checks each zone's mache_id against the current DOM
// summary. Returns a map of zone_path → stale mache_id for zones whose root
// element no longer exists in the DOM. Empty map = all valid.
//
// Fingerprint comparison is handled at the cache layer (comparing cached
// fingerprint against freshly computed fingerprint) rather than here,
// because recomputing the fingerprint requires knowing zone membership
// which is not available from the flat summary alone.
func ValidateSchemaZones(schemaJSON, summary string) map[string]string {
	var output CartographerOutput
	if err := json.Unmarshal([]byte(schemaJSON), &output); err != nil {
		return nil
	}
	stale := make(map[string]string)
	for _, m := range output.Mounts {
		if !strings.Contains(summary, "ID: "+m.MacheID+" ") {
			stale[m.VirtualPath] = m.MacheID
		}
	}
	return stale
}

// RepairSchema fixes hallucinated zone anchor IDs. If a zone's mache_id
// doesn't exist in the DOM but its primary_items do, the anchor is replaced
// with the first valid primary_item. Returns the repaired JSON and count
// of repairs made.
func RepairSchema(schemaJSON, summary string) (string, int) {
	var output CartographerOutput
	if err := json.Unmarshal([]byte(schemaJSON), &output); err != nil {
		return schemaJSON, 0
	}
	repaired := 0
	for i, m := range output.Mounts {
		if strings.Contains(summary, "ID: "+m.MacheID+" ") {
			continue // anchor is valid
		}
		// Anchor is hallucinated — find first valid primary_item.
		for _, pid := range m.PrimaryItems {
			if strings.Contains(summary, "ID: "+pid+" ") {
				output.Mounts[i].MacheID = pid
				repaired++
				break
			}
		}
	}
	if repaired == 0 {
		return schemaJSON, 0
	}
	fixed, err := json.Marshal(output)
	if err != nil {
		return schemaJSON, 0
	}
	return string(fixed), repaired
}

// ValidateSchemaBounds checks that each zone's cached bounds approximately
// match the current DOM element's bounds. Returns a map of zone_path →
// stale_mache_id for zones whose element center has moved beyond threshold
// (normalized viewport coordinates). An empty map means all bounds match.
//
// Zones with zero Bounds in the cached schema are skipped (no stored bounds).
// Elements missing from the summary are skipped (caught by ValidateSchemaZones).
func ValidateSchemaBounds(schemaJSON, summary string, threshold float64) map[string]string {
	var output CartographerOutput
	if err := json.Unmarshal([]byte(schemaJSON), &output); err != nil {
		return nil
	}

	// Build macheID → current bounds from the DOM summary.
	currentBounds := make(map[string][4]float64)
	for _, line := range strings.Split(summary, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "ID: ") {
			continue
		}
		// Extract element ID (first space-delimited token after "ID: ").
		rest := strings.TrimPrefix(trimmed, "ID: ")
		id := rest
		if idx := strings.Index(rest, " "); idx >= 0 {
			id = rest[:idx]
		}
		// Extract bounds using the shared regex from filter.go.
		m := BoundsRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		x, _ := strconv.ParseFloat(m[1], 64)
		y, _ := strconv.ParseFloat(m[2], 64)
		w, _ := strconv.ParseFloat(m[3], 64)
		h, _ := strconv.ParseFloat(m[4], 64)
		currentBounds[id] = [4]float64{x, y, w, h}
	}

	stale := make(map[string]string)
	for _, mount := range output.Mounts {
		if mount.Bounds == ([4]float64{}) {
			continue // no stored bounds — skip
		}
		cur, ok := currentBounds[mount.MacheID]
		if !ok {
			continue // not in summary — caught by ValidateSchemaZones
		}
		// Compare center points.
		cachedCX := mount.Bounds[0] + mount.Bounds[2]/2
		cachedCY := mount.Bounds[1] + mount.Bounds[3]/2
		curCX := cur[0] + cur[2]/2
		curCY := cur[1] + cur[3]/2
		dx := cachedCX - curCX
		dy := cachedCY - curCY
		dist := math.Sqrt(dx*dx + dy*dy)
		if dist > threshold {
			stale[mount.VirtualPath] = mount.MacheID
		}
	}
	return stale
}

// ValidateSchemaStructure compares structural fingerprints between a cached
// schema JSON and a freshly computed one. Returns a map of zone_path → mache_id
// for zones whose StructuralFP differs. Zones with empty StructuralFP on either
// side are skipped. An empty map means all structures match.
func ValidateSchemaStructure(cachedJSON, currentJSON string) map[string]string {
	var cached, current CartographerOutput
	if err := json.Unmarshal([]byte(cachedJSON), &cached); err != nil {
		return nil
	}
	if err := json.Unmarshal([]byte(currentJSON), &current); err != nil {
		return nil
	}

	currentFP := make(map[string]string, len(current.Mounts))
	for _, m := range current.Mounts {
		if m.StructuralFP != "" {
			currentFP[m.VirtualPath] = m.StructuralFP
		}
	}

	changed := make(map[string]string)
	for _, m := range cached.Mounts {
		if m.StructuralFP == "" {
			continue
		}
		curFP, ok := currentFP[m.VirtualPath]
		if !ok {
			continue // zone disappeared — caught by ValidateSchemaZones
		}
		if m.StructuralFP != curFP {
			changed[m.VirtualPath] = m.MacheID
		}
	}
	return changed
}

// ApplySchema parses the Cartographer JSON and builds the virtual FS.
func (e *Engine) ApplySchema(schemaJSON string) error {
	var output CartographerOutput
	if err := json.Unmarshal([]byte(schemaJSON), &output); err != nil {
		return fmt.Errorf("parse cartographer output: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.mounts = output.Mounts
	e.store = graph.NewMemoryStore()

	for _, m := range output.Mounts {
		e.insertMount(m)
	}
	return nil
}

// MergeSchema grafts new mounts into the existing filesystem.
// Unlike ApplySchema, it preserves the existing MemoryStore and mounts,
// but enforces the presheaf restriction map: if an incoming mount's path
// is a child of an existing mount (e.g., /main/player/controls refines
// /main/player), the parent mount is evicted. The coarse section is
// replaced by finer sub-sections.
func (e *Engine) MergeSchema(schemaJSON string) error {
	var output CartographerOutput
	if err := json.Unmarshal([]byte(schemaJSON), &output); err != nil {
		return fmt.Errorf("parse cartographer output: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	// Restriction map: evict existing mounts that are strict parents of
	// incoming mounts. This ensures the presheaf condition — you can't
	// have both /main/player and /main/player/controls as sections.
	kept := e.mounts[:0]
	for _, existing := range e.mounts {
		evict := false
		for _, incoming := range output.Mounts {
			if incoming.VirtualPath != existing.VirtualPath &&
				strings.HasPrefix(incoming.VirtualPath, existing.VirtualPath+"/") {
				evict = true
				break
			}
		}
		if !evict {
			kept = append(kept, existing)
		}
	}
	e.mounts = kept

	for _, m := range output.Mounts {
		e.insertMount(m)
	}
	e.mounts = append(e.mounts, output.Mounts...)
	return nil
}

// insertMount creates directory nodes along the path and leaf file nodes.
func (e *Engine) insertMount(m Mount) {
	p := strings.TrimPrefix(m.VirtualPath, "/")
	parts := strings.Split(p, "/")

	// Create intermediate directory nodes along the path.
	for i, part := range parts {
		if part == "" {
			continue
		}
		nodeID := strings.Join(parts[:i+1], "/")

		if _, err := e.store.GetNode(nodeID); err == nil {
			continue // already exists
		}

		node := &graph.Node{
			ID:       nodeID,
			Mode:     fs.ModeDir,
			Children: []string{},
		}

		if i == 0 {
			e.store.AddRoot(node)
		} else {
			e.store.AddNode(node)
			// Register as child of parent directory.
			parentID := strings.Join(parts[:i], "/")
			if parent, err := e.store.GetNode(parentID); err == nil {
				parent.Children = appendUnique(parent.Children, nodeID)
			}
		}
	}

	// The mount point directory already exists from the loop above.
	mountNode, err := e.store.GetNode(p)
	if err != nil {
		return
	}

	// Store mache_id as a property on the directory node.
	if mountNode.Properties == nil {
		mountNode.Properties = make(map[string][]byte)
	}
	mountNode.Properties["mache_id"] = []byte(m.MacheID)
	if m.Bounds != ([4]float64{}) {
		mountNode.Properties["bounds"] = []byte(fmt.Sprintf("[%.3f,%.3f,%.3f,%.3f]", m.Bounds[0], m.Bounds[1], m.Bounds[2], m.Bounds[3]))
	}

	// Add mache_id leaf file.
	macheIDFile := p + "/mache_id"
	e.store.AddNode(&graph.Node{ID: macheIDFile, Data: []byte(m.MacheID)})
	mountNode.Children = appendUnique(mountNode.Children, macheIDFile)

	// Add description leaf file.
	descFile := p + "/description"
	e.store.AddNode(&graph.Node{ID: descFile, Data: []byte(m.Description)})
	mountNode.Children = appendUnique(mountNode.Children, descFile)
}

// appendUnique appends val to slice only if not already present.
func appendUnique(slice []string, val string) []string {
	for _, v := range slice {
		if v == val {
			return slice
		}
	}
	return append(slice, val)
}

// ListDir returns child names at the given path.
func (e *Engine) ListDir(dirPath string) ([]string, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	p := cleanPath(dirPath)

	var childIDs []string
	if p == "" {
		// Root listing via MemoryStore roots.
		var err error
		childIDs, err = e.store.ListChildren("")
		if err != nil {
			return nil, err
		}
	} else {
		node, err := e.store.GetNode(p)
		if err != nil {
			return nil, fmt.Errorf("not found: %s", dirPath)
		}
		if !node.Mode.IsDir() {
			return nil, fmt.Errorf("%s is not a directory", dirPath)
		}
		childIDs = node.Children
	}

	names := make([]string, 0, len(childIDs))
	for _, childID := range childIDs {
		child, err := e.store.GetNode(childID)
		if err != nil {
			continue
		}
		name := path.Base(childID)
		if child.Mode.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// ReadFile returns file content at the given path.
func (e *Engine) ReadFile(filePath string) (string, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	p := cleanPath(filePath)
	node, err := e.store.GetNode(p)
	if err != nil {
		return "", fmt.Errorf("not found: %s", filePath)
	}
	if node.Mode.IsDir() {
		return "", fmt.Errorf("%s is a directory", filePath)
	}
	return string(node.Data), nil
}

// ResolveMacheID finds the mache_id for a given virtual path.
func (e *Engine) ResolveMacheID(nodePath string) (string, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	p := cleanPath(nodePath)
	node, err := e.store.GetNode(p)
	if err != nil {
		return "", fmt.Errorf("no mache_id found at %s", nodePath)
	}

	// Check Properties on directory nodes.
	if node.Properties != nil {
		if mid, ok := node.Properties["mache_id"]; ok {
			return string(mid), nil
		}
	}

	// If it's a file named "mache_id", return its content.
	if !node.Mode.IsDir() && path.Base(p) == "mache_id" {
		return string(node.Data), nil
	}

	// If it's a directory, look for a mache_id child file.
	if node.Mode.IsDir() {
		childID := p + "/mache_id"
		if childNode, err := e.store.GetNode(childID); err == nil {
			return string(childNode.Data), nil
		}
	}

	return "", fmt.Errorf("no mache_id found at %s", nodePath)
}

// ToTopology converts the engine state to mache schema types.
func (e *Engine) ToTopology() *api.Topology {
	e.mu.RLock()
	defer e.mu.RUnlock()
	topo := &api.Topology{Version: "v1"}
	for _, m := range e.mounts {
		topo.Nodes = append(topo.Nodes, api.Node{
			Name:     m.VirtualPath,
			Selector: m.MacheID,
		})
	}
	return topo
}

// ZoneSelectors returns a map of zone mache-id → CSS item_selector for
// all mounts that have a dynamic selector defined. Used by scrollPage to
// send selectors to the browser for re-evaluation after scroll.
func (e *Engine) ZoneSelectors() map[string]string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	selectors := make(map[string]string)
	for _, m := range e.mounts {
		if m.ItemSelector != "" {
			selectors[m.MacheID] = m.ItemSelector
		}
	}
	return selectors
}

// ---------------------------------------------------------------------------
// graph.Graph interface — allows Engine to be mounted in a CompositeGraph
// ---------------------------------------------------------------------------

func (e *Engine) GetNode(id string) (*graph.Node, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.store.GetNode(cleanPath(id))
}

func (e *Engine) ListChildren(id string) ([]string, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.store.ListChildren(cleanPath(id))
}

func (e *Engine) ReadContent(id string, buf []byte, offset int64) (int, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.store.ReadContent(cleanPath(id), buf, offset)
}

func (e *Engine) GetCallers(token string) ([]*graph.Node, error) { return nil, nil }
func (e *Engine) GetCallees(id string) ([]*graph.Node, error)    { return nil, nil }
func (e *Engine) Invalidate(id string)                           {}

func (e *Engine) Act(id, action, payload string) (*graph.ActionResult, error) {
	return nil, graph.ErrActNotSupported
}

// ---------------------------------------------------------------------------
// DOM summary parsing (unchanged — DOM-specific helpers)
// ---------------------------------------------------------------------------

// SummaryElement represents one parsed line from the DOM summary.
type SummaryElement struct {
	ID       string
	ParentID string
	Tag      string
	Text     string
	Path     string // DOM breadcrumb path (optional, e.g., "div.post > h3.title > a")
	AXRole   string // from CDP accessibility tree (optional)
	AXName   string // from CDP accessibility tree (optional)
	Color    string // semantic color name (e.g., "BLUE")
	Bounds   string // normalized coordinates [x, y, w, h]

	// Stacking context fields (from content.js computed styles + LayerTree)
	ZIndex       string  // "auto" or integer string
	Opacity      float64 // 0.0 to 1.0
	PaintOrder   int     // compositing paint order from LayerTree DFS (-1 = no layer)
	StackingRoot bool    // true if element creates a stacking context
	HasPaint     bool    // true if PaintOrder was parsed
}

// parseSummary extracts structured elements from the summary text.
// Expected format: ID: mache-X | Color: BLUE | Bounds: [x,y,w,h] | Parent: mache-Y | Tag: a | Text: "..."
// Optional AX fields: ... | AXRole: navigation | AXName: "Primary nav"
func parseSummary(summary string) []SummaryElement {
	var elements []SummaryElement
	for _, line := range strings.Split(summary, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ID: ") {
			continue
		}

		el := SummaryElement{ParentID: "none"}

		// Split by " | " and handle key-value pairs
		segments := strings.Split(line, " | ")
		for _, segment := range segments {
			segment = strings.TrimSpace(segment)
			if v, ok := strings.CutPrefix(segment, "ID: "); ok {
				el.ID = v
			} else if v, ok := strings.CutPrefix(segment, "Parent: "); ok {
				el.ParentID = v
			} else if v, ok := strings.CutPrefix(segment, "Tag: "); ok {
				el.Tag = v
			} else if v, ok := strings.CutPrefix(segment, "Color: "); ok {
				el.Color = v
			} else if v, ok := strings.CutPrefix(segment, "Bounds: "); ok {
				el.Bounds = v
			} else if v, ok := strings.CutPrefix(segment, "AXRole: "); ok {
				el.AXRole = v
			} else if v, ok := strings.CutPrefix(segment, "AXName: "); ok {
				el.AXName = strings.Trim(v, "\"")
			} else if v, ok := strings.CutPrefix(segment, "Path: "); ok {
				el.Path = v
			} else if v, ok := strings.CutPrefix(segment, "ZIndex: "); ok {
				el.ZIndex = v
			} else if v, ok := strings.CutPrefix(segment, "Opacity: "); ok {
				if f, err := strconv.ParseFloat(v, 64); err == nil {
					el.Opacity = f
				}
			} else if v, ok := strings.CutPrefix(segment, "PaintOrder: "); ok {
				if n, err := strconv.Atoi(v); err == nil {
					el.PaintOrder = n
					el.HasPaint = true
				}
			} else if v, ok := strings.CutPrefix(segment, "StackingRoot: "); ok {
				el.StackingRoot = v == "true"
			} else if v, ok := strings.CutPrefix(segment, "Text: "); ok {
				// Handle quoted text
				if strings.HasPrefix(v, "\"") && strings.HasSuffix(v, "\"") {
					el.Text = strings.Trim(v, "\"")
				} else {
					el.Text = v
				}
			}
		}

		if el.ID != "" {
			elements = append(elements, el)
		}
	}
	return elements
}

// buildParentMap buckets children by their parent ID.
func buildParentMap(elements []SummaryElement) map[string][]SummaryElement {
	pm := make(map[string][]SummaryElement)
	for _, el := range elements {
		pm[el.ParentID] = append(pm[el.ParentID], el)
	}
	return pm
}

// collectDescendants performs BFS from rootID to maxDepth, returning all descendants.
func collectDescendants(parentMap map[string][]SummaryElement, rootID string, maxDepth int) []SummaryElement {
	var result []SummaryElement
	type item struct {
		id    string
		depth int
	}
	queue := []item{{id: rootID, depth: 0}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		children := parentMap[cur.id]
		for _, child := range children {
			result = append(result, child)
			if cur.depth+1 < maxDepth {
				queue = append(queue, item{id: child.ID, depth: cur.depth + 1})
			}
		}
	}
	return result
}

const maxChildrenPerZone = 200

// selectChildItems determines which descendants appear in the children listing
// and _c/ directory. Primary items are listed first (preserving order), then
// supplementary text-bearing descendants are appended (needed for scanning
// tasks like WebArena where Navigator must read all page text). Supplementary
// items are capped to avoid flooding the list when zones are too broad.
func selectChildItems(descendants []SummaryElement, primaryItems []string) []SummaryElement {
	if len(primaryItems) > 0 {
		byID := make(map[string]SummaryElement, len(descendants))
		for _, d := range descendants {
			byID[d.ID] = d
		}
		seen := make(map[string]bool, len(primaryItems))
		var items []SummaryElement
		for _, id := range primaryItems {
			if d, ok := byID[id]; ok {
				items = append(items, d)
				seen[id] = true
			}
		}
		// Supplement with other text-bearing descendants not in primary items.
		// Cap supplementary items so they don't overwhelm the primary list.
		maxSupp := len(items) // at most 1:1 ratio with primary items
		if maxSupp < 10 {
			maxSupp = 10
		}
		suppCount := 0
		for _, d := range descendants {
			if d.Text != "" && !seen[d.ID] && suppCount < maxSupp {
				items = append(items, d)
				suppCount++
			}
		}
		return items
	}
	var items []SummaryElement
	for _, d := range descendants {
		if d.Text != "" {
			items = append(items, d)
		}
	}
	return items
}

// truncText truncates s to max characters with "..." suffix.
func truncText(s string, max int) string {
	if len(s) > max {
		return s[:max-3] + "..."
	}
	return s
}

// formatOrdinalChildren formats items as a hierarchical ordinal list.
// Items whose parent is also in the list are indented under their parent.
// Each line includes the tag name for semantic disambiguation:
//
//	[1] article: Post title
//	  [2] a: r/funny
//	  [3] span: 2 hours ago
//	[4] article: Another post
func formatOrdinalChildren(items []SummaryElement) string {
	// Build lookup: which items' parents are also in the list
	idToOrdinal := make(map[string]int, len(items))
	for i, d := range items {
		idToOrdinal[d.ID] = i + 1
	}

	var sb strings.Builder
	for i, d := range items {
		// Skip items whose parent is also in the list — shown nested below
		if _, parentInList := idToOrdinal[d.ParentID]; parentInList {
			continue
		}
		marker := lynxMarker(d.Tag, d.AXRole)
		fmt.Fprintf(&sb, "%s[%d] %s: %s\n", marker, i+1, d.Tag, truncText(d.Text, 80))
		// Show direct children indented
		for j, child := range items {
			if child.ParentID == d.ID {
				cm := lynxMarker(child.Tag, child.AXRole)
				fmt.Fprintf(&sb, "  %s[%d] %s: %s\n", cm, j+1, child.Tag, truncText(child.Text, 70))
			}
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// lynxMarker returns a Lynx-style prefix for element type differentiation.
// Links return "" (most common — no noise). Buttons, inputs, etc. get markers.
func lynxMarker(tag, axRole string) string {
	switch tag {
	case "button":
		return "(btn) "
	case "input":
		return "[___] "
	case "select":
		return "[v] "
	case "img":
		return "[img] "
	case "h1", "h2", "h3", "h4", "h5", "h6":
		return "# "
	}
	switch axRole {
	case "button":
		return "(btn) "
	case "checkbox":
		return "[ ] "
	case "radio":
		return "( ) "
	case "textbox", "searchbox", "combobox":
		return "[___] "
	case "tab":
		return "(tab) "
	case "heading":
		return "# "
	}
	return ""
}

// buildParentOfMap builds a child→parent lookup from parsed summary elements.
// Called once in LoadChildren and passed to collectZoneMembers for each zone,
// avoiding redundant O(N) map builds per zone.
func buildParentOfMap(elements []SummaryElement) map[string]string {
	m := make(map[string]string, len(elements))
	for _, el := range elements {
		m[el.ID] = el.ParentID
	}
	return m
}

// collectZoneMembers returns all elements that are descendants of the zone root,
// determined by walking each element's parent chain. This handles arbitrarily
// deep nesting (e.g., Reddit's virtual scroll containers) and picks up new
// elements loaded after scrolling, unlike collectByPrimaryItems which only
// matches the original primary item IDs.
func collectZoneMembers(elements []SummaryElement, zoneRootID string, parentOf map[string]string) []SummaryElement {
	// Memoized zone membership with depth cap to prevent cycles.
	cache := make(map[string]bool)
	var inZone func(string, int) bool
	inZone = func(id string, depth int) bool {
		if depth > 20 || id == "" || id == "none" {
			return false
		}
		if id == zoneRootID {
			return true
		}
		if cached, ok := cache[id]; ok {
			return cached
		}
		result := inZone(parentOf[id], depth+1)
		cache[id] = result
		return result
	}

	var result []SummaryElement
	for _, el := range elements {
		if el.ID != zoneRootID && inZone(el.ID, 0) {
			result = append(result, el)
		}
	}
	return result
}

// LoadChildren parses the summary and populates children/_c/ for each mounted zone.
// resolvedItems is an optional map of zone mache-id → fresh primary item IDs resolved
// by the browser via CSS selectors after scroll. When present, these override the
// schema's static PrimaryItems for that zone.
func (e *Engine) LoadChildren(summary string, resolvedItems map[string][]string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	elements := parseSummary(summary)
	if len(elements) == 0 {
		return
	}
	parentMap := buildParentMap(elements)
	parentOf := buildParentOfMap(elements)

	// Index all elements by ID for O(1) lookup (used by primary item fallback).
	byID := make(map[string]SummaryElement, len(elements))
	for _, el := range elements {
		byID[el.ID] = el
	}

	for _, m := range e.mounts {
		// Use browser-resolved items (from CSS selector) if available,
		// otherwise fall back to the schema's static primary items.
		// Union the schema's static primary_items with browser-resolved CSS items.
		// Filter out stale IDs that don't exist in the current DOM summary
		// (e.g., cached schema from a different tab has wrong mache-IDs).
		seen := make(map[string]bool)
		var effectivePrimary []string
		for _, id := range m.PrimaryItems {
			if _, ok := byID[id]; ok && !seen[id] {
				seen[id] = true
				effectivePrimary = append(effectivePrimary, id)
			}
		}
		if resolvedItems != nil {
			if res, ok := resolvedItems[m.MacheID]; ok {
				for _, id := range res {
					if !seen[id] {
						seen[id] = true
						effectivePrimary = append(effectivePrimary, id)
					}
				}
			}
		}

		// Collect all elements in this zone by walking parent chains to the
		// zone root. Handles arbitrarily deep nesting and picks up new
		// elements loaded after scrolling.
		descendants := collectZoneMembers(elements, m.MacheID, parentOf)

		// Fall back to BFS from zone root (in case parent chains don't
		// reach the zone root due to untagged intermediate containers).
		if len(descendants) == 0 {
			descendants = collectDescendants(parentMap, m.MacheID, 10)
		}

		// Third fallback: zone root is a leaf element (e.g., on HN the
		// Cartographer picks the first story <a> as zone root, but other
		// stories are siblings, not descendants). Collect primary items
		// directly from parsed elements.
		if len(descendants) == 0 && len(effectivePrimary) > 0 {
			for _, id := range effectivePrimary {
				if el, ok := byID[id]; ok {
					descendants = append(descendants, el)
				}
			}
		}

		// Fourth fallback — spatial containment: when parent-chain walking
		// and BFS both fail (because unregistered intermediate DOM containers
		// break the parent chain), claim orphaned elements whose center falls
		// within this zone's bounding box.
		if len(descendants) == 0 && m.Bounds != ([4]float64{}) {
			// Guard: skip zones that are essentially the full page (body/root).
			// A zone starting near origin and covering nearly the full viewport
			// would swallow every element. Real content zones (even large <main>
			// containers at 85-95% of viewport) have some offset or don't span
			// the full width+height simultaneously.
			isFullPage := m.Bounds[0] < 0.02 && m.Bounds[1] < 0.02 &&
				m.Bounds[2] > 0.96 && m.Bounds[3] > 0.96
			if !isFullPage {
				for _, el := range elements {
					if el.ID == m.MacheID {
						continue
					}
					elBounds, ok := parseBoundsString(el.Bounds)
					if !ok {
						continue
					}
					if boundsContains(m.Bounds, elBounds) {
						descendants = append(descendants, el)
					}
				}
			}
		}

		if len(descendants) == 0 {
			continue
		}
		// Cap at maxChildrenPerZone
		if len(descendants) > maxChildrenPerZone {
			descendants = descendants[:maxChildrenPerZone]
		}

		// Resolve the zone directory in the graph store.
		zoneID := cleanPath(m.VirtualPath)
		zoneNode, err := e.store.GetNode(zoneID)
		if err != nil || !zoneNode.Mode.IsDir() {
			continue
		}

		// Select items for the children listing and _c/ directory.
		// Ordinal numbering: _c/1, _c/2, ... so the model never sees raw mache IDs.
		childItems := selectChildItems(descendants, effectivePrimary)
		if len(childItems) == 0 {
			continue
		}

		// Add "children" file node with ordinal format.
		childrenFileID := zoneID + "/children"
		e.store.AddNode(&graph.Node{
			ID:   childrenFileID,
			Data: []byte(formatOrdinalChildren(childItems)),
		})
		zoneNode.Children = appendUnique(zoneNode.Children, childrenFileID)

		// Build "_c/" directory with ordinal subdirs: _c/1/, _c/2/, ...
		cDirID := zoneID + "/_c"
		cDir := &graph.Node{
			ID:       cDirID,
			Mode:     fs.ModeDir,
			Children: make([]string, 0, len(childItems)),
		}

		for i, d := range childItems {
			ordinal := fmt.Sprintf("%d", i+1)
			childDirID := cDirID + "/" + ordinal
			macheIDFileID := childDirID + "/mache_id"
			tagFileID := childDirID + "/tag"
			textFileID := childDirID + "/text"

			children := []string{macheIDFileID, tagFileID, textFileID}

			// Semantic enrichment: inject AX role, name, DOM path, color, and bounds when available.
			if d.AXRole != "" {
				id := childDirID + "/role"
				e.store.AddNode(&graph.Node{ID: id, Data: []byte(d.AXRole)})
				children = append(children, id)
			}
			if d.AXName != "" {
				id := childDirID + "/name"
				e.store.AddNode(&graph.Node{ID: id, Data: []byte(d.AXName)})
				children = append(children, id)
			}
			if d.Path != "" {
				id := childDirID + "/path"
				e.store.AddNode(&graph.Node{ID: id, Data: []byte(d.Path)})
				children = append(children, id)
			}
			if d.Color != "" {
				id := childDirID + "/color"
				e.store.AddNode(&graph.Node{ID: id, Data: []byte(d.Color)})
				children = append(children, id)
			}
			if d.Bounds != "" {
				id := childDirID + "/bounds"
				e.store.AddNode(&graph.Node{ID: id, Data: []byte(d.Bounds)})
				children = append(children, id)
			}

			childDir := &graph.Node{
				ID:         childDirID,
				Mode:       fs.ModeDir,
				Children:   children,
				Properties: map[string][]byte{"mache_id": []byte(d.ID)},
			}
			e.store.AddNode(childDir)
			e.store.AddNode(&graph.Node{ID: macheIDFileID, Data: []byte(d.ID)})
			e.store.AddNode(&graph.Node{ID: tagFileID, Data: []byte(d.Tag)})
			e.store.AddNode(&graph.Node{ID: textFileID, Data: []byte(d.Text)})

			cDir.Children = append(cDir.Children, childDirID)
		}
		e.store.AddNode(cDir)
		zoneNode.Children = appendUnique(zoneNode.Children, cDirID)
	}

	// Build a global text_index: every text-bearing element, regardless of
	// zone assignment. This lets grep find content even when elements are
	// orphaned (parent chains break, spatial containment misses).
	e.buildTextIndex(elements)
}

// buildTextIndex creates a root-level "text_index" file listing interactive
// and short-text elements from the DOM summary. Uses Lynx-style formatting:
// - Reading order (y-major, x-minor sort)
// - Grouped by spatial region (-- top -- / -- center -- / -- bottom --)
// - Element type markers: (btn), [___], #, (tab), etc.
// - Mache IDs in brackets for direct act() clickability
func (e *Engine) buildTextIndex(elements []SummaryElement) {
	// Tags that are inherently interactive.
	interactive := map[string]bool{
		"a": true, "button": true, "input": true,
		"select": true, "textarea": true, "summary": true,
	}
	// AX roles that indicate interactivity.
	interactiveRoles := map[string]bool{
		"tab": true, "button": true, "link": true,
		"checkbox": true, "radio": true, "switch": true,
		"menuitem": true, "option": true, "combobox": true,
		"searchbox": true, "textbox": true,
	}

	type indexEntry struct {
		el     SummaryElement
		cx, cy float64
	}
	var entries []indexEntry
	for _, el := range elements {
		if el.Text == "" {
			continue
		}
		isInteractive := interactive[el.Tag] || interactiveRoles[el.AXRole]
		isShort := len(el.Text) <= 100
		if !isInteractive && !isShort {
			continue
		}
		var cx, cy float64
		if b, ok := parseBoundsString(el.Bounds); ok {
			cx = b[0] + b[2]/2
			cy = b[1] + b[3]/2
		}
		entries = append(entries, indexEntry{el: el, cx: cx, cy: cy})
	}
	if len(entries) == 0 {
		return
	}

	// Sort by reading order: y-major, x-minor.
	sort.Slice(entries, func(i, j int) bool {
		if math.Abs(entries[i].cy-entries[j].cy) > 0.05 {
			return entries[i].cy < entries[j].cy
		}
		return entries[i].cx < entries[j].cx
	})

	var sb strings.Builder
	lastRegion := ""
	for _, ent := range entries {
		region := verticalRegion(ent.cy)
		if region != lastRegion {
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			fmt.Fprintf(&sb, "-- %s --\n", region)
			lastRegion = region
		}
		marker := lynxMarker(ent.el.Tag, ent.el.AXRole)
		text := ent.el.Text
		if len(text) > 80 {
			text = text[:77] + "..."
		}
		fmt.Fprintf(&sb, "%s[%s] %s\n", marker, ent.el.ID, text)
	}
	if sb.Len() == 0 {
		return
	}

	indexID := "text_index"
	e.store.AddRoot(&graph.Node{ID: indexID, Data: []byte(strings.TrimRight(sb.String(), "\n"))})
}

// SetPageText stores the full visible page text as a root-level file.
// This captures review text, article bodies, and other non-interactive content
// that element-tagging misses. Searchable via grep.
func (e *Engine) SetPageText(text string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if text == "" {
		return
	}
	e.store.AddRoot(&graph.Node{ID: "page_text", Data: []byte(text)})
}

// spatialLabel converts a bounds string "[x, y, w, h]" into a human-readable
// position label like "top-left", "center", "bottom-right". Uses the element
// center point mapped to a 3×3 grid.
func spatialLabel(boundsStr string) string {
	b, ok := parseBoundsString(boundsStr)
	if !ok {
		return ""
	}
	cx := b[0] + b[2]/2 // center x
	cy := b[1] + b[3]/2 // center y

	var vert, horiz string
	switch {
	case cy < 0.33:
		vert = "top"
	case cy < 0.66:
		vert = "mid"
	default:
		vert = "bottom"
	}
	switch {
	case cx < 0.33:
		horiz = "left"
	case cx < 0.66:
		horiz = "center"
	default:
		horiz = "right"
	}

	if vert == "mid" && horiz == "center" {
		return "center"
	}
	if horiz == "center" {
		return vert
	}
	if vert == "mid" {
		return horiz
	}
	return vert + "-" + horiz
}

// verticalRegion maps a normalized y center to a coarse region name.
// Used by buildTextIndex for Lynx-style section headers.
func verticalRegion(cy float64) string {
	switch {
	case cy < 0.33:
		return "top"
	case cy < 0.66:
		return "center"
	default:
		return "bottom"
	}
}
