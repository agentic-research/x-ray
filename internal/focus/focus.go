package focus

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/agentic-research/mache/graph"
)

// GetFrontmostApp returns "Google Chrome", "iTerm2", "Finder", etc.
func GetFrontmostApp() (string, error) {
	script := `tell application "System Events" to get name of first application process whose frontmost is true`
	out, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Router implements graph.Graph and dynamically routes requests to the correct
// mount point in the CompositeGraph based on the currently focused macOS app.
type Router struct {
	composite  *graph.CompositeGraph
	appMapping map[string]string // e.g. "Google Chrome" -> "browser"
	getApp     func() (string, error)
}

// NewRouter creates a new focus router.
func NewRouter(composite *graph.CompositeGraph, appMapping map[string]string) *Router {
	return &Router{
		composite:  composite,
		appMapping: appMapping,
		getApp:     GetFrontmostApp,
	}
}

// resolvePath prepends the correct prefix to the ID based on the active app.
func (r *Router) resolvePath(id string) (string, error) {
	app, err := r.getApp()
	if err != nil {
		return "", fmt.Errorf("focus: %w", err)
	}

	prefix, ok := r.appMapping[app]
	if !ok {
		return "", fmt.Errorf("focus: active app %q is not supported", app)
	}

	// If the ID is empty (root), just return the prefix itself so it hits the mount root.
	// Otherwise, construct the full absolute path.
	if id == "" {
		return prefix, nil
	}
	// Ensure the prefix is separated properly
	if !strings.HasPrefix(id, "/") {
		return prefix + "/" + id, nil
	}
	return prefix + id, nil
}

func (r *Router) GetNode(id string) (*graph.Node, error) {
	fullPath, err := r.resolvePath(id)
	if err != nil {
		return nil, err
	}
	return r.composite.GetNode(fullPath)
}

func (r *Router) ListChildren(id string) ([]string, error) {
	fullPath, err := r.resolvePath(id)
	if err != nil {
		return nil, err
	}
	return r.composite.ListChildren(fullPath)
}

func (r *Router) ReadContent(id string, buf []byte, offset int64) (int, error) {
	fullPath, err := r.resolvePath(id)
	if err != nil {
		return 0, err
	}
	return r.composite.ReadContent(fullPath, buf, offset)
}

func (r *Router) GetCallers(token string) ([]*graph.Node, error) {
	// Callers/Callees typically use full tokens anyway, but we just pass it through
	return r.composite.GetCallers(token)
}

func (r *Router) GetCallees(id string) ([]*graph.Node, error) {
	fullPath, err := r.resolvePath(id)
	if err != nil {
		return nil, err
	}
	return r.composite.GetCallees(fullPath)
}

func (r *Router) Invalidate(id string) {
	if fullPath, err := r.resolvePath(id); err == nil {
		r.composite.Invalidate(fullPath)
	}
}

func (r *Router) Act(id, action, payload string) (*graph.ActionResult, error) {
	fullPath, err := r.resolvePath(id)
	if err != nil {
		return nil, err
	}
	return r.composite.Act(fullPath, action, payload)
}
