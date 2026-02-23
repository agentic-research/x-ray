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
type IntentHandler interface {
	HandleIntent(ctx context.Context, intent string) (*navigator.ActionResult, string, error)
	ExecuteTool(ctx context.Context, fc *genai.FunctionCall) (string, *navigator.ActionResult)
	SetEngine(engine *mache.Engine)
	SetScrollFunc(fn func(ctx context.Context, direction string) error)
}
