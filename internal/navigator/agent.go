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
	resultFn   func(toolName string, args map[string]any, result string)

	// Viewport state (set after each scroll via SetViewport).
	viewportMu      sync.RWMutex
	viewportScrollY float64
	viewportPageH   float64
	viewportHeight  float64

	sectionMu    sync.RWMutex
	sectionHints string

	screenshotMu   sync.RWMutex
	screenshotData []byte // overlay screenshot (with mache-ID boxes)
	screenshotMIME string // "image/png" or "image/jpeg"

	FastMode bool // when true, strip ls/cat/stat tools and reduce iterations for low-latency voice

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

// GetViewport returns the current viewport as (startPct, endPct) in 0-100 range.
// Returns (0, 0) if no viewport has been set.
func (a *Agent) GetViewport() (startPct, endPct int) {
	a.viewportMu.RLock()
	defer a.viewportMu.RUnlock()
	if a.viewportPageH <= 0 {
		return 0, 0
	}
	startPct = int(a.viewportScrollY / a.viewportPageH * 100)
	endPct = int((a.viewportScrollY + a.viewportHeight) / a.viewportPageH * 100)
	if endPct > 100 {
		endPct = 100
	}
	return startPct, endPct
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

// SetResultFunc injects a callback fired after each tool execution,
// carrying the tool name, args, and the result string.
func (a *Agent) SetResultFunc(fn func(toolName string, args map[string]any, result string)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.resultFn = fn
}

// SetRefValidateFunc installs a guardrail that validates act() paths before
// execution. The function should return an error message to block the action,
// or "" to allow it.
func (a *Agent) SetRefValidateFunc(fn func(path string) string) {
	a.actTool.refMu.Lock()
	defer a.actTool.refMu.Unlock()
	a.actTool.refValidateFn = fn
}

// SetSectionHints stores pre-computed section hints to be injected into the
// next HandleIntent tree dump. Thread-safe; cleared after each HandleIntent call.
func (a *Agent) SetSectionHints(hints string) {
	a.sectionMu.Lock()
	defer a.sectionMu.Unlock()
	a.sectionHints = hints
}

// SetScreenshot stores the overlay screenshot for visual grounding.
// Called by the Doer before each HandleIntent so the Navigator can see the page.
func (a *Agent) SetScreenshot(data []byte, mime string) {
	a.screenshotMu.Lock()
	defer a.screenshotMu.Unlock()
	a.screenshotData = data
	a.screenshotMIME = mime
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

	// Tear down Live session after each intent (GeminiLiveGenerator only).
	if closer, ok := a.generator.(interface{ Close() }); ok {
		defer closer.Close()
	}

	tools := a.registry.Definitions()
	if readOnly {
		tools = a.registry.DefinitionsExcluding("act")
	} else if a.FastMode {
		// Fast mode: strip exploration tools so model acts directly from pre-filled tree dump.
		tools = a.registry.DefinitionsExcluding("ls", "cat", "stat")
	}

	config := &genai.GenerateContentConfig{
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{{Text: NavigatorSystemPrompt}},
		},
		Tools:       tools,
		Temperature: genai.Ptr(float32(1.0)),
		// Force tool calls — never return text. Prevents Navigator from
		// narrating "now playing" instead of actually clicking.
		ToolConfig: &genai.ToolConfig{
			FunctionCallingConfig: &genai.FunctionCallingConfig{
				Mode: genai.FunctionCallingConfigModeAny,
			},
		},
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

	// Grab overlay screenshot for visual grounding (Gemini cloud only — local
	// models like Ollama/Gemma are text-only and can't process images).
	var screenshotData []byte
	var screenshotMIME string
	switch a.generator.(type) {
	case *GeminiGenerator, *GeminiLiveGenerator:
		a.screenshotMu.Lock()
		screenshotData = a.screenshotData
		screenshotMIME = a.screenshotMIME
		a.screenshotMu.Unlock()
	}

	var history []*genai.Content
	if treeDump == "" {
		// No page loaded yet — tell the model so it knows to use goto().
		history = []*genai.Content{
			{Role: "user", Parts: []*genai.Part{{Text: intent + "\n\n[No page is currently loaded. Use goto(url) to navigate to a website first.]"}}},
		}
	} else {
		// Build user message with intent, tree dump, and optional overlay screenshot.
		// We include the tree dump directly in the user message instead of faking a
		// model function call round-trip, because thinking models (e.g. gemini-3.1-flash-lite)
		// require thought signatures on all model turns with function calls.
		userParts := []*genai.Part{{Text: intent + "\n\nCurrent page layout (output of `ls /`):\n" + treeDump}}
		if len(screenshotData) > 0 && screenshotMIME != "" {
			userParts = append(userParts, &genai.Part{
				InlineData: &genai.Blob{
					MIMEType: screenshotMIME,
					Data:     screenshotData,
				},
			})
			log.Printf("Navigator: injected %d byte overlay screenshot (%s)", len(screenshotData), screenshotMIME)
		}

		history = []*genai.Content{
			{Role: "user", Parts: userParts},
		}
	}

	// Set GBNF grammar for constrained decoding (GemmaGenerator only).
	// In readOnly mode, the model needs free text output, so skip grammar.
	if gemma, ok := a.generator.(*GemmaGenerator); ok && gemma.CLIMode && !readOnly {
		paths := EnumeratePaths(a.fs)
		gemma.Grammar = BuildGBNF(paths, readOnly)
		log.Printf("Navigator: GBNF grammar set with %d paths", len(paths))
	}

	iterCap := maxToolIterations
	if a.FastMode {
		iterCap = 8
	}
	maxRetries := 5 // extra budget for malformed/prohibited retries (don't eat real iterations)
	retries := 0
	realIter := 0
	for realIter < iterCap {

		log.Printf("Navigator: tool-use iteration %d/%d", realIter+1, iterCap)

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
			// invalid JSON. Nudge it to retry — does NOT count against iteration cap.
			if candidate.FinishReason == genai.FinishReasonMalformedFunctionCall {
				retries++
				log.Printf("Navigator: malformed function call (retry %d/%d, not counting against %d iterations)", retries, maxRetries, iterCap)
				if retries > maxRetries {
					return nil, "", fmt.Errorf("too many malformed function calls (%d)", retries)
				}
				history = append(history, &genai.Content{
					Role:  "user",
					Parts: []*genai.Part{{Text: "Your function call was malformed. Please try again with valid JSON arguments."}},
				})
				continue
			}
			// PROHIBITED_CONTENT: safety filter false-positive. Retry — does NOT count.
			if string(candidate.FinishReason) == "PROHIBITED_CONTENT" {
				retries++
				log.Printf("Navigator: PROHIBITED_CONTENT (retry %d/%d, not counting against iterations)", retries, maxRetries)
				if retries > maxRetries {
					return nil, "", fmt.Errorf("too many prohibited content retries (%d)", retries)
				}
				history = append(history, &genai.Content{
					Role:  "user",
					Parts: []*genai.Part{{Text: "Please proceed with the task."}},
				})
				continue
			}
			return nil, "", fmt.Errorf("empty response from model (finish_reason: %v)", candidate.FinishReason)
		}
		realIter++ // Only count iterations with actual content

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
				// Shallow copy to avoid mutating fc.Args (race + unexpected keys in tools).
				argsCopy := make(map[string]any, len(fc.Args)+1)
				for k, v := range fc.Args {
					argsCopy[k] = v
				}
				argsCopy["_iter"] = fmt.Sprintf("%d/%d", realIter, iterCap)
				pfn(fc.Name, argsCopy)
			}
			result, action := a.registry.Execute(ctx, fc)
			log.Printf("Navigator: tool=%s args=%v result=%q", fc.Name, fc.Args, result)

			a.mu.RLock()
			rfn := a.resultFn
			a.mu.RUnlock()
			if rfn != nil {
				rfn(fc.Name, fc.Args, result)
			}

			if action != nil {
				return action, "", nil
			}

			history = append(history, &genai.Content{
				Role: "user",
				Parts: []*genai.Part{{
					FunctionResponse: &genai.FunctionResponse{
						ID:       fc.ID,
						Name:     fc.Name,
						Response: map[string]any{"output": result},
					},
				}},
			})
			continue
		}

		// Model returned parts with no function call and no text (e.g., thinking-only).
		// Instead of crashing, nudge it to continue.
		log.Printf("Navigator: no actionable parts at iteration %d, nudging model", realIter)
		history = append(history, candidate.Content)
		history = append(history, &genai.Content{
			Role:  "user",
			Parts: []*genai.Part{{Text: "Please provide a function call or a text answer."}},
		})
		continue
	}

	return nil, "", fmt.Errorf("tool-use loop exceeded %d iterations without resolution", iterCap)
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

const maxTreeDepth = 7

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
- /browser/ — web page elements organized into spatial zones. Zone paths encode position: header/ = top of page, main/ = primary content area, sidebar/ = side panel, footer/ = bottom. Items in header/ appear first on the page.
- /iterm/ — terminal sessions (if iTerm2 is running). Contains windows/tabs/sessions with buffer content, status, and cwd.
- /interactions/active/ — your working memory. cat task to see current goal. Use act("/interactions/active/scratch", "type", "text") to record findings across steps.
  When the information genuinely doesn't exist after exhausting all navigation options: act("/interactions/active/status", "type", "failed:not found on page")
  When you have a DEFINITIVE answer, just respond with the answer text — completion is detected automatically. Do NOT call act() on status for success.
- /focus/ — Use this as a path prefix (e.g., /focus/main/...) when the user doesn't specify "browser" or "terminal". It automatically routes to the active macOS app (Chrome → /browser/, iTerm2 → /iterm/). Do NOT ls /focus/ directly. Use for ambiguous commands like "what am I looking at", "click this", "scroll down".

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
CRITICAL — NEVER type into a "running" session. The system will REJECT the command. ALWAYS do this first:
  1. Check /iterm/agent_sessions/ — if one exists and is "idle", use it.
  2. If no agent session exists, spawn one with iterm.new_tab() — then use that.
  3. NEVER use /iterm/active_session/ for typing — it may be the user's terminal or another program (like Claude Code).
  4. If a session has a pager (buffer ends with "(END)" or ":"), send act(path, "enter", "q") to quit it first.
AGENT SESSIONS: /iterm/agent_sessions/ contains ONLY sessions the agent spawned. These are safe to type into. Prefer reusing an existing agent session over spawning a new tab. For follow-up commands, check agent_sessions first.
PAGER AVOIDANCE: When running CLI tools that might page output (gh, git log, man), always prefix with GH_PAGER=cat PAGER=cat. Example: act(path, "type", "GH_PAGER=cat gh issue list\n")

INTERACTION HISTORY:
When processing a follow-up request, ALWAYS check /interactions/history/ for previous results. cat the "summary" file of recent interactions to understand what was already found. This avoids repeating work and provides context for follow-up questions.

CROSS-INTERFACE CONTEXT:
- When the user says "this repo", "this project", or "my repo": cat an agent_sessions/{sid}/cwd to find the working directory, then use the gh CLI to interact with GitHub. The gh CLI auto-detects the repo from the git remote.
- To open an issue in the browser: act on agent session with "type", "gh issue view {NUMBER} --web\n"). This opens the issue directly.
- To open the repo in the browser: act on agent session with "type", "gh browse\n") or use browser.goto().
- To open the issues page: act on agent session with "type", "gh issue list --web\n") — opens issues in the browser directly.
- IMPLEMENTATION tasks (coding, fixing, building): use the TERMINAL (agent_sessions), not the browser. Switch context from browser to terminal when the task requires running commands or editing code.

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
- Never guess a path. Use ls() only for discovery or verification of unknown paths; for known browser zones or [mache-N] IDs, direct cat/act is permitted without prior ls().
- You have exactly twelve tools: ls, cat, stat, act, grep, browser.scroll, browser.goto, browser.rescan, browser.list_tabs, browser.switch_tab, iterm.new_window, iterm.new_tab.
- If you cannot find an element, use browser.rescan() before giving up.
- When told to "search for X" on a site with a search bar (YouTube, Google, Amazon, etc.), type the query into the search input and press enter. When told to "find X" in existing page content, use the grep tool to scan DOM content.

TWO REFERENCE SYSTEMS — do NOT confuse them:
  children file: [N] label  →  click via ZONE PATH: act("/browser/main/feed/_c/N", "click")
  text_index:    [mache-N] label  →  click via BARE ID: act("mache-N", "click")
  ⚠ [16] in children is ordinal 16 — it is NOT mache-16! NEVER convert [N] to mache-N.
  ⚠ "#9" in user speech means issue/item NUMBER 9, NOT children ordinal [9]. Always grep for the item text first, then find its ordinal in children.

Strategy:
1. ls("/") to see mount points.
2. If the user specifies "browser", "web", or "Chrome" → use /browser/ directly.
3. If the user specifies "terminal" or "iTerm" → use /iterm/ directly.
4. Otherwise → use /focus/ (auto-detects the active app).
5. For INFORMATION RETRIEVAL: Use grep with a single distinctive keyword or combined synonyms (e.g., grep("review|rating")). If no match, move to browser.scroll() or cat page_text, do NOT re-grep with different terms.
6. For browser ACTIONS: If the target is not already identified by a [mache-N] ID or a specific zone path and ordinal, perform ONE grep call for text matching the objective. If grep yields a direct interactive element [mache-N] that clearly matches the objective, act("mache-N", "click") immediately. Otherwise (if grep yields only text matches, or no matches, or ambiguous [mache-N] matches), *always* use grep results to inform which browser zone's "children" file to cat. If grep yielded no zone-specific hints, prioritize /browser/main/, then /browser/header/, then /browser/footer/ to identify the specific interactive element. If grep does not yield any matches, cat "children" of the target zone. For browser zones like /browser/main/ or /browser/header/, you can always directly cat their 'children' file without a preceding ls() call. From children, identify all elements matching the objective. Among these, CRITICAL: ALWAYS select the most specific interactive child element (e.g., a link, button, or input) over its parent container (e.g., a div or span). Once found, act on this specific child element immediately by its ordinal [N] using its ZONE PATH with _c/N (e.g., act("/browser/main/feed/_c/3", "click")). Do NOT rescan if grep fails; always check children next.
7. For terminal: cat "buffer" → act with "type" to send commands.

SITE-SPECIFIC SHORTCUTS (use these instead of grep+guess when on these sites):
- youtube.com: To search, use browser.goto("https://www.youtube.com/results?search_query=QUERY") replacing QUERY with the search terms (use + for spaces). This skips typing and goes directly to results. To play a video from results, cat the first feed children file and click the FIRST link element (_c/1 or the first [N] a: entry).
- google.com: To search, use browser.goto("https://www.google.com/search?q=QUERY").
- IMPORTANT: When on a search results page, click the FIRST result immediately. Do not grep or scroll — the first result is almost always correct for the user's intent.

Be decisive. Once an actionable element (mache-N or _c/N) is identified, act immediately. Prioritize interactive elements like buttons, links, or inputs; never click a parent container if a more specific interactive child exists. One grep call should find what you need. If grep fails for an action target, cat children to find it — do NOT rescan or give up.

SEMI-FORMAL REASONING — think in three steps before every action:
1. PREMISES: State what you know. "I see /browser/main/content with 5 children. grep('review') found 3 matches in text_index."
2. TRACE: Project the outcome. "Clicking _c/7 (Reviews tab) should reveal review content below."
3. CONCLUSION: Decide. "Action: act('/browser/main/content/_c/7', click). Expected: reviews appear."
This prevents wasted actions — never click without knowing WHY it leads to the goal.

GREP STRATEGY:
- Use SHORT keywords (1-2 words), never full phrases. "ear cups being small" → grep("small|ear cups").
- Use regex OR: grep("price|cost"), grep("review|rating"), grep("small|tiny|little").
- Think about SYNONYMS: if the task says "small", also try "tiny|little|compact".
- grep results from text_index show [mache-N] tags. If multiple [mache-N] matches, CRITICAL: prioritize the most specific interactive element (e.g., button, link, input) over any parent container. Once identified, act("mache-N", "click") immediately.
- grep also searches page_text (full visible text) for content not in interactive elements.
- When grep returns long text blocks, read them carefully for ALL relevant names/items, not just the search term.

CLICKING RULES:
- From children [N] → act("<zone_path>/_c/N", "click")  — ALWAYS use the full zone path.
- From text_index [mache-N] → act("mache-N", "click")  — use the bare mache ID.
- NEVER mix them: children [16] does NOT mean mache-16. They are unrelated.

SCRATCHPAD — MANDATORY for multi-item collection:
- When finding names, items, or data: IMMEDIATELY save each finding to the scratchpad:
  act("/interactions/active/scratch", "type", "Found: <name/item>")
- Do this BEFORE navigating to the next page or clicking anything else.
- The scratchpad persists across continuations — it is your working memory.
- Before answering, cat("/interactions/active/scratch") to collect ALL findings.

EXTRACTING NAMES — when the task asks "who" or "name(s)":
- grep finds matching TEXT, but you still need the AUTHOR/REVIEWER NAME.
- After finding matching content, cat page_text and look for "Review by <name>" or author attribution NEAR each matching review.
- Save each name to the scratchpad immediately.

PAGINATION — only when you haven't found the target or goal asks for ALL items:
- Track visited pages in scratchpad: act("/interactions/active/scratch", "type", "visited: page 1")
- NEVER click Previous or revisit a page number you already recorded.
- Compare counts: if page says "12 Reviews" but you've only seen 5, keep looking.
- When collecting names/items, read the FULL content (cat page_text) and extract ALL matches.
- Before adding to scratchpad, cat("/interactions/active/scratch") first to avoid duplicates.
- If you already found the specific data the goal asks for, STOP and return the answer.

CONTINUATION: When your intent starts with [CONTINUATION], focus on the OVERALL TASK — the previous action is already done. Read the page content to extract the answer or take the next step toward the overall task.`
