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

	// Pre-fill ls("/") so the model starts with knowledge of the page structure.
	// This saves one full API round-trip (~0.5-1s) since the agent always starts here.
	rootEntries, _ := a.engine.ListDir("/")
	rootListing := strings.Join(rootEntries, "\n")
	log.Printf("Navigator: pre-filled ls(\"/\") → %s", rootListing)

	history := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{{Text: intent}}},
		{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{
			Name: "ls", Args: map[string]any{"path": "/"},
		}}}},
		{Role: "user", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
			Name:     "ls",
			Response: map[string]any{"output": rootListing},
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
   a. cat the zone's "children" file to see individual elements listed as: mache-ID | tag | "text"
   b. act on "_c/<mache-id>" inside that zone to target the specific child element.
5. If the zone has no "children" file, or the zone itself is the target, act on the zone path directly.
6. If the user asks for an item beyond what's visible (e.g., "click the 10th post" but only 3 shown), scroll("down") to load more content, then cat the children file again.

Example workflow for "click the first story" on a news page:
  ls("/")                              → header/  main/  footer/
  ls("/main/story_list")               → _c/  children  description  mache_id
  cat("/main/story_list/children")     → mache-13 | a | "First Story Title"
                                         mache-14 | a | "(example.com)"
  act("/main/story_list/_c/mache-13", "click")  → clicks the specific story link

Be decisive. Three to four tool calls should be enough: ls → ls zone → cat children → act.
If you need more items, add scroll → cat children → act (up to 8 iterations total).`
