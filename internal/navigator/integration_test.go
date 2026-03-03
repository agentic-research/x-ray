package navigator

import (
	"context"
	"strings"
	"testing"

	"github.com/agentic-research/x-ray/internal/mache"
	"google.golang.org/genai"
)

// Realistic HN-style schema + summary for end-to-end tool chain testing.
const hnSchema = `{
  "mounts": [
    {
      "virtual_path": "/header/nav",
      "mache_id": "mache-0",
      "description": "Top navigation with logo, links (new, past, comments, ask, show, jobs, submit)"
    },
    {
      "virtual_path": "/main/story_list",
      "mache_id": "mache-10",
      "description": "Main news feed with ranked story links",
      "primary_items": ["mache-11", "mache-18", "mache-25"]
    },
    {
      "virtual_path": "/footer/actions",
      "mache_id": "mache-100",
      "description": "Footer with More link and application info"
    }
  ]
}`

const hnSummary = `Interactive Elements:
ID: mache-0 | Parent: none | Tag: nav | Text: "Hacker News"
ID: mache-1 | Parent: mache-0 | Tag: a | Text: "Hacker News"
ID: mache-2 | Parent: mache-0 | Tag: a | Text: "new"
ID: mache-3 | Parent: mache-0 | Tag: a | Text: "past"
ID: mache-4 | Parent: mache-0 | Tag: a | Text: "comments"
ID: mache-5 | Parent: mache-0 | Tag: a | Text: "ask"
ID: mache-6 | Parent: mache-0 | Tag: a | Text: "show"
ID: mache-7 | Parent: mache-0 | Tag: a | Text: "jobs"
ID: mache-8 | Parent: mache-0 | Tag: a | Text: "submit"
ID: mache-10 | Parent: none | Tag: div | Text: "Stories"
ID: mache-11 | Parent: mache-10 | Tag: a | Text: "I built Timeframe, our family e-paper dashboard"
ID: mache-12 | Parent: mache-10 | Tag: a | Text: "(hawksley.org)"
ID: mache-13 | Parent: mache-10 | Tag: a | Text: "saeedesmaili"
ID: mache-14 | Parent: mache-10 | Tag: a | Text: "3 hours ago"
ID: mache-15 | Parent: mache-10 | Tag: a | Text: "hide"
ID: mache-16 | Parent: mache-10 | Tag: a | Text: "96 comments"
ID: mache-17 | Parent: mache-10 | Tag: span | Text: ""
ID: mache-18 | Parent: mache-10 | Tag: a | Text: "Global Intelligence Crisis"
ID: mache-19 | Parent: mache-10 | Tag: a | Text: "(citriniresearch.com)"
ID: mache-20 | Parent: mache-10 | Tag: a | Text: "tin7in"
ID: mache-21 | Parent: mache-10 | Tag: a | Text: "1 hour ago"
ID: mache-22 | Parent: mache-10 | Tag: a | Text: "hide"
ID: mache-23 | Parent: mache-10 | Tag: a | Text: "34 comments"
ID: mache-24 | Parent: mache-10 | Tag: span | Text: ""
ID: mache-25 | Parent: mache-10 | Tag: a | Text: "Loops is a federated, open-source TikTok"
ID: mache-26 | Parent: mache-10 | Tag: a | Text: "(joinloops.org)"
ID: mache-27 | Parent: mache-10 | Tag: a | Text: "Gooblebrai"
ID: mache-28 | Parent: mache-10 | Tag: a | Text: "3 hours ago"
ID: mache-29 | Parent: mache-10 | Tag: a | Text: "hide"
ID: mache-30 | Parent: mache-10 | Tag: a | Text: "64 comments"
ID: mache-100 | Parent: none | Tag: footer | Text: "More"
`

func buildHNEngine(t *testing.T) *mache.Engine {
	t.Helper()
	engine := mache.NewEngine()
	if err := engine.ApplySchema(hnSchema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	engine.LoadChildren(hnSummary, nil)
	return engine
}

func TestToolChainLsRoot(t *testing.T) {
	engine := buildHNEngine(t)
	agent := NewAgent(nil, "test", engine)

	result, _ := agent.ExecuteTool(context.Background(), &genai.FunctionCall{
		Name: "ls", Args: map[string]any{"path": "/"},
	})

	for _, zone := range []string{"header/", "main/", "footer/"} {
		if !strings.Contains(result, zone) {
			t.Errorf("ls('/') missing zone %q: %s", zone, result)
		}
	}
}

func TestToolChainLsToChildren(t *testing.T) {
	engine := buildHNEngine(t)
	agent := NewAgent(nil, "test", engine)

	// ls the story list zone
	result, _ := agent.ExecuteTool(context.Background(), &genai.FunctionCall{
		Name: "ls", Args: map[string]any{"path": "/main/story_list"},
	})

	if !strings.Contains(result, "children") {
		t.Fatalf("zone missing children file: %s", result)
	}

	// cat the children file
	result, _ = agent.ExecuteTool(context.Background(), &genai.FunctionCall{
		Name: "cat", Args: map[string]any{"path": "/main/story_list/children"},
	})

	// Should have ordinal children listing (Lynx-style, no quotes, no mache IDs)
	if !strings.Contains(result, "[1] ") {
		t.Errorf("children missing [1]: %s", result)
	}
	if !strings.Contains(result, "Timeframe") {
		t.Errorf("children missing first story: %s", result)
	}
	if !strings.Contains(result, "Global Intelligence Crisis") {
		t.Errorf("children missing second story: %s", result)
	}
	if !strings.Contains(result, "[3] ") {
		t.Errorf("children missing [3]: %s", result)
	}
	// No mache IDs exposed in children listing
	if strings.Contains(result, "mache-") {
		t.Errorf("children listing should not expose mache IDs: %s", result)
	}
}

func TestToolChainActOnChild(t *testing.T) {
	engine := buildHNEngine(t)
	agent := NewAgent(nil, "test", engine)

	// Ordinal path: _c/1 is the first primary item (mache-11)
	_, action := agent.ExecuteTool(context.Background(), &genai.FunctionCall{
		Name: "act", Args: map[string]any{
			"path":   "/main/story_list/_c/1",
			"action": "click",
		},
	})

	if action == nil {
		t.Fatal("act should return ActionResult")
	}
	if action.MacheID != "mache-11" {
		t.Errorf("expected mache-11, got %q", action.MacheID)
	}
	if action.Action != "click" {
		t.Errorf("expected click, got %q", action.Action)
	}
}

func TestToolChainActOnZone(t *testing.T) {
	engine := buildHNEngine(t)
	agent := NewAgent(nil, "test", engine)

	// Act directly on a zone (e.g. footer "More" link)
	_, action := agent.ExecuteTool(context.Background(), &genai.FunctionCall{
		Name: "act", Args: map[string]any{
			"path":   "/footer/actions",
			"action": "click",
		},
	})

	if action == nil {
		t.Fatal("act on zone should return ActionResult")
	}
	if action.MacheID != "mache-100" {
		t.Errorf("expected mache-100, got %q", action.MacheID)
	}
}

func TestToolChainActWithBareMacheID(t *testing.T) {
	engine := buildHNEngine(t)
	agent := NewAgent(nil, "test", engine)

	// Act with a bare mache ID (from text_index grep results).
	// Previously this returned "Error: node not found" because
	// CompositeGraph couldn't route "mache-22" (no mount prefix).
	result, action := agent.ExecuteTool(context.Background(), &genai.FunctionCall{
		Name: "act", Args: map[string]any{
			"path":   "mache-22",
			"action": "click",
		},
	})

	if action == nil {
		t.Fatalf("act on bare mache-id should return ActionResult, got: %s", result)
	}
	if action.MacheID != "mache-22" {
		t.Errorf("expected mache-22, got %q", action.MacheID)
	}
	if action.Action != "click" {
		t.Errorf("expected click, got %q", action.Action)
	}
}

func TestToolChainNavDescription(t *testing.T) {
	engine := buildHNEngine(t)
	agent := NewAgent(nil, "test", engine)

	result, _ := agent.ExecuteTool(context.Background(), &genai.FunctionCall{
		Name: "cat", Args: map[string]any{"path": "/header/nav/description"},
	})

	if !strings.Contains(result, "navigation") {
		t.Errorf("nav description should mention navigation: %s", result)
	}
}

func TestToolChainFullWorkflow(t *testing.T) {
	// Simulates what the Navigator agent does: ls → ls zone → cat children → act
	engine := buildHNEngine(t)
	agent := NewAgent(nil, "test", engine)

	// Step 1: ls("/")
	root, _ := agent.ExecuteTool(context.Background(), &genai.FunctionCall{
		Name: "ls", Args: map[string]any{"path": "/"},
	})
	if !strings.Contains(root, "main/") {
		t.Fatalf("step 1 failed: %s", root)
	}

	// Step 2: ls("/main/story_list")
	zone, _ := agent.ExecuteTool(context.Background(), &genai.FunctionCall{
		Name: "ls", Args: map[string]any{"path": "/main/story_list"},
	})
	if !strings.Contains(zone, "children") {
		t.Fatalf("step 2 failed: %s", zone)
	}

	// Step 3: cat("/main/story_list/children") — ordinal format
	children, _ := agent.ExecuteTool(context.Background(), &genai.FunctionCall{
		Name: "cat", Args: map[string]any{"path": "/main/story_list/children"},
	})
	if !strings.Contains(children, "Global Intelligence Crisis") {
		t.Fatalf("step 3 failed — missing second story text: %s", children)
	}

	// Step 4: act on the second story via ordinal path _c/2
	result, action := agent.ExecuteTool(context.Background(), &genai.FunctionCall{
		Name: "act", Args: map[string]any{
			"path":   "/main/story_list/_c/2",
			"action": "click",
		},
	})
	if action == nil {
		t.Fatalf("step 4 failed — no action: %s", result)
	}
	if action.MacheID != "mache-18" {
		t.Errorf("expected mache-18, got %q", action.MacheID)
	}
}
