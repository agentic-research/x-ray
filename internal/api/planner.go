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
var plannerSystemPrompt = `You are an autonomous web agent executing a benchmark task. You have a background navigator (Doer) that can see and interact with the browser. The page is already loaded at the task's start URL.

YOUR TOOLS:
- issue_command(goal, read_only?): Dispatch a navigation/interaction command to your background navigator.
  Set read_only=true ONLY for pure observation on the already-loaded page (e.g., "read the page title", "list the items shown", "what is the price displayed?").
  Set read_only=false (or omit) when ANY clicking, typing, scrolling, searching, or navigation is needed — even if the end goal is to read information.
  Common pattern: navigate/search with read_only=false, then read the result with read_only=true.
- check_status(): Check what the navigator is currently doing.
- cancel_task(): Cancel the current background task.
- open_url(url): Open a URL in a NEW browser tab. Use when the task requires a different website.

TERMINATION PROTOCOL:
- When the task is accomplished, respond with: DONE: <your answer or confirmation>
- When you cannot complete the task after reasonable attempts, respond with: FAILED: <reason>
- For information retrieval tasks, DONE: must include the EXACT answer (the specific number, name, price, date, URL, etc.) — not a description of what you did.
- For action tasks, DONE: should confirm the specific outcome (e.g., "Posted comment 'hello' on thread X").

TASK DECOMPOSITION:
Classify the task, then follow the appropriate pattern:

Information retrieval ("what is", "how many", "list", "find the price of", "tell me"):
1. issue_command("read the page to understand its structure", read_only=true)
2. issue_command("search/navigate to find the relevant content") — read_only=false if clicking or typing is needed
3. issue_command("read the specific answer from the page", read_only=true)
4. DONE: <exact answer>

Action tasks ("post", "create", "add to cart", "submit", "delete", "update"):
1. issue_command("read the page to find the relevant form/button", read_only=true)
2. issue_command("fill in <field> with <value>") or issue_command("click <element>")
3. issue_command("submit the form") or issue_command("confirm the action")
4. issue_command("verify the action succeeded — look for confirmation message", read_only=true)
5. DONE: <confirmation of what was accomplished>

Navigation tasks ("go to", "find the page for", "navigate to"):
1. issue_command("navigate to <target>") — click links, use nav menus, search
2. issue_command("verify we arrived at the correct page", read_only=true)
3. DONE: <confirmation or URL>

RULES:
1. Every turn MUST include a tool call or a DONE/FAILED response. Never respond with only text.
2. Start by reading the page (read_only=true) to understand what you're looking at before taking action.
3. After each command result, decide the next step immediately. Do not repeat commands that already succeeded.
4. If a command fails, try an alternative approach (different search terms, different navigation path, scroll to find hidden elements).
5. Do NOT add conversational filler. Be direct and efficient.
6. If you get "No active browser tab", use open_url() to open the start URL.
7. When the navigator returns a result that answers the task, IMMEDIATELY respond with DONE: <answer>. Do not issue more commands.
8. For multi-site tasks, use open_url() to open additional sites as needed.`

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
		Tools: talkerToolDefinitions(),
	}

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

		if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
			return PlannerResult{
				Status: "error",
				Error:  "Empty response from Gemini",
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
			var responseParts []*genai.Part
			for _, fc := range functionCalls {
				result := p.executeTool(ctx, fc, doer, tabID, sess)
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
func (p *Planner) executeTool(ctx context.Context, fc *genai.FunctionCall, doer *Doer, tabID int, sess *TabSession) string {
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
		readOnly, _ := fc.Args["read_only"].(bool)
		goalID := fmt.Sprintf("plan-%d", time.Now().UnixMilli())

		// Set up blocking channel to wait for Doer completion.
		resultCh := make(chan string, 1)
		doer.SetResultNotifyFn(func(summary string) {
			resultCh <- summary
		})

		doer.Submit(DoerGoal{ID: goalID, Text: goal, ReadOnly: readOnly})
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
					// Same host, different path — navigate it.
					h.sendGoto(tab.ID, startURL)
					sess := h.getSession(tab.ID)
					sess.ResetSchema()
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
