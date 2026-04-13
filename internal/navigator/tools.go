package navigator

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/agentic-research/mache/graph"
	"google.golang.org/genai"
)

// Sentinel errors for scroll position reporting.
var (
	ErrAtBottom = errors.New("already at bottom of page")
	ErrAtTop    = errors.New("already at top of page")
)

// --- ls ---

type LsTool struct{ fs *NavFS }

func (t *LsTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "ls",
		Description: "List the contents of a directory in the semantic filesystem. Returns file and directory names. Always start with ls(\"/\") to see the top-level zones.",
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"path": {Type: genai.TypeString, Description: "Directory path, e.g. '/' or '/browser/header/nav' or '/iterm/windows'"},
			},
			Required: []string{"path"},
		},
	}
}

func (t *LsTool) Execute(_ context.Context, args map[string]any) (string, *ActionResult) {
	p, _ := args["path"].(string)
	entries, err := t.fs.ListDir(p)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), nil
	}
	return strings.Join(entries, "\n"), nil
}

// --- cat ---

type CatTool struct{ fs *NavFS }

func (t *CatTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "cat",
		Description: "Read the contents of a file in the semantic filesystem. Use this to read 'description', 'buffer', or 'children' files.",
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"path": {Type: genai.TypeString, Description: "File path, e.g. '/browser/header/nav/description' or '/iterm/.../buffer'"},
			},
			Required: []string{"path"},
		},
	}
}

func (t *CatTool) Execute(_ context.Context, args map[string]any) (string, *ActionResult) {
	p, _ := args["path"].(string)
	content, err := t.fs.ReadFile(p)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), nil
	}
	return content, nil
}

// --- act ---

type ActTool struct {
	fs            *NavFS
	refMu         sync.RWMutex
	refValidateFn func(path string) string // optional guardrail
}

func (t *ActTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "act",
		Description: "Execute an action on the element at this virtual path or bare mache ID. Accepts virtual paths (e.g., '/browser/main/feed/_c/3') OR bare mache IDs from grep results (e.g., 'mache-42'). For browser elements: click, focus, type, enter. For terminal sessions: type (send text), enter (send keypress), focus (bring to front).",
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"path":    {Type: genai.TypeString, Description: "Virtual path to the element, e.g. '/browser/main/trending' or '/iterm/.../sessions/{id}'"},
				"action":  {Type: genai.TypeString, Description: "Action type: 'click', 'focus', 'type', or 'enter'"},
				"payload": {Type: genai.TypeString, Description: "Text to type (required for 'type' action, ignored otherwise)"},
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

	// Reference validation guardrail: catch mache-N after cat(children).
	t.refMu.RLock()
	fn := t.refValidateFn
	t.refMu.RUnlock()
	if fn != nil {
		if errMsg := fn(p); errMsg != "" {
			return errMsg, nil
		}
	}

	// Try graph-level Act() first. Interactive graphs (terminal, future AX)
	// handle the action directly and return a result.
	_, err := t.fs.Act(p, action, payload)
	if err == nil {
		// Action executed directly by the graph — no Doer dispatch needed.
		desc := fmt.Sprintf("Performed %s on %s", action, p)
		if payload != "" {
			desc = fmt.Sprintf("Typed %q into %s", payload, p)
		}
		// For terminal type actions, include the buffer so the navigator
		// sees stdout/stderr without a separate cat() call.
		if action == "type" && strings.Contains(p, "iterm") {
			bufPath := p
			// Trim to session dir and append /buffer.
			if idx := strings.Index(bufPath, "/buffer"); idx >= 0 {
				bufPath = bufPath[:idx] + "/buffer"
			} else {
				bufPath = strings.TrimSuffix(bufPath, "/") + "/buffer"
			}
			if buf, err := t.fs.ReadFile(bufPath); err == nil && buf != "" {
				desc += "\n\nTerminal output:\n" + buf
			}
		}
		return desc, nil
	}
	if !errors.Is(err, graph.ErrActNotSupported) {
		return fmt.Sprintf("Error: %v", err), nil
	}

	// Non-browser graphs (interactions, iterm) that returned ErrActNotSupported
	// should NOT fall through to browser dispatch — give the LLM a clear hint.
	if strings.HasPrefix(cleanPath(p), "interactions/") {
		return "Error: /interactions/active/ only supports act(path, \"type\", text) on scratch and status. " +
			"Use cat() to read other fields (intent, task, id, steps).", nil
	}
	if strings.Contains(p, "iterm") {
		return fmt.Sprintf("Error: %q does not support %q action. Terminal sessions support \"type\" only.", p, action), nil
	}

	// Graph doesn't support Act() (browser MemoryStore) — fall back to
	// ActionResult so the Doer dispatches via the Chrome extension.
	macheID, err := t.fs.ResolveMacheID(p)
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
	mu          sync.RWMutex
	scrollFn    func(ctx context.Context, direction string) error
	getViewport func() string // returns viewport position string after scroll
}

func (t *ScrollTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "browser.scroll",
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
	t.mu.RLock()
	fn := t.scrollFn
	t.mu.RUnlock()
	if fn == nil {
		return "Error: scroll not available in this context", nil
	}
	if err := fn(ctx, direction); err != nil {
		if errors.Is(err, ErrAtBottom) {
			msg := "Already at the bottom of the page — no more content below."
			if t.getViewport != nil {
				if vp := t.getViewport(); vp != "" {
					msg += " " + vp
				}
			}
			return msg, nil
		}
		if errors.Is(err, ErrAtTop) {
			msg := "Already at the top of the page — no more content above."
			if t.getViewport != nil {
				if vp := t.getViewport(); vp != "" {
					msg += " " + vp
				}
			}
			return msg, nil
		}
		return fmt.Sprintf("Error scrolling: %v", err), nil
	}
	msg := fmt.Sprintf("Scrolled %s.", direction)
	if t.getViewport != nil {
		if vp := t.getViewport(); vp != "" {
			msg += " " + vp + "."
		}
	}
	msg += " Use cat on the children file to see updated content."
	return msg, nil
}

// --- goto ---

type GotoTool struct{}

func (t *GotoTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "browser.goto",
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
		&ActionResult{Action: "browser.goto", Path: u}
}

// --- rescan ---

type RescanTool struct{ fs *NavFS }

func (t *RescanTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "browser.rescan",
		Description: "Rescan the page or a specific zone with a fresh screenshot. Without a path, rescans the full page. With a path, zooms into that zone for higher detail (e.g., a video player's internal controls). After rescanning, run ls('/') to see the updated structure.",
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"path": {Type: genai.TypeString, Description: "Optional: virtual path to zoom into, e.g. '/browser/main/player'. Omit for full-page rescan."},
			},
		},
	}
}

func (t *RescanTool) Execute(_ context.Context, args map[string]any) (string, *ActionResult) {
	p, _ := args["path"].(string)
	if p != "" && p != "/" {
		macheID, err := t.fs.ResolveMacheID(p)
		if err != nil {
			return fmt.Sprintf("Error: %v", err), nil
		}
		return fmt.Sprintf("Zooming into %s for detailed rescan...", p),
			&ActionResult{Action: "browser.rescan", MacheID: macheID, Path: p}
	}
	return "Rescanning page...", &ActionResult{Action: "browser.rescan"}
}

// --- list_tabs ---

type ListTabsTool struct {
	mu         sync.RWMutex
	listTabsFn func(ctx context.Context) ([]TabInfo, error)
}

func (t *ListTabsTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "browser.list_tabs",
		Description: "List all open browser tabs. Returns tab ID, title, and URL for each. Use this BEFORE goto() to check if the user already has the site open — switching tabs is instant while navigating loads a fresh page.",
	}
}

func (t *ListTabsTool) Execute(ctx context.Context, _ map[string]any) (string, *ActionResult) {
	t.mu.RLock()
	fn := t.listTabsFn
	t.mu.RUnlock()
	if fn == nil {
		return "Error: list_tabs not available in this context", nil
	}
	tabs, err := fn(ctx)
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
		Name:        "browser.switch_tab",
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
		&ActionResult{Action: "browser.switch_tab", Path: fmt.Sprintf("%d", tabID)}
}

// --- grep ---

type GrepTool struct{ fs *NavFS }

func (t *GrepTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "grep",
		Description: "Search zone children, descriptions, and the global text_index for a text pattern (case-insensitive, supports regex with |). Returns matching lines with zone paths. When results include [mache-N], use that ID directly with act() to click the element. Use SHORT single keywords (1-2 words), NOT full phrases. Supports regex: 'price|cost' matches either word.",
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"pattern": {Type: genai.TypeString, Description: "Text to search for (case-insensitive substring match)"},
			},
			Required: []string{"pattern"},
		},
	}
}

func (t *GrepTool) Execute(_ context.Context, args map[string]any) (string, *ActionResult) {
	pattern, _ := args["pattern"].(string)
	if pattern == "" {
		return "Error: pattern is required", nil
	}

	// Try to compile as regex (case-insensitive); fall back to literal substring.
	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		// Invalid regex — use literal case-insensitive match.
		re = nil
	}

	var matches []string
	t.grepWalk("", strings.ToLower(pattern), re, &matches)

	if len(matches) == 0 {
		return fmt.Sprintf("No matches for %q", pattern), nil
	}
	return strings.Join(matches, "\n"), nil
}

// grepWalk recursively walks the filesystem looking for "children" and
// "description" files, returning lines matching the pattern.
func (t *GrepTool) grepWalk(dirPath, lower string, re *regexp.Regexp, matches *[]string) {
	entries, err := t.fs.ListDir(dirPath)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := strings.TrimSuffix(entry, "/")
		var childPath string
		if dirPath == "" || dirPath == "/" {
			childPath = "/" + name
		} else {
			childPath = dirPath + "/" + name
		}

		// Search children, description, text_index, and page_text files.
		if name == "children" || name == "description" || name == "text_index" || name == "page_text" {
			content, err := t.fs.ReadFile(childPath)
			if err != nil || content == "" {
				continue
			}
			zonePath := strings.TrimSuffix(childPath, "/"+name)
			for _, line := range strings.Split(content, "\n") {
				matched := false
				if re != nil {
					matched = re.MatchString(line)
				} else {
					matched = strings.Contains(strings.ToLower(line), lower)
				}
				if matched {
					*matches = append(*matches, zonePath+": "+line)
				}
			}
			continue
		}

		// Recurse into directories (skip _c/ — those are individual children).
		if strings.HasSuffix(entry, "/") && name != "_c" {
			t.grepWalk(childPath, lower, re, matches)
		}
	}
}

// --- stat ---

type StatTool struct{ fs *NavFS }

func (t *StatTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "stat",
		Description: "Show size info for a file or directory. Returns char/line counts for files, child counts for directories. Use before cat() to gauge content size.",
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"path": {Type: genai.TypeString, Description: "Path to inspect, e.g. '/browser/main/feed/children' or '/browser/main/feed'"},
			},
			Required: []string{"path"},
		},
	}
}

func (t *StatTool) Execute(_ context.Context, args map[string]any) (string, *ActionResult) {
	p, _ := args["path"].(string)

	// Try as file first.
	content, err := t.fs.ReadFile(p)
	if err == nil {
		lines := strings.Count(content, "\n")
		if len(content) > 0 && !strings.HasSuffix(content, "\n") {
			lines++ // count last line without trailing newline
		}
		return fmt.Sprintf("file: %d chars, %d lines", len(content), lines), nil
	}

	// Try as directory.
	entries, err := t.fs.ListDir(p)
	if err != nil {
		return fmt.Sprintf("Error: %v", err), nil
	}
	dirs, files := 0, 0
	for _, e := range entries {
		if strings.HasSuffix(e, "/") {
			dirs++
		} else {
			files++
		}
	}
	return fmt.Sprintf("dir: %d entries (%d files, %d dirs)", len(entries), files, dirs), nil
}

// --- iterm.new_window ---

type NewWindowTool struct{ fs *NavFS }

func (t *NewWindowTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "iterm.new_window",
		Description: "Open a new iTerm2 terminal window with a fresh session.",
	}
}

func (t *NewWindowTool) Execute(_ context.Context, _ map[string]any) (string, *ActionResult) {
	res, err := t.fs.Act("/iterm/windows", "new_window", "")
	if err != nil {
		return fmt.Sprintf("Error: %v", err), nil
	}
	sid := extractSessionID(res.NodeID)
	return fmt.Sprintf("Opened new terminal window (session %s). Use /iterm/agent_sessions/%s/ to interact with it.", sid, sid), nil
}

// --- iterm.new_tab ---

type NewTabTool struct{ fs *NavFS }

func (t *NewTabTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "iterm.new_tab",
		Description: "Open a new tab in an iTerm2 terminal window.",
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"window_path": {Type: genai.TypeString, Description: "Path to the target window, e.g. '/iterm/windows/{id}'. Omit to use the first window."},
			},
		},
	}
}

func (t *NewTabTool) Execute(_ context.Context, args map[string]any) (string, *ActionResult) {
	p, _ := args["window_path"].(string)
	if p == "" {
		p = "/iterm/windows"
	}
	res, err := t.fs.Act(p, "new_tab", "")
	if err != nil {
		return fmt.Sprintf("Error: %v", err), nil
	}
	sid := extractSessionID(res.NodeID)
	return fmt.Sprintf("Opened new tab (session %s). Use /iterm/agent_sessions/%s/ to interact with it.", sid, sid), nil
}

// --- answer ---

// AnswerTool lets the model return a text answer directly.
// Used for read-only queries where the model already has the answer
// from the tree dump and doesn't need to call another tool.
type AnswerTool struct{}

func (a *AnswerTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "answer",
		Description: "Return a text answer to the user's question. Use this when you can answer from what you already see in the page tree — no need to call other tools first.",
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"text": {
					Type:        genai.TypeString,
					Description: "Your answer text",
				},
			},
			Required: []string{"text"},
		},
	}
}

func (a *AnswerTool) Execute(_ context.Context, args map[string]any) (string, *ActionResult) {
	text, _ := args["text"].(string)
	if text == "" {
		return "Error: answer text is required", nil
	}
	return text, nil
}

// extractSessionID strips mount and protocol prefixes from a NodeID.
// "iterm/iterm:886D7E2A-..." → "886D7E2A-..."
// "iterm:886D7E2A-..." → "886D7E2A-..."
func extractSessionID(nodeID string) string {
	if i := strings.LastIndex(nodeID, ":"); i >= 0 {
		return nodeID[i+1:]
	}
	return nodeID
}
