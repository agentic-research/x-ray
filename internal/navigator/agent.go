package navigator

import (
	"context"
	"fmt"
	"log"
	"math"
	"sort"
	"strconv"
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
	fs         *NavFS              // unified filesystem over CompositeGraph
	hotswap    *graph.HotSwapGraph // thread-safe graph swap (all reads go through this)
	mu         sync.RWMutex
	progressFn func(toolName string, args map[string]any)

	// Viewport state (set after each scroll via SetViewport).
	viewportMu      sync.RWMutex
	viewportScrollY float64
	viewportPageH   float64
	viewportHeight  float64

	registry      *ToolRegistry
	scrollTool    *ScrollTool
	listTabs      *ListTabsTool
	lsTool        *LsTool
	catTool       *CatTool
	actTool       *ActTool
	rescanTool    *RescanTool
	newWindowTool *NewWindowTool
	newTabTool    *NewTabTool
}

// NewAgent creates a Navigator agent backed by a unified graph.Graph.
// The graph is typically a CompositeGraph with mounts like "browser" and "iterm".
func NewAgent(gen ContentGenerator, model string, g graph.Graph) *Agent {
	if model == "" {
		model = "gemini-2.5-flash"
	}

	// Wrap the graph in HotSwapGraph so all reads are RLock-protected
	// and graph replacement is an atomic Swap. NavFS is created once
	// and never replaced — it delegates through the HotSwapGraph.
	hs := graph.NewHotSwapGraph(g)
	fs := NewNavFS(hs)

	ls := &LsTool{fs: fs}
	cat := &CatTool{fs: fs}
	stat := &StatTool{fs: fs}
	act := &ActTool{fs: fs}
	grepTool := &GrepTool{fs: fs}
	scroll := &ScrollTool{}
	goTo := &GotoTool{}
	rescan := &RescanTool{fs: fs}
	listTabs := &ListTabsTool{}
	switchTab := &SwitchTabTool{}
	newWindow := &NewWindowTool{fs: fs}
	newTab := &NewTabTool{fs: fs}

	reg := NewToolRegistry()
	reg.Register(ls)
	reg.Register(cat)
	reg.Register(stat)
	reg.Register(act)
	reg.Register(grepTool)
	reg.Register(scroll)
	reg.Register(goTo)
	reg.Register(rescan)
	reg.Register(listTabs)
	reg.Register(switchTab)
	reg.Register(newWindow)
	reg.Register(newTab)

	a := &Agent{
		generator:     gen,
		model:         model,
		fs:            fs,
		hotswap:       hs,
		registry:      reg,
		scrollTool:    scroll,
		listTabs:      listTabs,
		lsTool:        ls,
		catTool:       cat,
		actTool:       act,
		rescanTool:    rescan,
		newWindowTool: newWindow,
		newTabTool:    newTab,
	}
	// Wire viewport getter so scroll results include position info.
	scroll.getViewport = a.viewportString
	return a
}

// SetGraph atomically swaps the underlying graph (e.g., after remounting
// browser engine). In-flight reads on the old graph complete safely;
// subsequent reads see the new graph. Thread-safe via HotSwapGraph.
func (a *Agent) SetGraph(g graph.Graph) {
	a.hotswap.Swap(g)
}

// SetScrollFunc injects the scroll callback used by the scroll tool.
func (a *Agent) SetScrollFunc(fn func(ctx context.Context, direction string) error) {
	a.scrollTool.mu.Lock()
	defer a.scrollTool.mu.Unlock()
	a.scrollTool.scrollFn = fn
}

// SetViewport stores the current scroll position for display in tree dumps.
func (a *Agent) SetViewport(scrollY, scrollHeight, viewportHeight float64) {
	a.viewportMu.Lock()
	defer a.viewportMu.Unlock()
	a.viewportScrollY = scrollY
	a.viewportPageH = scrollHeight
	a.viewportHeight = viewportHeight
}

// viewportString returns a human-readable viewport position, e.g. "Viewport: 0-45% of page".
func (a *Agent) viewportString() string {
	a.viewportMu.RLock()
	defer a.viewportMu.RUnlock()
	if a.viewportPageH <= 0 {
		return ""
	}
	startPct := int(a.viewportScrollY / a.viewportPageH * 100)
	endPct := int((a.viewportScrollY + a.viewportHeight) / a.viewportPageH * 100)
	if endPct > 100 {
		endPct = 100
	}
	return fmt.Sprintf("Viewport: %d-%d%% of page", startPct, endPct)
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
		SafetySettings: []*genai.SafetySetting{
			{Category: genai.HarmCategoryHarassment, Threshold: genai.HarmBlockThresholdOff},
			{Category: genai.HarmCategoryHateSpeech, Threshold: genai.HarmBlockThresholdOff},
			{Category: genai.HarmCategorySexuallyExplicit, Threshold: genai.HarmBlockThresholdOff},
			{Category: genai.HarmCategoryDangerousContent, Threshold: genai.HarmBlockThresholdOff},
		},
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

	// Set GBNF grammar for constrained decoding (GemmaGenerator only).
	// In readOnly mode, the model needs free text output, so skip grammar.
	if gemma, ok := a.generator.(*GemmaGenerator); ok && gemma.CLIMode && !readOnly {
		paths := EnumeratePaths(a.fs)
		gemma.Grammar = BuildGBNF(paths, readOnly)
		log.Printf("Navigator: GBNF grammar set with %d paths", len(paths))
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
			// PROHIBITED_CONTENT: safety filter false-positive. Retry once.
			if string(candidate.FinishReason) == "PROHIBITED_CONTENT" {
				log.Printf("Navigator: PROHIBITED_CONTENT at iteration %d, retrying", i+1)
				history = append(history, &genai.Content{
					Role:  "user",
					Parts: []*genai.Part{{Text: "Please proceed with the task."}},
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

// buildTreeDump generates a compact tree listing of the virtual filesystem,
// prefixed with an ASCII spatial layout when zone bounds are available.
func (a *Agent) buildTreeDump() string {
	rootEntries, err := a.fs.ListDir("/")
	if err != nil || len(rootEntries) == 0 {
		return ""
	}
	var sb strings.Builder

	// Viewport position — tells the LLM where it is on the page.
	if vp := a.viewportString(); vp != "" {
		sb.WriteString(vp)
		sb.WriteByte('\n')
	}

	// ASCII layout first — gives spatial context before the tree.
	if layout := a.buildASCIILayout(); layout != "" {
		sb.WriteString(layout)
		sb.WriteByte('\n')
	}

	for _, entry := range rootEntries {
		a.walkTree(&sb, "/"+strings.TrimSuffix(entry, "/"), entry, "", 0)
	}
	return strings.TrimRight(sb.String(), "\n")
}

const maxTreeDepth = 3

func (a *Agent) walkTree(sb *strings.Builder, fullPath, name, indent string, depth int) {
	isDir := strings.HasSuffix(name, "/")
	if !isDir {
		// Skip printing the description filename — its content is inlined on the parent dir line.
		if name == "description" {
			return
		}
		fmt.Fprintf(sb, "%s%s\n", indent, name)
		return
	}
	if depth >= maxTreeDepth {
		fmt.Fprintf(sb, "%s%s\n", indent, name)
		return
	}
	// Don't recurse into _c/ — it can have dozens of entries.
	if name == "_c/" {
		fmt.Fprintf(sb, "%s%s\n", indent, name)
		return
	}
	// Inline description content next to zone directory name so the model
	// knows what each zone contains without needing to cat every description.
	dirPath := strings.TrimSuffix(fullPath, "/")
	if desc, err := a.fs.ReadFile(dirPath + "/description"); err == nil && desc != "" {
		fmt.Fprintf(sb, "%s%s — %s\n", indent, name, strings.TrimSpace(desc))
	} else {
		fmt.Fprintf(sb, "%s%s\n", indent, name)
	}
	entries, err := a.fs.ListDir(dirPath)
	if err != nil {
		return
	}
	for _, child := range entries {
		a.walkTree(sb, dirPath+"/"+strings.TrimSuffix(child, "/"), child, indent+"  ", depth+1)
	}
}

// zoneInfo holds parsed zone metadata for the ASCII layout.
type zoneInfo struct {
	path       string
	desc       string
	x, y, w, h float64
}

// buildASCIILayout renders a spatial ASCII grid of browser zones.
// Zones with off-screen or zero bounds are omitted.
func (a *Agent) buildASCIILayout() string {
	// Collect zones with bounds from the browser/ mount.
	var zones []zoneInfo
	a.collectZones("browser", &zones)
	if len(zones) == 0 {
		return ""
	}

	// Clamp to visible viewport [0,1] and filter off-screen zones.
	var visible []zoneInfo
	for _, z := range zones {
		// Clamp to viewport.
		x1 := math.Max(0, z.x)
		y1 := math.Max(0, z.y)
		x2 := math.Min(1, z.x+z.w)
		y2 := math.Min(1, z.y+z.h)
		if x2 <= x1 || y2 <= y1 {
			continue // fully off-screen or zero area
		}
		visible = append(visible, zoneInfo{
			path: z.path, desc: z.desc,
			x: x1, y: y1, w: x2 - x1, h: y2 - y1,
		})
	}
	if len(visible) == 0 {
		return ""
	}

	// Sort by y then x for top-to-bottom, left-to-right layout.
	sort.Slice(visible, func(i, j int) bool {
		if visible[i].y != visible[j].y {
			return visible[i].y < visible[j].y
		}
		return visible[i].x < visible[j].x
	})

	// Render to a character grid.
	const gridW, gridH = 72, 20
	grid := make([][]byte, gridH)
	for i := range grid {
		grid[i] = make([]byte, gridW)
		for j := range grid[i] {
			grid[i][j] = ' '
		}
	}

	for _, z := range visible {
		// Map normalized coords to grid.
		col0 := int(z.x * float64(gridW))
		row0 := int(z.y * float64(gridH))
		col1 := int((z.x + z.w) * float64(gridW))
		row1 := int((z.y + z.h) * float64(gridH))

		// Clamp to grid.
		if col0 < 0 {
			col0 = 0
		}
		if row0 < 0 {
			row0 = 0
		}
		if col1 > gridW {
			col1 = gridW
		}
		if row1 > gridH {
			row1 = gridH
		}
		if col1-col0 < 2 || row1-row0 < 1 {
			continue // too small to render
		}

		// Draw border.
		for c := col0; c < col1; c++ {
			if row0 < gridH {
				grid[row0][c] = '-'
			}
			if row1-1 < gridH {
				grid[row1-1][c] = '-'
			}
		}
		for r := row0; r < row1; r++ {
			if col0 < gridW {
				grid[r][col0] = '|'
			}
			if col1-1 < gridW {
				grid[r][col1-1] = '|'
			}
		}

		// Label: zone path (trimmed to fit).
		label := z.path
		maxLabel := col1 - col0 - 2
		if maxLabel < 1 {
			continue
		}
		if len(label) > maxLabel {
			label = label[:maxLabel]
		}
		labelRow := row0
		if labelRow < gridH {
			for i, ch := range label {
				if col0+1+i < col1-1 {
					grid[labelRow][col0+1+i] = byte(ch)
				}
			}
		}

		// Description on next row if space.
		if row0+1 < row1-1 && z.desc != "" {
			desc := z.desc
			if len(desc) > maxLabel {
				desc = desc[:maxLabel]
			}
			for i, ch := range desc {
				if col0+1+i < col1-1 {
					grid[row0+1][col0+1+i] = byte(ch)
				}
			}
		}
	}

	// Render grid to string, trimming trailing whitespace per row.
	var sb strings.Builder
	sb.WriteString("Page layout:\n")
	for _, row := range grid {
		line := strings.TrimRight(string(row), " ")
		if line != "" {
			sb.WriteString(line)
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

// collectZones recursively finds zone directories with bounds under the given path.
func (a *Agent) collectZones(dirPath string, zones *[]zoneInfo) {
	entries, err := a.fs.ListDir(dirPath)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry, "/") {
			continue
		}
		name := strings.TrimSuffix(entry, "/")
		childPath := dirPath + "/" + name
		if name == "_c" {
			continue
		}

		// Check for bounds property.
		if boundsStr, ok := a.fs.GetProperty(childPath, "bounds"); ok {
			desc, _ := a.fs.ReadFile(childPath + "/description")
			x, y, w, h := parseBounds(boundsStr)
			if w > 0 && h > 0 {
				// Use path relative to browser/ for display.
				displayPath := strings.TrimPrefix(childPath, "browser/")
				*zones = append(*zones, zoneInfo{
					path: displayPath, desc: desc,
					x: x, y: y, w: w, h: h,
				})
			}
		}

		// Recurse into subdirectories.
		a.collectZones(childPath, zones)
	}
}

// parseBounds extracts [x,y,w,h] from a bounds string like "[0.123,0.456,0.789,0.234]".
func parseBounds(s string) (x, y, w, h float64) {
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return x, y, w, h
	}
	x, _ = strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	y, _ = strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	w, _ = strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
	h, _ = strconv.ParseFloat(strings.TrimSpace(parts[3]), 64)
	return x, y, w, h
}

// NavigatorSystemPrompt is the system instruction shared by text and voice modes.
const NavigatorSystemPrompt = `You are 'The Navigator', an agent that helps users interact with web pages and terminal sessions through a semantic filesystem.

You have access to a semantic filesystem with multiple mount points:
- /browser/ — web page elements organized into logical zones (e.g., /browser/header/nav, /browser/main/content)
- /iterm/ — terminal sessions (if iTerm2 is running). Contains windows/tabs/sessions with buffer content, status, and cwd.
- /focus/ — a dynamic symlink that automatically routes to the currently active application (e.g., Chrome or iTerm2). Use this when the user says "what am I looking at" or "click this in my active window".

Your tools:

General (work on any mount):
- ls(path): List directory contents. Start with ls("/") to see available mount points.
- cat(path): Read a file. Use for "description", "children", "buffer", "status" files.
- stat(path): Show size info (chars/lines for files, child count for dirs). Use before cat() to gauge content size.
- grep(pattern): Search ALL children and description files for a pattern (case-insensitive, regex OK). Use SHORT keywords (1-2 words max), never full phrases. Supports | for alternatives: grep("review|rating"). To read full zone content, use cat() instead.
- act(path, action, payload?): Execute an action on the element at this path.
  For browser elements: "click", "focus", "type", "enter".
  For terminal sessions: "type" (send text — include \n for Enter), "enter" (send special key like "ctrl-c"), "focus" (bring window to front).

Browser-scoped (only affect the /browser/ mount):
- browser.scroll(direction): Scroll the browser page. Direction: "down" or "up".
- browser.goto(url): Navigate the browser to a new URL. Triggers a visual refresh of the /browser/ mount.
- browser.rescan(path?): Rescan the browser page with a fresh screenshot. Triggers a visual refresh.
- browser.list_tabs(): List all open browser tabs.
- browser.switch_tab(tab_id): Switch to an existing open browser tab by ID.

Terminal-scoped (only affect the /iterm/ mount):
- iterm.new_window(): Open a new iTerm2 terminal window.
- iterm.new_tab(window_path?): Open a new tab in a terminal window. Omit window_path for the first window.

TERMINAL SESSIONS:
When working with /iterm/ terminal sessions:
- Terminal sessions are located at /iterm/windows/{id}/tabs/{id}/sessions/{id}/
- You can ALSO use the shortcut /iterm/active_session/ to interact with the currently focused terminal.
1. cat the "buffer" file (e.g., /iterm/active_session/buffer) to see recent terminal output
2. cat the "status" file (e.g., /iterm/active_session/status) to check if the session is "idle" or "running"
3. Use act(path, "type", "command\n") to type and execute a command
4. Use act(path, "enter", "ctrl-c") to send special keys
5. To spawn a new terminal window, use iterm.new_window()
6. To spawn a new tab, use iterm.new_tab("/iterm/windows/{id}")
7. After typing a command, cat the buffer again to see the result

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
- You have exactly twelve tools: ls, cat, stat, act, grep, browser.scroll, browser.goto, browser.rescan, browser.list_tabs, browser.switch_tab, iterm.new_window, iterm.new_tab.
- If you cannot find an element, use browser.rescan() before giving up.
- When told to "search" or "find" text, ALWAYS use the grep tool to scan DOM content. Do NOT type into the website's search bar unless explicitly told to "type into the search bar".

Strategy:
1. ls("/") to see mount points (browser/, iterm/).
2. Navigate into the relevant mount based on the user's intent.
3. For INFORMATION RETRIEVAL: grep a single distinctive keyword. If no match, try a shorter/broader keyword before scrolling or rescanning.
4. For browser ACTIONS: cat "children" → act on "_c/N".
5. For terminal: cat "buffer" → act with "type" to send commands.

Be decisive. One grep call should find what you need. For actions: cat children → act.

CONTINUATION: When your intent starts with [CONTINUATION], focus on the OVERALL TASK — the previous action is already done. Read the page content to extract the answer or take the next step toward the overall task.`
