package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TaskResult captures the outcome of a single WebArena task.
type TaskResult struct {
	TaskID    int      `json:"task_id"`
	Intent    string   `json:"intent"`
	StartURL  string   `json:"start_url"`
	Success   bool     `json:"success"`
	Status    string   `json:"status"` // "done", "failed", "timeout", "error"
	Actions   []Action `json:"actions"`
	URLFinal  string   `json:"url_final"`
	Summary   string   `json:"summary,omitempty"`
	Error     string   `json:"error,omitempty"`
	ElapsedMs int64    `json:"elapsed_ms"`
}

// Action records a single step taken during task execution.
type Action struct {
	Step    int    `json:"step"`
	Action  string `json:"action"`
	MacheID string `json:"mache_id,omitempty"`
	Path    string `json:"path,omitempty"`
	Payload string `json:"payload,omitempty"`
}

// RunSummary aggregates results across all tasks.
type RunSummary struct {
	Timestamp  string  `json:"timestamp"`
	TotalTasks int     `json:"total_tasks"`
	Completed  int     `json:"completed"`
	Succeeded  int     `json:"succeeded"`
	Failed     int     `json:"failed"`
	Errors     int     `json:"errors"`
	Timeouts   int     `json:"timeouts"`
	ScorePct   float64 `json:"score_pct"`
	AvgElapsed float64 `json:"avg_elapsed_ms"`
}

// ResultWriter manages output to the results directory.
type ResultWriter struct {
	dir       string
	resultsF  *os.File
	results   []TaskResult
	startTime time.Time
}

// NewResultWriter creates a results directory and opens the JSONL file.
func NewResultWriter() (*ResultWriter, error) {
	ts := time.Now().Format("20060102_150405")
	dir := filepath.Join("results", "webarena_"+ts)
	if err := os.MkdirAll(filepath.Join(dir, "traces"), 0o755); err != nil {
		return nil, fmt.Errorf("create results dir: %w", err)
	}

	// Create a "latest" symlink for convenience.
	latestLink := filepath.Join("results", "webarena_latest")
	_ = os.Remove(latestLink)
	_ = os.Symlink("webarena_"+ts, latestLink)

	f, err := os.Create(filepath.Join(dir, "results.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("create results.jsonl: %w", err)
	}

	return &ResultWriter{
		dir:       dir,
		resultsF:  f,
		startTime: time.Now(),
	}, nil
}

// WriteResult appends a task result to the JSONL file and saves its trace.
func (w *ResultWriter) WriteResult(r TaskResult) error {
	w.results = append(w.results, r)

	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w.resultsF, "%s\n", line); err != nil {
		return err
	}

	// Write per-task trace.
	trace, _ := json.MarshalIndent(r, "", "  ")
	tracePath := filepath.Join(w.dir, "traces", fmt.Sprintf("%d.json", r.TaskID))
	return os.WriteFile(tracePath, trace, 0o644)
}

// WriteSummary computes and writes the aggregate summary.
func (w *ResultWriter) WriteSummary() error {
	_ = w.resultsF.Close()

	s := RunSummary{
		Timestamp:  w.startTime.Format(time.RFC3339),
		TotalTasks: len(w.results),
	}

	var totalElapsed int64
	for _, r := range w.results {
		totalElapsed += r.ElapsedMs
		switch r.Status {
		case "done":
			s.Completed++
			if r.Success {
				s.Succeeded++
			} else {
				s.Failed++
			}
		case "failed":
			s.Failed++
			s.Completed++
		case "timeout":
			s.Timeouts++
		case "error":
			s.Errors++
		}
	}

	if s.TotalTasks > 0 {
		s.ScorePct = float64(s.Succeeded) / float64(s.TotalTasks) * 100
		s.AvgElapsed = float64(totalElapsed) / float64(s.TotalTasks)
	}

	// Write summary.json
	data, _ := json.MarshalIndent(s, "", "  ")
	if err := os.WriteFile(filepath.Join(w.dir, "summary.json"), data, 0o644); err != nil {
		return err
	}

	// Write human-readable summary.txt
	var sb strings.Builder
	sb.WriteString("=== WebArena Evaluation Summary ===\n\n")
	sb.WriteString(fmt.Sprintf("Timestamp:  %s\n", s.Timestamp))
	sb.WriteString(fmt.Sprintf("Total:      %d tasks\n", s.TotalTasks))
	sb.WriteString(fmt.Sprintf("Succeeded:  %d\n", s.Succeeded))
	sb.WriteString(fmt.Sprintf("Failed:     %d\n", s.Failed))
	sb.WriteString(fmt.Sprintf("Timeouts:   %d\n", s.Timeouts))
	sb.WriteString(fmt.Sprintf("Errors:     %d\n", s.Errors))
	sb.WriteString(fmt.Sprintf("Score:      %.1f%%\n", s.ScorePct))
	sb.WriteString(fmt.Sprintf("Avg time:   %.0f ms\n\n", s.AvgElapsed))

	sb.WriteString(fmt.Sprintf("%-8s %-8s %-10s %s\n", "TaskID", "Status", "Time(ms)", "Summary"))
	sb.WriteString(strings.Repeat("-", 70) + "\n")
	for _, r := range w.results {
		summary := r.Summary
		if len(summary) > 40 {
			summary = summary[:37] + "..."
		}
		if r.Error != "" && summary == "" {
			summary = r.Error
			if len(summary) > 40 {
				summary = summary[:37] + "..."
			}
		}
		sb.WriteString(fmt.Sprintf("%-8d %-8s %-10d %s\n", r.TaskID, r.Status, r.ElapsedMs, summary))
	}

	return os.WriteFile(filepath.Join(w.dir, "summary.txt"), []byte(sb.String()), 0o644)
}

// Dir returns the results directory path.
func (w *ResultWriter) Dir() string {
	return w.dir
}
