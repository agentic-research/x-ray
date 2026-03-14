// cmd/replay/main.go — Offline Navigator replay for debugging decisions.
//
// Loads frozen YouTube testdata (screenshot + DOM summary), runs the real
// Gemini Navigator against it, and logs every tool call so you can see
// exactly why it picked the wrong element or forgot to press enter.
//
// No browser needed. Each run takes ~5-7s per step.
//
// Usage:
//
//	go run ./cmd/replay                          # run default scenario
//	go run ./cmd/replay -scenario testdata/replay_youtube.json
//	go run ./cmd/replay -runs 3                  # 3x for consistency check
//	go run ./cmd/replay -dump                    # show what Navigator sees (no LLM)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/png"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/agentic-research/mache/graph"
	"github.com/agentic-research/x-ray/internal/cartographer"
	"github.com/agentic-research/x-ray/internal/config"
	"github.com/agentic-research/x-ray/internal/mache"
	"github.com/agentic-research/x-ray/internal/navigator"
	"google.golang.org/genai"
)

const maxScreenshotWidth = 1280 // Gemini inline limit ~20MB total; keep screenshots reasonable

// schemaGenerator matches the GenerateSchema method on all cartographer backends.
type schemaGenerator interface {
	GenerateSchema(ctx context.Context, screenshot []byte, mimeType, summary string) (string, error)
}

type replayScenario struct {
	Name  string       `json:"name"`
	Steps []replayStep `json:"steps"`
}

type replayStep struct {
	Site                  string `json:"site"`
	Intent                string `json:"intent"`
	ExpectAction          string `json:"expect_action"`
	ExpectPathContains    string `json:"expect_path_contains"`
	ExpectPayloadContains string `json:"expect_payload_contains"`
	ExpectMacheID         string `json:"expect_mache_id"`
}

type toolCall struct {
	Tool   string `json:"tool"`
	Args   string `json:"args"`
	Result string `json:"result"`
}

type stepResult struct {
	Step      replayStep            `json:"step"`
	Pass      bool                  `json:"pass"`
	Action    *navigator.ActionResult `json:"action,omitempty"`
	TextResp  string                `json:"text_response,omitempty"`
	ToolCalls []toolCall            `json:"tool_calls"`
	Duration  time.Duration         `json:"duration"`
	Reason    string                `json:"reason"`
	Err       string                `json:"error,omitempty"`
}

var (
	scenarioFlag = flag.String("scenario", "testdata/replay_youtube.json", "replay scenario JSON")
	runsFlag     = flag.Int("runs", 1, "number of runs")
	dumpFlag     = flag.Bool("dump", false, "dump Navigator view without calling LLM")
	verboseFlag  = flag.Bool("v", false, "verbose tool call output")
)

func main() {
	flag.Parse()
	config.LoadEnv()

	scenario, err := loadScenario(*scenarioFlag)
	if err != nil {
		log.Fatalf("Failed to load scenario: %v", err)
	}

	ctx := context.Background()

	model := os.Getenv("GEMINI_MODEL")
	if model == "" {
		model = "gemini-2.5-flash"
	}

	cart := buildCartographer()
	schemaCache := map[string]string{}

	if *dumpFlag {
		runDump(ctx, cart, scenario, schemaCache)
		return
	}

	// Create Gemini client for Navigator.
	client, err := genai.NewClient(ctx, nil)
	if err != nil {
		log.Fatalf("Failed to initialize Gemini client: %v", err)
	}

	navGen, navModel := buildNavGenerator(client, model)

	fmt.Printf("=== X-Ray Navigator Replay ===\n")
	fmt.Printf("Scenario: %s | Model: %s | Runs: %d\n\n", scenario.Name, navModel, *runsFlag)

	allResults := make([][]stepResult, 0, *runsFlag)

	for run := 1; run <= *runsFlag; run++ {
		if *runsFlag > 1 {
			fmt.Printf("━━━━━━━━━━━━━━━━━━━━ RUN %d/%d ━━━━━━━━━━━━━━━━━━━━\n", run, *runsFlag)
		}

		var results []stepResult
		runStart := time.Now()

		for i, step := range scenario.Steps {
			fmt.Printf("\n--- Step %d/%d: %s (%s) ---\n", i+1, len(scenario.Steps), step.Intent, step.Site)

			r := runStep(ctx, step, cart, navGen, navModel, schemaCache)
			results = append(results, r)

			if r.Pass {
				fmt.Printf("  ✅ PASS: %s (%.1fs)\n", r.Reason, r.Duration.Seconds())
			} else {
				fmt.Printf("  ❌ FAIL: %s (%.1fs)\n", r.Reason, r.Duration.Seconds())
			}
		}

		runDur := time.Since(runStart)
		passed := 0
		for _, r := range results {
			if r.Pass {
				passed++
			}
		}
		fmt.Printf("\n━━━ %d/%d PASS  Total: %.1fs ━━━\n\n", passed, len(results), runDur.Seconds())

		allResults = append(allResults, results)
	}

	if *runsFlag > 1 {
		printMultiRunSummary(scenario, allResults)
	}

	// Save structured results for later analysis / optimization.
	ts := time.Now().Format("20060102_150405")
	resultsDir := fmt.Sprintf("results/replay/%s", ts)
	os.MkdirAll(resultsDir, 0o755)
	for i, run := range allResults {
		path := fmt.Sprintf("%s/run_%d.json", resultsDir, i+1)
		f, err := os.Create(path)
		if err == nil {
			enc := json.NewEncoder(f)
			enc.SetIndent("", "  ")
			enc.Encode(run)
			f.Close()
		}
	}
	fmt.Printf("Results saved: %s/\n", resultsDir)
}

func runStep(ctx context.Context, step replayStep, cart schemaGenerator, navGen navigator.ContentGenerator, navModel string, schemaCache map[string]string) stepResult {
	result := stepResult{Step: step}

	// 1. Load testdata
	screenshot, err := os.ReadFile(fmt.Sprintf("testdata/%s/page.png", step.Site))
	if err != nil {
		result.Err = fmt.Sprintf("read screenshot: %v", err)
		result.Reason = result.Err
		return result
	}

	summary := loadSummary(step.Site)

	// 2. Generate or cache schema
	schema, err := getOrGenerateSchema(ctx, cart, step.Site, summary, screenshot, schemaCache)
	if err != nil {
		result.Err = fmt.Sprintf("schema: %v", err)
		result.Reason = result.Err
		return result
	}

	// 3. Build mache engine, mount under "browser/" to match production paths.
	engine := mache.NewEngine()
	if err := engine.ApplySchema(schema); err != nil {
		result.Err = fmt.Sprintf("ApplySchema: %v", err)
		result.Reason = result.Err
		return result
	}
	engine.LoadChildren(summary, nil)

	composite := graph.NewCompositeGraph()
	if err := composite.Mount("browser", engine); err != nil {
		result.Err = fmt.Sprintf("Mount: %v", err)
		result.Reason = result.Err
		return result
	}

	// 4. Resize screenshot if too large for Gemini inline limit.
	screenshot = resizeScreenshot(screenshot, maxScreenshotWidth)

	// 5. Create Navigator with tool call logging
	nav := navigator.NewAgent(navGen, navModel, composite)
	nav.SetScreenshot(screenshot, "image/png")

	var calls []toolCall
	nav.SetResultFunc(func(toolName string, args map[string]any, resultStr string) {
		argsJSON, _ := json.Marshal(args)
		// Truncate long results for readability
		truncResult := resultStr
		if len(truncResult) > 200 {
			truncResult = truncResult[:197] + "..."
		}

		tc := toolCall{
			Tool:   toolName,
			Args:   string(argsJSON),
			Result: truncResult,
		}
		calls = append(calls, tc)

		// Print live
		idx := len(calls)
		fmt.Printf("  [%d] %s(%s)\n", idx, toolName, string(argsJSON))
		if *verboseFlag || toolName == "act" {
			// Always show act results, verbose shows all
			for _, line := range strings.Split(truncResult, "\n") {
				if line != "" {
					fmt.Printf("      → %s\n", line)
				}
			}
		}
	})

	// 5. Run Navigator
	start := time.Now()
	action, textResp, err := nav.HandleIntent(ctx, step.Intent, false)
	result.Duration = time.Since(start)
	result.ToolCalls = calls

	if err != nil {
		result.Err = err.Error()
		result.Reason = fmt.Sprintf("HandleIntent error: %v", err)
		return result
	}

	if textResp != "" {
		result.TextResp = textResp
		// Text response = Navigator answered instead of acting.
		// Check if we expected an action.
		if step.ExpectAction != "" {
			result.Reason = fmt.Sprintf("got text response instead of action: %s", truncate(textResp, 80))
			return result
		}
		result.Pass = true
		result.Reason = fmt.Sprintf("text: %s", truncate(textResp, 80))
		return result
	}

	if action == nil {
		result.Reason = "no action and no text response"
		return result
	}

	result.Action = action
	fmt.Printf("  → ActionResult{mache_id: %s, action: %s, path: %s, payload: %s}\n",
		action.MacheID, action.Action, action.Path, truncate(action.Payload, 40))

	// 6. Validate
	result.Pass = true
	var reasons []string

	if step.ExpectAction != "" && action.Action != step.ExpectAction {
		result.Pass = false
		reasons = append(reasons, fmt.Sprintf("expected action=%s got=%s", step.ExpectAction, action.Action))
	} else if step.ExpectAction != "" {
		reasons = append(reasons, fmt.Sprintf("action=%s", action.Action))
	}

	if step.ExpectPathContains != "" && !strings.Contains(strings.ToLower(action.Path), strings.ToLower(step.ExpectPathContains)) {
		result.Pass = false
		reasons = append(reasons, fmt.Sprintf("path %q doesn't contain %q", action.Path, step.ExpectPathContains))
	}

	if step.ExpectPayloadContains != "" && !strings.Contains(strings.ToLower(action.Payload), strings.ToLower(step.ExpectPayloadContains)) {
		result.Pass = false
		reasons = append(reasons, fmt.Sprintf("payload %q doesn't contain %q", action.Payload, step.ExpectPayloadContains))
	}

	if step.ExpectMacheID != "" && action.MacheID != step.ExpectMacheID {
		result.Pass = false
		reasons = append(reasons, fmt.Sprintf("expected mache_id=%s got=%s", step.ExpectMacheID, action.MacheID))
	}

	if len(reasons) > 0 {
		result.Reason = strings.Join(reasons, "; ")
	} else {
		result.Reason = fmt.Sprintf("action=%s on %s", action.Action, action.MacheID)
	}

	return result
}

func runDump(ctx context.Context, cart schemaGenerator, scenario *replayScenario, schemaCache map[string]string) {
	seen := map[string]bool{}
	for _, step := range scenario.Steps {
		if seen[step.Site] {
			continue
		}
		seen[step.Site] = true

		fmt.Printf("=== %s ===\n", step.Site)

		screenshot, err := os.ReadFile(fmt.Sprintf("testdata/%s/page.png", step.Site))
		if err != nil {
			fmt.Printf("  ERROR: %v\n\n", err)
			continue
		}
		summary := loadSummary(step.Site)

		schema, err := getOrGenerateSchema(ctx, cart, step.Site, summary, screenshot, schemaCache)
		if err != nil {
			fmt.Printf("  ERROR: %v\n\n", err)
			continue
		}

		engine := mache.NewEngine()
		if err := engine.ApplySchema(schema); err != nil {
			fmt.Printf("  ERROR: ApplySchema: %v\n\n", err)
			continue
		}
		engine.LoadChildren(summary, nil)

		composite := graph.NewCompositeGraph()
		if err := composite.Mount("browser", engine); err != nil {
			fmt.Printf("  ERROR: Mount: %v\n\n", err)
			continue
		}

		// Use NavFS for dump — same view as Navigator gets.
		dumpFS := navigator.NewNavFS(composite)
		entries, err := dumpFS.ListDir("/")
		if err != nil {
			fmt.Printf("  ls(\"/\"): ERROR: %v\n\n", err)
			continue
		}

		fmt.Println("ls(\"/\"):")
		for _, entry := range entries {
			fmt.Printf("  %s\n", entry)
		}
		fmt.Println()

		// Show children for each zone under /browser/
		browserEntries, err := dumpFS.ListDir("/browser/")
		if err == nil {
			for _, entry := range browserEntries {
				name := strings.TrimSuffix(entry, "/")
				childrenContent, _ := dumpFS.ReadFile("/browser/" + name + "/children")
				if childrenContent != "" {
					fmt.Printf("Zone: /browser/%s/children\n", name)
					for _, line := range strings.Split(childrenContent, "\n") {
						if line != "" {
							fmt.Printf("  %s\n", line)
						}
					}
					fmt.Println()
				}
			}
		}
		fmt.Println()
	}
}

func loadScenario(path string) (*replayScenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s replayScenario
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func loadSummary(site string) string {
	data, err := os.ReadFile(fmt.Sprintf("testdata/%s/page_summary.txt", site))
	if err != nil {
		log.Fatalf("Failed to read summary for %s: %v", site, err)
	}
	return string(data)
}

func buildCartographer() schemaGenerator {
	mode := os.Getenv("CARTOGRAPHER_MODE")
	switch strings.ToLower(mode) {
	case "cairn":
		gear := 5
		if g, err := strconv.Atoi(os.Getenv("CAIRN_GEAR")); err == nil {
			gear = g
		}
		scale := 10.0
		if s, err := strconv.ParseFloat(os.Getenv("CAIRN_SCALE"), 64); err == nil {
			scale = s
		}
		c := &cartographer.CairnCartographer{Gear: gear, Scale: scale}
		if v := os.Getenv("CAIRN_SHEAF"); v != "" && v != "0" && v != "false" {
			c.SheafFolding = true
		}
		if v := os.Getenv("CAIRN_CURVATURE"); v != "" && v != "0" && v != "false" {
			c.CurvatureDetection = true
		}
		return c
	case "tropical":
		return &cartographer.TropicalCartographer{}
	default:
		log.Fatalf("CARTOGRAPHER_MODE must be 'cairn' or 'tropical' for offline replay (no Gemini VLM)")
		return nil
	}
}

func buildNavGenerator(client *genai.Client, defaultModel string) (navigator.ContentGenerator, string) {
	if ep := os.Getenv("NAVIGATOR_ENDPOINT"); ep != "" {
		navModel := os.Getenv("NAVIGATOR_MODEL")
		if navModel == "" {
			navModel = "functiongemma:270m"
		}
		format := os.Getenv("NAVIGATOR_FORMAT")
		if format == "gemma" {
			return &navigator.GemmaGenerator{Endpoint: ep, Model: navModel}, navModel
		}
		return &navigator.OllamaGenerator{Endpoint: ep, Model: navModel}, navModel
	}
	return &navigator.GeminiGenerator{Client: client}, defaultModel
}

func getOrGenerateSchema(ctx context.Context, cart schemaGenerator, site, summary string, screenshot []byte, cache map[string]string) (string, error) {
	if s, ok := cache[site]; ok {
		return s, nil
	}
	schema, err := cart.GenerateSchema(ctx, screenshot, "image/png", summary)
	if err != nil {
		return "", err
	}
	cache[site] = schema
	return schema, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// resizeScreenshot downscales a PNG if it exceeds size limits.
// Gemini inline limit is ~20MB total request; keep screenshots under 2MB.
// Also caps height to prevent full-page screenshots from being sent.
func resizeScreenshot(data []byte, maxWidth int) []byte {
	const maxBytes = 2 * 1024 * 1024 // 2MB
	const maxHeight = 2048

	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return data // not a valid PNG, return as-is
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()

	// Calculate scale factor needed.
	scale := 1.0
	if w > maxWidth {
		scale = float64(maxWidth) / float64(w)
	}
	if float64(h)*scale > float64(maxHeight) {
		scale = float64(maxHeight) / float64(h)
	}

	if scale >= 1.0 && len(data) <= maxBytes {
		return data // already fine
	}

	// If only the byte size is too large, scale down by sqrt of ratio.
	if scale >= 1.0 && len(data) > maxBytes {
		scale = 0.7 // rough — PNG compression varies, but this usually works
	}

	newW := int(float64(w) * scale)
	newH := int(float64(h) * scale)
	if newW < 1 {
		newW = 1
	}
	if newH < 1 {
		newH = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
	for y := range newH {
		srcY := y * h / newH
		for x := range newW {
			srcX := x * w / newW
			dst.Set(x, y, img.At(bounds.Min.X+srcX, bounds.Min.Y+srcY))
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return data
	}
	log.Printf("Resized screenshot: %dx%d → %dx%d (%d → %d bytes)",
		w, h, newW, newH, len(data), buf.Len())
	return buf.Bytes()
}

func printMultiRunSummary(scenario *replayScenario, allResults [][]stepResult) {
	fmt.Println("╔══════════════════════════════════════════════════════════╗")
	fmt.Println("║  MULTI-RUN SUMMARY                                     ║")
	fmt.Println("╚══════════════════════════════════════════════════════════╝")
	fmt.Println()

	for i, step := range scenario.Steps {
		passes := 0
		var durations []float64
		for _, run := range allResults {
			if i < len(run) && run[i].Pass {
				passes++
				durations = append(durations, run[i].Duration.Seconds())
			}
		}
		icon := "○"
		if passes == len(allResults) {
			icon = "●"
		} else if passes > 0 {
			icon = "◐"
		}
		lat := "n/a"
		if len(durations) > 0 {
			avg := 0.0
			for _, d := range durations {
				avg += d
			}
			avg /= float64(len(durations))
			lat = fmt.Sprintf("%.1fs", avg)
		}
		fmt.Printf("  %s Step %d: %d/%d pass | %s | %s\n",
			icon, i+1, passes, len(allResults), lat, truncate(step.Intent, 45))
	}
	fmt.Println()
}
