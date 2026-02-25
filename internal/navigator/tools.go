package navigator

import (
	"context"
	"fmt"
	"strings"

	"github.com/jamesgardner/x-ray/internal/mache"
	"google.golang.org/genai"
)

// --- ls ---

type LsTool struct{ engine *mache.Engine }

func (t *LsTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "ls",
		Description: "List the contents of a directory in the semantic filesystem. Returns file and directory names. Always start with ls(\"/\") to see the top-level zones.",
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"path": {Type: genai.TypeString, Description: "Directory path, e.g. '/' or '/header/nav'"},
			},
			Required: []string{"path"},
		},
	}
}

func (t *LsTool) Execute(_ context.Context, args map[string]any) (string, *ActionResult) {
	p, _ := args["path"].(string)
	entries, err := t.engine.ListDir(p)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), nil
	}
	return strings.Join(entries, "\n"), nil
}

// --- cat ---

type CatTool struct{ engine *mache.Engine }

func (t *CatTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "cat",
		Description: "Read the contents of a file in the semantic filesystem. Use this to read 'description' files for context about a zone.",
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"path": {Type: genai.TypeString, Description: "File path, e.g. '/header/nav/description'"},
			},
			Required: []string{"path"},
		},
	}
}

func (t *CatTool) Execute(_ context.Context, args map[string]any) (string, *ActionResult) {
	p, _ := args["path"].(string)
	content, err := t.engine.ReadFile(p)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), nil
	}
	return content, nil
}

// --- act ---

type ActTool struct{ engine *mache.Engine }

func (t *ActTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "act",
		Description: "Execute a browser action on the element at this virtual path. This triggers a real click/focus/type/enter in the browser.",
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"path":    {Type: genai.TypeString, Description: "Virtual path to the element, e.g. '/main/trending'"},
				"action":  {Type: genai.TypeString, Description: "Action type: 'click', 'focus', 'type', or 'enter'"},
				"payload": {Type: genai.TypeString, Description: "Text to type into the element (required for 'type' action, ignored otherwise)"},
			},
			Required: []string{"path", "action"},
		},
	}
}

func (t *ActTool) Execute(_ context.Context, args map[string]any) (string, *ActionResult) {
	p, _ := args["path"].(string)
	action, _ := args["action"].(string)
	payload, _ := args["payload"].(string)
	if action == "" {
		action = "click"
	}
	macheID, err := t.engine.ResolveMacheID(p)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), nil
	}
	desc := fmt.Sprintf("Executing %s on %s (mache_id: %s)", action, p, macheID)
	if payload != "" {
		desc = fmt.Sprintf("Typing %q into %s (mache_id: %s)", payload, p, macheID)
	}
	return desc, &ActionResult{MacheID: macheID, Action: action, Path: p, Payload: payload}
}

// --- scroll ---

type ScrollTool struct {
	scrollFn func(ctx context.Context, direction string) error
}

func (t *ScrollTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "scroll",
		Description: "Scroll the page to load more content. Use when items shown are fewer than what the user needs (e.g., only 3 posts visible but user wants the 10th). After scrolling, cat the children file again to see newly loaded items.",
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"direction": {Type: genai.TypeString, Description: "Scroll direction: 'down' or 'up'. Default: 'down'"},
			},
		},
	}
}

func (t *ScrollTool) Execute(ctx context.Context, args map[string]any) (string, *ActionResult) {
	direction, _ := args["direction"].(string)
	if direction == "" {
		direction = "down"
	}
	if t.scrollFn == nil {
		return "Error: scroll not available in this context", nil
	}
	if err := t.scrollFn(ctx, direction); err != nil {
		return fmt.Sprintf("Error scrolling: %v", err), nil
	}
	return fmt.Sprintf("Scrolled %s. Use cat on the children file to see updated content.", direction), nil
}

// --- goto ---

type GotoTool struct{}

func (t *GotoTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "goto",
		Description: "Navigate the browser to a new URL. Use when the user wants to visit a different website (e.g., 'go to Reddit'). After navigation, the filesystem updates to reflect the new page — run ls('/') to explore it.",
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"url": {Type: genai.TypeString, Description: "Fully qualified URL, e.g. 'https://www.reddit.com'"},
			},
			Required: []string{"url"},
		},
	}
}

func (t *GotoTool) Execute(_ context.Context, args map[string]any) (string, *ActionResult) {
	u, _ := args["url"].(string)
	if u == "" {
		return "Error: url is required", nil
	}
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		u = "https://" + u
	}
	return fmt.Sprintf("Navigating to %s", u),
		&ActionResult{Action: "goto", Path: u}
}

// --- rescan ---

type RescanTool struct{ engine *mache.Engine }

func (t *RescanTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "rescan",
		Description: "Rescan the page or a specific zone with a fresh screenshot. Without a path, rescans the full page. With a path, zooms into that zone for higher detail (e.g., a video player's internal controls). After rescanning, run ls('/') to see the updated structure.",
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"path": {Type: genai.TypeString, Description: "Optional: virtual path to zoom into, e.g. '/main/player'. Omit for full-page rescan."},
			},
		},
	}
}

func (t *RescanTool) Execute(_ context.Context, args map[string]any) (string, *ActionResult) {
	p, _ := args["path"].(string)
	if p != "" && p != "/" {
		macheID, err := t.engine.ResolveMacheID(p)
		if err != nil {
			return fmt.Sprintf("Error: %v", err), nil
		}
		return fmt.Sprintf("Zooming into %s for detailed rescan...", p),
			&ActionResult{Action: "rescan", MacheID: macheID, Path: p}
	}
	return "Rescanning page...", &ActionResult{Action: "rescan"}
}

// --- list_tabs ---

type ListTabsTool struct {
	listTabsFn func(ctx context.Context) ([]TabInfo, error)
}

func (t *ListTabsTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "list_tabs",
		Description: "List all open browser tabs. Returns tab ID, title, and URL for each. Use this BEFORE goto() to check if the user already has the site open — switching tabs is instant while navigating loads a fresh page.",
	}
}

func (t *ListTabsTool) Execute(ctx context.Context, _ map[string]any) (string, *ActionResult) {
	if t.listTabsFn == nil {
		return "Error: list_tabs not available in this context", nil
	}
	tabs, err := t.listTabsFn(ctx)
	if err != nil {
		return fmt.Sprintf("Error listing tabs: %v", err), nil
	}
	if len(tabs) == 0 {
		return "No open tabs found.", nil
	}
	var sb strings.Builder
	for _, tab := range tabs {
		fmt.Fprintf(&sb, "[%d] %s — %s\n", tab.ID, tab.Title, tab.URL)
	}
	return sb.String(), nil
}

// --- switch_tab ---

type SwitchTabTool struct{}

func (t *SwitchTabTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "switch_tab",
		Description: "Switch to an existing open browser tab by its ID (from list_tabs). After switching, the filesystem updates to reflect the new page. This is much faster than goto() when the page is already open.",
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"tab_id": {Type: genai.TypeInteger, Description: "The tab ID to switch to (from list_tabs output)"},
			},
			Required: []string{"tab_id"},
		},
	}
}

func (t *SwitchTabTool) Execute(_ context.Context, args map[string]any) (string, *ActionResult) {
	tabIDRaw, _ := args["tab_id"].(float64) // JSON numbers are float64
	tabID := int(tabIDRaw)
	if tabID == 0 {
		return "Error: tab_id is required", nil
	}
	return fmt.Sprintf("Switching to tab %d", tabID),
		&ActionResult{Action: "switch_tab", Path: fmt.Sprintf("%d", tabID)}
}
