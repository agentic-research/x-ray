package mache

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/agentic-research/mache/api"
)

// Mount represents one entry from the Cartographer's output.
type Mount struct {
	VirtualPath  string   `json:"virtual_path"`
	MacheID      string   `json:"mache_id"`
	Description  string   `json:"description"`
	PrimaryItems []string `json:"primary_items"`
	ItemSelector string   `json:"item_selector,omitempty"`
}

// CartographerOutput is the top-level JSON from the Cartographer.
type CartographerOutput struct {
	Mounts []Mount `json:"mounts"`
}

// Entry represents a node in the virtual filesystem.
type Entry struct {
	Name     string
	IsDir    bool
	Content  string
	Children map[string]*Entry
	MacheID  string
}

// Engine holds the virtual semantic filesystem.
type Engine struct {
	root   *Entry
	mounts []Mount
}

func NewEngine() *Engine {
	return &Engine{
		root: &Entry{Name: "/", IsDir: true, Children: make(map[string]*Entry)},
	}
}

// HasSchema reports whether at least one mount has been applied.
func (e *Engine) HasSchema() bool {
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
	e.mounts = output.Mounts

	e.root = &Entry{Name: "/", IsDir: true, Children: make(map[string]*Entry)}

	for _, m := range output.Mounts {
		e.insertMount(m)
	}
	return nil
}

// insertMount creates directory entries along the path and leaf files.
func (e *Engine) insertMount(m Mount) {
	p := strings.TrimPrefix(m.VirtualPath, "/")
	parts := strings.Split(p, "/")

	current := e.root
	for _, part := range parts {
		if part == "" {
			continue
		}
		child, ok := current.Children[part]
		if !ok {
			child = &Entry{Name: part, IsDir: true, Children: make(map[string]*Entry)}
			current.Children[part] = child
		}
		current = child
	}

	current.MacheID = m.MacheID
	current.Children["mache_id"] = &Entry{Name: "mache_id", Content: m.MacheID}
	current.Children["description"] = &Entry{Name: "description", Content: m.Description}
}

// ListDir returns child names at the given path.
func (e *Engine) ListDir(dirPath string) ([]string, error) {
	entry, err := e.resolve(dirPath)
	if err != nil {
		return nil, err
	}
	if !entry.IsDir {
		return nil, fmt.Errorf("%s is not a directory", dirPath)
	}
	names := make([]string, 0, len(entry.Children))
	for name, child := range entry.Children {
		display := name
		if child.IsDir {
			display += "/"
		}
		names = append(names, display)
	}
	sort.Strings(names)
	return names, nil
}

// ReadFile returns file content at the given path.
func (e *Engine) ReadFile(filePath string) (string, error) {
	entry, err := e.resolve(filePath)
	if err != nil {
		return "", err
	}
	if entry.IsDir {
		return "", fmt.Errorf("%s is a directory", filePath)
	}
	return entry.Content, nil
}

// ResolveMacheID finds the mache_id for a given virtual path.
func (e *Engine) ResolveMacheID(nodePath string) (string, error) {
	entry, err := e.resolve(nodePath)
	if err != nil {
		return "", err
	}
	if entry.MacheID != "" {
		return entry.MacheID, nil
	}
	if !entry.IsDir && entry.Name == "mache_id" {
		return entry.Content, nil
	}
	if entry.IsDir {
		if child, ok := entry.Children["mache_id"]; ok {
			return child.Content, nil
		}
	}
	return "", fmt.Errorf("no mache_id found at %s", nodePath)
}

// ToTopology converts the engine state to mache schema types.
func (e *Engine) ToTopology() *api.Topology {
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
	selectors := make(map[string]string)
	for _, m := range e.mounts {
		if m.ItemSelector != "" {
			selectors[m.MacheID] = m.ItemSelector
		}
	}
	return selectors
}

// SummaryElement represents one parsed line from the DOM summary.
type SummaryElement struct {
	ID       string
	ParentID string
	Tag      string
	Text     string
	Path     string // DOM breadcrumb path (optional, e.g., "div.post > h3.title > a")
	AXRole   string // from CDP accessibility tree (optional)
	AXName   string // from CDP accessibility tree (optional)
}

// parseSummary extracts structured elements from the summary text.
// Expected format: ID: mache-X | Parent: mache-Y | Tag: a | Text: "..."
// Optional AX fields: ... | AXRole: navigation | AXName: "Primary nav"
func parseSummary(summary string) []SummaryElement {
	var elements []SummaryElement
	for _, line := range strings.Split(summary, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ID: ") {
			continue
		}
		// Split first 4 fields (ID, Parent, Tag, Text+rest).
		// Use SplitN to keep Text intact if it contains " | ".
		parts := strings.SplitN(line, " | ", 4)
		if len(parts) < 4 {
			continue
		}
		id := strings.TrimPrefix(parts[0], "ID: ")
		parentID := strings.TrimPrefix(parts[1], "Parent: ")
		tag := strings.TrimPrefix(parts[2], "Tag: ")

		// parts[3] = Text: "..." possibly followed by | AXRole: ... | AXName: "..."
		// Find the closing quote of the Text value to split off AX fields.
		rest := strings.TrimPrefix(parts[3], "Text: ")
		var text string
		var trailing string
		if strings.HasPrefix(rest, "\"") {
			if end := strings.Index(rest[1:], "\""); end >= 0 {
				text = rest[1 : end+1]
				trailing = rest[end+2:] // everything after closing quote
			} else {
				text = strings.Trim(rest, "\"")
			}
		} else {
			text = rest
		}

		el := SummaryElement{ID: id, ParentID: parentID, Tag: tag, Text: text}

		// Parse optional trailing fields (Path, AXRole, AXName)
		for _, segment := range strings.Split(trailing, " | ") {
			segment = strings.TrimSpace(segment)
			if v, ok := strings.CutPrefix(segment, "AXRole: "); ok {
				el.AXRole = v
			} else if v, ok := strings.CutPrefix(segment, "AXName: "); ok {
				el.AXName = strings.Trim(v, "\"")
			} else if v, ok := strings.CutPrefix(segment, "Path: "); ok {
				el.Path = v
			}
		}

		elements = append(elements, el)
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

// formatGroupedChildren formats descendants into numbered item groups.
// If primaryItems is non-empty (LLM-identified primary clickable elements),
// each primary item starts a new group with subsequent elements as metadata.
// Otherwise falls back to the empty-separator heuristic.
func formatGroupedChildren(descendants []SummaryElement, primaryItems []string) string {
	if len(primaryItems) > 0 {
		return formatByPrimaryItems(descendants, primaryItems)
	}

	// Heuristic fallback: detect empty-text separators.
	var emptyCount int
	for _, d := range descendants {
		if d.Text == "" {
			emptyCount++
		}
	}
	if emptyCount >= 2 && emptyCount < len(descendants)/2 {
		return formatByEmptySeparator(descendants)
	}

	// Flat list fallback.
	var lines []string
	for _, d := range descendants {
		lines = append(lines, fmt.Sprintf("%s | %s | \"%s\"", d.ID, d.Tag, d.Text))
	}
	return strings.Join(lines, "\n")
}

// formatByPrimaryItems outputs a compact numbered list of primary items.
// Only includes items from the provided primary list (either from the
// Cartographer's schema or browser-resolved via CSS selectors after scroll).
func formatByPrimaryItems(descendants []SummaryElement, primaryItems []string) string {
	// Index descendants by ID for O(1) lookup.
	byID := make(map[string]SummaryElement, len(descendants))
	for _, d := range descendants {
		byID[d.ID] = d
	}

	var sb strings.Builder
	itemNum := 0

	for _, id := range primaryItems {
		d, ok := byID[id]
		if !ok {
			continue
		}
		itemNum++
		fmt.Fprintf(&sb, "Item %d: %s | %s | \"%s\"\n", itemNum, d.ID, d.Tag, d.Text)
	}

	return strings.TrimRight(sb.String(), "\n")
}

// formatByEmptySeparator groups elements: each empty-text element starts a new
// numbered item group.
func formatByEmptySeparator(descendants []SummaryElement) string {
	var sb strings.Builder
	itemNum := 0
	inGroup := false

	for _, d := range descendants {
		if d.Text == "" {
			// Empty text = start of new item group.
			itemNum++
			if inGroup {
				sb.WriteString("\n")
			}
			fmt.Fprintf(&sb, "--- Item %d ---\n", itemNum)
			inGroup = true
			continue
		}

		// Skip structural containers (tbody, etc.) — they aren't actionable.
		if d.Tag == "tbody" || d.Tag == "thead" || d.Tag == "table" {
			continue
		}

		if !inGroup {
			// Elements before the first separator get their own group.
			itemNum++
			fmt.Fprintf(&sb, "--- Item %d ---\n", itemNum)
			inGroup = true
		}
		fmt.Fprintf(&sb, "  %s | %s | \"%s\"\n", d.ID, d.Tag, d.Text)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// collectZoneMembers returns all elements that are descendants of the zone root,
// determined by walking each element's parent chain. This handles arbitrarily
// deep nesting (e.g., Reddit's virtual scroll containers) and picks up new
// elements loaded after scrolling, unlike collectByPrimaryItems which only
// matches the original primary item IDs.
func collectZoneMembers(elements []SummaryElement, zoneRootID string) []SummaryElement {
	parentOf := make(map[string]string, len(elements))
	for _, el := range elements {
		parentOf[el.ID] = el.ParentID
	}

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
	elements := parseSummary(summary)
	if len(elements) == 0 {
		return
	}
	parentMap := buildParentMap(elements)

	// Index all elements by ID for O(1) lookup (used by primary item fallback).
	byID := make(map[string]SummaryElement, len(elements))
	for _, el := range elements {
		byID[el.ID] = el
	}

	for _, m := range e.mounts {
		// Use browser-resolved items (from CSS selector) if available,
		// otherwise fall back to the schema's static primary items.
		effectivePrimary := m.PrimaryItems
		if resolvedItems != nil {
			if resolved, ok := resolvedItems[m.MacheID]; ok && len(resolved) > 0 {
				effectivePrimary = resolved
			}
		}

		// Collect all elements in this zone by walking parent chains to the
		// zone root. Handles arbitrarily deep nesting and picks up new
		// elements loaded after scrolling.
		descendants := collectZoneMembers(elements, m.MacheID)

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

		// Resolve the zone directory
		zoneEntry, err := e.resolve(m.VirtualPath)
		if err != nil || !zoneEntry.IsDir {
			continue
		}

		zoneEntry.Children["children"] = &Entry{
			Name:    "children",
			Content: formatGroupedChildren(descendants, effectivePrimary),
		}

		// Build "_c/" directory with per-child subdirs
		cDir := &Entry{Name: "_c", IsDir: true, Children: make(map[string]*Entry)}
		for _, d := range descendants {
			childDir := &Entry{
				Name:     d.ID,
				IsDir:    true,
				MacheID:  d.ID,
				Children: make(map[string]*Entry),
			}
			childDir.Children["mache_id"] = &Entry{Name: "mache_id", Content: d.ID}
			childDir.Children["tag"] = &Entry{Name: "tag", Content: d.Tag}
			childDir.Children["text"] = &Entry{Name: "text", Content: d.Text}
			cDir.Children[d.ID] = childDir
		}
		zoneEntry.Children["_c"] = cDir
	}
}

// resolve navigates the tree to find the entry at the given path.
func (e *Engine) resolve(p string) (*Entry, error) {
	p = path.Clean(p)
	p = strings.TrimPrefix(p, "/")
	if p == "" || p == "." {
		return e.root, nil
	}
	parts := strings.Split(p, "/")
	current := e.root
	for _, part := range parts {
		if !current.IsDir {
			return nil, fmt.Errorf("not a directory: %s", part)
		}
		child, ok := current.Children[part]
		if !ok {
			return nil, fmt.Errorf("not found: %s", p)
		}
		current = child
	}
	return current, nil
}
