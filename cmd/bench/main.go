package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/jamesgardner/x-ray/internal/cartographer"
	"github.com/jamesgardner/x-ray/internal/mache"
	"github.com/jamesgardner/x-ray/internal/navigator"
	"github.com/joho/godotenv"
	"google.golang.org/genai"
)

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
	_ = godotenv.Load(".envrc")

	cases, err := loadCases("testdata/bench_cases.json")
	if err != nil {
		log.Fatalf("Failed to load bench cases: %v", err)
	}

	ctx := context.Background()
	client, err := genai.NewClient(ctx, nil)
	if err != nil {
		log.Fatalf("Failed to initialize Gemini client: %v", err)
	}

	model := os.Getenv("GEMINI_MODEL")
	if model == "" {
		model = "gemini-2.5-flash"
	}

	cart := cartographer.NewAgent(client, model)
	navGen, navModel := buildNavGenerator(client, model)

	schemaCache := map[string]string{} // site → schemaJSON
	var results []benchResult

	fmt.Println("=== X-Ray Navigation Benchmark ===")
	fmt.Println()
	fmt.Printf("%-13s %-26s %-9s %-13s %-8s %s\n",
		"Site", "Intent", "Result", "MacheID", "Time", "Iters")
	fmt.Println(strings.Repeat("\u2500", 78))

	for _, tc := range cases {
		schema, err := getOrGenerateSchema(ctx, cart, tc.Site, schemaCache)
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
		action, _, err := nav.HandleIntent(ctx, tc.Intent)
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

func getOrGenerateSchema(ctx context.Context, cart *cartographer.Agent, site string, cache map[string]string) (string, error) {
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

	// Validate no hallucinated IDs
	if bad := mache.ValidateSchema(schema, summary); len(bad) > 0 {
		return "", fmt.Errorf("hallucinated IDs in schema: %v", bad)
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
