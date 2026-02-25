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
	tools  []Tool
	byName map[string]Tool
}

// NewToolRegistry creates an empty registry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{byName: make(map[string]Tool)}
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

// Execute dispatches a FunctionCall to the matching tool.
func (r *ToolRegistry) Execute(ctx context.Context, fc *genai.FunctionCall) (string, *ActionResult) {
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
