package api

import (
	"context"

	"github.com/agentic-research/mache/graph"
	"github.com/agentic-research/x-ray/internal/navigator"
	"google.golang.org/genai"
)

// SchemaGenerator abstracts the Cartographer for testing.
type SchemaGenerator interface {
	GenerateSchema(ctx context.Context, screenshot []byte, mimeType, summary string) (string, error)
}

// IntentHandler abstracts the Navigator for testing.
type IntentHandler interface {
	HandleIntent(ctx context.Context, intent string, readOnly bool) (*navigator.ActionResult, string, error)
	ExecuteTool(ctx context.Context, fc *genai.FunctionCall) (string, *navigator.ActionResult)
	SetGraph(g graph.Graph)
	SetScrollFunc(fn func(ctx context.Context, direction string) error)
	SetProgressFunc(fn func(toolName string, args map[string]any))
	SetListTabsFunc(fn func(ctx context.Context) ([]navigator.TabInfo, error))
}
