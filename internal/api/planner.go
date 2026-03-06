package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/agentic-research/x-ray/internal/guardrails"
	"github.com/agentic-research/x-ray/internal/navigator"
	"google.golang.org/genai"
)

const maxPlannerTurns = 15

// Planner uses the regular Gemini REST API (not Live) to decompose a high-level
// task into Doer commands. This is the non-voice equivalent of the Talker in voice.go.
type Planner struct {
	handler *Handler
	client  *genai.Client
	model   string
}

// PlannerAction records a single tool call made by the Planner.
type PlannerAction struct {
	Turn      int            `json:"turn"`
	Tool      string         `json:"tool"`
	Args      map[string]any `json:"args"`
	Result    string         `json:"result"`
	ReadOnly  bool           `json:"read_only"`
	ElapsedMs int64          `json:"elapsed_ms"`
}

// PlannerResult is the JSON response from HandleAgentTask.
type PlannerResult struct {
	Status   string          `json:"status"` // "done", "failed", "error", "cancelled"
	Summary  string          `json:"summary"`
	Success  bool            `json:"success"`
	Error    string          `json:"error,omitempty"`
	Turns    int             `json:"turns"`
	URLFinal string          `json:"url_final,omitempty"`
	Actions  []PlannerAction `json:"actions,omitempty"`
}

// sitePrimers maps WebArena site names to short structural hints.
// These give the agent the same "at a glance" awareness a human would have.
var sitePrimers = map[string]string{
	"shopping": `SITE: Magento e-commerce store.
- Product pages have tabs: Details, More Info, Reviews. Click the tab to reveal content.
- Reviews are paginated (10/page). Check "X Reviews" count vs what you've seen.
- Category pages have sorting dropdowns and layered navigation filters on the left.
- Search bar is at top; do NOT type into it unless the task says to.`,

	"shopping_admin": `SITE: Magento Admin panel (URL ends in /admin).
- You are on the ADMIN dashboard, not the storefront. If the page looks like a store, navigate to /admin.
- Left sidebar has main navigation: Sales, Catalog, Customers, Marketing, Reports.
- Data grids have column sorting, filters, and pagination. Use "Filters" button to search.
- Dashboard shows revenue, orders, top products at a glance — but these are ALL-TIME data, NOT filtered by date.
- For "best-selling" queries: Reports → Products → Bestsellers.
- CRITICAL: If the task mentions a YEAR or DATE RANGE (e.g. "in 2022", "last year"):
  1. First navigate to Reports → Products → Bestsellers
  2. Set date period using the year from the task: From = 01/01/[YEAR], To = 12/31/[YEAR]
  3. Click "Show Report" to reload data with the filter applied
  4. ONLY THEN read the results. The dashboard bestseller widget does NOT filter by date.`,

	"reddit": `SITE: Reddit forum (Postmill).
- Content is organized by subreddits (/f/<name>). Posts have comments in nested threads.
- Sort options: Hot, New, Top, Active. Default sort is "Hot" (most upvoted).
- IMPORTANT: "most recent" means sort by "New" FIRST. Click the "New" sort link before reading posts.
- Comments can be collapsed. User profiles show post/comment history.`,

	"gitlab": `SITE: GitLab instance.
- Projects have tabs: Repository, Issues, Merge Requests, CI/CD, Wiki.
- Issues and MRs have labels, milestones, assignees. Use sidebar filters.
- File browser shows repo contents. Use breadcrumb nav for directories.`,

	"wikipedia": `SITE: Wikipedia.
- Articles have a table of contents with section links at the top.
- Content is in sections with headings. Use Cmd+F / grep for specific facts.
- Infoboxes on the right side contain structured data (dates, stats, etc.).`,

	"map": `SITE: OpenStreetMap instance.
- Search bar at top for locations. Map is interactive (pan/zoom).
- Search results appear in a sidebar list. Click to see details.
- Directions between two points via the route button.`,
}

// plannerSystemPrompt adapts the Talker system prompt for autonomous benchmark execution.
// Tool schemas are provided via the Tools parameter — do NOT re-describe syntax here.
var plannerSystemPrompt = `You are an autonomous web agent. You dispatch commands to a browser navigator.

IMPORTANT: All sites are LOCAL instances at localhost ports. NEVER navigate to public websites
(reddit.com, gitlab.com, amazon.com, etc.). Navigate within the current site using
create_interaction with clicks and links. Use open_url ONLY for localhost URLs.

TOOLS:
- create_interaction(intent, read_only): Send a goal to the navigator.
- open_url(url): Open a DIFFERENT localhost site (never a public URL).

TASK TYPES — recognize what kind of task this is:
- NAVIGATE ("Open...", "Go to...", "Show me..."): Navigate to the correct page.
  Use create_interaction to click links/menus. Once on the target page, call
  finish(answer=[], success=true). The final URL is what matters, not the answer.
- RETRIEVE: Find and return specific data from the page(s). Follow the STRATEGY below.
- ACTION: Perform a change (edit, submit, create, delete). Use create_interaction steps.

STRATEGY — think like a human with Cmd+F:

NOTE: If a "Current page layout" is included with the task, you ALREADY have the page
structure. SKIP step 1 and go directly to step 2 or 3.

1. ORIENT (only if no layout provided): create_interaction("cat children of main zone", read_only=true).
   Read the result. Identify tabs, sections, pagination, counts.

2. REVEAL: If content is behind a tab or collapsed section (e.g. "Reviews" tab):
   create_interaction("click the Reviews tab", read_only=false).

3. SEARCH: Grep for specific keywords:
   create_interaction("use grep to find <SHORT keyword|synonym>", read_only=true).
   Use 1-2 word keywords. Regex OR for synonyms: "small|tiny".

4. EXTRACT: Grep finds TEXT but not associated NAMES/metadata.
   create_interaction("cat page_text and find the reviewer name next to each review mentioning <keyword>, save all names to scratchpad", read_only=true).

5. PERSIST: ALWAYS save before navigating away:
   create_interaction("save findings to scratchpad", read_only=false).

6. PAGINATE: create_interaction("click Next page", read_only=false).
   If there is no "Next" button or you are on the last page, STOP and go to COLLECT.
   After each successful page navigation, repeat steps 3-5.

COLLECT: create_interaction("read scratchpad", read_only=true). Then call finish().

COMPLETENESS — before answering, VERIFY:
- If you already found the specific data requested, you may answer immediately.
- Only paginate if: (a) you haven't found the target, or (b) the goal explicitly asks for ALL items.
- When paginating: tell the Doer which pages to visit. NEVER revisit a page already checked.
- Does a review count (e.g., "12 Reviews") exceed what you've seen? Keep looking.
- Could results span multiple sections or tabs? Check each one.
Semi-formal check: "I found N matches. The page says M total. N < M → INCOMPLETE."

NOT FOUND — think before giving up:
- If the Navigator reports "Not found", ask yourself: am I on the RIGHT page?
  - Did I navigate to the correct section/tab/sort order first?
  - For admin panels: am I on /admin, not the storefront?
  - For reddit: did I sort by "New" if looking for "most recent"?
- If you haven't set up the page correctly, do that FIRST, then retry.
- Only accept "not found" and call finish(answer=[], success=true) after you have:
  (1) navigated to the correct page/section, (2) revealed relevant tabs/content, and
  (3) tried at least one search. Three strikes = give up.

TERMINATION — always use the finish() tool:
- finish(answer=["Alice", "Bob"], success=true) — list of results
- finish(answer=["42"], success=true) — single value (still wrap in array)
- finish(answer=[{"key":"val","count":3}], success=true) — structured objects when task asks for them
- finish(answer=[], success=true) — NAVIGATE tasks, or no matching items after thorough search
- finish(answer=["technical failure reason"], success=false) — only for technical failures

RULES:
- If you have the page layout, skip ORIENT and go straight to REVEAL or SEARCH.
- ALWAYS reveal before search. Click tabs before grepping.
- ALWAYS extract in context. Grep finds text; you need associated names/data.
- ALWAYS persist before navigating. Scratchpad survives; memory does not.
- NEVER say "search for X" — say "use grep to find X".
- NEVER repeat a failed command. Try different keywords or cat page_text.
- Every turn MUST include a tool call (create_interaction, open_url, or finish).`

// plannerToolDefinitions returns a minimal tool set for the synchronous Planner.
// Unlike the voice Talker, Planner blocks until each command completes, so
// check_interaction and cancel_interaction are unnecessary. Fewer tools = simpler JSON
// schema = fewer MALFORMED_FUNCTION_CALL errors from Gemini.
func plannerToolDefinitions() []*genai.Tool {
	return []*genai.Tool{{
		FunctionDeclarations: []*genai.FunctionDeclaration{
			{
				Name:        "create_interaction",
				Description: "Send a goal to the browser navigator. Returns a summary when done.",
				Parameters: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"intent":    {Type: genai.TypeString, Description: "What to do in the browser"},
						"read_only": {Type: genai.TypeString, Description: "Set to 'true' to just observe the page, or 'false' to interact (click, type, scroll)", Enum: []string{"true", "false"}},
					},
					Required: []string{"intent"},
				},
			},
			{
				Name:        "open_url",
				Description: "Open a URL in a new browser tab.",
				Parameters: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"url": {Type: genai.TypeString, Description: "URL to open"},
					},
					Required: []string{"url"},
				},
			},
			{
				Name:        "finish",
				Description: "Complete the task with a structured answer. Use this instead of typing DONE.",
				Parameters: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"answer": {
							Type:        genai.TypeArray,
							Description: "The answer as a list of strings. Single answers: [\"42\"]. Multiple: [\"Alice\", \"Bob\"]. Not found: [].",
							Items:       &genai.Schema{Type: genai.TypeString},
						},
						"success": {
							Type:        genai.TypeBoolean,
							Description: "True if the task was completed successfully, false if it failed.",
						},
					},
					Required: []string{"answer", "success"},
				},
			},
		},
	}}
}

// plannerCtx holds mutable state for a Planner task execution.
// open_url updates these when creating a new tab.
type plannerCtx struct {
	tabID       int
	sess        *TabSession
	doer        *Doer
	allowedHost string // hostname from start_url; empty = no restriction
}

// RunTask executes a high-level task using the Planner→Doer loop.
// Blocks until the task completes, fails, or the context is cancelled.
// siteHint is an optional site type (e.g. "shopping", "reddit") used to
// inject structural awareness into the system prompt.
func (p *Planner) RunTask(ctx context.Context, intent string, tabID int, siteHint, allowedHost string) PlannerResult {
	pctx := &plannerCtx{
		tabID:       tabID,
		sess:        p.handler.getSession(tabID),
		doer:        p.handler.getOrCreateDoer(tabID, p.handler.getSession(tabID)),
		allowedHost: allowedHost,
	}

	// Build system prompt, optionally prepending a site primer.
	sysPrompt := plannerSystemPrompt
	if primer, ok := sitePrimers[siteHint]; ok {
		sysPrompt = primer + "\n\n" + sysPrompt
		log.Printf("Planner: injected site primer for %q", siteHint)
	}

	// Pre-fill page layout so the Planner can skip the ORIENT turn.
	// Type-assert to *navigator.Agent to access BuildASCIILayout().
	intentWithLayout := intent
	if agent, ok := pctx.sess.Navigator.(*navigator.Agent); ok {
		if layout := agent.BuildASCIILayout(); layout != "" {
			intentWithLayout = intent + "\n\nCurrent page layout (element IDs match _c/ paths):\n" + layout
			log.Printf("Planner: injected ASCII layout (%d chars) into first turn", len(layout))
		}
	}

	// Build initial conversation history.
	history := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{{Text: intentWithLayout}}},
	}

	config := &genai.GenerateContentConfig{
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{{Text: sysPrompt}},
		},
		Tools:       plannerToolDefinitions(),
		Temperature: genai.Ptr(float32(1.0)),
		SafetySettings: []*genai.SafetySetting{
			{Category: genai.HarmCategoryHarassment, Threshold: genai.HarmBlockThresholdOff},
			{Category: genai.HarmCategoryHateSpeech, Threshold: genai.HarmBlockThresholdOff},
			{Category: genai.HarmCategorySexuallyExplicit, Threshold: genai.HarmBlockThresholdOff},
			{Category: genai.HarmCategoryDangerousContent, Threshold: genai.HarmBlockThresholdOff},
		},
	}

	malformedRetries := 0
	const maxMalformedRetries = 5
	var actions []PlannerAction

	for turn := 0; turn < maxPlannerTurns; turn++ {
		if ctx.Err() != nil {
			return PlannerResult{Status: "cancelled", Summary: "Task cancelled.", Turns: turn, Actions: actions}
		}

		resp, err := p.client.Models.GenerateContent(ctx, p.model, history, config)
		if err != nil {
			return PlannerResult{
				Status:  "failed",
				Error:   fmt.Sprintf("Gemini API error: %v", err),
				Turns:   turn,
				Actions: actions,
			}
		}

		// Diagnostic: log full response shape on every turn for debugging.
		if len(resp.Candidates) > 0 {
			c := resp.Candidates[0]
			contentDesc := "nil"
			if c.Content != nil {
				parts := make([]string, 0, len(c.Content.Parts))
				for _, p := range c.Content.Parts {
					if p.FunctionCall != nil {
						argsJSON, _ := json.Marshal(p.FunctionCall.Args)
						parts = append(parts, fmt.Sprintf("FC(%s, %s)", p.FunctionCall.Name, string(argsJSON)))
					} else if p.Text != "" {
						parts = append(parts, fmt.Sprintf("Text(%s)", truncate(p.Text, 80)))
					} else {
						parts = append(parts, "Other")
					}
				}
				contentDesc = fmt.Sprintf("role=%s parts=[%s]", c.Content.Role, strings.Join(parts, ", "))
			}
			log.Printf("Planner: turn %d response: finish=%s content=%s", turn, c.FinishReason, contentDesc)
		} else {
			log.Printf("Planner: turn %d response: no candidates", turn)
		}

		if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
			// MALFORMED_FUNCTION_CALL: Gemini tried to call a tool but produced
			// invalid JSON. Nudge it to retry rather than aborting.
			finishReason := ""
			if len(resp.Candidates) > 0 {
				finishReason = string(resp.Candidates[0].FinishReason)
			}
			if finishReason == string(genai.FinishReasonMalformedFunctionCall) {
				malformedRetries++
				log.Printf("Planner: malformed function call at turn %d (retry %d/%d)", turn, malformedRetries, maxMalformedRetries)
				if malformedRetries > maxMalformedRetries {
					return PlannerResult{
						Status:  "failed",
						Error:   fmt.Sprintf("Gemini produced %d consecutive malformed function calls", malformedRetries),
						Turns:   turn,
						Actions: actions,
					}
				}
				// Maintain proper turn alternation: model placeholder, then user nudge.
				// IMPORTANT: Tell the model to retry the SAME tool call, not start over.
				// Without this, the model re-issues create_interaction from scratch when
				// finish() had a JSON syntax error, wasting minutes of runtime.
				history = append(history, &genai.Content{
					Role:  "model",
					Parts: []*genai.Part{{Text: "Let me try that again."}},
				})
				history = append(history, &genai.Content{
					Role:  "user",
					Parts: []*genai.Part{{Text: "Your previous function call had invalid JSON syntax. Do NOT start over or re-gather data. Re-call the SAME tool you just tried (e.g. finish) with corrected JSON."}},
				})
				continue
			}

			// PROHIBITED_CONTENT: Gemini safety filter false-positive.
			// Retry once — often non-deterministic.
			if finishReason == "PROHIBITED_CONTENT" {
				malformedRetries++
				log.Printf("Planner: PROHIBITED_CONTENT at turn %d (retry %d/%d)", turn, malformedRetries, maxMalformedRetries)
				if malformedRetries <= maxMalformedRetries {
					history = append(history, &genai.Content{
						Role:  "model",
						Parts: []*genai.Part{{Text: "Let me rephrase."}},
					})
					history = append(history, &genai.Content{
						Role:  "user",
						Parts: []*genai.Part{{Text: "Please proceed with the task. Call create_interaction with the appropriate intent."}},
					})
					continue
				}
			}

			errMsg := "Empty response from Gemini"
			if resp.PromptFeedback != nil {
				if resp.PromptFeedback.BlockReason != "" {
					errMsg += fmt.Sprintf(" (blocked: %s)", resp.PromptFeedback.BlockReason)
				}
				if resp.PromptFeedback.BlockReasonMessage != "" {
					errMsg += fmt.Sprintf(" — %s", resp.PromptFeedback.BlockReasonMessage)
				}
			}
			if len(resp.Candidates) > 0 && resp.Candidates[0].FinishReason != "" {
				errMsg += fmt.Sprintf(" (finish_reason: %s)", resp.Candidates[0].FinishReason)
			}
			log.Printf("Planner: empty response at turn %d: %s (model=%s)", turn, errMsg, p.model)
			return PlannerResult{
				Status:  "failed",
				Error:   errMsg,
				Turns:   turn,
				Actions: actions,
			}
		}

		modelContent := resp.Candidates[0].Content
		history = append(history, modelContent)

		// Process parts: look for function calls and text.
		var functionCalls []*genai.FunctionCall
		var textParts []string

		for _, part := range modelContent.Parts {
			if part.FunctionCall != nil {
				functionCalls = append(functionCalls, part.FunctionCall)
			}
			if part.Text != "" {
				textParts = append(textParts, part.Text)
			}
		}

		// Check for terminal text responses.
		fullText := strings.Join(textParts, "\n")
		if fullText != "" && len(functionCalls) == 0 {
			if after, ok := strings.CutPrefix(fullText, "DONE:"); ok {
				summary := guardrails.NormalizeAnswer(strings.TrimSpace(after), guardrails.GuardrailsEnabled())
				log.Printf("Planner: DONE after %d turns: %s", turn+1, summary)
				return PlannerResult{
					Status:  "completed",
					Summary: summary,
					Success: true,
					Turns:   turn + 1,
					Actions: actions,
				}
			}
			if after, ok := strings.CutPrefix(fullText, "FAILED:"); ok {
				reason := strings.TrimSpace(after)
				log.Printf("Planner: FAILED after %d turns: %s", turn+1, reason)
				return PlannerResult{
					Status:  "failed",
					Summary: reason,
					Turns:   turn + 1,
					Actions: actions,
				}
			}
			// Non-terminal text — could be thinking aloud. Check for DONE/FAILED anywhere.
			if idx := strings.Index(fullText, "DONE:"); idx >= 0 {
				summary := guardrails.NormalizeAnswer(strings.TrimSpace(fullText[idx+5:]), guardrails.GuardrailsEnabled())
				log.Printf("Planner: DONE (embedded) after %d turns: %s", turn+1, summary)
				return PlannerResult{
					Status:  "completed",
					Summary: summary,
					Success: true,
					Turns:   turn + 1,
					Actions: actions,
				}
			}
			if idx := strings.Index(fullText, "FAILED:"); idx >= 0 {
				reason := strings.TrimSpace(fullText[idx+7:])
				log.Printf("Planner: FAILED (embedded) after %d turns: %s", turn+1, reason)
				return PlannerResult{
					Status:  "failed",
					Summary: reason,
					Turns:   turn + 1,
					Actions: actions,
				}
			}
			// Model produced text without terminal marker and no tools — treat as thinking.
			log.Printf("Planner: non-terminal text at turn %d: %s", turn+1, truncate(fullText, 100))
		}

		// Execute function calls.
		if len(functionCalls) > 0 {
			malformedRetries = 0 // Reset on successful parse.

			// Check for finish() tool — terminates the loop immediately.
			for _, fc := range functionCalls {
				if fc.Name == "finish" {
					actions = append(actions, PlannerAction{
						Turn: turn, Tool: "finish", Args: fc.Args,
					})
					return p.handleFinish(fc.Args, turn+1, actions)
				}
			}

			var responseParts []*genai.Part
			for _, fc := range functionCalls {
				toolStart := time.Now()
				result := p.executeTool(ctx, fc, pctx, intent)
				toolElapsed := time.Since(toolStart).Milliseconds()
				log.Printf("Planner: tool %s → %s", fc.Name, truncate(result, 200))

				readOnly := fmt.Sprintf("%v", fc.Args["read_only"]) == "true"
				actions = append(actions, PlannerAction{
					Turn:      turn,
					Tool:      fc.Name,
					Args:      fc.Args,
					Result:    truncate(result, 500),
					ReadOnly:  readOnly,
					ElapsedMs: toolElapsed,
				})

				responseParts = append(responseParts, &genai.Part{
					FunctionResponse: &genai.FunctionResponse{
						Name:     fc.Name,
						Response: map[string]any{"output": result},
					},
				})

				// Navigator "not found" — let the Planner decide what to do next.
				// It may need to navigate elsewhere, click a tab, or sort differently
				// before the data becomes visible. Don't auto-finish.
			}
			history = append(history, &genai.Content{
				Role:  "user",
				Parts: responseParts,
			})
		}
	}

	return PlannerResult{
		Status:  "failed",
		Summary: "Exhausted maximum turns without completing the task.",
		Turns:   maxPlannerTurns,
		Actions: actions,
	}
}

// handleFinish extracts the structured answer from a finish() tool call.
func (p *Planner) handleFinish(args map[string]any, turns int, actions []PlannerAction) PlannerResult {
	success, _ := args["success"].(bool)

	// Serialize the raw answer directly — Gemini may return strings, objects,
	// or a mix. Preserving structure lets the eval pipeline parse it correctly.
	// Previous code only extracted strings, silently dropping structured objects
	// (e.g. [{"username":"Alice","count":3}] → [] → "not_found_error").
	summary := "[]"
	if rawAnswer, ok := args["answer"]; ok {
		if b, err := json.Marshal(rawAnswer); err == nil {
			summary = string(b)
		}
	}

	status := "completed"
	if !success {
		status = "failed"
	}

	log.Printf("Planner: finish() after %d turns: success=%v answer=%s", turns, success, summary)
	return PlannerResult{
		Status:  status,
		Summary: summary,
		Success: success,
		Turns:   turns,
		Actions: actions,
	}
}

// executeTool dispatches a single Planner tool call.
// For create_interaction, it blocks until the Doer completes (unlike the voice Talker which is async).
// open_url mutates pctx to point at the newly created tab's session and doer.
func (p *Planner) executeTool(ctx context.Context, fc *genai.FunctionCall, pctx *plannerCtx, taskContext string) string {
	switch fc.Name {
	case "open_url":
		rawURL, _ := fc.Args["url"].(string)
		if rawURL == "" {
			return "Error: url is required."
		}
		if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
			rawURL = "https://" + rawURL
		}
		// Domain jail: if the task provided a start URL, only allow the same hostname.
		if pctx.allowedHost != "" {
			u, err := url.Parse(rawURL)
			if err != nil || u.Host != pctx.allowedHost {
				log.Printf("Planner: BLOCKED open_url %s (allowed host: %s)", rawURL, pctx.allowedHost)
				return fmt.Sprintf("Blocked: %s — only %s URLs are allowed. Use the correct localhost port.", rawURL, pctx.allowedHost)
			}
		}

		// Remember old tab ID so we can detect the new one.
		oldTabID := pctx.tabID
		p.handler.sendCreateTab(rawURL)

		// Poll for the new tab ID (activeVoiceTab changes when TAB_ACTIVATED arrives).
		newTabID := oldTabID
		deadline := time.After(15 * time.Second)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
	pollLoop:
		for {
			select {
			case <-ticker.C:
				p.handler.mu.Lock()
				candidate := p.handler.activeVoiceTab
				p.handler.mu.Unlock()
				if candidate != 0 && candidate != oldTabID {
					newTabID = candidate
					break pollLoop
				}
			case <-deadline:
				log.Printf("Planner: open_url timed out waiting for new tab ID")
				break pollLoop
			case <-ctx.Done():
				return "Cancelled."
			}
		}

		// Switch context to the new tab.
		if newTabID != oldTabID {
			pctx.tabID = newTabID
			pctx.sess = p.handler.getSession(newTabID)
			pctx.doer = p.handler.getOrCreateDoer(newTabID, pctx.sess)
			log.Printf("Planner: open_url switched to tab %d", newTabID)
		}

		// Wait for the NEW tab's schema to be ready.
		select {
		case <-pctx.sess.GetSchemaReady():
			return fmt.Sprintf("Opened %s in tab %d. Page is loaded.", rawURL, newTabID)
		case <-time.After(30 * time.Second):
			return fmt.Sprintf("Opened %s in tab %d but page load timed out.", rawURL, newTabID)
		case <-ctx.Done():
			return "Cancelled."
		}

	case "create_interaction":
		intent, _ := fc.Args["intent"].(string)
		if intent == "" {
			return "Error: intent is required."
		}
		readOnly := fmt.Sprintf("%v", fc.Args["read_only"]) == "true"
		ixID := fmt.Sprintf("plan-%d", time.Now().UnixMilli())

		// Set up blocking channel to wait for Doer completion.
		resultCh := make(chan string, 1)
		pctx.doer.SetResultNotifyFn(func(summary string) {
			resultCh <- summary
		})

		pctx.doer.Submit(Interaction{ID: ixID, Intent: intent, ReadOnly: readOnly, Context: taskContext})
		log.Printf("Planner: create_interaction %q (read_only=%v) on tab %d", intent, readOnly, pctx.tabID)

		select {
		case summary := <-resultCh:
			return summary
		case <-ctx.Done():
			pctx.doer.Cancel()
			pctx.doer.SetResultNotifyFn(nil)
			return "Cancelled."
		}

	case "check_interaction":
		status, intent, step, result := pctx.doer.State().Snapshot()
		switch status {
		case StatusIdle:
			return "No task in progress. Ready for a new command."
		case StatusInProgress:
			return fmt.Sprintf("Working on: %q. Current step: %s", intent, step)
		case StatusCompleted:
			if result != nil {
				return fmt.Sprintf("Completed: %s", result.Summary)
			}
			return "Task completed."
		case StatusFailed:
			if result != nil {
				return fmt.Sprintf("Failed: %s", result.Summary)
			}
			return "Task failed."
		case StatusCancelled:
			if result != nil {
				return fmt.Sprintf("Cancelled: %s", result.Summary)
			}
			return "Task was cancelled."
		}
		return "Unknown status."

	case "cancel_interaction":
		pctx.doer.Cancel()
		return "Task cancelled."

	default:
		return fmt.Sprintf("Unknown tool: %s", fc.Name)
	}
}

// resolveTab figures out which Chrome tab to use for a task.
//
// Strategy:
//  1. If startURL is provided, always create a fresh tab. Tab reuse causes
//     CDP chrome-extension:// crashes when Chrome gets into a bad state.
//  2. If no startURL, use the active voice tab (or the requested tabID).
//  3. Returns the real Chrome tab ID (never 0 in the happy path).
func (h *Handler) resolveTab(ctx context.Context, requestedTabID int, startURL string) int {
	// No start URL — use whatever tab is already active.
	if startURL == "" {
		h.mu.Lock()
		active := h.activeVoiceTab
		h.mu.Unlock()
		if active != 0 {
			return active
		}
		return requestedTabID
	}

	// Always create a fresh tab to avoid CDP chrome-extension:// crashes
	// that happen when reusing tabs across different product pages.
	log.Printf("Planner: creating fresh tab for %s", startURL)

	// Wait for the extension to connect before sending CREATE_TAB.
	// If agentd starts before Chrome, sendCreateTab would silently fail
	// and we'd grab whatever stale tab the extension reports.
	extDeadline := time.After(30 * time.Second)
	extTicker := time.NewTicker(200 * time.Millisecond)
	defer extTicker.Stop()
	for {
		h.mu.Lock()
		connected := h.conn != nil
		h.mu.Unlock()
		if connected {
			break
		}
		select {
		case <-extTicker.C:
		case <-extDeadline:
			log.Printf("Planner: timed out waiting for extension connection")
			return requestedTabID
		case <-ctx.Done():
			return 0
		}
	}

	// Record the current activeVoiceTab so we can detect the NEW one.
	h.mu.Lock()
	prevTab := h.activeVoiceTab
	h.mu.Unlock()

	h.sendCreateTab(startURL)

	// Wait for TAB_ACTIVATED to give us the real tab ID.
	deadline := time.After(15 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			h.mu.Lock()
			candidate := h.activeVoiceTab
			h.mu.Unlock()
			if candidate != 0 && candidate != prevTab {
				log.Printf("Planner: new tab created with ID %d", candidate)
				return candidate
			}
		case <-deadline:
			// Fallback: check if activeVoiceTab was set.
			h.mu.Lock()
			active := h.activeVoiceTab
			h.mu.Unlock()
			if active != 0 {
				return active
			}
			log.Printf("Planner: timed out waiting for new tab ID, using %d", requestedTabID)
			return requestedTabID
		case <-ctx.Done():
			return 0
		}
	}
}

// HandleAgentTask is the HTTP handler for POST /agent/task.
// It navigates to the start URL (if provided), runs the Planner loop, and returns the result.
func (h *Handler) HandleAgentTask(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Intent   string `json:"intent"`
		TabID    int    `json:"tab_id"`
		StartURL string `json:"start_url"`
		SiteHint string `json:"site_hint"`
		Cookies  []struct {
			Name   string `json:"name"`
			Value  string `json:"value"`
			Domain string `json:"domain"`
			Path   string `json:"path"`
		} `json:"cookies"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if req.Intent == "" {
		http.Error(w, "intent is required", http.StatusBadRequest)
		return
	}

	if h.planner == nil {
		http.Error(w, "planner not configured (no Gemini client)", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	tabID := req.TabID

	// When pre-auth cookies are present, open about:blank first so the browser
	// doesn't fire an unauthenticated GET to the start URL (which would 302 to
	// the login page before cookies are injected). After cookies are set, we
	// navigate to the real URL.
	resolveURL := req.StartURL
	if len(req.Cookies) > 0 && req.StartURL != "" {
		resolveURL = "about:blank"
	}

	// Resolve a real tab: use existing inventory or create a new one.
	tabID = h.resolveTab(ctx, tabID, resolveURL)
	if tabID == 0 && ctx.Err() != nil {
		writeJSON(w, PlannerResult{Status: "cancelled", Summary: "Request cancelled."})
		return
	}

	sess := h.getSession(tabID)

	// Inject pre-auth cookies, then navigate to the real start URL.
	if len(req.Cookies) > 0 {
		h.injectCookies(ctx, tabID, req.Cookies, req.StartURL)
	}

	// Wait for schema to be ready before running the Planner.
	// Guard against stale schemas: if the session URL doesn't match our target,
	// a capture from the previous page may have signaled ready. Reset and wait
	// for the correct page's capture to complete.
	for attempt := 0; attempt < 2; attempt++ {
		if !sess.GetEngine().HasSchema() {
			select {
			case <-sess.GetSchemaReady():
				log.Printf("Planner: schema ready (tab %d)", tabID)
			case <-time.After(30 * time.Second):
				log.Printf("Planner: schema timeout (tab %d), proceeding anyway", tabID)
			case <-ctx.Done():
				writeJSON(w, PlannerResult{Status: "cancelled", Summary: "Request cancelled."})
				return
			}
		}
		// Verify the schema is for the correct page, not a stale capture.
		if req.StartURL != "" && sess.GetCurrentURL() != "" {
			if CacheKey(sess.GetCurrentURL()) != CacheKey(req.StartURL) {
				log.Printf("Planner: stale schema detected (have %s, want %s) — resetting (tab %d)",
					sess.GetCurrentURL(), req.StartURL, tabID)
				sess.ResetSchema()
				continue
			}
		}
		break
	}

	// Extract allowed hostname from start_url for domain jail.
	// In eval mode this prevents the agent from escaping to the live internet.
	// In interactive mode (no start_url), no restriction is applied.
	allowedHost := ""
	if req.StartURL != "" {
		if u, err := url.Parse(req.StartURL); err == nil {
			allowedHost = u.Host // includes port — prevents hallucinated ports
		}
	}

	log.Printf("Planner: starting task: %s (allowed_host=%q)", truncate(req.Intent, 100), allowedHost)
	taskStart := time.Now()
	result := h.planner.RunTask(ctx, req.Intent, tabID, req.SiteHint, allowedHost)
	elapsed := time.Since(taskStart)

	// Capture the final URL for NAVIGATE-type task evaluation.
	// Use the active tab (may have changed if open_url switched tabs).
	if activeSess := h.getVoiceSession(); activeSess != nil {
		result.URLFinal = activeSess.GetCurrentURL()
	} else {
		result.URLFinal = sess.GetCurrentURL()
	}

	log.Printf("Planner: task finished: status=%s turns=%d elapsed=%s summary=%s",
		result.Status, result.Turns, elapsed.Round(time.Millisecond), truncate(result.Summary, 100))

	writeJSON(w, result)
}

// HandleAgentReset is the HTTP handler for POST /agent/reset.
// Resets schema state for the active tab so the next task starts fresh.
// Does NOT navigate — resolveTab handles reusing tabs and navigating to the new URL.
func (h *Handler) HandleAgentReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	h.mu.Lock()
	tabID := h.activeVoiceTab
	h.mu.Unlock()

	if tabID == 0 {
		writeJSON(w, map[string]string{"status": "ok", "message": "no active tab"})
		return
	}

	// Reset the session's schema state so the next task starts fresh.
	sess := h.getSession(tabID)
	sess.ResetSchema()

	// Clear interaction state so findings from the previous task don't leak.
	if sess.Tasks != nil {
		sess.Tasks.Reset()
	}

	log.Printf("Reset: cleared schema + scratchpad for tab %d", tabID)
	writeJSON(w, map[string]string{"status": "ok", "tab_id": fmt.Sprintf("%d", tabID)})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
