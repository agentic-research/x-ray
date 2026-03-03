// Package tasks provides a graph.Graph mount for tracking active goal state.
// Mounted at /tasks/ in the CompositeGraph, it gives the Navigator read/write
// access to the current task and a scratchpad for accumulating findings.
//
// Layout:
//
//	tasks/
//	  active/
//	    task      — current goal text (read-only, set by Doer)
//	    scratch   — working notes (Navigator appends via act("type", text))
package tasks

import (
	"io/fs"
	"strings"
	"sync"
	"time"

	"github.com/agentic-research/mache/graph"
)

// Graph implements graph.Graph for the /tasks/ mount point.
type Graph struct {
	mu      sync.RWMutex
	task    string // current goal text
	scratch string // Navigator-writable scratchpad
}

// New creates an empty task graph.
func New() *Graph { return &Graph{} }

// SetTask replaces the active task text (called by Doer on goal start).
func (g *Graph) SetTask(text string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.task = text
	g.scratch = "" // reset scratch on new goal
}

// ClearTask resets both task and scratch (called when goal finishes).
func (g *Graph) ClearTask() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.task = ""
	g.scratch = ""
}

// AppendScratch appends a line to the scratch pad.
func (g *Graph) AppendScratch(text string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.scratch != "" {
		g.scratch += "\n"
	}
	g.scratch += text
}

// Scratch returns the current scratch content.
func (g *Graph) Scratch() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.scratch
}

// ── graph.Graph interface ────────────────────────────────────────────────────

const (
	nodeActive        = "active"
	nodeActiveTask    = "active/task"
	nodeActiveScratch = "active/scratch"
)

func (g *Graph) GetNode(id string) (*graph.Node, error) {
	id = strings.TrimPrefix(id, "/")

	switch id {
	case "", "active":
		return &graph.Node{
			ID:       id,
			Mode:     fs.ModeDir | 0o555,
			ModTime:  time.Now(),
			Children: []string{nodeActiveTask, nodeActiveScratch},
		}, nil
	case nodeActiveTask:
		g.mu.RLock()
		data := g.task
		g.mu.RUnlock()
		return &graph.Node{
			ID:      id,
			Data:    []byte(data),
			ModTime: time.Now(),
		}, nil
	case nodeActiveScratch:
		g.mu.RLock()
		data := g.scratch
		g.mu.RUnlock()
		return &graph.Node{
			ID:      id,
			Data:    []byte(data),
			ModTime: time.Now(),
		}, nil
	}
	return nil, graph.ErrNotFound
}

func (g *Graph) ListChildren(id string) ([]string, error) {
	id = strings.TrimPrefix(id, "/")
	switch id {
	case "":
		return []string{nodeActive}, nil
	case "active":
		return []string{nodeActiveTask, nodeActiveScratch}, nil
	}
	return nil, graph.ErrNotFound
}

func (g *Graph) ReadContent(id string, buf []byte, offset int64) (int, error) {
	node, err := g.GetNode(id)
	if err != nil {
		return 0, err
	}
	if node.Data == nil {
		return 0, nil
	}
	if offset >= int64(len(node.Data)) {
		return 0, nil
	}
	n := copy(buf, node.Data[offset:])
	return n, nil
}

// Act supports writing to the scratch pad.
// act("active/scratch", "type", "some text") appends to scratch.
func (g *Graph) Act(id, action, payload string) (*graph.ActionResult, error) {
	id = strings.TrimPrefix(id, "/")
	if id == nodeActiveScratch && action == "type" {
		g.AppendScratch(payload)
		return &graph.ActionResult{
			NodeID: id,
			Action: action,
			Path:   id,
		}, nil
	}
	return nil, graph.ErrActNotSupported
}

func (g *Graph) GetCallers(token string) ([]*graph.Node, error) { return nil, nil }
func (g *Graph) GetCallees(id string) ([]*graph.Node, error)    { return nil, nil }
func (g *Graph) Invalidate(id string)                           {}

// Compile-time interface check.
var _ graph.Graph = (*Graph)(nil)
