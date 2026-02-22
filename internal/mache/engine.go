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
	VirtualPath string `json:"virtual_path"`
	MacheID     string `json:"mache_id"`
	Description string `json:"description"`
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
