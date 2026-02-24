package navigator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"google.golang.org/genai"
)

// ContentGenerator abstracts the LLM call so Navigator can use Gemini, Ollama, or a mock.
type ContentGenerator interface {
	GenerateContent(ctx context.Context, model string, history []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error)
}

// GeminiGenerator wraps the real genai.Client.
type GeminiGenerator struct {
	Client *genai.Client
}

func (g *GeminiGenerator) GenerateContent(ctx context.Context, model string, history []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	return g.Client.Models.GenerateContent(ctx, model, history, config)
}

// OllamaGenerator talks to Ollama/OpenAI-compatible endpoints.
// The genai.Client can't be reused here — it emits Gemini-specific wire format
// (contents/parts JSON + /v1beta/models/{model}:generateContent paths).
// Ollama speaks OpenAI format: /v1/chat/completions with messages/content.
type OllamaGenerator struct {
	Endpoint   string // e.g. http://localhost:11434/v1
	Model      string // e.g. llama3.2
	HTTPClient *http.Client
}

func (o *OllamaGenerator) GenerateContent(ctx context.Context, model string, history []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	if model == "" {
		model = o.Model
	}

	messages := o.convertHistory(history, config)
	tools := o.convertTools(config)

	reqBody := map[string]any{
		"model":    model,
		"messages": messages,
	}
	if len(tools) > 0 {
		reqBody["tools"] = tools
	}
	if config != nil && config.Temperature != nil {
		reqBody["temperature"] = *config.Temperature
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpClient := o.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.Endpoint+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", o.Endpoint+"/chat/completions", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	return o.parseResponse(respBody)
}

// convertHistory maps []*genai.Content → OpenAI messages array.
func (o *OllamaGenerator) convertHistory(history []*genai.Content, config *genai.GenerateContentConfig) []map[string]any {
	var messages []map[string]any

	// System instruction → system message.
	if config != nil && config.SystemInstruction != nil {
		for _, part := range config.SystemInstruction.Parts {
			if part.Text != "" {
				messages = append(messages, map[string]any{
					"role":    "system",
					"content": part.Text,
				})
			}
		}
	}

	for _, content := range history {
		role := content.Role
		if role == "model" {
			role = "assistant"
		}

		for _, part := range content.Parts {
			if part.Text != "" {
				messages = append(messages, map[string]any{
					"role":    role,
					"content": part.Text,
				})
			}
			if part.FunctionCall != nil {
				fc := part.FunctionCall
				argsJSON, _ := json.Marshal(fc.Args)
				messages = append(messages, map[string]any{
					"role":    "assistant",
					"content": nil,
					"tool_calls": []map[string]any{{
						"id":   fc.Name, // use name as ID for simplicity
						"type": "function",
						"function": map[string]any{
							"name":      fc.Name,
							"arguments": string(argsJSON),
						},
					}},
				})
			}
			if part.FunctionResponse != nil {
				fr := part.FunctionResponse
				output, _ := fr.Response["output"].(string)
				messages = append(messages, map[string]any{
					"role":         "tool",
					"tool_call_id": fr.Name,
					"content":      output,
				})
			}
		}
	}

	return messages
}

// convertTools maps genai.Tool definitions → OpenAI tools format.
func (o *OllamaGenerator) convertTools(config *genai.GenerateContentConfig) []map[string]any {
	if config == nil {
		return nil
	}
	var tools []map[string]any
	for _, tool := range config.Tools {
		for _, fd := range tool.FunctionDeclarations {
			params := convertSchema(fd.Parameters)
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        fd.Name,
					"description": fd.Description,
					"parameters":  params,
				},
			})
		}
	}
	return tools
}

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

// openAIResponse is the minimal OpenAI chat completion response structure.
type openAIResponse struct {
	Choices []openAIChoice `json:"choices"`
}

type openAIChoice struct {
	Message openAIMessage `json:"message"`
}

type openAIMessage struct {
	Role      string           `json:"role"`
	Content   *string          `json:"content"`
	ToolCalls []openAIToolCall `json:"tool_calls,omitempty"`
}

type openAIToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openAIFunctionCall `json:"function"`
}

type openAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// parseResponse maps an OpenAI chat completion response → *genai.GenerateContentResponse.
func (o *OllamaGenerator) parseResponse(body []byte) (*genai.GenerateContentResponse, error) {
	var oaiResp openAIResponse
	if err := json.Unmarshal(body, &oaiResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	if len(oaiResp.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response")
	}

	msg := oaiResp.Choices[0].Message
	var parts []*genai.Part

	// Tool calls → FunctionCall parts.
	if len(msg.ToolCalls) > 0 {
		for _, tc := range msg.ToolCalls {
			var args map[string]any
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				args = map[string]any{}
			}
			parts = append(parts, &genai.Part{
				FunctionCall: &genai.FunctionCall{
					Name: tc.Function.Name,
					Args: args,
				},
			})
		}
	} else if msg.Content != nil && *msg.Content != "" {
		// Text response.
		parts = append(parts, &genai.Part{Text: *msg.Content})
	}

	return &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content: &genai.Content{
				Role:  "model",
				Parts: parts,
			},
		}},
	}, nil
}
