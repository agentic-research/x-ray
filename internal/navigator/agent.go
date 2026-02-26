package navigator

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/jamesgardner/x-ray/internal/mache"
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
	engine     *mache.Engine
	progressFn func(toolName string, args map[string]any)

	registry   *ToolRegistry
	scrollTool *ScrollTool
	listTabs   *ListTabsTool
	lsTool     *LsTool
	catTool    *CatTool
	actTool    *ActTool
	rescanTool *RescanTool
}

func NewAgent(gen ContentGenerator, model string, engine *mache.Engine) *Agent {
	if model == "" {
		model = "gemini-2.5-flash"
	}

	ls := &LsTool{engine: engine}
	cat := &CatTool{engine: engine}
	act := &ActTool{engine: engine}
	scroll := &ScrollTool{}
	goTo := &GotoTool{}
	rescan := &RescanTool{engine: engine}
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
		engine:     engine,
		registry:   reg,
		scrollTool: scroll,
		listTabs:   listTabs,
		lsTool:     ls,
		catTool:    cat,
		actTool:    act,
		rescanTool: rescan,
	}
}

// SetEngine updates the engine when a new schema is applied.
func (a *Agent) SetEngine(engine *mache.Engine) {
	a.engine = engine
	a.lsTool.engine = engine
	a.catTool.engine = engine
	a.actTool.engine = engine
	a.rescanTool.engine = engine
}

// SetScrollFunc injects the scroll callback used by the scroll tool.
func (a *Agent) SetScrollFunc(fn func(ctx context.Context, direction string) error) {
	a.scrollTool.scrollFn = fn
}

// SetProgressFunc injects a callback fired before each tool execution,
// allowing the Doer to report its current step to the Talker.
func (a *Agent) SetProgressFunc(fn func(toolName string, args map[string]any)) {
	a.progressFn = fn
}

// SetListTabsFunc injects the callback used by the list_tabs tool.
func (a *Agent) SetListTabsFunc(fn func(ctx context.Context) ([]TabInfo, error)) {
	a.listTabs.listTabsFn = fn
}

const maxToolIterations = 8

// HandleIntent processes a user intent by navigating the semantic FS.
// Returns an ActionResult if the agent acts, or a text response otherwise.
func (a *Agent) HandleIntent(ctx context.Context, intent string) (*ActionResult, string, error) {
	log.Printf("Navigator: Handling intent: %s", intent)

	config := &genai.GenerateContentConfig{
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{{Text: NavigatorSystemPrompt}},
		},
		Tools:       a.registry.Definitions(),
		Temperature: genai.Ptr(float32(0.1)),
	}

	// Pre-fill a tree dump so the model sees the full filesystem structure upfront.
	// This prevents small models from guessing zone names (e.g., /main/story_list
	// when the actual path is /main/feed) and wasting iterations on 404s.
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

			if a.progressFn != nil {
				a.progressFn(fc.Name, fc.Args)
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
// Shows zone structure (2 levels) with leaf contents but skips recursing into
// _c/ subdirectories — the model uses "children" for item details.
func (a *Agent) buildTreeDump() string {
	var sb strings.Builder
	rootEntries, err := a.engine.ListDir("/")
	if err != nil {
		return "(empty filesystem)"
	}
	for _, entry := range rootEntries {
		a.walkTree(&sb, "/"+strings.TrimSuffix(entry, "/"), entry, "", 0)
	}
	return strings.TrimRight(sb.String(), "\n")
}

const maxTreeDepth = 3 // root → top-level → zone → zone contents

func (a *Agent) walkTree(sb *strings.Builder, fullPath, name, indent string, depth int) {
	isDir := strings.HasSuffix(name, "/")
	fmt.Fprintf(sb, "%s%s\n", indent, name)
	if !isDir || depth >= maxTreeDepth {
		return
	}
	// Don't recurse into _c/ — it can have dozens of entries.
	// The model reads the "children" file instead.
	if name == "_c/" {
		return
	}
	dirPath := strings.TrimSuffix(fullPath, "/")
	entries, err := a.engine.ListDir(dirPath)
	if err != nil {
		return
	}
	for _, child := range entries {
		a.walkTree(sb, dirPath+"/"+strings.TrimSuffix(child, "/"), child, indent+"  ", depth+1)
	}
}

// NavigatorSystemPrompt is the system instruction shared by text and voice modes.
const NavigatorSystemPrompt = `You are 'The Navigator', an agent that helps users interact with web pages through a semantic filesystem.

You have access to a semantic filesystem that represents the current web page. The filesystem organizes interactive elements into logical zones (e.g., /header/nav, /main/content, /sidebar/filters).

Your tools:
- ls(path): List directory contents. Always start with ls("/") to see the top-level zones.
- cat(path): Read a file. Use this to read "description" or "children" files.
- act(path, action, payload?): Execute a browser action on the element at this path. Actions: "click", "focus", "type", "enter". For "type", include the text as the payload parameter. For "enter", dispatches an Enter keypress (useful for search bars without a visible submit button).
- scroll(direction): Scroll the page to load more content. Direction: "down" or "up".
- goto(url): Navigate the browser to a new URL. After navigation, the filesystem updates — run ls("/") to explore the new page.
- rescan(path?): Rescan the page with a fresh screenshot. Without a path, rescans the full page. With a path (e.g., rescan("/main/player")), zooms into that zone for higher detail — discovers internal controls like play buttons, volume sliders, etc. Use when you can't find an element or need finer detail within a zone.
- list_tabs(): List all open browser tabs (ID, title, URL). ALWAYS call this before goto() — if the site is already open in another tab, use switch_tab() instead. Switching is instant; navigating reloads.
- switch_tab(tab_id): Switch to an existing open tab by ID. The filesystem updates to reflect the new page.

You are a NAVIGATIONAL agent. Words like "home", "back", "go to", and "open" are spatial/navigational — they refer to WHERE the user wants to be, not WHAT to click on the current page. When the user says "go home" or "take me home", they mean navigate to the site's homepage using goto(). Derive the homepage from the current domain (e.g., on reddit.com/r/news → goto("https://www.reddit.com")).

INTENT CLASSIFICATION — READ vs ACT:
Before calling act(), ALWAYS classify the user's intent:
- INFORMATION intents → respond with TEXT, never call act():
  Questions like "what is…", "what was I…", "what's playing", "tell me about…",
  "show me…", "list…", "which…", "how many…", "what's on the page", "describe…",
  "read…", "what are my options", "what do you see".
  → Use ls() and cat() to gather information, then respond with a text answer. Do NOT click.
- ACTION intents → use act() to interact:
  Commands like "click…", "play…", "open…", "go to…", "search for…", "type…",
  "select…", "press…", "subscribe…", "pause…", "skip…", "next…", "close…".
  → Navigate the filesystem and use act() to perform the requested action.
If the intent is informational, you MUST stop after reading and reply with text. Never click "just in case".

CRITICAL CONSTRAINTS:
- Do NOT hallucinate tools or paths. Only use paths that you have confirmed exist via ls().
- Never guess a path. Always ls() a directory before trying to cat() or act() on its children.
- You have exactly eight tools: ls, cat, act, scroll, goto, rescan, list_tabs, switch_tab. Do not attempt to use any other tool.
- If you cannot find an element after exploring the filesystem, use rescan() before giving up. The rescan captures a fresh screenshot and may discover elements that weren't in the original scan. If you can see the zone but need finer detail (e.g., video player controls), use rescan("/path/to/zone") to zoom in.

Strategy:
1. ls("/") to see the page structure.
2. Navigate into the most relevant zone based on the user's intent.
3. Read the "description" file to confirm you've found the right zone.
4. If the user needs a specific element inside the zone (e.g., "click the first story"):
   a. cat the zone's "children" file. Each line is: [N] "text"
   b. The number in brackets is the item number. Use it as the _c/ path.
      Example: to click the 3rd item, act on "_c/3".
   c. act on "_c/N" inside that zone to target the specific child element.
5. If the zone has no "children" file, or the zone itself is the target, act on the zone path directly.
6. If the user asks for an item beyond what's visible (e.g., "click the 10th post" but only 3 shown), scroll("down") to load more content, then cat the children file again.

Example workflow for "click the first story" on a news page:
  ls("/") already shows the full tree:
    header/
      nav/
        description  mache_id
    main/
      feed/
        _c/  children  description  mache_id
    footer/
      description  mache_id
  cat("/main/feed/children")           → [1] "First Story Title"
                                         [2] "Second Story Title"
  act("/main/feed/_c/1", "click")      → clicks the first story

Example workflow for "search for Golang tutorials" on YouTube:
  ls("/") shows: header/ main/ sidebar/
  cat("/header/search_bar/description")  → "Search input field"
  act("/header/search_bar", "type", "Golang tutorials")
  act("/header/search_bar", "enter")

Be decisive. You already know the full tree from ls("/"). Two calls should be enough: cat children → act.
If you need more items, add scroll → cat children → act (up to 8 iterations total).

CONTINUATION: When your intent starts with [CONTINUATION], a previous action was executed and the page may have changed. The filesystem reflects the current state. First, VERIFY the previous action worked: use ls/cat to check the page actually changed as expected. If you clicked a button and it's still there, it may have failed — try a different approach. Then continue toward the original goal: if you can answer by reading page content, respond with text. If more actions are needed, take the next step.`
