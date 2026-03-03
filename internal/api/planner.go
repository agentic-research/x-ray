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

// PlannerResult is the JSON response from HandleAgentTask.
type PlannerResult struct {
	Status   string `json:"status"` // "done", "failed", "error", "cancelled"
	Summary  string `json:"summary"`
	Success  bool   `json:"success"`
	Error    string `json:"error,omitempty"`
	Turns    int    `json:"turns"`
	URLFinal string `json:"url_final,omitempty"`
}

// plannerSystemPrompt adapts the Talker system prompt for autonomous benchmark execution.
// Tool schemas are provided via the Tools parameter — do NOT re-describe syntax here.
var plannerSystemPrompt = `You are an autonomous web agent. You dispatch commands to a browser navigator that can see and interact with web pages. The page is already loaded at the task's start URL.

TOOLS:
- issue_command(goal, read_only): Send a goal to the navigator.
  read_only=true: observe only (read text, check structure). No clicks or typing.
  read_only=false: interact (click, type, scroll, navigate). Use this by default.
- open_url(url): Open a completely different website.

WORKFLOW — Break every task into these steps:
1. GREP FIRST: issue_command("use grep to find <keyword>", read_only=true). Use SHORT keywords (1-2 words) and regex OR for synonyms, e.g., "small|tiny" NOT "ear cups being small". The navigator has a grep tool that scans the DOM text — always grep for the answer directly before doing anything else.
2. INTERACT (only if grep fails): issue_command("click the tab/link to reveal content", read_only=false). Click tabs, expand sections — never scroll blindly.
3. READ: issue_command("read the specific content needed", read_only=true)
4. ANSWER: Respond with DONE: or FAILED:

CRITICAL PATTERNS:
- Content behind tabs (e.g., Reviews): grep first, then click the tab, then grep again.
- Hidden content: click to reveal, never scroll hoping to find it.
- NEVER say "search for X" — say "use grep to find X". The word "search" is ambiguous and the navigator may type into the website's search bar instead.
- If read_only=true fails to find content, ALWAYS try read_only=false next to interact with the page.
- When looking for a tab or button to click, tell the navigator: "cat the children of the main content zone to find the <tab name> link, then click it". Do NOT just say "click the Reviews tab" — the navigator needs to see the children list first.

WORKING MEMORY — the navigator has a scratchpad at /tasks/active/scratch:
- When collecting MULTIPLE items (names, prices, etc.) across pages, ALWAYS tell the navigator: "save your findings to the scratchpad before navigating to the next page."
- Example: issue_command("grep for reviews mentioning 'small', then save each reviewer name to the scratchpad, then click Page Next", read_only=false)
- Before answering DONE, issue_command("read the scratchpad to collect all findings", read_only=true).
- This prevents losing findings when the page changes.

COMPLETENESS — before answering, VERIFY:
- Does the page have pagination ("Next", "Page 1 of N", ">>")?  If so, check ALL pages.
- Does a review count (e.g., "12 Reviews") exceed what you've seen? Keep looking.
- Could results span multiple sections or tabs? Check each one.
- Accumulate ALL matching items across pages before answering.
Semi-formal check: "I found N matches. The page says M total. N < M → INCOMPLETE."

TERMINATION:
- DONE: followed by the EXACT answer (number, name, price, list of names, etc.)
- DONE: N/A — when the task asks for information that genuinely does not exist (e.g., no reviews match, no matching product found). Only use after thorough exploration.
- FAILED: followed by the reason (only for technical failures, NOT for "content not found").

RULES:
- Every turn MUST include a tool call OR a DONE/FAILED response. No thinking aloud.
- Never repeat a failed command. Try a different approach.
- Never use read_only=true twice in a row if the first attempt failed to find what you need.
- When collecting items (names, prices, counts), NEVER answer after checking only one page. Always verify there are no more pages.`

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
func (p *Planner) RunTask(ctx context.Context, intent string, tabID int) PlannerResult {
	sess := p.handler.getSession(tabID)
	doer := p.handler.getOrCreateDoer(tabID, sess)

	// Build initial conversation history.
	history := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{{Text: intent}}},
	}

	config := &genai.GenerateContentConfig{
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{{Text: plannerSystemPrompt}},
		},
		Tools:       plannerToolDefinitions(),
		Temperature: genai.Ptr(float32(0.1)),
		SafetySettings: []*genai.SafetySetting{
			{Category: genai.HarmCategoryHarassment, Threshold: genai.HarmBlockThresholdOff},
			{Category: genai.HarmCategoryHateSpeech, Threshold: genai.HarmBlockThresholdOff},
			{Category: genai.HarmCategorySexuallyExplicit, Threshold: genai.HarmBlockThresholdOff},
			{Category: genai.HarmCategoryDangerousContent, Threshold: genai.HarmBlockThresholdOff},
		},
	}

	malformedRetries := 0
	const maxMalformedRetries = 5

	for turn := 0; turn < maxPlannerTurns; turn++ {
		if ctx.Err() != nil {
			return PlannerResult{Status: "cancelled", Summary: "Task cancelled.", Turns: turn}
		}

		resp, err := p.client.Models.GenerateContent(ctx, p.model, history, config)
		if err != nil {
			return PlannerResult{
				Status: "error",
				Error:  fmt.Sprintf("Gemini API error: %v", err),
				Turns:  turn,
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
						Status: "error",
						Error:  fmt.Sprintf("Gemini produced %d consecutive malformed function calls", malformedRetries),
						Turns:  turn,
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
				Status: "error",
				Error:  errMsg,
				Turns:  turn,
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
				}
			}
			if after, ok := strings.CutPrefix(fullText, "FAILED:"); ok {
				reason := strings.TrimSpace(after)
				log.Printf("Planner: FAILED after %d turns: %s", turn+1, reason)
				return PlannerResult{
					Status:  "failed",
					Summary: reason,
					Turns:   turn + 1,
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
				}
			}
			if idx := strings.Index(fullText, "FAILED:"); idx >= 0 {
				reason := strings.TrimSpace(fullText[idx+7:])
				log.Printf("Planner: FAILED (embedded) after %d turns: %s", turn+1, reason)
				return PlannerResult{
					Status:  "failed",
					Summary: reason,
					Turns:   turn + 1,
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
				result := p.executeTool(ctx, fc, doer, tabID, sess, intent)
				log.Printf("Planner: tool %s → %s", fc.Name, truncate(result, 200))
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
	result := h.planner.RunTask(ctx, req.Intent, tabID)

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
