package navigator

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/genai"
)

// Tool is a single capability the Navigator can invoke.
type Tool interface {
	Declaration() *genai.FunctionDeclaration
	Execute(ctx context.Context, args map[string]any) (string, *ActionResult)
}

// ToolRegistry holds all registered tools and dispatches calls.
type ToolRegistry struct {
	tools   []Tool
	byName  map[string]Tool
	blocked map[string]bool
}

// NewToolRegistry creates an empty registry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{byName: make(map[string]Tool), blocked: make(map[string]bool)}
}

// Register adds a tool to the registry.
func (r *ToolRegistry) Register(t Tool) {
	r.tools = append(r.tools, t)
	r.byName[t.Declaration().Name] = t
}

// Definitions returns the []*genai.Tool slice for GenerateContentConfig.
func (r *ToolRegistry) Definitions() []*genai.Tool {
	decls := make([]*genai.FunctionDeclaration, len(r.tools))
	for i, t := range r.tools {
		decls[i] = t.Declaration()
	}
	return []*genai.Tool{{FunctionDeclarations: decls}}
}

// DefinitionsExcluding returns tool definitions minus the named tools.
// Used to strip act() from read-only intent sessions.
func (r *ToolRegistry) DefinitionsExcluding(names ...string) []*genai.Tool {
	exclude := make(map[string]bool, len(names))
	for _, n := range names {
		exclude[n] = true
	}
	var decls []*genai.FunctionDeclaration
	for _, t := range r.tools {
		if !exclude[t.Declaration().Name] {
			decls = append(decls, t.Declaration())
		}
	}
	return []*genai.Tool{{FunctionDeclarations: decls}}
}

// SetBlocked marks the named tools as blocked at the dispatch level.
// Blocked tools are rejected by Execute even if the model calls them.
func (r *ToolRegistry) SetBlocked(names ...string) {
	for _, n := range names {
		r.blocked[n] = true
	}
}

// ClearBlocked removes all dispatch-level blocks.
func (r *ToolRegistry) ClearBlocked() {
	for k := range r.blocked {
		delete(r.blocked, k)
	}
}

// Execute dispatches a FunctionCall to the matching tool.
func (r *ToolRegistry) Execute(ctx context.Context, fc *genai.FunctionCall) (string, *ActionResult) {
	if r.blocked[fc.Name] {
		return fmt.Sprintf("Tool %q is blocked in this session (read-only mode)", fc.Name), nil
	}
	t, ok := r.byName[fc.Name]
	if !ok {
		return fmt.Sprintf("Unknown tool: %s", fc.Name), nil
	}
	return t.Execute(ctx, fc.Args)
}

// ToolList returns a compact "- name(params): description" listing for the system prompt.
func (r *ToolRegistry) ToolList() string {
	var sb strings.Builder
	for _, t := range r.tools {
		d := t.Declaration()
		var params []string
		if d.Parameters != nil {
			for name := range d.Parameters.Properties {
				params = append(params, name)
			}
		}
		if len(params) > 0 {
			fmt.Fprintf(&sb, "- %s(%s): %s\n", d.Name, strings.Join(params, ", "), d.Description)
		} else {
			fmt.Fprintf(&sb, "- %s(): %s\n", d.Name, d.Description)
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}
