package navigator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jamesgardner/x-ray/internal/mache"
	"google.golang.org/genai"
)

// Compile-time interface checks.
var (
	_ ContentGenerator = (*GeminiGenerator)(nil)
	_ ContentGenerator = (*OllamaGenerator)(nil)
	_ ContentGenerator = (*GemmaGenerator)(nil)
)

// mockGenerator records calls and returns canned responses.
type mockGenerator struct {
	calls    []mockCall
	response *genai.GenerateContentResponse
	err      error

	// responses is a queue; if set, each call pops the front.
	responses []*genai.GenerateContentResponse
}

type mockCall struct {
	Model   string
	History []*genai.Content
}

func (m *mockGenerator) GenerateContent(_ context.Context, model string, history []*genai.Content, _ *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	m.calls = append(m.calls, mockCall{Model: model, History: history})
	if len(m.responses) > 0 {
		resp := m.responses[0]
		m.responses = m.responses[1:]
		return resp, nil
	}
	return m.response, m.err
}

func TestMockGeneratorRecordsCalls(t *testing.T) {
	mock := &mockGenerator{
		response: &genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{{
				Content: &genai.Content{
					Role:  "model",
					Parts: []*genai.Part{{Text: "hello"}},
				},
			}},
		},
	}

	_, err := mock.GenerateContent(context.Background(), "test-model", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mock.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(mock.calls))
	}
	if mock.calls[0].Model != "test-model" {
		t.Errorf("expected model 'test-model', got %q", mock.calls[0].Model)
	}
}

func TestHandleIntentWithMockGenerator(t *testing.T) {
	// Build engine with the test schema from agent_test.go.
	engine := mache.NewEngine()
	if err := engine.ApplySchema(testSchema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	engine.LoadChildren(testSummary, nil)

	// Mock returns: ls("/main/stories") → cat children → act on mache-11 → text confirmation.
	// The agent pre-fills ls("/") so iteration 1 is the first GenerateContent call.
	mock := &mockGenerator{
		responses: []*genai.GenerateContentResponse{
			// Iteration 1: model calls ls("/main/stories")
			{Candidates: []*genai.Candidate{{Content: &genai.Content{
				Role: "model",
				Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
					Name: "ls", Args: map[string]any{"path": "/main/stories"},
				}}},
			}}}},
			// Iteration 2: model calls cat("/main/stories/children")
			{Candidates: []*genai.Candidate{{Content: &genai.Content{
				Role: "model",
				Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
					Name: "cat", Args: map[string]any{"path": "/main/stories/children"},
				}}},
			}}}},
			// Iteration 3: model calls act on the first story (ordinal path)
			{Candidates: []*genai.Candidate{{Content: &genai.Content{
				Role: "model",
				Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
					Name: "act", Args: map[string]any{"path": "/main/stories/_c/1", "action": "click"},
				}}},
			}}}},
		},
	}

	agent := &Agent{generator: mock, model: "test", engine: engine}
	action, _, err := agent.HandleIntent(context.Background(), "click the first story")
	if err != nil {
		t.Fatalf("HandleIntent: %v", err)
	}
	if action == nil {
		t.Fatal("expected action, got nil")
	}
	if action.MacheID != "mache-11" {
		t.Errorf("expected mache-11, got %q", action.MacheID)
	}
	if action.Action != "click" {
		t.Errorf("expected click, got %q", action.Action)
	}

	// The mock should have been called 3 times (ls, cat, act).
	if len(mock.calls) != 3 {
		t.Errorf("expected 3 GenerateContent calls, got %d", len(mock.calls))
	}
}

func TestHandleIntentTextResponse(t *testing.T) {
	engine := mache.NewEngine()
	if err := engine.ApplySchema(testSchema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	engine.LoadChildren(testSummary, nil)

	mock := &mockGenerator{
		response: &genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{{Content: &genai.Content{
				Role:  "model",
				Parts: []*genai.Part{{Text: "I found the navigation bar at the top."}},
			}}},
		},
	}

	agent := &Agent{generator: mock, model: "test", engine: engine}
	action, text, err := agent.HandleIntent(context.Background(), "where is the nav?")
	if err != nil {
		t.Fatalf("HandleIntent: %v", err)
	}
	if action != nil {
		t.Error("expected no action for text response")
	}
	if !strings.Contains(text, "navigation bar") {
		t.Errorf("expected text about navigation, got %q", text)
	}
}

func TestOllamaGeneratorConversion(t *testing.T) {
	// Stand up a fake OpenAI-compatible server.
	var gotRequest map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { _ = r.Body.Close() }()
		_ = json.NewDecoder(r.Body).Decode(&gotRequest)

		resp := map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": "I'll help with that.",
				},
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	gen := &OllamaGenerator{
		Endpoint:   server.URL,
		Model:      "test-model",
		HTTPClient: server.Client(),
	}

	history := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{{Text: "hello"}}},
	}
	config := &genai.GenerateContentConfig{
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{{Text: "You are a helper."}},
		},
		Tools:       ToolDefinitions(),
		Temperature: genai.Ptr(float32(0.1)),
	}

	resp, err := gen.GenerateContent(context.Background(), "", history, config)
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}

	// Verify response was parsed correctly.
	if len(resp.Candidates) == 0 {
		t.Fatal("no candidates")
	}
	text := resp.Candidates[0].Content.Parts[0].Text
	if text != "I'll help with that." {
		t.Errorf("unexpected text: %q", text)
	}

	// Verify the request was formed correctly.
	model, _ := gotRequest["model"].(string)
	if model != "test-model" {
		t.Errorf("expected model 'test-model', got %q", model)
	}

	messages, _ := gotRequest["messages"].([]any)
	if len(messages) < 2 {
		t.Fatalf("expected at least 2 messages (system + user), got %d", len(messages))
	}

	// First message should be system.
	sysMsg, _ := messages[0].(map[string]any)
	if sysMsg["role"] != "system" {
		t.Errorf("first message should be system, got %q", sysMsg["role"])
	}

	// Tools should be present.
	tools, _ := gotRequest["tools"].([]any)
	if len(tools) == 0 {
		t.Error("expected tools in request")
	}
}

func TestOllamaGeneratorToolCallResponse(t *testing.T) {
	// Fake server returns a tool call.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": nil,
					"tool_calls": []map[string]any{{
						"id":   "call_1",
						"type": "function",
						"function": map[string]any{
							"name":      "ls",
							"arguments": `{"path":"/"}`,
						},
					}},
				},
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	gen := &OllamaGenerator{
		Endpoint:   server.URL,
		Model:      "test",
		HTTPClient: server.Client(),
	}

	resp, err := gen.GenerateContent(context.Background(), "", []*genai.Content{
		{Role: "user", Parts: []*genai.Part{{Text: "list root"}}},
	}, nil)
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}

	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		t.Fatal("no candidate content")
	}

	part := resp.Candidates[0].Content.Parts[0]
	if part.FunctionCall == nil {
		t.Fatal("expected FunctionCall part")
	}
	if part.FunctionCall.Name != "ls" {
		t.Errorf("expected tool name 'ls', got %q", part.FunctionCall.Name)
	}
	path, _ := part.FunctionCall.Args["path"].(string)
	if path != "/" {
		t.Errorf("expected path '/', got %q", path)
	}
}

// --- GemmaGenerator tests ---

func TestGemmaGeneratorParsesFunctionCall(t *testing.T) {
	// Fake server returns a text response containing a Gemma-style function call.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { _ = r.Body.Close() }()
		content := `{"name": "ls", "parameters": {"path": "/"}}`
		resp := map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": content,
				},
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	gen := &GemmaGenerator{
		Endpoint:   server.URL,
		Model:      "gemma3",
		HTTPClient: server.Client(),
	}

	resp, err := gen.GenerateContent(context.Background(), "", []*genai.Content{
		{Role: "user", Parts: []*genai.Part{{Text: "list root"}}},
	}, &genai.GenerateContentConfig{Tools: ToolDefinitions()})
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}

	part := resp.Candidates[0].Content.Parts[0]
	if part.FunctionCall == nil {
		t.Fatal("expected FunctionCall, got text")
	}
	if part.FunctionCall.Name != "ls" {
		t.Errorf("expected name 'ls', got %q", part.FunctionCall.Name)
	}
	path, _ := part.FunctionCall.Args["path"].(string)
	if path != "/" {
		t.Errorf("expected path '/', got %q", path)
	}
}

func TestGemmaGeneratorParsesTextResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { _ = r.Body.Close() }()
		resp := map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": "Done, I clicked the first story.",
				},
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	gen := &GemmaGenerator{
		Endpoint:   server.URL,
		Model:      "gemma3",
		HTTPClient: server.Client(),
	}

	resp, err := gen.GenerateContent(context.Background(), "", []*genai.Content{
		{Role: "user", Parts: []*genai.Part{{Text: "what did you do?"}}},
	}, nil)
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}

	part := resp.Candidates[0].Content.Parts[0]
	if part.FunctionCall != nil {
		t.Error("expected text response, got FunctionCall")
	}
	if !strings.Contains(part.Text, "clicked the first story") {
		t.Errorf("unexpected text: %q", part.Text)
	}
}

func TestGemmaGeneratorEmbedsToolsInPrompt(t *testing.T) {
	var gotRequest map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { _ = r.Body.Close() }()
		_ = json.NewDecoder(r.Body).Decode(&gotRequest)
		resp := map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "ok"},
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	gen := &GemmaGenerator{
		Endpoint:   server.URL,
		Model:      "gemma3",
		HTTPClient: server.Client(),
	}

	config := &genai.GenerateContentConfig{
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{{Text: "You are a navigator."}},
		},
		Tools: ToolDefinitions(),
	}

	_, err := gen.GenerateContent(context.Background(), "", []*genai.Content{
		{Role: "user", Parts: []*genai.Part{{Text: "hi"}}},
	}, config)
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}

	// Verify: no "tools" key in request (Gemma embeds in system prompt).
	if _, hasTools := gotRequest["tools"]; hasTools {
		t.Error("GemmaGenerator should NOT send 'tools' parameter — definitions go in system prompt")
	}

	// Verify: system message contains tool definitions.
	messages, _ := gotRequest["messages"].([]any)
	if len(messages) == 0 {
		t.Fatal("no messages in request")
	}
	sysMsg, _ := messages[0].(map[string]any)
	sysContent, _ := sysMsg["content"].(string)
	if !strings.Contains(sysContent, `"ls"`) {
		t.Errorf("system prompt should contain ls tool definition, got: %s", sysContent)
	}
	if !strings.Contains(sysContent, `"act"`) {
		t.Errorf("system prompt should contain act tool definition, got: %s", sysContent)
	}
}

func TestGemmaGeneratorMultiTurnHistory(t *testing.T) {
	var gotRequest map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { _ = r.Body.Close() }()
		_ = json.NewDecoder(r.Body).Decode(&gotRequest)
		resp := map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": `{"name": "act", "parameters": {"path": "/main/stories/_c/1", "action": "click"}}`,
				},
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	gen := &GemmaGenerator{
		Endpoint:   server.URL,
		Model:      "gemma3",
		HTTPClient: server.Client(),
	}

	// Simulate mid-loop history: user → model called ls → result → now what?
	history := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{{Text: "click the first story"}}},
		{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			Name: "ls", Args: map[string]any{"path": "/"},
		}}}},
		{Role: "user", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
			Name:     "ls",
			Response: map[string]any{"output": "header/\nmain/\nfooter/"},
		}}}},
	}

	resp, err := gen.GenerateContent(context.Background(), "", history, &genai.GenerateContentConfig{
		Tools: ToolDefinitions(),
	})
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}

	// Should parse the act() call from response.
	part := resp.Candidates[0].Content.Parts[0]
	if part.FunctionCall == nil {
		t.Fatal("expected FunctionCall")
	}
	if part.FunctionCall.Name != "act" {
		t.Errorf("expected 'act', got %q", part.FunctionCall.Name)
	}

	// Verify history was converted: function call → assistant JSON, function response → user result.
	messages, _ := gotRequest["messages"].([]any)
	var roles []string
	for _, m := range messages {
		msg, _ := m.(map[string]any)
		roles = append(roles, msg["role"].(string))
	}
	// system, user, assistant (ls call as JSON), user (result), should be 4 messages
	if len(messages) < 4 {
		t.Errorf("expected at least 4 messages, got %d: %v", len(messages), roles)
	}
}

// --- Live Ollama integration tests ---
// Skipped unless OLLAMA_TEST=1 is set. Requires `ollama serve` running locally.
//
// Run with:
//   OLLAMA_TEST=1 go test -v -run TestOllamaIntegration ./internal/navigator/...
//
// Optional env vars:
//   NAVIGATOR_ENDPOINT  (default: http://localhost:11434/v1)
//   NAVIGATOR_MODEL     (default: llama3.2)

func ollamaEndpoint() string {
	if ep := os.Getenv("NAVIGATOR_ENDPOINT"); ep != "" {
		return ep
	}
	return "http://localhost:11434/v1"
}

func ollamaModel() string {
	if m := os.Getenv("NAVIGATOR_MODEL"); m != "" {
		return m
	}
	return "llama3.2"
}

func skipUnlessOllama(t *testing.T) {
	t.Helper()
	if os.Getenv("OLLAMA_TEST") == "" {
		t.Skip("OLLAMA_TEST not set — skipping live Ollama test")
	}
}

func TestOllamaIntegrationSingleToolCall(t *testing.T) {
	skipUnlessOllama(t)

	gen := &OllamaGenerator{
		Endpoint: ollamaEndpoint(),
		Model:    ollamaModel(),
	}

	config := &genai.GenerateContentConfig{
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{{Text: "You have a tool called ls(path) that lists directory contents. When asked to explore, call ls with path '/'. Only call the tool, do not respond with text."}},
		},
		Tools:       ToolDefinitions(),
		Temperature: genai.Ptr(float32(0.1)),
	}

	history := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{{Text: "List the root directory."}}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := gen.GenerateContent(ctx, "", history, config)
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}

	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		t.Fatal("no candidate content")
	}

	parts := resp.Candidates[0].Content.Parts
	t.Logf("Response parts: %d", len(parts))
	for i, p := range parts {
		if p.Text != "" {
			t.Logf("  part[%d] text: %q", i, p.Text)
		}
		if p.FunctionCall != nil {
			t.Logf("  part[%d] tool_call: %s(%v)", i, p.FunctionCall.Name, p.FunctionCall.Args)
		}
	}

	// We expect at least one function call to ls.
	var foundLS bool
	for _, p := range parts {
		if p.FunctionCall != nil && p.FunctionCall.Name == "ls" {
			foundLS = true
			break
		}
	}
	if !foundLS {
		t.Error("expected model to call ls(), but it didn't — model may not support tool_calls")
	}
}

func TestOllamaIntegrationMultiTurn(t *testing.T) {
	skipUnlessOllama(t)

	// Full ls → cat → act loop driven by the real model against our HN fixture.
	engine := mache.NewEngine()
	if err := engine.ApplySchema(hnSchema); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	engine.LoadChildren(hnSummary, nil)

	gen := &OllamaGenerator{
		Endpoint: ollamaEndpoint(),
		Model:    ollamaModel(),
	}

	agent := &Agent{generator: gen, model: ollamaModel(), engine: engine}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	t.Log("Sending intent: 'click the first story'")
	action, text, err := agent.HandleIntent(ctx, "click the first story")
	if err != nil {
		t.Fatalf("HandleIntent: %v", err)
	}

	if action != nil {
		t.Logf("Action: %s on %s (path: %s)", action.Action, action.MacheID, action.Path)
		// Verify it clicked something in the story list zone.
		if !strings.HasPrefix(action.Path, "/main/story_list") {
			t.Errorf("expected action in /main/story_list, got path %q", action.Path)
		}
	} else if text != "" {
		t.Logf("Text response (no action): %s", text)
		t.Error("expected an action result, got text — model may not be following tool-use pattern")
	} else {
		t.Error("got neither action nor text")
	}
}

func TestOllamaIntegrationFunctionResponseRoundTrip(t *testing.T) {
	skipUnlessOllama(t)

	// Test that we can send a tool call result back and get a coherent follow-up.
	gen := &OllamaGenerator{
		Endpoint: ollamaEndpoint(),
		Model:    ollamaModel(),
	}

	config := &genai.GenerateContentConfig{
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{{Text: NavigatorSystemPrompt}},
		},
		Tools:       ToolDefinitions(),
		Temperature: genai.Ptr(float32(0.1)),
	}

	// Simulate: user asks → model called ls("/") → we return result → model should continue.
	history := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{{Text: "click the first story"}}},
		{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			Name: "ls", Args: map[string]any{"path": "/"},
		}}}},
		{Role: "user", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
			Name:     "ls",
			Response: map[string]any{"output": "header/\nmain/\nfooter/"},
		}}}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := gen.GenerateContent(ctx, "", history, config)
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}

	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		t.Fatal("no candidate content")
	}

	parts := resp.Candidates[0].Content.Parts
	t.Logf("After tool response, model returned %d parts:", len(parts))
	for i, p := range parts {
		if p.Text != "" {
			t.Logf("  part[%d] text: %q", i, p.Text)
		}
		if p.FunctionCall != nil {
			t.Logf("  part[%d] tool_call: %s(%v)", i, p.FunctionCall.Name, p.FunctionCall.Args)
		}
	}

	// Model should either call another tool (ls on a zone) or produce text.
	// Either is valid — we just want to confirm the round-trip didn't error.
	hasContent := false
	for _, p := range parts {
		if p.Text != "" || p.FunctionCall != nil {
			hasContent = true
			break
		}
	}
	if !hasContent {
		t.Error("model returned empty parts after function response")
	}
}

func TestOllamaIntegrationToolFormatDiagnostic(t *testing.T) {
	skipUnlessOllama(t)

	// Diagnostic test: logs the raw HTTP response from Ollama so we can inspect
	// tool_call format quirks. Not a pass/fail test — purely informational.
	gen := &OllamaGenerator{
		Endpoint: ollamaEndpoint(),
		Model:    ollamaModel(),
	}

	// Make a raw HTTP request to see exactly what Ollama returns.
	tools := gen.convertTools(&genai.GenerateContentConfig{Tools: ToolDefinitions()})
	reqBody := map[string]any{
		"model": ollamaModel(),
		"messages": []map[string]any{
			{"role": "system", "content": "You navigate a filesystem. Call ls(path) to list contents. Always start with ls('/')."},
			{"role": "user", "content": "Show me what's at the root."},
		},
		"tools":       tools,
		"temperature": 0.1,
	}

	body, _ := json.Marshal(reqBody)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "POST", ollamaEndpoint()+"/chat/completions", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HTTP request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var raw json.RawMessage
	_ = json.NewDecoder(resp.Body).Decode(&raw)

	formatted, _ := json.MarshalIndent(raw, "", "  ")
	fmt.Fprintf(os.Stderr, "\n=== Raw Ollama response ===\n%s\n===========================\n", string(formatted))

	t.Logf("HTTP %d, response logged to stderr", resp.StatusCode)
}
