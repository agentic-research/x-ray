package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// WATask represents a single WebArena task from the task JSON.
// Matches the output of: webarena-verified agent-input-get --config ...
type WATask struct {
	TaskID           int      `json:"task_id"`
	Intent           string   `json:"intent"`
	IntentTemplateID int      `json:"intent_template_id"`
	Sites            []string `json:"sites"`
	StartURLs        []string `json:"start_urls"`
	StartURL         string   `json:"start_url"` // legacy single-URL format
	TaskType         string   `json:"task_type"` // "RETRIEVE", "NAVIGATE", "ACTION", etc.
	EvalType         string   `json:"eval_type"` // evaluation method from task metadata
}

// URL template placeholders → default local ports.
var siteURLDefaults = map[string]string{
	"__SHOPPING__":       "http://localhost:7770",
	"__SHOPPING_ADMIN__": "http://localhost:7780",
	"__REDDIT__":         "http://localhost:9999",
	"__GITLAB__":         "http://localhost:8023",
	"__WIKIPEDIA__":      "http://localhost:8888",
	"__MAP__":            "http://localhost:3000",
}

// resolveStartURL picks the first start URL and resolves any __SITE__ templates.
func (t WATask) resolveStartURL() string {
	url := t.StartURL
	if url == "" && len(t.StartURLs) > 0 {
		url = t.StartURLs[0]
	}
	for placeholder, defaultURL := range siteURLDefaults {
		if strings.Contains(url, placeholder) {
			url = strings.ReplaceAll(url, placeholder, defaultURL)
		}
	}
	return url
}

func main() {
	log.Println("=== X-Ray WebArena Evaluation Harness ===")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("Interrupted, shutting down...")
		cancel()
	}()

	agentdURL := envOr("AGENTD_URL", "http://localhost:8080")
	tasksPath := envOr("WEBARENA_TASKS", "testdata/webarena_tasks.json")
	subset := envOr("WEBARENA_SUBSET", "hard")
	timeoutSec, _ := strconv.Atoi(envOr("WEBARENA_TIMEOUT", "120"))
	if timeoutSec <= 0 {
		timeoutSec = 120
	}

	tasks, err := loadTasks(tasksPath, subset)
	if err != nil {
		log.Fatalf("Failed to load tasks: %v", err)
	}
	log.Printf("Loaded %d tasks (subset=%s)", len(tasks), subset)

	// Verify agentd is reachable.
	if err := checkAgentd(ctx, agentdURL); err != nil {
		log.Fatalf("Cannot reach agentd at %s: %v\nStart agentd first: task run", agentdURL, err)
	}
	log.Printf("Connected to agentd at %s", agentdURL)

	writer, err := NewResultWriter()
	if err != nil {
		log.Fatalf("Failed to create results directory: %v", err)
	}
	log.Printf("Results directory: %s", writer.Dir())

	fmt.Println()
	fmt.Printf("%-8s %-8s %-10s %s\n", "TaskID", "Status", "Time(ms)", "Intent")
	fmt.Println(strings.Repeat("-", 70))

	for i, task := range tasks {
		if ctx.Err() != nil {
			log.Printf("Interrupted after %d/%d tasks", i, len(tasks))
			break
		}

		// Reset browser state between tasks to prevent leakage.
		if i > 0 {
			resetBrowser(ctx, agentdURL)
		}

		result := runTask(ctx, agentdURL, task, time.Duration(timeoutSec)*time.Second)

		if err := writer.WriteResult(result); err != nil {
			log.Printf("Failed to write result for task %d: %v", task.TaskID, err)
		}

		intent := task.Intent
		if len(intent) > 45 {
			intent = intent[:42] + "..."
		}
		fmt.Printf("%-8d %-8s %-10d %s\n", result.TaskID, result.Status, result.ElapsedMs, intent)
	}

	fmt.Println(strings.Repeat("-", 70))

	if err := writer.WriteSummary(); err != nil {
		log.Printf("Failed to write summary: %v", err)
	}

	// Print final score.
	succeeded := 0
	for _, r := range writer.results {
		if r.Success {
			succeeded++
		}
	}
	total := len(writer.results)
	pct := 0.0
	if total > 0 {
		pct = float64(succeeded) / float64(total) * 100
	}
	fmt.Printf("\nScore: %d/%d (%.1f%%)\n", succeeded, total, pct)
	fmt.Printf("Results: %s\n", writer.Dir())
}

// plannerResult mirrors api.PlannerResult for JSON decoding.
type plannerResult struct {
	Status   string `json:"status"`
	Summary  string `json:"summary"`
	Success  bool   `json:"success"`
	Error    string `json:"error"`
	Turns    int    `json:"turns"`
	URLFinal string `json:"url_final"`
}

// runTask executes a single WebArena task via POST /agent/task.
// The Planner inside agentd handles navigation, multi-step execution, and answer extraction.
func runTask(ctx context.Context, agentdURL string, task WATask, timeout time.Duration) TaskResult {
	start := time.Now()
	taskCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	startURL := task.resolveStartURL()
	result := TaskResult{
		TaskID:   task.TaskID,
		Intent:   task.Intent,
		StartURL: startURL,
		TaskType: task.TaskType,
	}

	body, _ := json.Marshal(map[string]any{
		"intent":    task.Intent,
		"tab_id":    0,
		"start_url": startURL,
	})

	req, err := http.NewRequestWithContext(taskCtx, http.MethodPost, agentdURL+"/agent/task", bytes.NewReader(body))
	if err != nil {
		result.Status = "error"
		result.Error = fmt.Sprintf("create request: %v", err)
		result.ElapsedMs = time.Since(start).Milliseconds()
		return result
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if taskCtx.Err() != nil {
			result.Status = "timeout"
			result.Error = "task timed out"
		} else {
			result.Status = "error"
			result.Error = fmt.Sprintf("POST /agent/task: %v", err)
		}
		result.ElapsedMs = time.Since(start).Milliseconds()
		return result
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		result.Status = "error"
		result.Error = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(respBody))
		result.ElapsedMs = time.Since(start).Milliseconds()
		return result
	}

	var pr plannerResult
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		result.Status = "error"
		result.Error = fmt.Sprintf("decode response: %v", err)
		result.ElapsedMs = time.Since(start).Milliseconds()
		return result
	}

	result.Status = pr.Status
	result.Summary = pr.Summary
	result.Success = pr.Success
	result.URLFinal = pr.URLFinal
	if pr.Error != "" {
		result.Error = pr.Error
	}
	result.ElapsedMs = time.Since(start).Milliseconds()
	return result
}

// checkAgentd verifies agentd is reachable by hitting GET /status.
func checkAgentd(ctx context.Context, agentdURL string) error {
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(checkCtx, http.MethodGet, agentdURL+"/status", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

func loadTasks(path, subset string) ([]WATask, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var allTasks []WATask
	if err := json.Unmarshal(data, &allTasks); err != nil {
		return nil, fmt.Errorf("parse tasks: %w", err)
	}

	if subset == "full" || subset == "" {
		return allTasks, nil
	}

	// Check for comma-separated task IDs.
	if strings.Contains(subset, ",") || isNumeric(subset) {
		ids := map[int]bool{}
		for _, s := range strings.Split(subset, ",") {
			s = strings.TrimSpace(s)
			id, err := strconv.Atoi(s)
			if err != nil {
				return nil, fmt.Errorf("invalid task ID %q in subset", s)
			}
			ids[id] = true
		}
		var filtered []WATask
		for _, t := range allTasks {
			if ids[t.TaskID] {
				filtered = append(filtered, t)
			}
		}
		if len(filtered) == 0 {
			return nil, fmt.Errorf("no tasks matched IDs: %s", subset)
		}
		return filtered, nil
	}

	// Named subsets.
	if subset == "hard" {
		return allTasks, nil
	}

	// Site-based filtering: "shopping", "gitlab", "reddit", etc.
	// Matches tasks where any site in the task's Sites list matches.
	knownSites := map[string]bool{
		"shopping": true, "shopping_admin": true, "reddit": true,
		"gitlab": true, "wikipedia": true, "map": true,
	}
	if knownSites[subset] {
		var filtered []WATask
		for _, t := range allTasks {
			for _, s := range t.Sites {
				if s == subset {
					filtered = append(filtered, t)
					break
				}
			}
		}
		if len(filtered) == 0 {
			return nil, fmt.Errorf("no tasks matched site %q", subset)
		}
		return filtered, nil
	}

	return nil, fmt.Errorf("unknown subset %q (use 'full', 'hard', site name, or comma-separated task IDs)", subset)
}

func isNumeric(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}

// resetBrowser resets schema state between tasks via POST /agent/reset.
// Does NOT navigate to about:blank — resolveTab will reuse existing tabs
// and navigate them to the new start URL, which is faster and avoids
// creating new tabs every time.
func resetBrowser(ctx context.Context, agentdURL string) {
	resetCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(resetCtx, http.MethodPost, agentdURL+"/agent/reset", nil)
	if err != nil {
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("Reset: %v", err)
		return
	}
	_ = resp.Body.Close()
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
