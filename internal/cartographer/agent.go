package cartographer

import (
	"context"
	"fmt"
	"log"

	"google.golang.org/genai"
)

// Agent represents Stage 1: The Cartographer
// Responsible for taking a visual + HTML snapshot and generating a Mache JSON Schema.
type Agent struct {
	client *genai.Client
	model  string
}

// NewAgent initializes a new Cartographer agent.
func NewAgent(client *genai.Client, model string) *Agent {
	if model == "" {
		model = "gemini-2.5-flash" // Default to Flash to avoid free-tier rate limits on Pro
	}
	return &Agent{
		client: client,
		model:  model,
	}
}

// GenerateSchema takes a screenshot (bytes), image mime type, and raw HTML (string)
// and returns a Mache Topology Schema mapping.
func (a *Agent) GenerateSchema(ctx context.Context, screenshot []byte, mimeType, rawHTML string) (string, error) {
	log.Println("Cartographer: Generating dynamic Mache schema from visual+HTML context...")

	// Configure the request with Structured Outputs
	config := &genai.GenerateContentConfig{
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{
				{Text: SystemPrompt},
			},
		},
		ResponseMIMEType: "application/json",
		ResponseSchema:   GetSchemaDefinition(),
		Temperature:      genai.Ptr(float32(0.1)), // Low temperature for deterministic mapping
	}

	// Build the multimodal content
	userContent := &genai.Content{
		Role: "user",
		Parts: []*genai.Part{
			{
				InlineData: &genai.Blob{
					Data:     screenshot,
					MIMEType: mimeType,
				},
			},
			{Text: "Here is a list of interactive elements (with data-mache-id) found on the page:\n\n" + rawHTML},
			{Text: "Generate the semantic filesystem schema for the interactive elements."},
		},
	}

	res, err := a.client.Models.GenerateContent(ctx, a.model, []*genai.Content{userContent}, config)
	if err != nil {
		return "", fmt.Errorf("GenerateContent failed: %w", err)
	}

	if len(res.Candidates) == 0 {
		return "", fmt.Errorf("no candidates returned")
	}

	part := res.Candidates[0].Content.Parts[0]

	if part.Text != "" {
		return part.Text, nil
	}

	return "", fmt.Errorf("unexpected response part type")
}
