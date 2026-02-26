package api

import (
	"context"

	"github.com/jamesgardner/x-ray/internal/mache"
	"github.com/jamesgardner/x-ray/internal/navigator"
	"google.golang.org/genai"
)

// SchemaGenerator abstracts the Cartographer for testing.
type SchemaGenerator interface {
	GenerateSchema(ctx context.Context, screenshot []byte, mimeType, summary string) (string, error)
}

// IntentHandler abstracts the Navigator for testing.
// TODO: Adding a new Navigator tool requires changes in 5 places: tool definition
// (agent.go ToolDefinitions), execution (agent.go ExecuteTool), system prompt
// (agent.go NavigatorSystemPrompt), callback wiring (doer.go executeGoal), and
// this interface. Consider a tool registry pattern to consolidate.
type IntentHandler interface {
	HandleIntent(ctx context.Context, intent string, readOnly bool) (*navigator.ActionResult, string, error)
	ExecuteTool(ctx context.Context, fc *genai.FunctionCall) (string, *navigator.ActionResult)
	SetEngine(engine *mache.Engine)
	SetScrollFunc(fn func(ctx context.Context, direction string) error)
	SetProgressFunc(fn func(toolName string, args map[string]any))
	SetListTabsFunc(fn func(ctx context.Context) ([]navigator.TabInfo, error))
}
