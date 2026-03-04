package navigator

import (
	"context"
	"fmt"
	"log"
	"math"
	"os"
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

	sectionMu    sync.RWMutex
	sectionHints string

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

// SetSectionHints stores pre-computed section hints to be injected into the
// next HandleIntent tree dump. Thread-safe; cleared after each HandleIntent call.
func (a *Agent) SetSectionHints(hints string) {
	a.sectionMu.Lock()
	defer a.sectionMu.Unlock()
	a.sectionHints = hints
}

// SetListTabsFunc injects the callback used by the list_tabs tool.
func (a *Agent) SetListTabsFunc(fn func(ctx context.Context) ([]TabInfo, error)) {
	a.listTabs.mu.Lock()
	defer a.listTabs.mu.Unlock()
	a.listTabs.listTabsFn = fn
}

const maxToolIterations = 20

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
		Temperature: genai.Ptr(float32(1.0)),
		SafetySettings: []*genai.SafetySetting{
			{Category: genai.HarmCategoryHarassment, Threshold: genai.HarmBlockThresholdOff},
			{Category: genai.HarmCategoryHateSpeech, Threshold: genai.HarmBlockThresholdOff},
			{Category: genai.HarmCategorySexuallyExplicit, Threshold: genai.HarmBlockThresholdOff},
			{Category: genai.HarmCategoryDangerousContent, Threshold: genai.HarmBlockThresholdOff},
		},
	}

	// Pre-fill a tree dump so the model sees the full filesystem structure upfront.
	treeDump := a.buildTreeDump()

	// Inject section hints (previously successful actions) if available.
	a.sectionMu.RLock()
	hints := a.sectionHints
	a.sectionMu.RUnlock()
	if hints != "" {
		treeDump += "\n\n" + hints
	}

	if os.Getenv("XRAY_DEBUG") == "1" {
		log.Printf("Navigator: pre-filled tree:\n%s", treeDump)
	}

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
	if layout := a.BuildASCIILayout(); layout != "" {
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

// elemInfo holds a single clickable/text element for the ASCII render.
type elemInfo struct {
	ordinal int
	text    string
	tag     string
	x, y    float64 // center position (normalized)
	w, h    float64
}

// buildASCIILayout renders a spatial ASCII grid showing actual page elements
// with clickable ordinal IDs. Instead of empty zone boxes, the LLM sees
// "[1] Home  [2] Electronics  [3] Cameras" painted at their real positions,
// like a Lynx-style text rendering of the page.
func (a *Agent) BuildASCIILayout() string {
	// Collect zones with bounds from the browser/ mount.
	var zones []zoneInfo
	a.collectZones("browser", &zones)
	if len(zones) == 0 {
		return ""
	}

	// Collect all child elements across all zones.
	var elems []elemInfo
	for _, z := range zones {
		a.collectZoneElements(z.path, &elems)
	}

	// Clamp zones to visible viewport [0,1].
	var visible []zoneInfo
	for _, z := range zones {
		x1 := math.Max(0, z.x)
		y1 := math.Max(0, z.y)
		x2 := math.Min(1, z.x+z.w)
		y2 := math.Min(1, z.y+z.h)
		if x2 <= x1 || y2 <= y1 {
			continue
		}
		visible = append(visible, zoneInfo{
			path: z.path, desc: z.desc,
			x: x1, y: y1, w: x2 - x1, h: y2 - y1,
		})
	}

	if len(visible) == 0 && len(elems) == 0 {
		return ""
	}

	// Render to a character grid.
	const gridW, gridH = 80, 24
	grid := make([][]byte, gridH)
	for i := range grid {
		grid[i] = make([]byte, gridW)
		for j := range grid[i] {
			grid[i][j] = ' '
		}
	}

	// 1. Draw zone borders (light dotted lines).
	for _, z := range visible {
		col0 := int(z.x * float64(gridW))
		row0 := int(z.y * float64(gridH))
		col1 := int((z.x + z.w) * float64(gridW))
		row1 := int((z.y + z.h) * float64(gridH))
		col0 = clampInt(col0, 0, gridW-1)
		row0 = clampInt(row0, 0, gridH-1)
		col1 = clampInt(col1, 1, gridW)
		row1 = clampInt(row1, 1, gridH)
		if col1-col0 < 2 || row1-row0 < 1 {
			continue
		}
		// Horizontal borders.
		for c := col0; c < col1; c++ {
			grid[row0][c] = '-'
			if row1-1 < gridH {
				grid[row1-1][c] = '-'
			}
		}
		// Vertical borders.
		for r := row0; r < row1; r++ {
			grid[r][col0] = '|'
			if col1-1 < gridW {
				grid[r][col1-1] = '|'
			}
		}
	}

	// 2. Paint elements: "[ordinal] text" at their grid position.
	// Filter off-screen elements and sort by y then x.
	var onScreen []elemInfo
	for _, el := range elems {
		// Skip elements fully outside the [0,1] viewport.
		if el.y+el.h <= 0 || el.y >= 1 || el.x+el.w <= 0 || el.x >= 1 {
			continue
		}
		onScreen = append(onScreen, el)
	}
	sort.Slice(onScreen, func(i, j int) bool {
		if onScreen[i].y != onScreen[j].y {
			return onScreen[i].y < onScreen[j].y
		}
		return onScreen[i].x < onScreen[j].x
	})

	for _, el := range onScreen {
		if el.text == "" {
			continue
		}
		// Map element position to grid, nudging 1 col right to avoid zone border.
		col := int(el.x*float64(gridW)) + 1
		row := int(el.y * float64(gridH))
		col = clampInt(col, 1, gridW-1)
		row = clampInt(row, 0, gridH-1)

		// Format: [ordinal] text
		label := fmt.Sprintf("[%d] %s", el.ordinal, el.text)
		// Truncate to fit in the grid from this position.
		maxLen := gridW - col
		if maxLen <= 0 {
			continue
		}
		if len(label) > maxLen {
			label = label[:maxLen]
		}

		// Paint label, skipping cells already occupied by earlier elements.
		for i := 0; i < len(label); i++ {
			c := col + i
			if c >= gridW {
				break
			}
			if grid[row][c] != ' ' && grid[row][c] != '-' {
				// Cell occupied — try to nudge down one row.
				if row+1 < gridH && grid[row+1][c] == ' ' {
					row++
					i-- // retry this character on the new row
					continue
				}
				continue // skip collision
			}
			grid[row][c] = label[i]
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

// collectZoneElements reads the _c/ children of a zone and extracts
// ordinal, text, tag, and bounds for the ASCII render.
func (a *Agent) collectZoneElements(zonePath string, elems *[]elemInfo) {
	cPath := "browser/" + zonePath + "/_c"
	entries, err := a.fs.ListDir(cPath)
	if err != nil {
		return
	}
	for _, entry := range entries {
		ordStr := strings.TrimSuffix(entry, "/")
		ordinal, err := strconv.Atoi(ordStr)
		if err != nil {
			continue
		}
		childPath := cPath + "/" + ordStr

		text, _ := a.fs.ReadFile(childPath + "/text")
		tag, _ := a.fs.ReadFile(childPath + "/tag")
		boundsStr, _ := a.fs.ReadFile(childPath + "/bounds")

		text = strings.TrimSpace(text)
		if text == "" {
			// Fall back to AX name for elements with no visible text.
			text, _ = a.fs.ReadFile(childPath + "/name")
			text = strings.TrimSpace(text)
		}
		if text == "" {
			continue
		}
		// Truncate long text to keep the grid readable.
		if len(text) > 25 {
			text = text[:22] + "..."
		}

		var x, y, w, h float64
		if boundsStr != "" {
			x, y, w, h = parseBounds(boundsStr)
		}
		if w <= 0 || h <= 0 {
			continue // no valid bounds — skip
		}

		*elems = append(*elems, elemInfo{
			ordinal: ordinal,
			text:    text,
			tag:     strings.TrimSpace(tag),
			x:       x, y: y, w: w, h: h,
		})
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
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
- /tasks/active/ — your working memory. cat task to see current goal. Use act("/tasks/active/scratch", "type", "text") to record findings across steps.
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

TWO REFERENCE SYSTEMS — do NOT confuse them:
  children file: [N] label  →  click via ZONE PATH: act("/browser/main/feed/_c/N", "click")
  text_index:    [mache-N] label  →  click via BARE ID: act("mache-N", "click")
  ⚠ [16] in children is ordinal 16 — it is NOT mache-16! NEVER convert [N] to mache-N.

Strategy:
1. ls("/") to see mount points (browser/, iterm/).
2. Navigate into the relevant mount based on the user's intent.
3. For INFORMATION RETRIEVAL: grep a single distinctive keyword. If no match, try a shorter/broader keyword before scrolling or rescanning.
4. For browser ACTIONS: cat "children" of the target zone, find the item by its ordinal [N], then act on the ZONE PATH with _c/N (e.g., act("/browser/main/feed/_c/3", "click")). If grep fails to find a tab/button, ALWAYS cat children next — do NOT rescan immediately.
5. For terminal: cat "buffer" → act with "type" to send commands.

Be decisive. One grep call should find what you need. If grep fails for an action target, cat children to find it — do NOT rescan or give up.

SEMI-FORMAL REASONING — think in three steps before every action:
1. PREMISES: State what you know. "I see /browser/main/content with 5 children. grep('review') found 3 matches in text_index."
2. TRACE: Project the outcome. "Clicking _c/7 (Reviews tab) should reveal review content below."
3. CONCLUSION: Decide. "Action: act('/browser/main/content/_c/7', click). Expected: reviews appear."
This prevents wasted actions — never click without knowing WHY it leads to the goal.

GREP STRATEGY:
- Use SHORT keywords (1-2 words), never full phrases. "ear cups being small" → grep("small|ear cups").
- Use regex OR: grep("price|cost"), grep("review|rating"), grep("small|tiny|little").
- Think about SYNONYMS: if the task says "small", also try "tiny|little|compact".
- grep results from text_index show [mache-N] tags — click via bare ID: act("mache-42", "click").
- grep also searches page_text (full visible text) for content not in interactive elements.
- When grep returns long text blocks, read them carefully for ALL relevant names/items, not just the search term.

CLICKING RULES:
- From children [N] → act("<zone_path>/_c/N", "click")  — ALWAYS use the full zone path.
- From text_index [mache-N] → act("mache-N", "click")  — use the bare mache ID.
- NEVER mix them: children [16] does NOT mean mache-16. They are unrelated.

SCRATCHPAD — MANDATORY for multi-item collection:
- When finding names, items, or data: IMMEDIATELY save each finding to the scratchpad:
  act("/tasks/active/scratch", "type", "Found: <name/item>")
- Do this BEFORE navigating to the next page or clicking anything else.
- The scratchpad persists across continuations — it is your working memory.
- Before answering, cat("/tasks/active/scratch") to collect ALL findings.

EXTRACTING NAMES — when the task asks "who" or "name(s)":
- grep finds matching TEXT, but you still need the AUTHOR/REVIEWER NAME.
- After finding matching content, cat page_text and look for "Review by <name>" or author attribution NEAR each matching review.
- Save each name to the scratchpad immediately.

COMPLETENESS — before reporting an answer:
- Check for pagination: grep("next|page|>>"). If found, navigate ALL pages.
- Compare counts: if page says "12 Reviews" but you've only seen 5, keep looking.
- When collecting names/items from text, read the FULL content (cat page_text) and extract ALL matches, not just grep hits. People phrase things differently.
- Accumulate findings across pages/sections before answering.

CONTINUATION: When your intent starts with [CONTINUATION], focus on the OVERALL TASK — the previous action is already done. Read the page content to extract the answer or take the next step toward the overall task.`
