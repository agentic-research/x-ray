package navigator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/agentic-research/x-ray/internal/config"
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
	const maxRetries = 5
	for attempt := range maxRetries {
		resp, err := g.Client.Models.GenerateContent(ctx, model, history, config)
		if err == nil {
			return resp, nil
		}
		errStr := err.Error()
		is429 := strings.Contains(errStr, "429") || strings.Contains(errStr, "RESOURCE_EXHAUSTED")
		if !is429 || attempt == maxRetries-1 {
			return nil, err
		}
		backoff := time.Duration(1<<uint(attempt)) * time.Second
		log.Printf("GeminiGenerator: 429 (attempt %d/%d), retrying in %v", attempt+1, maxRetries, backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, fmt.Errorf("unreachable")
}

// OllamaGenerator talks to Ollama/OpenAI-compatible endpoints.
// The genai.Client can't be reused here — it emits Gemini-specific wire format
// (contents/parts JSON + /v1beta/models/{model}:generateContent paths).
// Ollama speaks OpenAI format: /v1/chat/completions with messages/content.
type OllamaGenerator struct {
	Endpoint   string // e.g. http://localhost:11434/v1
	Model      string // e.g. llama3.2
	Ollama     config.OllamaConfig
	HTTPClient *http.Client
}

func (o *OllamaGenerator) GenerateContent(ctx context.Context, model string, history []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	if model == "" {
		model = o.Model
	}

	messages := o.convertHistory(history, config)
	tools := o.convertTools(config)

	reqBody := map[string]any{
		"model":           model,
		"messages":        messages,
		"response_format": map[string]string{"type": "json_object"},
	}
	o.Ollama.Apply(reqBody)
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
				// JSON encode the output to safely escape newlines and raw log formatting
				outputJSON, _ := json.Marshal(output)
				messages = append(messages, map[string]any{
					"role":         "tool",
					"tool_call_id": fr.Name,
					"content":      string(outputJSON),
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

// ---------------------------------------------------------------------------
// GemmaGenerator — local Gemma model with CLI or JSON function calling
// ---------------------------------------------------------------------------

// GemmaGenerator talks to a locally-served Gemma model. Supports two modes:
//
// CLIMode=false (default): JSON function calls parsed via regex.
// CLIMode=true: space-delimited CLI commands (e.g. "act click /browser/btn").
//
// When Grammar is set, it's sent as the "grammar" field in the request body
// for GBNF-constrained decoding. Requires llama.cpp server (supports grammar
// on /v1/chat/completions). Ollama does NOT expose grammar — use llama-server.
type GemmaGenerator struct {
	Endpoint   string // e.g. http://localhost:11434/v1
	Model      string // e.g. gemma3:270m
	Ollama     config.OllamaConfig
	HTTPClient *http.Client
	CLIMode    bool   // use CLI command format instead of JSON
	Grammar    string // GBNF grammar for constrained decoding (optional, set per-call)
}

// jsonBlockRe extracts the outermost {...} block from LLM text output.
// We use regex only for extraction, then json.Unmarshal for actual parsing.
var jsonBlockRe = regexp.MustCompile(`\{[\s\S]*\}`)

// llmFunctionCall is the struct for JSON-based function calls from local models.
// Supports both "parameters" (Gemma) and "arguments" (Qwen) keys.
type llmFunctionCall struct {
	Name       string         `json:"name"`
	Parameters map[string]any `json:"parameters,omitempty"`
	Arguments  map[string]any `json:"arguments,omitempty"`
}

// args returns the merged parameters, preferring "arguments" (Qwen) over "parameters" (Gemma).
func (fc *llmFunctionCall) args() map[string]any {
	if len(fc.Arguments) > 0 {
		return fc.Arguments
	}
	if len(fc.Parameters) > 0 {
		return fc.Parameters
	}
	return map[string]any{}
}

// cliCommands is the set of valid CLI command prefixes.
var cliCommands = map[string]bool{
	"ls": true, "cat": true, "act": true, "grep": true,
	"browser.scroll": true, "browser.goto": true, "browser.rescan": true,
	"browser.list_tabs": true, "browser.switch_tab": true,
	"iterm.new_window": true, "iterm.new_tab": true,
}

// actionGlyphs maps action glyphs to canonical action names.
// The fine-tuned model emits these single-token Unicode symbols instead of
// English verbs to avoid embedding confusion (cos(click,focus)=0.41 in base model).
var glyphToAction = map[string]string{
	"►": "click",
	"⊙": "focus",
	"✎": "type",
	"⏎": "enter",
}

// actionToGlyph is the reverse mapping for output formatting.
var actionToGlyph = map[string]string{
	"click": "►",
	"focus": "⊙",
	"type":  "✎",
	"enter": "⏎",
}

func (g *GemmaGenerator) GenerateContent(ctx context.Context, model string, history []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	if model == "" {
		model = g.Model
	}

	messages := g.convertHistory(history, config)

	reqBody := map[string]any{
		"model":    model,
		"messages": messages,
	}
	g.Ollama.Apply(reqBody)
	if config != nil && config.Temperature != nil {
		reqBody["temperature"] = *config.Temperature
	}
	if g.Grammar != "" {
		reqBody["grammar"] = g.Grammar
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpClient := g.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.Endpoint+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", g.Endpoint+"/chat/completions", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	return g.parseResponse(respBody)
}

// convertHistory builds chat messages with tool definitions embedded in the system prompt.
func (g *GemmaGenerator) convertHistory(history []*genai.Content, config *genai.GenerateContentConfig) []map[string]any {
	var messages []map[string]any

	// Build system prompt: original system instruction + tool definitions.
	var sysParts []string
	if config != nil && config.SystemInstruction != nil {
		for _, part := range config.SystemInstruction.Parts {
			if part.Text != "" {
				sysParts = append(sysParts, part.Text)
			}
		}
	}

	// Embed tool definitions in the system prompt.
	if config != nil && len(config.Tools) > 0 {
		if g.CLIMode {
			sysParts = append(sysParts, buildCLIToolPrompt(config.Tools))
		} else {
			sysParts = append(sysParts, buildJSONToolPrompt(config.Tools))
		}
	}

	if len(sysParts) > 0 {
		messages = append(messages, map[string]any{
			"role":    "system",
			"content": strings.Join(sysParts, "\n\n"),
		})
	}

	// Convert history entries.
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
				var callStr string
				if g.CLIMode {
					callStr = FunctionCallToCLI(fc)
				} else {
					callJSON, _ := json.Marshal(map[string]any{
						"name":       fc.Name,
						"parameters": fc.Args,
					})
					callStr = string(callJSON)
				}
				messages = append(messages, map[string]any{
					"role":    "assistant",
					"content": callStr,
				})
			}
			if part.FunctionResponse != nil {
				fr := part.FunctionResponse
				output, _ := fr.Response["output"].(string)
				outputJSON, _ := json.Marshal(output)
				messages = append(messages, map[string]any{
					"role":    "user",
					"content": fmt.Sprintf("Result of %s: %s", fr.Name, string(outputJSON)),
				})
			}
		}
	}

	return messages
}

// buildJSONToolPrompt formats tool definitions as JSON (original Gemma format).
func buildJSONToolPrompt(tools []*genai.Tool) string {
	var toolDefs []string
	for _, tool := range tools {
		for _, fd := range tool.FunctionDeclarations {
			params := convertSchema(fd.Parameters)
			def, _ := json.Marshal(map[string]any{
				"name":        fd.Name,
				"description": fd.Description,
				"parameters":  params,
			})
			toolDefs = append(toolDefs, string(def))
		}
	}
	return fmt.Sprintf(
		"You have access to the following tools:\n%s\n\n"+
			"When you want to call a tool, respond with ONLY a JSON object in this exact format:\n"+
			`{"name": "tool_name", "parameters": {"param": "value"}}`+"\n\n"+
			"Do not wrap it in markdown. Do not add explanation before or after the JSON. "+
			"If you want to speak to the user (not call a tool), respond with plain text only.",
		strings.Join(toolDefs, "\n"),
	)
}

// buildCLIToolPrompt formats tool definitions as CLI help text.
func buildCLIToolPrompt(tools []*genai.Tool) string {
	var sb strings.Builder
	sb.WriteString("Commands:\n")
	for _, tool := range tools {
		for _, fd := range tool.FunctionDeclarations {
			switch fd.Name {
			case "ls":
				sb.WriteString("  ls <path>\n")
			case "cat":
				sb.WriteString("  cat <path>\n")
			case "grep":
				sb.WriteString("  grep <pattern>\n")
			case "act":
				sb.WriteString("  act <action> <path> [\"payload\"]\n")
				sb.WriteString("    actions: ► (click), ⊙ (focus), ✎ (type), ⏎ (enter)\n")
			case "browser.scroll":
				sb.WriteString("  browser.scroll <down|up>\n")
			case "browser.goto":
				sb.WriteString("  browser.goto <url>\n")
			case "browser.rescan":
				sb.WriteString("  browser.rescan [path]\n")
			case "browser.list_tabs":
				sb.WriteString("  browser.list_tabs\n")
			case "browser.switch_tab":
				sb.WriteString("  browser.switch_tab <id>\n")
			case "iterm.new_window":
				sb.WriteString("  iterm.new_window\n")
			case "iterm.new_tab":
				sb.WriteString("  iterm.new_tab [window_path]\n")
			}
		}
	}
	sb.WriteString("\nRespond with a single command, or plain text to answer questions.")
	return sb.String()
}

// parseResponse extracts function calls from Gemma's text output or returns as plain text.
func (g *GemmaGenerator) parseResponse(body []byte) (*genai.GenerateContentResponse, error) {
	var oaiResp openAIResponse
	if err := json.Unmarshal(body, &oaiResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	if len(oaiResp.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response")
	}

	msg := oaiResp.Choices[0].Message
	content := ""
	if msg.Content != nil {
		content = strings.TrimSpace(*msg.Content)
	}

	// Try CLI parse first (works in both modes — auto-detect).
	if fc := ParseCLICommand(content); fc != nil {
		log.Printf("GemmaGenerator: parsed CLI command %s(%v)", fc.Name, fc.Args)
		return functionCallResponse(fc), nil
	}

	// Fall back to JSON struct unmarshaling — handles all formatting quirks,
	// empty parameters, nested objects, and both "parameters"/"arguments" keys.
	if block := jsonBlockRe.FindString(content); block != "" {
		var fc llmFunctionCall
		if err := json.Unmarshal([]byte(block), &fc); err == nil && fc.Name != "" {
			log.Printf("GemmaGenerator: parsed JSON function call %s(%v)", fc.Name, fc.args())
			return functionCallResponse(&genai.FunctionCall{Name: fc.Name, Args: fc.args()}), nil
		}
	}

	// No function call found — treat as plain text response.
	return &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content: &genai.Content{
				Role:  "model",
				Parts: []*genai.Part{{Text: content}},
			},
		}},
	}, nil
}

func functionCallResponse(fc *genai.FunctionCall) *genai.GenerateContentResponse {
	return &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			Content: &genai.Content{
				Role:  "model",
				Parts: []*genai.Part{{FunctionCall: fc}},
			},
		}},
	}
}

// ---------------------------------------------------------------------------
// CLI ↔ FunctionCall conversion
// ---------------------------------------------------------------------------

// ParseCLICommand parses a CLI command string into a FunctionCall.
// Returns nil if the string is not a recognized command.
//
// Formats:
//
//	ls /path
//	cat /path
//	act click /path
//	act type /path "payload text"
//	browser.scroll down
//	browser.goto https://example.com
//	browser.rescan /path
//	browser.list_tabs
//	browser.switch_tab 3
//	iterm.new_window
//	iterm.new_tab /iterm/windows/0
func ParseCLICommand(s string) *genai.FunctionCall {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}

	// No-arg commands.
	if s == "browser.list_tabs" {
		return &genai.FunctionCall{Name: "browser.list_tabs", Args: map[string]any{}}
	}
	if s == "iterm.new_window" {
		return &genai.FunctionCall{Name: "iterm.new_window", Args: map[string]any{}}
	}

	// Split command name from arguments.
	parts := strings.SplitN(s, " ", 2)
	cmd := parts[0]
	if !cliCommands[cmd] {
		return nil
	}

	rest := ""
	if len(parts) == 2 {
		rest = parts[1]
	}

	switch cmd {
	case "ls":
		if rest == "" {
			return nil
		}
		return &genai.FunctionCall{Name: "ls", Args: map[string]any{"path": rest}}

	case "cat":
		if rest == "" {
			return nil
		}
		return &genai.FunctionCall{Name: "cat", Args: map[string]any{"path": rest}}

	case "grep":
		if rest == "" {
			return nil
		}
		return &genai.FunctionCall{Name: "grep", Args: map[string]any{"pattern": rest}}

	case "act":
		return parseActCLI(rest)

	case "browser.scroll":
		if rest == "" {
			rest = "down"
		}
		return &genai.FunctionCall{Name: "browser.scroll", Args: map[string]any{"direction": rest}}

	case "browser.goto":
		if rest == "" {
			return nil
		}
		return &genai.FunctionCall{Name: "browser.goto", Args: map[string]any{"url": rest}}

	case "browser.rescan":
		args := map[string]any{}
		if rest != "" {
			args["path"] = rest
		}
		return &genai.FunctionCall{Name: "browser.rescan", Args: args}

	case "browser.switch_tab":
		id, err := strconv.Atoi(strings.TrimSpace(rest))
		if err != nil {
			return nil
		}
		return &genai.FunctionCall{Name: "browser.switch_tab", Args: map[string]any{"tab_id": float64(id)}}

	case "iterm.new_tab":
		args := map[string]any{}
		if rest != "" {
			args["window_path"] = rest
		}
		return &genai.FunctionCall{Name: "iterm.new_tab", Args: args}
	}

	return nil
}

// parseActCLI parses the arguments of an act command.
// Formats: "► /path" or "✎ /path \"payload\"" (glyphs) or "click /path" (plain).
func parseActCLI(rest string) *genai.FunctionCall {
	if rest == "" {
		return nil
	}

	// Split: action path [payload]
	parts := strings.SplitN(rest, " ", 2)
	if len(parts) < 2 {
		return nil
	}
	action := parts[0]
	// Translate glyph to canonical action name if present.
	if canonical, ok := glyphToAction[action]; ok {
		action = canonical
	}
	remaining := parts[1]

	// Check for quoted payload: /path "payload"
	if idx := strings.Index(remaining, " \""); idx != -1 {
		path := remaining[:idx]
		payload := remaining[idx+2:]                // skip ' "'
		payload = strings.TrimSuffix(payload, "\"") // strip trailing quote
		return &genai.FunctionCall{
			Name: "act",
			Args: map[string]any{"action": action, "path": path, "payload": payload},
		}
	}

	return &genai.FunctionCall{
		Name: "act",
		Args: map[string]any{"action": action, "path": remaining},
	}
}

// FunctionCallToCLI converts a FunctionCall to CLI command string.
func FunctionCallToCLI(fc *genai.FunctionCall) string {
	getString := func(key string) string {
		v, _ := fc.Args[key].(string)
		return v
	}

	switch fc.Name {
	case "ls":
		return "ls " + getString("path")
	case "cat":
		return "cat " + getString("path")
	case "grep":
		return "grep " + getString("pattern")
	case "act":
		action := getString("action")
		// Emit glyph for CLI-mode history formatting.
		if glyph, ok := actionToGlyph[action]; ok {
			action = glyph
		}
		path := getString("path")
		if payload := getString("payload"); payload != "" {
			return fmt.Sprintf("act %s %s \"%s\"", action, path, payload)
		}
		return fmt.Sprintf("act %s %s", action, path)
	case "browser.scroll":
		return "browser.scroll " + getString("direction")
	case "browser.goto":
		return "browser.goto " + getString("url")
	case "browser.rescan":
		if p := getString("path"); p != "" {
			return "browser.rescan " + p
		}
		return "browser.rescan"
	case "browser.list_tabs":
		return "browser.list_tabs"
	case "browser.switch_tab":
		if id, ok := fc.Args["tab_id"].(float64); ok {
			return fmt.Sprintf("browser.switch_tab %d", int(id))
		}
		return "browser.switch_tab 0"
	case "iterm.new_window":
		return "iterm.new_window"
	case "iterm.new_tab":
		if p := getString("window_path"); p != "" {
			return "iterm.new_tab " + p
		}
		return "iterm.new_tab"
	default:
		// Fallback to JSON for unknown tools.
		data, _ := json.Marshal(map[string]any{"name": fc.Name, "parameters": fc.Args})
		return string(data)
	}
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
