package navigator

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/agentic-research/mache/graph"
)

// NavFS provides filesystem-style operations on top of a graph.Graph.
// Used by Navigator tools so they stay source-agnostic.
type NavFS struct {
	g graph.Graph
}

// NewNavFS wraps a graph.Graph with filesystem helper methods.
func NewNavFS(g graph.Graph) *NavFS {
	return &NavFS{g: g}
}

// ListDir returns child names at the given path, with "/" suffix for directories.
func (f *NavFS) ListDir(dirPath string) ([]string, error) {
	p := f.resolvePath(dirPath)

	childIDs, err := f.g.ListChildren(p)
	if err != nil {
		return nil, fmt.Errorf("not found: %s", dirPath)
	}

	names := make([]string, 0, len(childIDs))
	for _, childID := range childIDs {
		child, err := f.g.GetNode(childID)
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

// ReadFile returns the text content of a file node.
func (f *NavFS) ReadFile(filePath string) (string, error) {
	p := f.resolvePath(filePath)
	node, err := f.g.GetNode(p)
	if err != nil {
		return "", fmt.Errorf("not found: %s", filePath)
	}
	if node.Mode.IsDir() {
		return "", fmt.Errorf("%s is a directory", filePath)
	}
	return string(node.Data), nil
}

// ResolveMacheID finds the mache_id for a given virtual path.
// Also accepts bare mache IDs (e.g., "mache-42") directly — useful when
// the Navigator gets a mache_id from text_index grep results.
func (f *NavFS) ResolveMacheID(nodePath string) (string, error) {
	clean := cleanPath(nodePath)
	if strings.HasPrefix(clean, "mache-") {
		return clean, nil
	}
	p := f.resolvePath(nodePath)
	node, err := f.g.GetNode(p)
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
		if childNode, err := f.g.GetNode(childID); err == nil {
			return string(childNode.Data), nil
		}
	}

	return "", fmt.Errorf("no mache_id found at %s", nodePath)
}

// GetProperty returns a property value from a directory node.
func (f *NavFS) GetProperty(nodePath, key string) (string, bool) {
	p := f.resolvePath(nodePath)
	node, err := f.g.GetNode(p)
	if err != nil || node.Properties == nil {
		return "", false
	}
	v, ok := node.Properties[key]
	if !ok {
		return "", false
	}
	return string(v), true
}

// Act performs an action on the node at the given path, routing through
// the underlying graph. Returns ErrActNotSupported for passive graphs.
func (f *NavFS) Act(nodePath, action, payload string) (*graph.ActionResult, error) {
	p := f.resolvePath(nodePath)
	return f.g.Act(p, action, payload)
}

// HasContent reports whether the filesystem has any content (non-empty root).
func (f *NavFS) HasContent() bool {
	children, err := f.g.ListChildren("")
	return err == nil && len(children) > 0
}

func cleanPath(p string) string {
	p = path.Clean(p)
	p = strings.TrimPrefix(p, "/")
	if p == "." {
		return ""
	}
	return p
}

// resolvePath cleans a path and, if it doesn't exist in the graph, tries
// prepending "browser/" as a fallback. LLMs frequently omit the /browser/
// prefix when generating paths from the tree dump.
func (f *NavFS) resolvePath(rawPath string) string {
	p := cleanPath(rawPath)
	// Check if the path exists as-is.
	if _, err := f.g.GetNode(p); err == nil {
		return p
	}
	// Also check if it's a valid directory (ListChildren succeeds).
	if _, err := f.g.ListChildren(p); err == nil {
		return p
	}
	// Try prepending "browser/" if not already prefixed.
	if !strings.HasPrefix(p, "browser/") && p != "browser" && p != "" {
		candidate := "browser/" + p
		if _, err := f.g.GetNode(candidate); err == nil {
			return candidate
		}
		if _, err := f.g.ListChildren(candidate); err == nil {
			return candidate
		}
	}
	return p // Return original — let the caller produce the error.
}
