package navigator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/genai"
)

// --- ParseCLICommand tests ---

func TestParseCLICommand(t *testing.T) {
	tests := []struct {
		input    string
		wantName string
		wantArgs map[string]any
	}{
		{"ls /", "ls", map[string]any{"path": "/"}},
		{"ls /browser/header", "ls", map[string]any{"path": "/browser/header"}},
		{"cat /browser/main/description", "cat", map[string]any{"path": "/browser/main/description"}},
		// Glyph format (fine-tuned model output)
		{"act ► /browser/main/feed/_c/3", "act", map[string]any{"action": "click", "path": "/browser/main/feed/_c/3"}},
		{"act ⊙ /aws/rds/prod-db-4/", "act", map[string]any{"action": "focus", "path": "/aws/rds/prod-db-4/"}},
		{`act ✎ /iterm/sessions/1 "git status"`, "act", map[string]any{"action": "type", "path": "/iterm/sessions/1", "payload": "git status"}},
		{"act ⏎ /iterm/sessions/1", "act", map[string]any{"action": "enter", "path": "/iterm/sessions/1"}},
		// Plain text format (backward compat)
		{"act click /browser/main/feed/_c/3", "act", map[string]any{"action": "click", "path": "/browser/main/feed/_c/3"}},
		{"act focus /aws/rds/prod-db-4/", "act", map[string]any{"action": "focus", "path": "/aws/rds/prod-db-4/"}},
		{`act type /iterm/sessions/1 "git status"`, "act", map[string]any{"action": "type", "path": "/iterm/sessions/1", "payload": "git status"}},
		{"browser.scroll down", "browser.scroll", map[string]any{"direction": "down"}},
		{"browser.scroll up", "browser.scroll", map[string]any{"direction": "up"}},
		{"browser.goto https://github.com", "browser.goto", map[string]any{"url": "https://github.com"}},
		{"browser.rescan /browser/main", "browser.rescan", map[string]any{"path": "/browser/main"}},
		{"browser.rescan", "browser.rescan", map[string]any{}},
		{"browser.list_tabs", "browser.list_tabs", map[string]any{}},
		{"browser.switch_tab 3", "browser.switch_tab", map[string]any{"tab_id": float64(3)}},
		{"iterm.new_window", "iterm.new_window", map[string]any{}},
		{"iterm.new_tab /iterm/windows/0", "iterm.new_tab", map[string]any{"window_path": "/iterm/windows/0"}},
		{"iterm.new_tab", "iterm.new_tab", map[string]any{}},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			fc := ParseCLICommand(tt.input)
			if fc == nil {
				t.Fatalf("ParseCLICommand(%q) returned nil", tt.input)
			}
			if fc.Name != tt.wantName {
				t.Errorf("name: got %q, want %q", fc.Name, tt.wantName)
			}
			for k, want := range tt.wantArgs {
				got, ok := fc.Args[k]
				if !ok {
					t.Errorf("missing arg %q", k)
					continue
				}
				if got != want {
					t.Errorf("arg %q: got %v (%T), want %v (%T)", k, got, got, want, want)
				}
			}
		})
	}
}

func TestParseCLICommandRejectsNonCommands(t *testing.T) {
	nonCommands := []string{
		"",
		"hello world",
		"I found the button at /browser/main.",
		"The search results show 3 items.",
		`{"name": "ls", "parameters": {"path": "/"}}`, // JSON is not CLI
	}
	for _, s := range nonCommands {
		if fc := ParseCLICommand(s); fc != nil {
			t.Errorf("ParseCLICommand(%q) should return nil, got %s(%v)", s, fc.Name, fc.Args)
		}
	}
}

func TestParseCLICommandWithWhitespace(t *testing.T) {
	fc := ParseCLICommand("  ls /browser/header  ")
	if fc == nil {
		t.Fatal("expected non-nil")
	}
	if fc.Name != "ls" {
		t.Errorf("name: got %q, want ls", fc.Name)
	}
}

// --- FunctionCallToCLI tests ---

func TestFunctionCallToCLI(t *testing.T) {
	tests := []struct {
		fc   *genai.FunctionCall
		want string
	}{
		{
			&genai.FunctionCall{Name: "ls", Args: map[string]any{"path": "/"}},
			"ls /",
		},
		{
			&genai.FunctionCall{Name: "cat", Args: map[string]any{"path": "/browser/main/description"}},
			"cat /browser/main/description",
		},
		{
			&genai.FunctionCall{Name: "act", Args: map[string]any{"action": "click", "path": "/browser/main/_c/1"}},
			"act ► /browser/main/_c/1",
		},
		{
			&genai.FunctionCall{Name: "act", Args: map[string]any{"action": "type", "path": "/iterm/s/1", "payload": "make run"}},
			`act ✎ /iterm/s/1 "make run"`,
		},
		{
			&genai.FunctionCall{Name: "browser.scroll", Args: map[string]any{"direction": "down"}},
			"browser.scroll down",
		},
		{
			&genai.FunctionCall{Name: "browser.goto", Args: map[string]any{"url": "https://github.com"}},
			"browser.goto https://github.com",
		},
		{
			&genai.FunctionCall{Name: "browser.list_tabs", Args: map[string]any{}},
			"browser.list_tabs",
		},
		{
			&genai.FunctionCall{Name: "browser.switch_tab", Args: map[string]any{"tab_id": float64(5)}},
			"browser.switch_tab 5",
		},
		{
			&genai.FunctionCall{Name: "iterm.new_window", Args: map[string]any{}},
			"iterm.new_window",
		},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := FunctionCallToCLI(tt.fc)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCLIRoundTrip(t *testing.T) {
	// CLI string → ParseCLICommand → FunctionCallToCLI → same string.
	cmds := []string{
		"ls /",
		"ls /browser/header",
		"cat /browser/main/description",
		"act ► /browser/main/_c/1",
		"browser.scroll down",
		"browser.goto https://github.com",
		"browser.list_tabs",
		"browser.switch_tab 3",
		"iterm.new_window",
		"iterm.new_tab /iterm/windows/0",
	}
	for _, cmd := range cmds {
		fc := ParseCLICommand(cmd)
		if fc == nil {
			t.Fatalf("ParseCLICommand(%q) returned nil", cmd)
		}
		got := FunctionCallToCLI(fc)
		if got != cmd {
			t.Errorf("round-trip failed: %q → %q", cmd, got)
		}
	}
}

// --- GemmaGenerator CLI mode tests ---

func TestGemmaGeneratorCLIModeParsesCommand(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { _ = r.Body.Close() }()
		resp := map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{
					"role":    "assistant",
					"content": "act ► /browser/main/feed/_c/3",
				},
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	gen := &GemmaGenerator{
		Endpoint:   server.URL,
		Model:      "test",
		HTTPClient: server.Client(),
		CLIMode:    true,
	}

	resp, err := gen.GenerateContent(context.Background(), "", []*genai.Content{
		{Role: "user", Parts: []*genai.Part{{Text: "click post 3"}}},
	}, nil)
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}

	part := resp.Candidates[0].Content.Parts[0]
	if part.FunctionCall == nil {
		t.Fatal("expected FunctionCall from CLI parse")
	}
	if part.FunctionCall.Name != "act" {
		t.Errorf("name: got %q, want act", part.FunctionCall.Name)
	}
	action, _ := part.FunctionCall.Args["action"].(string)
	if action != "click" {
		t.Errorf("action: got %q, want click", action)
	}
	path, _ := part.FunctionCall.Args["path"].(string)
	if path != "/browser/main/feed/_c/3" {
		t.Errorf("path: got %q, want /browser/main/feed/_c/3", path)
	}
}

func TestGemmaGeneratorCLIModeEmbedsCLIToolDefs(t *testing.T) {
	var gotRequest map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { _ = r.Body.Close() }()
		_ = json.NewDecoder(r.Body).Decode(&gotRequest)
		resp := map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "ls /"},
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	gen := &GemmaGenerator{
		Endpoint:   server.URL,
		Model:      "test",
		HTTPClient: server.Client(),
		CLIMode:    true,
	}

	config := &genai.GenerateContentConfig{
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{{Text: "You are a navigator."}},
		},
		Tools: testToolDefs(),
	}

	_, err := gen.GenerateContent(context.Background(), "", []*genai.Content{
		{Role: "user", Parts: []*genai.Part{{Text: "hi"}}},
	}, config)
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}

	messages, _ := gotRequest["messages"].([]any)
	sysMsg, _ := messages[0].(map[string]any)
	sysContent, _ := sysMsg["content"].(string)

	// CLI mode should embed CLI syntax, not JSON schemas.
	if !strings.Contains(sysContent, "ls <path>") {
		t.Error("system prompt should contain CLI syntax 'ls <path>'")
	}
	if !strings.Contains(sysContent, "act <action> <path>") {
		t.Error("system prompt should contain 'act <action> <path>'")
	}
	// Should NOT contain JSON tool definitions.
	if strings.Contains(sysContent, `"parameters"`) {
		t.Error("CLI mode should not have JSON tool schemas in system prompt")
	}
}

func TestGemmaGeneratorCLIModeSendsGrammar(t *testing.T) {
	var gotRequest map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { _ = r.Body.Close() }()
		_ = json.NewDecoder(r.Body).Decode(&gotRequest)
		resp := map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "ls /"},
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	gen := &GemmaGenerator{
		Endpoint:   server.URL,
		Model:      "test",
		HTTPClient: server.Client(),
		CLIMode:    true,
		Grammar:    "root ::= \"ls /\"\n",
	}

	_, err := gen.GenerateContent(context.Background(), "", []*genai.Content{
		{Role: "user", Parts: []*genai.Part{{Text: "list"}}},
	}, nil)
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}

	grammar, ok := gotRequest["grammar"].(string)
	if !ok || grammar == "" {
		t.Error("expected grammar field in request body")
	}
	if !strings.Contains(grammar, "ls /") {
		t.Errorf("grammar should contain 'ls /', got %q", grammar)
	}
}

func TestGemmaGeneratorCLIModeHistoryFormat(t *testing.T) {
	var gotRequest map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { _ = r.Body.Close() }()
		_ = json.NewDecoder(r.Body).Decode(&gotRequest)
		resp := map[string]any{
			"choices": []map[string]any{{
				"message": map[string]any{"role": "assistant", "content": "act ► /main/_c/1"},
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	gen := &GemmaGenerator{
		Endpoint:   server.URL,
		Model:      "test",
		HTTPClient: server.Client(),
		CLIMode:    true,
	}

	history := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{{Text: "click story"}}},
		{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			Name: "ls", Args: map[string]any{"path": "/"},
		}}}},
		{Role: "user", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
			Name:     "ls",
			Response: map[string]any{"output": "main/\nheader/"},
		}}}},
	}

	_, err := gen.GenerateContent(context.Background(), "", history, nil)
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}

	messages, _ := gotRequest["messages"].([]any)
	// Find the assistant message — should be CLI format, not JSON.
	for _, m := range messages {
		msg, _ := m.(map[string]any)
		if msg["role"] == "assistant" {
			content, _ := msg["content"].(string)
			if content != "ls /" {
				t.Errorf("assistant history should be CLI 'ls /', got %q", content)
			}
		}
	}
}

// --- BuildGBNF tests ---

func TestBuildGBNFContainsPaths(t *testing.T) {
	paths := []string{"/", "/browser", "/browser/header", "/browser/main/_c/1"}
	grammar := BuildGBNF(paths, false)

	for _, p := range paths {
		if !strings.Contains(grammar, `"`+p+`"`) {
			t.Errorf("grammar should contain path %q", p)
		}
	}

	if !strings.Contains(grammar, "act-cmd") {
		t.Error("grammar should include act-cmd when excludeAct=false")
	}
}

func TestBuildGBNFExcludesAct(t *testing.T) {
	grammar := BuildGBNF([]string{"/"}, true)

	if strings.Contains(grammar, "act-cmd") {
		t.Error("grammar should NOT include act-cmd when excludeAct=true")
	}
	if !strings.Contains(grammar, "ls-cmd") {
		t.Error("grammar should still include ls-cmd")
	}
}

func TestBuildGBNFEmptyPathsFallback(t *testing.T) {
	grammar := BuildGBNF(nil, false)

	// Should have unconstrained path rule.
	if !strings.Contains(grammar, "[a-zA-Z0-9_/.]*") {
		t.Error("empty paths should produce unconstrained path rule")
	}
}

func TestBuildGBNFStructure(t *testing.T) {
	grammar := BuildGBNF([]string{"/", "/browser"}, false)

	required := []string{
		"root ::= tool-call",
		"ls-cmd", "cat-cmd", "act-cmd",
		"scroll-cmd", "goto-cmd",
		"action ::=",
		"valid-path ::=",
	}
	for _, r := range required {
		if !strings.Contains(grammar, r) {
			t.Errorf("grammar missing %q", r)
		}
	}
}
