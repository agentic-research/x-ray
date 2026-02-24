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
}

// Agent represents Stage 2: The Navigator.
type Agent struct {
	generator ContentGenerator
	model     string
	engine    *mache.Engine
	scrollFn  func(ctx context.Context, direction string) error
}

func NewAgent(gen ContentGenerator, model string, engine *mache.Engine) *Agent {
	if model == "" {
		model = "gemini-2.5-flash"
	}
	return &Agent{generator: gen, model: model, engine: engine}
}

// SetEngine updates the engine when a new schema is applied.
func (a *Agent) SetEngine(engine *mache.Engine) {
	a.engine = engine
}

// SetScrollFunc injects the scroll callback used by the scroll tool.
func (a *Agent) SetScrollFunc(fn func(ctx context.Context, direction string) error) {
	a.scrollFn = fn
}

// ToolDefinitions returns the tool declarations for ls/cat/act.
func ToolDefinitions() []*genai.Tool {
	return []*genai.Tool{{
		FunctionDeclarations: []*genai.FunctionDeclaration{
			{
				Name:        "ls",
				Description: "List the contents of a directory in the semantic filesystem. Returns file and directory names. Always start with ls(\"/\") to see the top-level zones.",
				Parameters: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"path": {Type: genai.TypeString, Description: "Directory path, e.g. '/' or '/header/nav'"},
					},
					Required: []string{"path"},
				},
			},
			{
				Name:        "cat",
				Description: "Read the contents of a file in the semantic filesystem. Use this to read 'description' files for context about a zone.",
				Parameters: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"path": {Type: genai.TypeString, Description: "File path, e.g. '/header/nav/description'"},
					},
					Required: []string{"path"},
				},
			},
			{
				Name:        "act",
				Description: "Execute a browser action on the element at this virtual path. This triggers a real click/focus in the browser.",
				Parameters: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"path":   {Type: genai.TypeString, Description: "Virtual path to the element, e.g. '/main/trending'"},
						"action": {Type: genai.TypeString, Description: "Action type: 'click' or 'focus'"},
					},
					Required: []string{"path", "action"},
				},
			},
			{
				Name:        "scroll",
				Description: "Scroll the page to load more content. Use when items shown are fewer than what the user needs (e.g., only 3 posts visible but user wants the 10th). After scrolling, cat the children file again to see newly loaded items.",
				Parameters: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"direction": {Type: genai.TypeString, Description: "Scroll direction: 'down' or 'up'. Default: 'down'"},
					},
				},
			},
		},
	}}
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
		Tools:       ToolDefinitions(),
		Temperature: genai.Ptr(float32(0.1)),
	}

	// Pre-fill a tree dump so the model sees the full filesystem structure upfront.
	// This prevents small models from guessing zone names (e.g., /main/story_list
	// when the actual path is /main/feed) and wasting iterations on 404s.
	treeDump := a.buildTreeDump()
	log.Printf("Navigator: pre-filled tree:\n%s", treeDump)

	history := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{{Text: intent}}},
		{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			Name: "ls", Args: map[string]any{"path": "/"},
		}}}},
		{Role: "user", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
			Name:     "ls",
			Response: map[string]any{"output": treeDump},
		}}}},
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
		part := candidate.Content.Parts[0]

		if part.Text != "" {
			return nil, part.Text, nil
		}

		if part.FunctionCall != nil {
			fc := part.FunctionCall
			history = append(history, &genai.Content{
				Role:  "model",
				Parts: []*genai.Part{{FunctionCall: fc}},
			})

			result, action := a.ExecuteTool(ctx, fc)
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

// ExecuteTool dispatches a function call to the Mache engine and returns the
// result string and an optional ActionResult (non-nil when act() fires).
func (a *Agent) ExecuteTool(ctx context.Context, fc *genai.FunctionCall) (string, *ActionResult) {
	args := fc.Args
	switch fc.Name {
	case "ls":
		p, _ := args["path"].(string)
		entries, err := a.engine.ListDir(p)
		if err != nil {
			return fmt.Sprintf("Error: %v", err), nil
		}
		return strings.Join(entries, "\n"), nil

	case "cat":
		p, _ := args["path"].(string)
		content, err := a.engine.ReadFile(p)
		if err != nil {
			return fmt.Sprintf("Error: %v", err), nil
		}
		return content, nil

	case "act":
		p, _ := args["path"].(string)
		action, _ := args["action"].(string)
		if action == "" {
			action = "click"
		}
		macheID, err := a.engine.ResolveMacheID(p)
		if err != nil {
			return fmt.Sprintf("Error: %v", err), nil
		}
		return fmt.Sprintf("Executing %s on %s (mache_id: %s)", action, p, macheID),
			&ActionResult{MacheID: macheID, Action: action, Path: p}

	case "scroll":
		direction, _ := args["direction"].(string)
		if direction == "" {
			direction = "down"
		}
		if a.scrollFn == nil {
			return "Error: scroll not available in this context", nil
		}
		if err := a.scrollFn(ctx, direction); err != nil {
			return fmt.Sprintf("Error scrolling: %v", err), nil
		}
		return fmt.Sprintf("Scrolled %s. Use cat on the children file to see updated content.", direction), nil

	default:
		return fmt.Sprintf("Unknown tool: %s", fc.Name), nil
	}
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
- act(path, action): Execute a browser action on the element at this path. Actions: "click", "focus".
- scroll(direction): Scroll the page to load more content. Direction: "down" or "up".

CRITICAL CONSTRAINTS:
- Do NOT hallucinate tools or paths. Only use paths that you have confirmed exist via ls().
- Never guess a path. Always ls() a directory before trying to cat() or act() on its children.
- You have exactly four tools: ls, cat, act, scroll. Do not attempt to use any other tool.

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

Be decisive. You already know the full tree from ls("/"). Two calls should be enough: cat children → act.
If you need more items, add scroll → cat children → act (up to 8 iterations total).`
