package mache

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/agentic-research/mache/api"
	"github.com/agentic-research/mache/graph"
)

// Mount represents one entry from the Cartographer's output.
type Mount struct {
	VirtualPath  string   `json:"virtual_path"`
	MacheID      string   `json:"mache_id"`
	Description  string   `json:"description"`
	CSSSelector  string   `json:"css_selector,omitempty"`
	PrimaryItems []string `json:"primary_items"`
	ItemSelector string   `json:"item_selector,omitempty"`
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
	var output CartographerOutput
	if err := json.Unmarshal([]byte(schemaJSON), &output); err != nil {
		return nil
	}
	var bad []string
	for _, m := range output.Mounts {
		if !strings.Contains(summary, "ID: "+m.MacheID+" ") {
			bad = append(bad, m.MacheID)
		}
	}
	return bad
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
// Unlike ApplySchema, it preserves the existing MemoryStore and mounts.
// The Cartographer is instructed to output absolute paths (e.g., /main/player/controls),
// so no prefix concatenation is needed — just insert directly.
func (e *Engine) MergeSchema(schemaJSON string) error {
	var output CartographerOutput
	if err := json.Unmarshal([]byte(schemaJSON), &output); err != nil {
		return fmt.Errorf("parse cartographer output: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()

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
// and _c/ directory. With primary items, only those are listed (preserving
// order). Otherwise, all descendants with non-empty text are included.
func selectChildItems(descendants []SummaryElement, primaryItems []string) []SummaryElement {
	if len(primaryItems) > 0 {
		byID := make(map[string]SummaryElement, len(descendants))
		for _, d := range descendants {
			byID[d.ID] = d
		}
		var items []SummaryElement
		for _, id := range primaryItems {
			if d, ok := byID[id]; ok {
				items = append(items, d)
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

// formatOrdinalChildren formats items as a simple ordinal list: [N] "text".
// The model uses N to reference items via _c/N paths.
func formatOrdinalChildren(items []SummaryElement) string {
	var sb strings.Builder
	for i, d := range items {
		fmt.Fprintf(&sb, "[%d] \"%s\"\n", i+1, d.Text)
	}
	return strings.TrimRight(sb.String(), "\n")
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
			descendants = collectDescendants(parentMap, m.MacheID, 2)
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
}
