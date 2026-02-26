package navigator

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/agentic-research/mache/graph"
	"google.golang.org/genai"
)

// Compile-time checks.
var (
	_ ContentGenerator = (*GeminiGenerator)(nil)
	_ ContentGenerator = (*GemmaGenerator)(nil)
)

// ActionResult is returned when the Navigator decides to act on an element.
type ActionResult struct {
	MacheID string `json:"mache_id"`
	Action  string `json:"action"`
	Path    string `json:"path"`
	Payload string `json:"payload,omitempty"`
}

// TabInfo describes an open browser tab (returned by LIST_TABS round-trip).
type TabInfo struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

// Agent represents Stage 2: The Navigator.
type Agent struct {
	generator  ContentGenerator
	model      string
	fs         *NavFS // unified filesystem over CompositeGraph
	mu         sync.RWMutex
	progressFn func(toolName string, args map[string]any)

	registry   *ToolRegistry
	scrollTool *ScrollTool
	listTabs   *ListTabsTool
	lsTool     *LsTool
	catTool    *CatTool
	actTool    *ActTool
	rescanTool *RescanTool
}

// NewAgent creates a Navigator agent backed by a unified graph.Graph.
// The graph is typically a CompositeGraph with mounts like "browser" and "iterm".
func NewAgent(gen ContentGenerator, model string, g graph.Graph) *Agent {
	if model == "" {
		model = "gemini-2.5-flash"
	}

	fs := NewNavFS(g)

	ls := &LsTool{fs: fs}
	cat := &CatTool{fs: fs}
	act := &ActTool{fs: fs}
	scroll := &ScrollTool{}
	goTo := &GotoTool{}
	rescan := &RescanTool{fs: fs}
	listTabs := &ListTabsTool{}
	switchTab := &SwitchTabTool{}

	reg := NewToolRegistry()
	reg.Register(ls)
	reg.Register(cat)
	reg.Register(act)
	reg.Register(scroll)
	reg.Register(goTo)
	reg.Register(rescan)
	reg.Register(listTabs)
	reg.Register(switchTab)

	return &Agent{
		generator:  gen,
		model:      model,
		fs:         fs,
		registry:   reg,
		scrollTool: scroll,
		listTabs:   listTabs,
		lsTool:     ls,
		catTool:    cat,
		actTool:    act,
		rescanTool: rescan,
	}
}

// SetGraph swaps the underlying graph (e.g., after remounting browser engine).
func (a *Agent) SetGraph(g graph.Graph) {
	newFS := NewNavFS(g)
	a.fs = newFS
	a.lsTool.fs = newFS
	a.catTool.fs = newFS
	a.actTool.fs = newFS
	a.rescanTool.fs = newFS
}

// SetScrollFunc injects the scroll callback used by the scroll tool.
func (a *Agent) SetScrollFunc(fn func(ctx context.Context, direction string) error) {
	a.scrollTool.mu.Lock()
	defer a.scrollTool.mu.Unlock()
	a.scrollTool.scrollFn = fn
}

// SetProgressFunc injects a callback fired before each tool execution,
// allowing the Doer to report its current step to the Talker.
func (a *Agent) SetProgressFunc(fn func(toolName string, args map[string]any)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.progressFn = fn
}

// SetListTabsFunc injects the callback used by the list_tabs tool.
func (a *Agent) SetListTabsFunc(fn func(ctx context.Context) ([]TabInfo, error)) {
	a.listTabs.mu.Lock()
	defer a.listTabs.mu.Unlock()
	a.listTabs.listTabsFn = fn
}

const maxToolIterations = 8

// HandleIntent processes a user intent by navigating the semantic FS.
// Returns an ActionResult if the agent acts, or a text response otherwise.
// When readOnly is true, the act() tool is stripped from the schema so the
// model cannot dispatch clicks — it must answer with text.
func (a *Agent) HandleIntent(ctx context.Context, intent string, readOnly bool) (*ActionResult, string, error) {
	log.Printf("Navigator: Handling intent (readOnly=%v): %s", readOnly, intent)

	tools := a.registry.Definitions()
	if readOnly {
		tools = a.registry.DefinitionsExcluding("act")
	}

	config := &genai.GenerateContentConfig{
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{{Text: NavigatorSystemPrompt}},
		},
		Tools:       tools,
		Temperature: genai.Ptr(float32(0.1)),
	}

	// Pre-fill a tree dump so the model sees the full filesystem structure upfront.
	treeDump := a.buildTreeDump()
	log.Printf("Navigator: pre-filled tree:\n%s", treeDump)

	var history []*genai.Content
	if treeDump == "" {
		// No page loaded yet — tell the model so it knows to use goto().
		history = []*genai.Content{
			{Role: "user", Parts: []*genai.Part{{Text: intent + "\n\n[No page is currently loaded. Use goto(url) to navigate to a website first.]"}}},
		}
	} else {
		history = []*genai.Content{
			{Role: "user", Parts: []*genai.Part{{Text: intent}}},
			{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
				Name: "ls", Args: map[string]any{"path": "/"},
			}}}},
			{Role: "user", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
				Name:     "ls",
				Response: map[string]any{"output": treeDump},
			}}}},
		}
	}

	for i := range maxToolIterations {
		log.Printf("Navigator: tool-use iteration %d/%d", i+1, maxToolIterations)

		res, err := a.generator.GenerateContent(ctx, a.model, history, config)
		if err != nil {
			return nil, "", fmt.Errorf("GenerateContent failed: %w", err)
		}
		if len(res.Candidates) == 0 {
			return nil, "", fmt.Errorf("no candidates returned")
		}

		candidate := res.Candidates[0]
		if candidate.Content == nil || len(candidate.Content.Parts) == 0 {
			// MALFORMED_FUNCTION_CALL: Gemini tried to call a tool but produced
			// invalid JSON. Nudge it to retry rather than aborting.
			if candidate.FinishReason == genai.FinishReasonMalformedFunctionCall {
				log.Printf("Navigator: malformed function call at iteration %d, retrying", i+1)
				history = append(history, &genai.Content{
					Role:  "user",
					Parts: []*genai.Part{{Text: "Your function call was malformed. Please try again with valid JSON arguments."}},
				})
				continue
			}
			return nil, "", fmt.Errorf("empty response from model (finish_reason: %v)", candidate.FinishReason)
		}
		var fc *genai.FunctionCall
		var finalMsg string
		for _, p := range candidate.Content.Parts {
			if p.FunctionCall != nil {
				fc = p.FunctionCall
				break
			}
			if p.Text != "" && !p.Thought {
				finalMsg = p.Text
			}
		}

		if fc == nil && finalMsg != "" {
			return nil, finalMsg, nil
		}

		if fc != nil {
			history = append(history, candidate.Content)

			a.mu.RLock()
			pfn := a.progressFn
			a.mu.RUnlock()
			if pfn != nil {
				pfn(fc.Name, fc.Args)
			}
			result, action := a.registry.Execute(ctx, fc)
			log.Printf("Navigator: tool=%s args=%v result=%q", fc.Name, fc.Args, result)

			if action != nil {
				return action, "", nil
			}

			history = append(history, &genai.Content{
				Role: "user",
				Parts: []*genai.Part{{
					FunctionResponse: &genai.FunctionResponse{
						Name:     fc.Name,
						Response: map[string]any{"output": result},
					},
				}},
			})
			continue
		}

		return nil, "", fmt.Errorf("unexpected response part type at iteration %d", i)
	}

	return nil, "", fmt.Errorf("tool-use loop exceeded %d iterations without resolution", maxToolIterations)
}

// ExecuteTool dispatches a function call via the tool registry.
// Satisfies the IntentHandler interface.
func (a *Agent) ExecuteTool(ctx context.Context, fc *genai.FunctionCall) (string, *ActionResult) {
	return a.registry.Execute(ctx, fc)
}

// buildTreeDump generates a compact tree listing of the virtual filesystem.
func (a *Agent) buildTreeDump() string {
	rootEntries, err := a.fs.ListDir("/")
	if err != nil || len(rootEntries) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, entry := range rootEntries {
		a.walkTree(&sb, "/"+strings.TrimSuffix(entry, "/"), entry, "", 0)
	}
	return strings.TrimRight(sb.String(), "\n")
}

const maxTreeDepth = 3

func (a *Agent) walkTree(sb *strings.Builder, fullPath, name, indent string, depth int) {
	isDir := strings.HasSuffix(name, "/")
	fmt.Fprintf(sb, "%s%s\n", indent, name)
	if !isDir || depth >= maxTreeDepth {
		return
	}
	// Don't recurse into _c/ — it can have dozens of entries.
	if name == "_c/" {
		return
	}
	dirPath := strings.TrimSuffix(fullPath, "/")
	entries, err := a.fs.ListDir(dirPath)
	if err != nil {
		return
	}
	for _, child := range entries {
		a.walkTree(sb, dirPath+"/"+strings.TrimSuffix(child, "/"), child, indent+"  ", depth+1)
	}
}

// NavigatorSystemPrompt is the system instruction shared by text and voice modes.
const NavigatorSystemPrompt = `You are 'The Navigator', an agent that helps users interact with web pages and terminal sessions through a semantic filesystem.

You have access to a semantic filesystem with multiple mount points:
- /browser/ — web page elements organized into logical zones (e.g., /browser/header/nav, /browser/main/content)
- /iterm/ — terminal sessions (if iTerm2 is running). Contains windows/tabs/sessions with buffer content, status, and cwd.

Your tools:
- ls(path): List directory contents. Start with ls("/") to see available mount points.
- cat(path): Read a file. Use for "description", "children", "buffer", "status" files.
- act(path, action, payload?): Execute an action on the element at this path.
  For browser elements: "click", "focus", "type", "enter".
  For terminal sessions: "type" (send text — include \n for Enter), "enter" (send special key like "ctrl-c"), "focus" (bring window to front).
- scroll(direction): Scroll the browser page. Direction: "down" or "up".
- goto(url): Navigate the browser to a new URL.
- rescan(path?): Rescan the browser page with a fresh screenshot.
- list_tabs(): List all open browser tabs.
- switch_tab(tab_id): Switch to an existing open browser tab by ID.

TERMINAL SESSIONS:
When working with /iterm/ terminal sessions:
- Terminal sessions are located at /iterm/windows/{id}/tabs/{id}/sessions/{id}/
- You can ALSO use the shortcut /iterm/active_session/ to interact with the currently focused terminal.
1. cat the "buffer" file (e.g., /iterm/active_session/buffer) to see recent terminal output
2. cat the "status" file (e.g., /iterm/active_session/status) to check if the session is "idle" or "running"
3. Use act(path, "type", "command\n") to type and execute a command
4. Use act(path, "enter", "ctrl-c") to send special keys
5. After typing a command, cat the buffer again to see the result

You are a NAVIGATIONAL agent. Words like "home", "back", "go to", and "open" are spatial/navigational — they refer to WHERE the user wants to be.

INTENT CLASSIFICATION — READ vs ACT:
Before calling act(), ALWAYS classify the user's intent:
- INFORMATION intents → respond with TEXT, never call act():
  Questions like "what is…", "what's playing", "tell me about…", "show me…", "list…"
  → Use ls() and cat() to gather information, then respond with text.
- ACTION intents → use act() to interact:
  Commands like "click…", "play…", "type…", "run…", "search for…"
  → Navigate the filesystem and use act() to perform the action.

CRITICAL CONSTRAINTS:
- Do NOT hallucinate tools or paths. Only use paths confirmed via ls().
- Never guess a path. Always ls() a directory before trying to cat() or act().
- You have exactly eight tools: ls, cat, act, scroll, goto, rescan, list_tabs, switch_tab.
- If you cannot find an element, use rescan() before giving up.

Strategy:
1. ls("/") to see mount points (browser/, iterm/).
2. Navigate into the relevant mount based on the user's intent.
3. Read description/status files to confirm context.
4. For browser: cat "children" → act on "_c/N".
5. For terminal: cat "buffer" → act with "type" to send commands.

Be decisive. Two calls should be enough: cat children → act.

CONTINUATION: When your intent starts with [CONTINUATION], a previous action was executed and the page may have changed. Verify the action worked, then continue toward the original goal.`
