// Package interactions provides a graph.Graph mount for tracking interaction
// lifecycle. Mounted at /interactions/ in the CompositeGraph, it gives the
// Navigator read/write access to the current interaction's intent, status,
// and scratchpad, while providing the Doer with structured completion signals.
//
// Layout:
//
//	interactions/
//	  active/
//	    id        — interaction ID (read-only)
//	    intent    — what was requested (read-only)
//	    task      — COMPAT alias for intent
//	    status    — "in_progress" | "completed" | "failed:<reason>"
//	    scratch   — working notes (Navigator appends via act("type", text))
//	    steps/    — audit trail of Navigator actions (read-only, populated by Doer)
//	  history/    — last 10 completed interactions
//	    {id}/
//	      intent
//	      status
//	      summary
//	      scratch
package interactions

import (
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"time"

	"github.com/agentic-research/mache/graph"
)

const maxHistory = 10

// DedupFunc checks if text is a duplicate of existing scratchpad content.
// Returns true if the write should be silently blocked.
type DedupFunc func(scratchpad, text string) bool

// Record tracks one interaction's lifecycle.
type Record struct {
	ID      string
	Intent  string
	Status  string // "in_progress", "completed", "failed:<reason>"
	Scratch string
	Summary string
	Steps   []string
}

// Graph implements graph.Graph for the /interactions/ mount point.
type Graph struct {
	mu      sync.RWMutex
	active  *Record
	history []*Record // ring buffer, cap maxHistory
	dedupFn DedupFunc
}

// New creates an empty interaction graph.
func New() *Graph { return &Graph{} }

// ── Doer/Planner API ──────────────────────────────────────────────────────────

// StartInteraction begins a new interaction, clearing any previous active state.
func (g *Graph) StartInteraction(id, intent string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.active = &Record{
		ID:     id,
		Intent: intent,
		Status: "in_progress",
	}
}

// FinishInteraction moves the active interaction to history with the given summary.
func (g *Graph) FinishInteraction(summary string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active == nil {
		return
	}
	g.active.Summary = summary
	g.history = append(g.history, g.active)
	if len(g.history) > maxHistory {
		g.history = g.history[len(g.history)-maxHistory:]
	}
	g.active = nil
}

// Status returns the active interaction's status (thread-safe).
// Returns "" if no active interaction.
func (g *Graph) Status() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.active == nil {
		return ""
	}
	return g.active.Status
}

// Scratch returns the current scratch content (thread-safe).
// Returns active interaction's scratch if one exists, otherwise the most
// recent history entry's scratch (so callers can read it after finish).
func (g *Graph) Scratch() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.active != nil {
		return g.active.Scratch
	}
	if len(g.history) > 0 {
		return g.history[len(g.history)-1].Scratch
	}
	return ""
}

// RecordStep appends a step entry to the active interaction's audit trail.
func (g *Graph) RecordStep(text string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active != nil {
		g.active.Steps = append(g.active.Steps, text)
	}
}

// SetDedupFunc installs a deduplication guardrail for scratchpad writes.
func (g *Graph) SetDedupFunc(fn DedupFunc) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.dedupFn = fn
}

// Reset clears all state (active + history).
func (g *Graph) Reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.active = nil
	g.history = nil
}

// ── graph.Graph interface ────────────────────────────────────────────────────

func (g *Graph) GetNode(id string) (*graph.Node, error) {
	id = strings.TrimPrefix(id, "/")

	g.mu.RLock()
	defer g.mu.RUnlock()

	switch {
	// Root
	case id == "":
		children := []string{"active"}
		if len(g.history) > 0 {
			children = append(children, "history")
		}
		return &graph.Node{
			ID:       id,
			Mode:     fs.ModeDir | 0o555,
			ModTime:  time.Now(),
			Children: children,
		}, nil

	// Active directory
	case id == "active":
		return &graph.Node{
			ID:       id,
			Mode:     fs.ModeDir | 0o555,
			ModTime:  time.Now(),
			Children: g.activeChildren(),
		}, nil

	// Active fields
	case id == "active/id":
		return g.activeFieldNode(id, g.activeID()), nil
	case id == "active/intent", id == "active/task":
		return g.activeFieldNode(id, g.activeIntent()), nil
	case id == "active/status":
		return g.activeFieldNode(id, g.activeStatus()), nil
	case id == "active/scratch":
		return g.activeFieldNode(id, g.activeScratch()), nil

	// Steps directory
	case id == "active/steps":
		return g.stepsDir()

	// Individual step: active/steps/0, active/steps/1, ...
	case strings.HasPrefix(id, "active/steps/"):
		return g.stepNode(id)

	// History directory
	case id == "history":
		return g.historyDir()

	// History entry directory: history/{id}
	case strings.HasPrefix(id, "history/"):
		return g.historyNode(id)
	}

	return nil, graph.ErrNotFound
}

func (g *Graph) ListChildren(id string) ([]string, error) {
	id = strings.TrimPrefix(id, "/")

	g.mu.RLock()
	defer g.mu.RUnlock()

	switch {
	case id == "":
		children := []string{"active"}
		if len(g.history) > 0 {
			children = append(children, "history")
		}
		return children, nil

	case id == "active":
		return g.activeChildren(), nil

	case id == "active/steps":
		if g.active == nil {
			return nil, nil
		}
		children := make([]string, len(g.active.Steps))
		for i := range g.active.Steps {
			children[i] = fmt.Sprintf("active/steps/%d", i)
		}
		return children, nil

	case id == "history":
		children := make([]string, len(g.history))
		for i, rec := range g.history {
			children[i] = "history/" + rec.ID
		}
		return children, nil

	case strings.HasPrefix(id, "history/"):
		parts := strings.SplitN(id, "/", 3)
		if len(parts) == 2 {
			// history/{id} — list fields
			for _, rec := range g.history {
				if rec.ID == parts[1] {
					return []string{
						id + "/intent",
						id + "/status",
						id + "/summary",
						id + "/scratch",
					}, nil
				}
			}
		}
		return nil, graph.ErrNotFound
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

// Act supports writing to scratch and status.
func (g *Graph) Act(id, action, payload string) (*graph.ActionResult, error) {
	id = strings.TrimPrefix(id, "/")

	if action != "type" {
		return nil, graph.ErrActNotSupported
	}

	switch id {
	case "active/scratch":
		g.mu.Lock()
		if g.active == nil {
			g.mu.Unlock()
			return nil, graph.ErrActNotSupported
		}
		fn := g.dedupFn
		if fn != nil && fn(g.active.Scratch, payload) {
			g.mu.Unlock()
			// Silently accept — LLM thinks it wrote successfully.
			return &graph.ActionResult{NodeID: id, Action: action, Path: id}, nil
		}
		if g.active.Scratch != "" {
			g.active.Scratch += "\n"
		}
		g.active.Scratch += payload
		g.mu.Unlock()
		return &graph.ActionResult{NodeID: id, Action: action, Path: id}, nil

	case "active/status":
		if payload != "completed" && !strings.HasPrefix(payload, "failed:") {
			return nil, fmt.Errorf("invalid status %q: must be \"completed\" or \"failed:<reason>\"", payload)
		}
		g.mu.Lock()
		if g.active == nil {
			g.mu.Unlock()
			return nil, graph.ErrActNotSupported
		}
		g.active.Status = payload
		g.mu.Unlock()
		return &graph.ActionResult{NodeID: id, Action: action, Path: id}, nil
	}

	return nil, graph.ErrActNotSupported
}

func (g *Graph) GetCallers(token string) ([]*graph.Node, error) { return nil, nil }
func (g *Graph) GetCallees(id string) ([]*graph.Node, error)    { return nil, nil }
func (g *Graph) Invalidate(id string)                           {}

// Compile-time interface check.
var _ graph.Graph = (*Graph)(nil)

// ── helpers (must be called with mu held) ────────────────────────────────────

func (g *Graph) activeChildren() []string {
	base := []string{"active/id", "active/intent", "active/task", "active/status", "active/scratch", "active/steps"}
	return base
}

func (g *Graph) activeID() string {
	if g.active == nil {
		return ""
	}
	return g.active.ID
}

func (g *Graph) activeIntent() string {
	if g.active == nil {
		return ""
	}
	return g.active.Intent
}

func (g *Graph) activeStatus() string {
	if g.active == nil {
		return ""
	}
	return g.active.Status
}

func (g *Graph) activeScratch() string {
	if g.active == nil {
		return ""
	}
	return g.active.Scratch
}

func (g *Graph) activeFieldNode(id, data string) *graph.Node {
	return &graph.Node{
		ID:      id,
		Data:    []byte(data),
		ModTime: time.Now(),
	}
}

func (g *Graph) stepsDir() (*graph.Node, error) {
	children := []string{}
	if g.active != nil {
		for i := range g.active.Steps {
			children = append(children, fmt.Sprintf("active/steps/%d", i))
		}
	}
	return &graph.Node{
		ID:       "active/steps",
		Mode:     fs.ModeDir | 0o555,
		ModTime:  time.Now(),
		Children: children,
	}, nil
}

func (g *Graph) stepNode(id string) (*graph.Node, error) {
	if g.active == nil {
		return nil, graph.ErrNotFound
	}
	indexStr := strings.TrimPrefix(id, "active/steps/")
	var idx int
	if _, err := fmt.Sscanf(indexStr, "%d", &idx); err != nil || idx < 0 || idx >= len(g.active.Steps) {
		return nil, graph.ErrNotFound
	}
	return &graph.Node{
		ID:      id,
		Data:    []byte(g.active.Steps[idx]),
		ModTime: time.Now(),
	}, nil
}

func (g *Graph) historyDir() (*graph.Node, error) {
	children := make([]string, len(g.history))
	for i, rec := range g.history {
		children[i] = "history/" + rec.ID
	}
	return &graph.Node{
		ID:       "history",
		Mode:     fs.ModeDir | 0o555,
		ModTime:  time.Now(),
		Children: children,
	}, nil
}

func (g *Graph) historyNode(id string) (*graph.Node, error) {
	parts := strings.SplitN(strings.TrimPrefix(id, "history/"), "/", 2)
	recID := parts[0]

	for _, rec := range g.history {
		if rec.ID != recID {
			continue
		}

		// history/{id} — directory
		if len(parts) == 1 {
			return &graph.Node{
				ID:      id,
				Mode:    fs.ModeDir | 0o555,
				ModTime: time.Now(),
				Children: []string{
					id + "/intent",
					id + "/status",
					id + "/summary",
					id + "/scratch",
				},
			}, nil
		}

		// history/{id}/{field}
		var data string
		switch parts[1] {
		case "intent":
			data = rec.Intent
		case "status":
			data = rec.Status
		case "summary":
			data = rec.Summary
		case "scratch":
			data = rec.Scratch
		default:
			return nil, graph.ErrNotFound
		}
		return &graph.Node{
			ID:      id,
			Data:    []byte(data),
			ModTime: time.Now(),
		}, nil
	}

	return nil, graph.ErrNotFound
}
