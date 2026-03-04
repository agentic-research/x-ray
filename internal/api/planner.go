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

	"shopping_admin": `SITE: Magento Admin panel.
- Left sidebar has main navigation: Sales, Catalog, Customers, Marketing, Reports.
- Data grids have column sorting, filters, and pagination. Use "Filters" button to search.
- Dashboard shows revenue, orders, top products at a glance.`,

	"reddit": `SITE: Reddit forum (Postmill).
- Content is organized by subreddits (/f/<name>). Posts have comments in nested threads.
- Sort options: Hot, New, Top, Active. Comments can be collapsed.
- User profiles show post/comment history.`,

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

TOOLS:
- issue_command(goal, read_only): Send a goal to the navigator.
- open_url(url): Open a different website.

STRATEGY — think like a human with Cmd+F:

1. ORIENT: issue_command("cat page_text to see what is visible", read_only=true).
   Read the result. Identify tabs, sections, pagination, counts.

2. REVEAL: If content is behind a tab or collapsed section:
   issue_command("cat children of main zone, find <tab>, click it", read_only=false).

3. SEARCH: Now grep for specific keywords:
   issue_command("use grep to find <SHORT keyword|synonym>", read_only=true).
   Use 1-2 word keywords. Regex OR for synonyms: "small|tiny".

4. EXTRACT: Grep finds TEXT but not associated NAMES/metadata.
   issue_command("cat page_text and find the reviewer name next to each review mentioning <keyword>, save all names to scratchpad", read_only=true).

5. PERSIST: ALWAYS save before navigating away:
   issue_command("save findings to scratchpad", read_only=false).

6. PAGINATE: issue_command("click Next page", read_only=false).
   After each page, repeat steps 3-5.

COLLECT: issue_command("read scratchpad", read_only=true). Then DONE: <answer>.

COMPLETENESS — before answering, VERIFY:
- Does the page have pagination ("Next", "Page 1 of N", ">>")?  If so, check ALL pages.
- Does a review count (e.g., "12 Reviews") exceed what you've seen? Keep looking.
- Could results span multiple sections or tabs? Check each one.
- Accumulate ALL matching items across pages before answering.
Semi-formal check: "I found N matches. The page says M total. N < M → INCOMPLETE."

TERMINATION:
- DONE: followed by the EXACT answer (number, name, price, list of names, etc.)
- DONE: N/A — when the task asks for information that genuinely does not exist. Only use after thorough exploration.
- FAILED: followed by the reason (only for technical failures, NOT for "content not found").

RULES:
- ALWAYS orient first. Never grep a cold page.
- ALWAYS reveal before search. Click tabs before grepping.
- ALWAYS extract in context. Grep finds text; you need associated names/data.
- ALWAYS persist before navigating. Scratchpad survives; memory does not.
- NEVER say "search for X" — say "use grep to find X".
- NEVER repeat a failed command. Try different keywords or cat page_text.
- Every turn MUST include a tool call OR DONE/FAILED.`

// plannerToolDefinitions returns a minimal tool set for the synchronous Planner.
// Unlike the voice Talker, Planner blocks until each command completes, so
// check_status and cancel_task are unnecessary. Fewer tools = simpler JSON
// schema = fewer MALFORMED_FUNCTION_CALL errors from Gemini.
func plannerToolDefinitions() []*genai.Tool {
	return []*genai.Tool{{
		FunctionDeclarations: []*genai.FunctionDeclaration{
			{
				Name:        "issue_command",
				Description: "Send a goal to the browser navigator. Returns a summary when done.",
				Parameters: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"goal":      {Type: genai.TypeString, Description: "What to do in the browser"},
						"read_only": {Type: genai.TypeString, Description: "Set to 'true' to just observe the page, or 'false' to interact (click, type, scroll)", Enum: []string{"true", "false"}},
					},
					Required: []string{"goal"},
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
		},
	}}
}

// RunTask executes a high-level task using the Planner→Doer loop.
// Blocks until the task completes, fails, or the context is cancelled.
// siteHint is an optional site type (e.g. "shopping", "reddit") used to
// inject structural awareness into the system prompt.
func (p *Planner) RunTask(ctx context.Context, intent string, tabID int, siteHint string) PlannerResult {
	sess := p.handler.getSession(tabID)
	doer := p.handler.getOrCreateDoer(tabID, sess)

	// Build system prompt, optionally prepending a site primer.
	sysPrompt := plannerSystemPrompt
	if primer, ok := sitePrimers[siteHint]; ok {
		sysPrompt = primer + "\n\n" + sysPrompt
		log.Printf("Planner: injected site primer for %q", siteHint)
	}

	// Build initial conversation history.
	history := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{{Text: intent}}},
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
				Status:  "error",
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
						Status:  "error",
						Error:   fmt.Sprintf("Gemini produced %d consecutive malformed function calls", malformedRetries),
						Turns:   turn,
						Actions: actions,
					}
				}
				// Maintain proper turn alternation: model placeholder, then user nudge.
				history = append(history, &genai.Content{
					Role:  "model",
					Parts: []*genai.Part{{Text: "Let me try that again."}},
				})
				history = append(history, &genai.Content{
					Role:  "user",
					Parts: []*genai.Part{{Text: "Your previous function call had invalid JSON. Call the issue_command tool with {\"goal\": \"your goal here\", \"read_only\": true} or {\"goal\": \"your goal here\"}."}},
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
						Parts: []*genai.Part{{Text: "Please proceed with the task. Call issue_command with the appropriate goal."}},
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
				Status:  "error",
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
				summary := strings.TrimSpace(after)
				log.Printf("Planner: DONE after %d turns: %s", turn+1, summary)
				return PlannerResult{
					Status:  "done",
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
				summary := strings.TrimSpace(fullText[idx+5:])
				log.Printf("Planner: DONE (embedded) after %d turns: %s", turn+1, summary)
				return PlannerResult{
					Status:  "done",
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
			var responseParts []*genai.Part
			for _, fc := range functionCalls {
				toolStart := time.Now()
				result := p.executeTool(ctx, fc, doer, tabID, sess, intent)
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

// executeTool dispatches a single Planner tool call.
// For issue_command, it blocks until the Doer completes (unlike the voice Talker which is async).
func (p *Planner) executeTool(ctx context.Context, fc *genai.FunctionCall, doer *Doer, tabID int, sess *TabSession, taskContext string) string {
	switch fc.Name {
	case "open_url":
		url, _ := fc.Args["url"].(string)
		if url == "" {
			return "Error: url is required."
		}
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			url = "https://" + url
		}
		p.handler.sendCreateTab(url)
		// Wait for the new tab's schema to be ready.
		select {
		case <-sess.GetSchemaReady():
			return fmt.Sprintf("Opened %s. Page is loaded.", url)
		case <-time.After(30 * time.Second):
			return fmt.Sprintf("Opened %s but page load timed out.", url)
		case <-ctx.Done():
			return "Cancelled."
		}

	case "issue_command":
		goal, _ := fc.Args["goal"].(string)
		if goal == "" {
			return "Error: goal is required."
		}
		readOnly := fmt.Sprintf("%v", fc.Args["read_only"]) == "true"
		goalID := fmt.Sprintf("plan-%d", time.Now().UnixMilli())

		// Set up blocking channel to wait for Doer completion.
		resultCh := make(chan string, 1)
		doer.SetResultNotifyFn(func(summary string) {
			resultCh <- summary
		})

		doer.Submit(DoerGoal{ID: goalID, Text: goal, ReadOnly: readOnly, TaskContext: taskContext})
		log.Printf("Planner: issue_command %q (read_only=%v)", goal, readOnly)

		select {
		case summary := <-resultCh:
			return summary
		case <-ctx.Done():
			doer.Cancel()
			doer.SetResultNotifyFn(nil)
			return "Cancelled."
		}

	case "check_status":
		status, goalText, step, result := doer.State().Snapshot()
		switch status {
		case DoerIdle:
			return "No task in progress. Ready for a new command."
		case DoerExecuting:
			return fmt.Sprintf("Working on: %q. Current step: %s", goalText, step)
		case DoerDone:
			if result != nil {
				return fmt.Sprintf("Completed: %s", result.Summary)
			}
			return "Task completed."
		case DoerFailed:
			if result != nil {
				return fmt.Sprintf("Failed: %s", result.Summary)
			}
			return "Task failed."
		}
		return "Unknown status."

	case "cancel_task":
		doer.Cancel()
		return "Task cancelled."

	default:
		return fmt.Sprintf("Unknown tool: %s", fc.Name)
	}
}

// resolveTab figures out which Chrome tab to use for a task.
//
// Strategy:
//  1. If startURL is provided, ask the extension for all open tabs.
//     If an existing tab matches the same host, navigate it to startURL.
//     Otherwise, create a new tab.
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

	// Parse the target host for matching.
	targetHost := ""
	if u, err := url.Parse(startURL); err == nil {
		targetHost = u.Host
	}

	// Ask extension for current tab inventory.
	tabs := h.listTabsSync(ctx, 5*time.Second)

	// Look for an existing tab on the same host.
	if targetHost != "" {
		for _, tab := range tabs {
			if u, err := url.Parse(tab.URL); err == nil && u.Host == targetHost {
				log.Printf("Planner: reusing tab %d (%s) for %s", tab.ID, tab.URL, startURL)
				if tab.URL != startURL {
					// Same host, different path — navigate to the new URL and
					// wait for a fresh schema before returning. Without this wait,
					// the planner would see HasSchema()=true from the OLD page
					// and skip the schema-ready gate, running against stale data.
					sess := h.getSession(tab.ID)
					sess.ResetSchema()
					h.sendGoto(tab.ID, startURL)
					log.Printf("Planner: navigating reused tab %d to %s, waiting for schema", tab.ID, startURL)
					select {
					case <-sess.GetSchemaReady():
						log.Printf("Planner: schema ready after tab reuse (tab %d)", tab.ID)
					case <-time.After(30 * time.Second):
						log.Printf("Planner: schema timeout after tab reuse (tab %d), proceeding", tab.ID)
					case <-ctx.Done():
						return 0
					}
				}
				return tab.ID
			}
		}
	}

	// No matching tab — create a new one and wait for its ID.
	log.Printf("Planner: no existing tab for %s, creating new tab", startURL)
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
			if candidate != 0 && candidate != requestedTabID {
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

// listTabsSync asks the extension for all open tabs and blocks until the response.
func (h *Handler) listTabsSync(ctx context.Context, timeout time.Duration) []TabInfo {
	// Use a temporary session to receive the response. We use tab 0 since
	// handleTabsListed delivers to the voice session.
	h.mu.Lock()
	voiceTab := h.activeVoiceTab
	h.mu.Unlock()

	sess := h.getSession(voiceTab)

	// Drain stale response.
	select {
	case <-sess.TabsListedCh:
	default:
	}

	h.sendListTabs()

	select {
	case tabs := <-sess.TabsListedCh:
		log.Printf("Planner: got tab inventory: %d tabs", len(tabs))
		return tabs
	case <-time.After(timeout):
		log.Printf("Planner: LIST_TABS timed out")
		return nil
	case <-ctx.Done():
		return nil
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

	// Resolve a real tab: use existing inventory or create a new one.
	tabID = h.resolveTab(ctx, tabID, req.StartURL)
	if tabID == 0 && ctx.Err() != nil {
		writeJSON(w, PlannerResult{Status: "cancelled", Summary: "Request cancelled."})
		return
	}

	sess := h.getSession(tabID)

	// Wait for schema to be ready before running the Planner.
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

	log.Printf("Planner: starting task: %s", truncate(req.Intent, 100))
	result := h.planner.RunTask(ctx, req.Intent, tabID, req.SiteHint)

	// Capture the final URL for NAVIGATE-type task evaluation.
	result.URLFinal = sess.GetCurrentURL()

	log.Printf("Planner: task finished: status=%s turns=%d summary=%s",
		result.Status, result.Turns, truncate(result.Summary, 100))

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

	log.Printf("Reset: cleared schema for tab %d", tabID)
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
