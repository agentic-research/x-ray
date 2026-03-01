package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/agentic-research/mache/graph"
	"github.com/agentic-research/x-ray/internal/navigator"
	"google.golang.org/genai"
)

// convertSchema maps *genai.Schema → JSON Schema-compatible map.
func convertSchema(s *genai.Schema) map[string]any {
	if s == nil {
		return map[string]any{"type": "object"}
	}
	m := map[string]any{
		"type": schemaTypeStr(s.Type),
	}
	if len(s.Properties) > 0 {
		props := make(map[string]any)
		for k, v := range s.Properties {
			props[k] = map[string]any{
				"type":        schemaTypeStr(v.Type),
				"description": v.Description,
			}
		}
		m["properties"] = props
	}
	if len(s.Required) > 0 {
		m["required"] = s.Required
	}
	return m
}

func schemaTypeStr(t genai.Type) string {
	switch t {
	case genai.TypeString:
		return "string"
	case genai.TypeNumber:
		return "number"
	case genai.TypeInteger:
		return "integer"
	case genai.TypeBoolean:
		return "boolean"
	case genai.TypeArray:
		return "array"
	case genai.TypeObject:
		return "object"
	default:
		return "string"
	}
}

func main() {
	// 1. Get tool definitions from Agent
	g := graph.NewMemoryStore()
	_ = navigator.NewAgent(&navigator.GemmaGenerator{}, "gemma", g)

	// Hacky way to extract the config and tools, since Agent doesn't expose tools directly
	// Actually, we can just instantiate the tools and get their Declarations!
	tools := []genai.FunctionDeclaration{
		*(&navigator.LsTool{}).Declaration(),
		*(&navigator.CatTool{}).Declaration(),
		*(&navigator.ActTool{}).Declaration(),
		*(&navigator.ScrollTool{}).Declaration(),
		*(&navigator.GotoTool{}).Declaration(),
		*(&navigator.RescanTool{}).Declaration(),
		*(&navigator.ListTabsTool{}).Declaration(),
		*(&navigator.SwitchTabTool{}).Declaration(),
		*(&navigator.NewWindowTool{}).Declaration(),
		*(&navigator.NewTabTool{}).Declaration(),
	}

	var toolDefs []string
	for _, fd := range tools {
		params := convertSchema(fd.Parameters)
		def, _ := json.Marshal(map[string]any{
			"name":        fd.Name,
			"description": fd.Description,
			"parameters":  params,
		})
		toolDefs = append(toolDefs, string(def))
	}

	sysParts := []string{navigator.NavigatorSystemPrompt}

	sysParts = append(sysParts, fmt.Sprintf(
		"You have access to the following tools:\n%s\n\n"+
			"When you want to call a tool, respond with ONLY a JSON object in this exact format:\n"+
			`{"name": "tool_name", "parameters": {"param": "value"}}`+"\n\n"+
			"Do not wrap it in markdown. Do not add explanation before or after the JSON. "+
			"If you want to speak to the user (not call a tool), respond with plain text only.",
		strings.Join(toolDefs, "\n"),
	))

	finalPrompt := strings.Join(sysParts, "\n\n")

	fmt.Println(finalPrompt)
}
