package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/agentic-research/x-ray/internal/cartographer"
	"github.com/agentic-research/x-ray/internal/config"
	"github.com/agentic-research/x-ray/internal/mache"
	"github.com/agentic-research/x-ray/internal/navigator"
	"google.golang.org/genai"
)

// schemaGenerator matches the GenerateSchema method on all cartographer backends.
type schemaGenerator interface {
	GenerateSchema(ctx context.Context, screenshot []byte, mimeType, summary string) (string, error)
}

type benchCase struct {
	Site          string `json:"site"`
	Intent        string `json:"intent"`
	ExpectMacheID string `json:"expect_mache_id"`
	ExpectText    string `json:"expect_text"`
}

type benchResult struct {
	tc      benchCase
	pass    bool
	macheID string
	elapsed time.Duration
	iters   int
	err     error
}

func main() {
	config.LoadEnv()

	cases, err := loadCases("testdata/bench_cases.json")
	if err != nil {
		log.Fatalf("Failed to load bench cases: %v", err)
	}

	ctx := context.Background()

	model := os.Getenv("GEMINI_MODEL")
	if model == "" {
		model = "gemini-2.5-flash"
	}

	cartMode := os.Getenv("CARTOGRAPHER_MODE")
	if cartMode == "" {
		cartMode = "gemini"
	}

	cart, needsGemini := buildCartographer(model)

	// Only create Gemini client if the cartographer or navigator needs it.
	var client *genai.Client
	navEndpoint := os.Getenv("NAVIGATOR_ENDPOINT")
	if needsGemini || navEndpoint == "" {
		var err error
		client, err = genai.NewClient(ctx, nil)
		if err != nil {
			log.Fatalf("Failed to initialize Gemini client: %v", err)
		}
	}

	// Gemini cartographer needs the client, which is only available now.
	if needsGemini {
		cart = cartographer.NewAgent(client, model)
	}

	navGen, navModel := buildNavGenerator(client, model)

	schemaCache := map[string]string{} // site → schemaJSON
	var results []benchResult

	fmt.Println("=== X-Ray Navigation Benchmark ===")
	fmt.Printf("Cartographer: %s\n", cartMode)
	fmt.Println()
	fmt.Printf("%-13s %-26s %-9s %-13s %-8s %s\n",
		"Site", "Intent", "Result", "MacheID", "Time", "Iters")
	fmt.Println(strings.Repeat("\u2500", 78))

	for _, tc := range cases {
		schema, err := getOrGenerateSchema(ctx, cart, tc.Site, schemaCache, cartMode == "gemini")
		if err != nil {
			r := benchResult{tc: tc, err: err}
			results = append(results, r)
			printRow(r)
			continue
		}

		engine := mache.NewEngine()
		if err := engine.ApplySchema(schema); err != nil {
			r := benchResult{tc: tc, err: fmt.Errorf("ApplySchema: %w", err)}
			results = append(results, r)
			printRow(r)
			continue
		}

		summary := loadSummary(tc.Site)
		engine.LoadChildren(summary, nil)

		nav := navigator.NewAgent(navGen, navModel, engine)

		start := time.Now()
		action, _, err := nav.HandleIntent(ctx, tc.Intent, false)
		elapsed := time.Since(start)

		r := benchResult{
			tc:      tc,
			elapsed: elapsed,
		}

		if err != nil {
			r.err = err
		} else if action != nil {
			r.macheID = action.MacheID
			r.pass = action.MacheID == tc.ExpectMacheID
		}

		// Count iterations from log output isn't feasible; use a rough estimate
		// based on latency (typically ~1s per iteration for cloud, less for local).
		r.iters = estimateIters(elapsed)

		results = append(results, r)
		printRow(r)
	}

	fmt.Println(strings.Repeat("\u2500", 78))
	printSummary(results)
}

func loadCases(path string) ([]benchCase, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cases []benchCase
	if err := json.Unmarshal(data, &cases); err != nil {
		return nil, err
	}
	return cases, nil
}

func buildNavGenerator(client *genai.Client, defaultModel string) (navigator.ContentGenerator, string) {
	if ep := os.Getenv("NAVIGATOR_ENDPOINT"); ep != "" {
		navModel := os.Getenv("NAVIGATOR_MODEL")
		if navModel == "" {
			navModel = "functiongemma:270m"
		}
		format := os.Getenv("NAVIGATOR_FORMAT")
		if format == "gemma" {
			log.Printf("Navigator: using Gemma model %s at %s", navModel, ep)
			return &navigator.GemmaGenerator{Endpoint: ep, Model: navModel}, navModel
		}
		log.Printf("Navigator: using local model %s at %s (OpenAI format)", navModel, ep)
		return &navigator.OllamaGenerator{Endpoint: ep, Model: navModel}, navModel
	}
	return &navigator.GeminiGenerator{Client: client}, defaultModel
}

func buildCartographer(defaultModel string) (schemaGenerator, bool) {
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
		log.Printf("Cartographer: cairn (gear=%d, scale=%.1f)", gear, scale)
		return &cartographer.CairnCartographer{Gear: gear, Scale: scale}, false
	case "tropical":
		log.Printf("Cartographer: tropical")
		return &cartographer.TropicalCartographer{}, false
	default:
		log.Printf("Cartographer: gemini (%s)", defaultModel)
		return nil, true // placeholder — filled after client init
	}
}

func getOrGenerateSchema(ctx context.Context, cart schemaGenerator, site string, cache map[string]string, validateIDs bool) (string, error) {
	if s, ok := cache[site]; ok {
		return s, nil
	}

	summary := loadSummary(site)
	screenshot, err := os.ReadFile(fmt.Sprintf("testdata/%s/page.png", site))
	if err != nil {
		return "", fmt.Errorf("read screenshot: %w", err)
	}

	log.Printf("Generating schema for %s...", site)
	schema, err := cart.GenerateSchema(ctx, screenshot, "image/png", summary)
	if err != nil {
		return "", fmt.Errorf("GenerateSchema: %w", err)
	}

	// Validate no hallucinated IDs (only relevant for VLM-based cartographers).
	if validateIDs {
		if bad := mache.ValidateSchema(schema, summary); len(bad) > 0 {
			return "", fmt.Errorf("hallucinated IDs in schema: %v", bad)
		}
	}

	cache[site] = schema
	return schema, nil
}

func loadSummary(site string) string {
	data, err := os.ReadFile(fmt.Sprintf("testdata/%s/page_summary.txt", site))
	if err != nil {
		log.Fatalf("Failed to read summary for %s: %v", site, err)
	}
	return string(data)
}

func estimateIters(d time.Duration) int {
	// The navigator pre-fills ls("/") as iteration 0, then does tool calls.
	// Minimum 2 iterations (cat children + act). Rough estimate from latency.
	n := int(d.Seconds()/1.0) + 1
	if n < 2 {
		n = 2
	}
	if n > 8 {
		n = 8
	}
	return n
}

func printRow(r benchResult) {
	result := "FAIL"
	if r.pass {
		result = "PASS"
	}
	macheID := r.macheID
	if macheID == "" {
		macheID = "-"
	}
	timeStr := fmt.Sprintf("%.1fs", r.elapsed.Seconds())
	itersStr := fmt.Sprintf("%d", r.iters)

	if r.err != nil {
		result = "ERR"
		macheID = "-"
		timeStr = "-"
		itersStr = "-"
		log.Printf("  Error: %v", r.err)
	}

	fmt.Printf("%-13s %-26s %-9s %-13s %-8s %s\n",
		r.tc.Site, r.tc.Intent, result, macheID, timeStr, itersStr)
}

func printSummary(results []benchResult) {
	passed := 0
	var totalLatency time.Duration
	totalIters := 0
	valid := 0

	for _, r := range results {
		if r.err != nil {
			continue
		}
		valid++
		if r.pass {
			passed++
		}
		totalLatency += r.elapsed
		totalIters += r.iters
	}

	total := len(results)
	pct := 0.0
	avgLatency := 0.0
	avgIters := 0.0
	if valid > 0 {
		pct = float64(passed) / float64(total) * 100
		avgLatency = totalLatency.Seconds() / float64(valid)
		avgIters = float64(totalIters) / float64(valid)
	}

	fmt.Printf("Result: %d/%d passed (%.0f%%)   Avg latency: %.1fs   Avg iterations: %.1f\n",
		passed, total, pct, avgLatency, avgIters)
}
