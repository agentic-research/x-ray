package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
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
	Status  string `json:"status"` // "done", "failed", "error", "cancelled"
	Summary string `json:"summary"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
	Turns   int    `json:"turns"`
}

// plannerSystemPrompt adapts the Talker system prompt for autonomous benchmark execution.
var plannerSystemPrompt = `You are an autonomous web agent executing a benchmark task. You have a background navigator (Doer) that can see and interact with the browser.

YOUR TOOLS:
- issue_command(goal, read_only?): Dispatch a navigation/interaction command to your background navigator. This works for actions AND questions about the page. Examples: "click the first story", "go to reddit.com", "search for golang tutorials", "scroll down", "read the main heading".
  Set read_only=true when the task requires READING information from the page (e.g., "what is the price?", "list the items", "describe the page").
  Leave read_only=false (or omit) when the task requires an ACTION (e.g., "click...", "type...", "submit...", "navigate to...").
- check_status(): Check what the navigator is currently doing.
- cancel_task(): Cancel the current background task.
- open_url(url): Open a URL in a NEW browser tab.

TERMINATION PROTOCOL:
- When the task is accomplished, respond with: DONE: <your answer or confirmation>
- When you cannot complete the task after reasonable attempts, respond with: FAILED: <reason>
- For information retrieval tasks, DONE: should include the specific answer.
- For action tasks, DONE: should confirm what was accomplished.

BEHAVIOR:
1. Break the task into steps. Use issue_command() for each step.
2. After each command completes, analyze the result and decide the next step.
3. For information retrieval tasks, set read_only=true on issue_command to ensure the navigator reads rather than acts.
4. If a command fails or returns unexpected results, try alternative approaches before giving up.
5. Do NOT add conversational filler. Be direct and efficient.
6. If you get "No active browser tab", use open_url() to open a website first.
7. When the navigator returns a result that answers the task, immediately respond with DONE: <answer>.`

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
	sess := h.getSession(req.TabID)

	// Navigate to start URL if provided.
	if req.StartURL != "" {
		log.Printf("Planner: navigating to start URL: %s", req.StartURL)
		sess.ResetSchema()
		h.sendCreateTab(req.StartURL)

		select {
		case <-sess.GetSchemaReady():
			log.Printf("Planner: start URL loaded")
		case <-time.After(30 * time.Second):
			log.Printf("Planner: start URL load timed out, proceeding anyway")
		case <-ctx.Done():
			writeJSON(w, PlannerResult{Status: "cancelled", Summary: "Request cancelled."})
			return
		}
	}

	log.Printf("Planner: starting task: %s", truncate(req.Intent, 100))
	result := h.planner.RunTask(ctx, req.Intent, req.TabID)
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
