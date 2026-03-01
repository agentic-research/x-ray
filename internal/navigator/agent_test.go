package navigator

import (
	"context"
	"strings"
	"testing"

	"github.com/agentic-research/x-ray/internal/mache"
	"google.golang.org/genai"
)

const testSchema = `{
  "mounts": [
    {
      "virtual_path": "/header/nav",
      "mache_id": "mache-0",
      "description": "Top navigation bar"
    },
    {
      "virtual_path": "/main/stories",
      "mache_id": "mache-10",
      "description": "Main story listing",
      "primary_items": ["mache-11", "mache-13"]
    },
    {
      "virtual_path": "/footer/links",
      "mache_id": "mache-50",
      "description": "Footer navigation"
    }
  ]
}`

const testSummary = `Interactive Elements:
ID: mache-0 | Parent: none | Tag: nav | Text: "Navigation"
ID: mache-1 | Parent: mache-0 | Tag: a | Text: "Home"
ID: mache-2 | Parent: mache-0 | Tag: a | Text: "About"
ID: mache-10 | Parent: none | Tag: div | Text: "Stories"
ID: mache-11 | Parent: mache-10 | Tag: a | Text: "First Story Title"
ID: mache-12 | Parent: mache-10 | Tag: span | Text: "(example.com)"
ID: mache-13 | Parent: mache-10 | Tag: a | Text: "Second Story"
ID: mache-14 | Parent: mache-10 | Tag: span | Text: "(other.com)"
ID: mache-50 | Parent: none | Tag: footer | Text: "Footer"
`

func newTestAgent() *Agent {
	engine := mache.NewEngine()
	if err := engine.ApplySchema(testSchema); err != nil {
		panic(err)
	}
	engine.LoadChildren(testSummary, nil)
	return NewAgent(nil, "test", engine)
}

func TestExecuteToolLs(t *testing.T) {
	agent := newTestAgent()
	fc := &genai.FunctionCall{Name: "ls", Args: map[string]any{"path": "/"}}

	result, action := agent.ExecuteTool(context.Background(), fc)
	if action != nil {
		t.Fatal("ls should not return an action")
	}
	// Root should list the three zone parent dirs
	for _, want := range []string{"header/", "main/", "footer/"} {
		if !strings.Contains(result, want) {
			t.Errorf("ls(\"/\") missing %q in result: %s", want, result)
		}
	}
}

func TestExecuteToolLsSubdir(t *testing.T) {
	agent := newTestAgent()
	fc := &genai.FunctionCall{Name: "ls", Args: map[string]any{"path": "/main/stories"}}

	result, action := agent.ExecuteTool(context.Background(), fc)
	if action != nil {
		t.Fatal("ls should not return an action")
	}
	// Zone dir should have _c/, children, description, mache_id
	for _, want := range []string{"_c/", "children", "description", "mache_id"} {
		if !strings.Contains(result, want) {
			t.Errorf("ls zone missing %q in result: %s", want, result)
		}
	}
}

func TestExecuteToolCat(t *testing.T) {
	agent := newTestAgent()
	fc := &genai.FunctionCall{Name: "cat", Args: map[string]any{"path": "/header/nav/description"}}

	result, action := agent.ExecuteTool(context.Background(), fc)
	if action != nil {
		t.Fatal("cat should not return an action")
	}
	if result != "Top navigation bar" {
		t.Errorf("unexpected description content: %q", result)
	}
}

func TestExecuteToolCatChildren(t *testing.T) {
	agent := newTestAgent()
	fc := &genai.FunctionCall{Name: "cat", Args: map[string]any{"path": "/main/stories/children"}}

	result, action := agent.ExecuteTool(context.Background(), fc)
	if action != nil {
		t.Fatal("cat should not return an action")
	}
	// Children file should list primary items in compact format
	if !strings.Contains(result, "[1]") {
		t.Errorf("children file missing [1]: %s", result)
	}
	if !strings.Contains(result, "First Story Title") {
		t.Errorf("children file missing story title: %s", result)
	}
	if !strings.Contains(result, "[2]") {
		t.Errorf("children file missing [2]: %s", result)
	}
}

func TestExecuteToolAct(t *testing.T) {
	agent := newTestAgent()
	// Ordinal path: _c/1 is the first primary item (mache-11)
	fc := &genai.FunctionCall{Name: "act", Args: map[string]any{
		"path":   "/main/stories/_c/1",
		"action": "click",
	}}

	result, action := agent.ExecuteTool(context.Background(), fc)
	if action == nil {
		t.Fatal("act should return an ActionResult")
	}
	if action.MacheID != "mache-11" {
		t.Errorf("expected MacheID mache-11, got %q", action.MacheID)
	}
	if action.Action != "click" {
		t.Errorf("expected action click, got %q", action.Action)
	}
	if !strings.Contains(result, "mache-11") {
		t.Errorf("result should mention mache-11: %s", result)
	}
}

func TestExecuteToolActDefaultAction(t *testing.T) {
	agent := newTestAgent()
	// Ordinal path: _c/2 is the second primary item (mache-13)
	fc := &genai.FunctionCall{Name: "act", Args: map[string]any{
		"path":   "/main/stories/_c/2",
		"action": "",
	}}

	_, action := agent.ExecuteTool(context.Background(), fc)
	if action == nil {
		t.Fatal("act should return an ActionResult")
	}
	if action.Action != "click" {
		t.Errorf("empty action should default to click, got %q", action.Action)
	}
}

func TestExecuteToolActBadPath(t *testing.T) {
	agent := newTestAgent()
	fc := &genai.FunctionCall{Name: "act", Args: map[string]any{
		"path":   "/nonexistent/thing",
		"action": "click",
	}}

	result, action := agent.ExecuteTool(context.Background(), fc)
	if action != nil {
		t.Fatal("bad path should not return an action")
	}
	if !strings.Contains(result, "Error") {
		t.Errorf("expected error string, got %q", result)
	}
}

func TestExecuteToolUnknown(t *testing.T) {
	agent := newTestAgent()
	fc := &genai.FunctionCall{Name: "delete", Args: map[string]any{"path": "/"}}

	result, action := agent.ExecuteTool(context.Background(), fc)
	if action != nil {
		t.Fatal("unknown tool should not return an action")
	}
	if !strings.Contains(result, "Unknown tool") {
		t.Errorf("expected 'Unknown tool' message, got %q", result)
	}
}

func TestExecuteToolScrollNoFunc(t *testing.T) {
	agent := newTestAgent()
	fc := &genai.FunctionCall{Name: "browser.scroll", Args: map[string]any{"direction": "down"}}

	result, action := agent.ExecuteTool(context.Background(), fc)
	if action != nil {
		t.Fatal("scroll should not return an action")
	}
	if !strings.Contains(result, "not available") {
		t.Errorf("expected 'not available' error, got %q", result)
	}
}

func TestExecuteToolScrollWithFunc(t *testing.T) {
	agent := newTestAgent()
	scrollCalled := false
	agent.SetScrollFunc(func(_ context.Context, direction string) error {
		scrollCalled = true
		if direction != "down" {
			t.Errorf("expected direction 'down', got %q", direction)
		}
		return nil
	})

	fc := &genai.FunctionCall{Name: "browser.scroll", Args: map[string]any{"direction": "down"}}
	result, action := agent.ExecuteTool(context.Background(), fc)
	if action != nil {
		t.Fatal("scroll should not return an action")
	}
	if !scrollCalled {
		t.Error("scroll function was not called")
	}
	if !strings.Contains(result, "Scrolled down") {
		t.Errorf("expected 'Scrolled down' message, got %q", result)
	}
}

func TestExecuteToolScrollDefaultDirection(t *testing.T) {
	agent := newTestAgent()
	var gotDirection string
	agent.SetScrollFunc(func(_ context.Context, direction string) error {
		gotDirection = direction
		return nil
	})

	fc := &genai.FunctionCall{Name: "browser.scroll", Args: map[string]any{}}
	agent.ExecuteTool(context.Background(), fc)
	if gotDirection != "down" {
		t.Errorf("expected default direction 'down', got %q", gotDirection)
	}
}

func TestExecuteToolActType(t *testing.T) {
	agent := newTestAgent()
	fc := &genai.FunctionCall{Name: "act", Args: map[string]any{
		"path":    "/main/stories/_c/1",
		"action":  "type",
		"payload": "hello world",
	}}

	result, action := agent.ExecuteTool(context.Background(), fc)
	if action == nil {
		t.Fatal("act type should return an ActionResult")
	}
	if action.Action != "type" {
		t.Errorf("expected action type, got %q", action.Action)
	}
	if action.Payload != "hello world" {
		t.Errorf("expected payload 'hello world', got %q", action.Payload)
	}
	if action.MacheID != "mache-11" {
		t.Errorf("expected MacheID mache-11, got %q", action.MacheID)
	}
	if !strings.Contains(result, "Typing") {
		t.Errorf("result should mention typing: %s", result)
	}
}

func TestExecuteToolActEnter(t *testing.T) {
	agent := newTestAgent()
	fc := &genai.FunctionCall{Name: "act", Args: map[string]any{
		"path":   "/main/stories/_c/1",
		"action": "enter",
	}}

	result, action := agent.ExecuteTool(context.Background(), fc)
	if action == nil {
		t.Fatal("act enter should return an ActionResult")
	}
	if action.Action != "enter" {
		t.Errorf("expected action enter, got %q", action.Action)
	}
	if action.Payload != "" {
		t.Errorf("enter should have empty payload, got %q", action.Payload)
	}
	if !strings.Contains(result, "Executing enter") {
		t.Errorf("result should mention executing enter: %s", result)
	}
}

func TestExecuteToolRescanRoot(t *testing.T) {
	agent := newTestAgent()
	// rescan("/") should behave as full-page rescan, not error on missing mache_id.
	fc := &genai.FunctionCall{Name: "browser.rescan", Args: map[string]any{
		"path": "/",
	}}

	result, action := agent.ExecuteTool(context.Background(), fc)
	if action == nil {
		t.Fatal("rescan('/') should return an ActionResult")
	}
	if action.Action != "browser.rescan" {
		t.Errorf("expected action rescan, got %q", action.Action)
	}
	if action.MacheID != "" {
		t.Errorf("full-page rescan should have empty MacheID, got %q", action.MacheID)
	}
	if action.Path != "" {
		t.Errorf("full-page rescan should have empty Path, got %q", action.Path)
	}
	if strings.Contains(result, "Error") {
		t.Errorf("rescan('/') should not error: %s", result)
	}
}
