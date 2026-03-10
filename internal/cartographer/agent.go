package cartographer

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

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

	// Retry with exponential backoff on 429/RESOURCE_EXHAUSTED.
	var res *genai.GenerateContentResponse
	const maxRetries = 5
	for attempt := range maxRetries {
		var apiErr error
		res, apiErr = a.client.Models.GenerateContent(ctx, a.model, []*genai.Content{userContent}, config)
		if apiErr == nil {
			break
		}
		errStr := apiErr.Error()
		is429 := strings.Contains(errStr, "429") || strings.Contains(errStr, "RESOURCE_EXHAUSTED")
		if !is429 || attempt == maxRetries-1 {
			return "", fmt.Errorf("GenerateContent failed: %w", apiErr)
		}
		backoff := time.Duration(1<<uint(attempt)) * time.Second
		log.Printf("Cartographer: 429 (attempt %d/%d), retrying in %v", attempt+1, maxRetries, backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return "", ctx.Err()
		}
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
