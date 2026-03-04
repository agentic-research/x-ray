package navigator

import (
	"context"
	"strings"
	"testing"

	"github.com/agentic-research/mache/graph"
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

// --- stat tool tests ---

func TestExecuteToolStatFile(t *testing.T) {
	agent := newTestAgent()
	fc := &genai.FunctionCall{Name: "stat", Args: map[string]any{"path": "/main/stories/children"}}

	result, action := agent.ExecuteTool(context.Background(), fc)
	if action != nil {
		t.Fatal("stat should not return an action")
	}
	if !strings.Contains(result, "file:") {
		t.Errorf("expected 'file:' prefix, got %q", result)
	}
	if !strings.Contains(result, "chars") {
		t.Errorf("expected char count, got %q", result)
	}
	if !strings.Contains(result, "lines") {
		t.Errorf("expected line count, got %q", result)
	}
}

func TestExecuteToolStatDir(t *testing.T) {
	agent := newTestAgent()
	fc := &genai.FunctionCall{Name: "stat", Args: map[string]any{"path": "/main/stories"}}

	result, action := agent.ExecuteTool(context.Background(), fc)
	if action != nil {
		t.Fatal("stat should not return an action")
	}
	if !strings.Contains(result, "dir:") {
		t.Errorf("expected 'dir:' prefix, got %q", result)
	}
	if !strings.Contains(result, "entries") {
		t.Errorf("expected entry count, got %q", result)
	}
}

func TestExecuteToolStatBadPath(t *testing.T) {
	agent := newTestAgent()
	fc := &genai.FunctionCall{Name: "stat", Args: map[string]any{"path": "/nonexistent"}}

	result, _ := agent.ExecuteTool(context.Background(), fc)
	if !strings.Contains(result, "Error") {
		t.Errorf("expected error for bad path, got %q", result)
	}
}

// --- ASCII layout tests ---

const layoutSchema = `{
  "mounts": [
    {
      "virtual_path": "/header/nav",
      "mache_id": "mache-0",
      "description": "Top navigation",
      "bounds": [0, 0, 1, 0.1]
    },
    {
      "virtual_path": "/sidebar/menu",
      "mache_id": "mache-10",
      "description": "Side menu with 6 items",
      "bounds": [0, 0.1, 0.2, 0.8]
    },
    {
      "virtual_path": "/main/content",
      "mache_id": "mache-20",
      "description": "Main content area",
      "bounds": [0.2, 0.1, 0.8, 0.7]
    },
    {
      "virtual_path": "/footer/links",
      "mache_id": "mache-50",
      "description": "Footer navigation",
      "bounds": [0, 0.9, 1, 0.1]
    }
  ]
}`

func newLayoutTestAgent() *Agent {
	engine := mache.NewEngine()
	if err := engine.ApplySchema(layoutSchema); err != nil {
		panic(err)
	}
	// Load children so the ASCII render has actual elements to paint.
	summary := `Interactive Elements:
ID: mache-1 | Parent: mache-0 | Tag: a | Text: "Home" | Bounds: [0.01, 0.02, 0.08, 0.05]
ID: mache-2 | Parent: mache-0 | Tag: a | Text: "Electronics" | Bounds: [0.12, 0.02, 0.10, 0.05]
ID: mache-3 | Parent: mache-0 | Tag: a | Text: "Cameras" | Bounds: [0.25, 0.02, 0.08, 0.05]
ID: mache-21 | Parent: mache-20 | Tag: h1 | Text: "Sony Alpha A7 III" | Bounds: [0.25, 0.15, 0.40, 0.05]
ID: mache-22 | Parent: mache-20 | Tag: span | Text: "$1,998.00" | Bounds: [0.25, 0.22, 0.15, 0.04]
ID: mache-23 | Parent: mache-20 | Tag: a | Text: "Add to Cart" | Bounds: [0.25, 0.30, 0.12, 0.04]
ID: mache-24 | Parent: mache-20 | Tag: a | Text: "Description" | Bounds: [0.25, 0.55, 0.10, 0.04]
ID: mache-25 | Parent: mache-20 | Tag: a | Text: "Reviews" | Bounds: [0.40, 0.55, 0.08, 0.04]
ID: mache-26 | Parent: mache-20 | Tag: a | Text: "Specifications" | Bounds: [0.55, 0.55, 0.12, 0.04]
ID: mache-51 | Parent: mache-50 | Tag: a | Text: "About" | Bounds: [0.01, 0.92, 0.06, 0.04]
ID: mache-52 | Parent: mache-50 | Tag: a | Text: "Privacy" | Bounds: [0.10, 0.92, 0.06, 0.04]
`
	engine.LoadChildren(summary, nil)
	composite := graph.NewCompositeGraph()
	if err := composite.Mount("browser", engine); err != nil {
		panic(err)
	}
	return NewAgent(nil, "test", composite)
}

func TestBuildASCIILayout(t *testing.T) {
	agent := newLayoutTestAgent()
	layout := agent.BuildASCIILayout()

	if layout == "" {
		t.Fatal("expected non-empty ASCII layout")
	}
	if !strings.Contains(layout, "Page layout:") {
		t.Error("missing 'Page layout:' header")
	}
	// Elements with ordinals and text should appear.
	for _, want := range []string{"Home", "Reviews", "Add to Cart", "About"} {
		if !strings.Contains(layout, want) {
			t.Errorf("layout missing element %q:\n%s", want, layout)
		}
	}
	// Ordinal markers should appear (at least [1]).
	if !strings.Contains(layout, "[") {
		t.Errorf("layout missing ordinal markers:\n%s", layout)
	}
	t.Logf("ASCII layout:\n%s", layout)
}

func TestBuildASCIILayoutProductPage(t *testing.T) {
	// Simulate a product page with nav tabs, a product title, and action buttons.
	// Verify the ASCII render shows clickable elements at roughly correct positions.
	schema := `{
  "mounts": [
    {
      "virtual_path": "/header/nav",
      "mache_id": "mache-0",
      "description": "Top navigation",
      "bounds": [0, 0, 1, 0.08]
    },
    {
      "virtual_path": "/main/product",
      "mache_id": "mache-10",
      "description": "Product detail",
      "bounds": [0.1, 0.10, 0.8, 0.80]
    }
  ]
}`
	summary := `Interactive Elements:
ID: mache-1 | Parent: mache-0 | Tag: a | Text: "Home" | Bounds: [0.02, 0.02, 0.06, 0.04]
ID: mache-2 | Parent: mache-0 | Tag: a | Text: "Electronics" | Bounds: [0.10, 0.02, 0.10, 0.04]
ID: mache-3 | Parent: mache-0 | Tag: a | Text: "Cameras" | Bounds: [0.22, 0.02, 0.08, 0.04]
ID: mache-11 | Parent: mache-10 | Tag: h1 | Text: "Fujifilm X100VI" | Bounds: [0.15, 0.14, 0.30, 0.05]
ID: mache-12 | Parent: mache-10 | Tag: span | Text: "$1,599.00" | Bounds: [0.15, 0.22, 0.12, 0.04]
ID: mache-13 | Parent: mache-10 | Tag: button | Text: "Add to Cart" | Bounds: [0.15, 0.30, 0.14, 0.05]
ID: mache-14 | Parent: mache-10 | Tag: a | Text: "Description" | Bounds: [0.15, 0.50, 0.10, 0.04]
ID: mache-15 | Parent: mache-10 | Tag: a | Text: "Reviews" | Bounds: [0.30, 0.50, 0.08, 0.04]
ID: mache-16 | Parent: mache-10 | Tag: a | Text: "Specifications" | Bounds: [0.42, 0.50, 0.12, 0.04]
`
	engine := mache.NewEngine()
	if err := engine.ApplySchema(schema); err != nil {
		t.Fatal(err)
	}
	engine.LoadChildren(summary, nil)
	composite := graph.NewCompositeGraph()
	if err := composite.Mount("browser", engine); err != nil {
		t.Fatal(err)
	}
	agent := NewAgent(nil, "test", composite)
	layout := agent.BuildASCIILayout()
	t.Logf("Product page ASCII:\n%s", layout)

	// Nav items should be in the top rows.
	if !strings.Contains(layout, "Home") {
		t.Error("missing Home nav link")
	}
	if !strings.Contains(layout, "Electronics") {
		t.Error("missing Electronics nav link")
	}
	// Product content should be present.
	if !strings.Contains(layout, "Fujifilm") {
		t.Error("missing product title")
	}
	if !strings.Contains(layout, "$1,599.00") {
		t.Error("missing product price")
	}
	if !strings.Contains(layout, "Add to Cart") {
		t.Error("missing Add to Cart button")
	}
	// Tab navigation — this is the key Reviews tab scenario.
	if !strings.Contains(layout, "Reviews") {
		t.Errorf("missing Reviews tab — the whole point:\n%s", layout)
	}
	if !strings.Contains(layout, "Description") {
		t.Error("missing Description tab")
	}
	if !strings.Contains(layout, "Specifications") {
		t.Error("missing Specifications tab")
	}
	// Every element should have an ordinal prefix.
	lines := strings.Split(layout, "\n")
	elemCount := 0
	for _, line := range lines {
		if strings.Contains(line, "[") && strings.Contains(line, "]") {
			elemCount++
		}
	}
	if elemCount < 5 {
		t.Errorf("expected at least 5 lines with ordinal markers, got %d", elemCount)
	}
}

func TestBuildASCIILayoutSpatialOrder(t *testing.T) {
	// Verify elements are positioned top-to-bottom: nav items above product content.
	schema := `{
  "mounts": [
    {
      "virtual_path": "/header/nav",
      "mache_id": "mache-0",
      "description": "Nav",
      "bounds": [0, 0, 1, 0.08]
    },
    {
      "virtual_path": "/main/body",
      "mache_id": "mache-10",
      "description": "Body",
      "bounds": [0, 0.10, 1, 0.80]
    }
  ]
}`
	summary := `Interactive Elements:
ID: mache-1 | Parent: mache-0 | Tag: a | Text: "TopNav" | Bounds: [0.02, 0.02, 0.08, 0.04]
ID: mache-11 | Parent: mache-10 | Tag: a | Text: "BottomLink" | Bounds: [0.02, 0.80, 0.10, 0.04]
`
	engine := mache.NewEngine()
	if err := engine.ApplySchema(schema); err != nil {
		t.Fatal(err)
	}
	engine.LoadChildren(summary, nil)
	composite := graph.NewCompositeGraph()
	if err := composite.Mount("browser", engine); err != nil {
		t.Fatal(err)
	}
	agent := NewAgent(nil, "test", composite)
	layout := agent.BuildASCIILayout()
	t.Logf("Spatial order ASCII:\n%s", layout)

	topIdx := strings.Index(layout, "TopNav")
	bottomIdx := strings.Index(layout, "BottomLink")
	if topIdx < 0 || bottomIdx < 0 {
		t.Fatalf("missing elements:\n%s", layout)
	}
	if topIdx >= bottomIdx {
		t.Errorf("TopNav (pos %d) should appear before BottomLink (pos %d) in the render", topIdx, bottomIdx)
	}
}

func TestBuildASCIILayoutOffScreen(t *testing.T) {
	// Zones with negative bounds should be clamped or excluded.
	// Elements inside visible zones should appear; off-screen zones should not contribute.
	schema := `{
      "mounts": [
        {
          "virtual_path": "/header/feed",
          "mache_id": "mache-1",
          "description": "Off-screen zone",
          "bounds": [0.5, -6.4, 0.3, 0.08]
        },
        {
          "virtual_path": "/main/content",
          "mache_id": "mache-2",
          "description": "Visible zone",
          "bounds": [0.2, 0.1, 0.6, 0.8]
        }
      ]
    }`
	summary := `Interactive Elements:
ID: mache-3 | Parent: mache-2 | Tag: a | Text: "VisibleLink" | Bounds: [0.30, 0.30, 0.10, 0.04]
ID: mache-4 | Parent: mache-1 | Tag: a | Text: "OffScreenLink" | Bounds: [0.55, -6.0, 0.10, 0.04]
`
	engine := mache.NewEngine()
	if err := engine.ApplySchema(schema); err != nil {
		t.Fatal(err)
	}
	engine.LoadChildren(summary, nil)
	composite := graph.NewCompositeGraph()
	if err := composite.Mount("browser", engine); err != nil {
		t.Fatal(err)
	}
	agent := NewAgent(nil, "test", composite)
	layout := agent.BuildASCIILayout()
	t.Logf("Off-screen test:\n%s", layout)

	// Visible zone element should be present.
	if !strings.Contains(layout, "VisibleLink") {
		t.Errorf("visible element missing from layout:\n%s", layout)
	}
	// Off-screen element should NOT appear (bounds have negative y, off viewport).
	if strings.Contains(layout, "OffScreenLink") {
		t.Error("off-screen element should not appear in layout")
	}
}

func TestBuildASCIILayoutInTreeDump(t *testing.T) {
	agent := newLayoutTestAgent()
	dump := agent.buildTreeDump()

	// Tree dump should include both ASCII layout and tree listing.
	if !strings.Contains(dump, "Page layout:") {
		t.Error("tree dump missing ASCII layout")
	}
	if !strings.Contains(dump, "header/") {
		t.Error("tree dump missing tree listing")
	}
}

func TestParseBounds(t *testing.T) {
	x, y, w, h := parseBounds("[0.123,0.456,0.789,0.234]")
	if x != 0.123 || y != 0.456 || w != 0.789 || h != 0.234 {
		t.Errorf("unexpected bounds: %f,%f,%f,%f", x, y, w, h)
	}

	// Invalid input should return zeroes.
	x, y, w, h = parseBounds("garbage")
	if x != 0 || y != 0 || w != 0 || h != 0 {
		t.Errorf("expected zero bounds for garbage input, got %f,%f,%f,%f", x, y, w, h)
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
