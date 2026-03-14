// cmd/golden/main.go — Golden path test harness for YouTube demo.
//
// Sends the demo sequence through the Doer HTTP API and captures
// full diagnostics: URLs, actions taken, zone paths clicked,
// timing per step, and the complete agentd log.
//
// Usage:
//
//	go run ./cmd/golden                      # single run
//	go run ./cmd/golden -runs 3              # 3 runs
//	go run ./cmd/golden -runs 3 -tag before  # tagged for A/B comparison
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	agentdURL = flag.String("url", "http://localhost:8080", "agentd URL")
	runs      = flag.Int("runs", 1, "number of test runs")
	tag       = flag.String("tag", "", "tag for A/B comparison")
	timeout   = flag.Duration("timeout", 90*time.Second, "timeout per step")
)

// Golden path steps — the YouTube demo flow.
var steps = []string{
	"Go to youtube.com",
	"Search for Minecraft speedruns",
	"Click on the first video in the search results",
	"What is the title of this video?",
}

type StepResult struct {
	Step     int    `json:"step"`
	Intent   string `json:"intent"`
	Status   string `json:"status"`
	Duration string `json:"duration"`
	DurSec   float64 `json:"duration_s"`

	// Full diagnostics from /status
	URLBefore string `json:"url_before"`
	URLAfter  string `json:"url_after"`
	Summary   string `json:"summary,omitempty"`
	Error     string `json:"error,omitempty"`
	StepDesc  string `json:"step_desc,omitempty"`
	InterID   string `json:"interaction_id,omitempty"`

	// All poll snapshots for debugging
	Polls []PollSnapshot `json:"polls"`
}

type PollSnapshot struct {
	Elapsed string `json:"elapsed"`
	Status  string `json:"status"`
	Step    string `json:"step,omitempty"`
	URL     string `json:"url,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type RunResult struct {
	Run      int          `json:"run"`
	Tag      string       `json:"tag,omitempty"`
	Passed   int          `json:"passed"`
	Failed   int          `json:"failed"`
	Total    int          `json:"total"`
	Duration string       `json:"duration"`
	DurSec   float64      `json:"duration_s"`
	Steps    []StepResult `json:"steps"`
}

func main() {
	flag.Parse()

	// Check agentd is reachable
	if _, err := http.Get(*agentdURL + "/status"); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: agentd not reachable at %s\nStart it with: task demo-video\n", *agentdURL)
		os.Exit(1)
	}
	fmt.Printf("agentd reachable at %s\n", *agentdURL)

	// Results dir
	ts := time.Now().Format("20060102_150405")
	dirName := ts
	if *tag != "" {
		dirName += "_" + *tag
	}
	resultsDir := filepath.Join("results", "golden", dirName)
	os.MkdirAll(resultsDir, 0o755)
	fmt.Printf("Results: %s/\n\n", resultsDir)

	allResults := make([]RunResult, 0, *runs)
	totalClean := 0

	for r := 1; r <= *runs; r++ {
		fmt.Printf("━━━━━━━━━━━━━━━━━━━━ RUN %d/%d ━━━━━━━━━━━━━━━━━━━━\n", r, *runs)

		// Reset between runs
		post(*agentdURL+"/agent/reset", "{}")
		time.Sleep(time.Second)

		result := runGoldenPath(r)
		allResults = append(allResults, result)

		// Write run result
		runFile := filepath.Join(resultsDir, fmt.Sprintf("run_%d.json", r))
		writeJSON(runFile, result)

		if result.Failed == 0 {
			totalClean++
			fmt.Printf("\n✅ Run %d: %d/%d PASSED in %s\n\n", r, result.Passed, result.Total, result.Duration)
		} else {
			fmt.Printf("\n❌ Run %d: %d/%d passed, %d failed in %s\n\n", r, result.Passed, result.Total, result.Failed, result.Duration)
		}
	}

	// Final analysis
	fmt.Println("╔══════════════════════════════════════════════════════════╗")
	fmt.Printf("║  ANALYSIS%s\n", pad("", 48)+"║")
	fmt.Println("╚══════════════════════════════════════════════════════════╝")
	fmt.Println()

	printAnalysis(allResults)

	fmt.Printf("\nResults: %s/\n", resultsDir)
	fmt.Printf("Clean: %d/%d\n", totalClean, *runs)

	if totalClean < *runs {
		os.Exit(1)
	}
}

func runGoldenPath(runNum int) RunResult {
	start := time.Now()
	var stepResults []StepResult
	passed, failed := 0, 0

	for i, intent := range steps {
		stepNum := i + 1
		fmt.Printf("\n  Step %d/%d: %s\n", stepNum, len(steps), intent)

		result := runStep(stepNum, intent)
		stepResults = append(stepResults, result)

		if result.Status == "pass" {
			passed++
			fmt.Printf("  ✅ %s (%s) url=%s\n", result.Status, result.Duration, truncate(result.URLAfter, 60))
			if result.Summary != "" {
				fmt.Printf("     summary: %s\n", truncate(result.Summary, 100))
			}
		} else {
			failed++
			fmt.Printf("  ❌ %s (%s) error=%s\n", result.Status, result.Duration, truncate(result.Error, 80))
			fmt.Printf("     url=%s\n", result.URLAfter)
		}

		// Brief settle between steps
		time.Sleep(2 * time.Second)
	}

	dur := time.Since(start)
	return RunResult{
		Run:      runNum,
		Tag:      *tag,
		Passed:   passed,
		Failed:   failed,
		Total:    len(steps),
		Duration: dur.Round(time.Second).String(),
		DurSec:   dur.Seconds(),
		Steps:    stepResults,
	}
}

func runStep(stepNum int, intent string) StepResult {
	result := StepResult{
		Step:   stepNum,
		Intent: intent,
	}

	// Get URL before
	if status := getStatus(); status != nil {
		result.URLBefore = getString(status, "url")
	}

	start := time.Now()

	// Submit to Doer
	body := fmt.Sprintf(`{"intent":%q,"tab_id":0}`, intent)
	resp, err := post(*agentdURL+"/doer", body)
	if err != nil {
		result.Status = "submit_failed"
		result.Error = err.Error()
		result.Duration = time.Since(start).Round(time.Millisecond).String()
		result.DurSec = time.Since(start).Seconds()
		return result
	}
	_ = resp

	// Poll until done
	deadline := time.Now().Add(*timeout)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		elapsed := time.Since(start)

		status := getStatus()
		if status == nil {
			continue
		}

		s := getString(status, "status")
		snap := PollSnapshot{
			Elapsed: elapsed.Round(time.Second).String(),
			Status:  s,
			Step:    getString(status, "step"),
			URL:     getString(status, "url"),
			Summary: truncate(getString(status, "summary"), 200),
		}
		result.Polls = append(result.Polls, snap)

		fmt.Printf("\r    [%s] %s: %s", elapsed.Round(time.Second), s, truncate(getString(status, "step"), 50))

		switch s {
		case "completed", "idle", "ready":
			fmt.Println()
			dur := time.Since(start)
			result.Status = "pass"
			result.Duration = dur.Round(time.Millisecond).String()
			result.DurSec = dur.Seconds()
			result.URLAfter = getString(status, "url")
			result.Summary = getString(status, "summary")
			result.InterID = getString(status, "interaction_id")
			result.StepDesc = getString(status, "step")
			return result
		case "failed":
			fmt.Println()
			dur := time.Since(start)
			result.Status = "fail"
			result.Duration = dur.Round(time.Millisecond).String()
			result.DurSec = dur.Seconds()
			result.URLAfter = getString(status, "url")
			result.Error = getString(status, "error")
			if result.Error == "" {
				result.Error = getString(status, "summary")
			}
			result.Summary = getString(status, "summary")
			result.InterID = getString(status, "interaction_id")
			return result
		}
	}

	fmt.Println()
	dur := time.Since(start)
	result.Status = "timeout"
	result.Duration = dur.Round(time.Millisecond).String()
	result.DurSec = dur.Seconds()
	if status := getStatus(); status != nil {
		result.URLAfter = getString(status, "url")
	}
	return result
}

func getStatus() map[string]any {
	resp, err := http.Get(*agentdURL + "/status?tab_id=0")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var m map[string]any
	json.NewDecoder(resp.Body).Decode(&m)
	return m
}

func post(url, body string) (map[string]any, error) {
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	return m, nil
}

func getString(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func pad(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

func writeJSON(path string, v any) {
	f, err := os.Create(path)
	if err != nil {
		return
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func printAnalysis(results []RunResult) {
	if len(results) == 0 {
		return
	}

	numSteps := len(steps)
	fmt.Println("  Per-step breakdown:")
	fmt.Println("  ┌─────┬────────────────────────────────────────┬───────┬──────────┬──────────────────────────┐")
	fmt.Println("  │ #   │ Intent                                 │ Pass  │ Latency  │ URL After                │")
	fmt.Println("  ├─────┼────────────────────────────────────────┼───────┼──────────┼──────────────────────────┤")

	for si := 0; si < numSteps; si++ {
		var durations []float64
		passes := 0
		var lastURL string
		for _, r := range results {
			if si < len(r.Steps) {
				s := r.Steps[si]
				if s.Status == "pass" {
					passes++
					durations = append(durations, s.DurSec)
				}
				lastURL = s.URLAfter
			}
		}

		rate := fmt.Sprintf("%d/%d", passes, len(results))
		lat := "n/a"
		if len(durations) > 0 {
			avg := 0.0
			min, max := durations[0], durations[0]
			for _, d := range durations {
				avg += d
				if d < min {
					min = d
				}
				if d > max {
					max = d
				}
			}
			avg /= float64(len(durations))
			lat = fmt.Sprintf("%.0fs", avg)
			if len(durations) > 1 {
				lat = fmt.Sprintf("%.0fs (%.0f-%.0f)", avg, min, max)
			}
		}

		icon := "○"
		if passes == len(results) {
			icon = "●"
		} else if passes > 0 {
			icon = "◐"
		}

		intent := truncate(steps[si], 38)
		url := truncate(lastURL, 24)
		fmt.Printf("  │ %d %s │ %-38s │ %-5s │ %-8s │ %-24s │\n", si+1, icon, intent, rate, lat, url)
	}
	fmt.Println("  └─────┴────────────────────────────────────────┴───────┴──────────┴──────────────────────────┘")

	// Show failures with details
	for _, r := range results {
		for _, s := range r.Steps {
			if s.Status != "pass" {
				fmt.Printf("\n  FAILURE: Run %d Step %d — %s\n", r.Run, s.Step, s.Status)
				fmt.Printf("    Intent: %s\n", s.Intent)
				fmt.Printf("    Error:  %s\n", s.Error)
				fmt.Printf("    URL:    %s → %s\n", s.URLBefore, s.URLAfter)
				if len(s.Polls) > 0 {
					fmt.Printf("    Last poll: %s\n", s.Polls[len(s.Polls)-1].Step)
				}
			}
		}
	}
}
