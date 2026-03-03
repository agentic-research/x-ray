package cartographer

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/agentic-research/x-ray/internal/config"
)

// OllamaAgent talks to an OpenAI-compatible vision endpoint (Ollama, vLLM, etc.)
// instead of Gemini. Use for local VLMs like LLaVA, Qwen2-VL, etc.
type OllamaAgent struct {
	Endpoint   string // e.g. http://localhost:11434/v1
	Model      string // e.g. llava:13b
	Ollama     config.OllamaConfig
	HTTPClient *http.Client
}

func (o *OllamaAgent) GenerateSchema(ctx context.Context, screenshot []byte, mimeType, summary string) (string, error) {
	log.Printf("Cartographer (local): generating schema via %s (%s)", o.Endpoint, o.Model)

	client := o.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	// Build multimodal message with image + text.
	var contentParts []any

	if len(screenshot) > 0 {
		b64 := base64.StdEncoding.EncodeToString(screenshot)
		contentParts = append(contentParts, map[string]any{
			"type": "image_url",
			"image_url": map[string]string{
				"url": fmt.Sprintf("data:%s;base64,%s", mimeType, b64),
			},
		})
	}

	contentParts = append(contentParts,
		map[string]any{
			"type": "text",
			"text": "Here is a list of interactive elements (with data-mache-id) found on the page:\n\n" + summary,
		},
		map[string]any{
			"type": "text",
			"text": "Generate the semantic filesystem schema for the interactive elements. Respond with ONLY valid JSON matching this format: {\"mounts\": [{\"virtual_path\": \"/header/nav\", \"mache_id\": \"mache-1\", \"description\": \"...\", \"primary_items\": [], \"item_selector\": \"\"}]}",
		},
	)

	reqBody := map[string]any{
		"model": o.Model,
		"messages": []map[string]any{
			{"role": "system", "content": SystemPrompt},
			{"role": "user", "content": contentParts},
		},
		"temperature": 0.1,
		"stream":      false,
	}
	o.Ollama.Apply(reqBody)

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	url := o.Endpoint + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			log.Printf("Cartographer (local): error closing response body: %v", cerr)
		}
	}()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no choices returned")
	}

	content := result.Choices[0].Message.Content

	// Extract JSON from response — model may wrap it in markdown fences.
	content = extractJSON(content)

	return content, nil
}

// extractJSON strips markdown code fences if present.
func extractJSON(s string) string {
	// Try to find ```json ... ``` block.
	if idx := indexOf(s, "```json"); idx >= 0 {
		s = s[idx+7:]
		if end := indexOf(s, "```"); end >= 0 {
			return s[:end]
		}
	}
	if idx := indexOf(s, "```"); idx >= 0 {
		s = s[idx+3:]
		if end := indexOf(s, "```"); end >= 0 {
			return s[:end]
		}
	}
	return s
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
