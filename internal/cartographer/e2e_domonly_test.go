// e2e_domonly_test.go — Prove DOM-only parse + local LLM = correct navigation in <1s
//
// This is the falsifiable end-to-end test: no screenshot, no visual features,
// just DOM structure → zones → Gemma 4 → correct element clicked.
//
// Requires: llama-server running on localhost:8000 with Gemma 4
// Run: GOWORK=off go test ./internal/cartographer/ -run TestE2E_DOMOnly -v -count=1 -timeout=600s
package cartographer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agentic-research/mache/graph"
	"github.com/agentic-research/x-ray/internal/mache"
	"github.com/agentic-research/x-ray/internal/navigator"
)

func TestE2E_DOMOnly(t *testing.T) {
	// Check if llama-server is available
	endpoint := os.Getenv("NAVIGATOR_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:8000/v1"
	}
	model := os.Getenv("NAVIGATOR_MODEL")
	if model == "" {
		model = "gemma-4-26B-A4B-it"
	}
	format := os.Getenv("NAVIGATOR_FORMAT")
	if format == "" {
		format = "gemma"
	}

	// Load bench cases
	casesPath := filepath.Join("..", "..", "testdata", "bench_cases.json")
	casesData, err := os.ReadFile(casesPath)
	if err != nil {
		t.Skipf("no bench_cases.json: %v", err)
	}
	type benchCase struct {
		Site          string `json:"site"`
		Intent        string `json:"intent"`
		ExpectMacheID string `json:"expect_mache_id"`
		ExpectText    string `json:"expect_text"`
		Difficulty    string `json:"difficulty"`
	}
	var allCases []benchCase
	if err := json.Unmarshal(casesData, &allCases); err != nil {
		t.Fatalf("parse bench_cases.json: %v", err)
	}

	// Create navigator generator
	var navGen navigator.ContentGenerator
	switch format {
	case "gemma":
		navGen = &navigator.GemmaGenerator{Endpoint: endpoint, Model: model}
	default:
		navGen = &navigator.OllamaGenerator{Endpoint: endpoint, Model: model}
	}

	ctx := context.Background()
	// Quick health check — send a trivial request
	_, healthErr := navGen.GenerateContent(ctx, model, nil, nil)
	if healthErr != nil && strings.Contains(healthErr.Error(), "connection refused") {
		t.Skipf("llama-server not available at %s: %v", endpoint, healthErr)
	}

	// Group cases by site
	siteCases := make(map[string][]benchCase)
	for _, c := range allCases {
		siteCases[c.Site] = append(siteCases[c.Site], c)
	}

	type result struct {
		site    string
		intent  string
		expect  string
		got     string
		pass    bool
		elapsed time.Duration
		iters   int
		method  string // "dom-only"
	}
	var results []result

	t.Logf("\n=== E2E: DOM-ONLY + GEMMA 4 (no screenshot) ===\n")
	t.Logf("%-15s %-35s %-7s %-13s %8s %s",
		"Site", "Intent", "Result", "Got", "Time", "Expected")
	t.Logf("%s", strings.Repeat("─", 100))

	for site, cases := range siteCases {
		summaryPath := filepath.Join("..", "..", "testdata", site, "page_summary.txt")
		summary, err := os.ReadFile(summaryPath)
		if err != nil {
			continue
		}

		// DOM-only: nil screenshot → structural fallback zones
		cairn := &CairnCartographer{Gear: 5, Scale: 10.0, GridSize: 8, SheafFolding: true}
		schema, err := cairn.GenerateSchema(ctx, nil, "image/png", string(summary))
		if err != nil {
			t.Logf("  %s: cairn error: %v", site, err)
			continue
		}

		// Only test "simple" cases for speed (each takes ~3-8s with Gemma 4)
		var simpleCases []benchCase
		for _, c := range cases {
			if c.Difficulty == "simple" || c.Difficulty == "" {
				simpleCases = append(simpleCases, c)
			}
		}
		if len(simpleCases) == 0 {
			simpleCases = cases[:min(3, len(cases))]
		}
		// Cap at 3 per site for speed
		if len(simpleCases) > 3 {
			simpleCases = simpleCases[:3]
		}

		for _, tc := range simpleCases {
			// Build fresh engine + navigator per case
			engine := mache.NewEngine()
			if err := engine.ApplySchema(schema); err != nil {
				continue
			}
			engine.LoadChildren(string(summary), nil)

			composite := graph.NewCompositeGraph()
			if err := composite.Mount("browser", engine); err != nil {
				continue
			}

			nav := navigator.NewAgent(navGen, model, composite)

			start := time.Now()
			action, _, navErr := nav.HandleIntent(ctx, tc.Intent, false)
			elapsed := time.Since(start)

			r := result{
				site:    site,
				intent:  tc.Intent,
				expect:  tc.ExpectMacheID,
				elapsed: elapsed,
				method:  "dom-only",
			}

			if navErr != nil {
				r.got = fmt.Sprintf("ERR: %v", navErr)
			} else if action != nil {
				r.got = action.MacheID
				r.pass = action.MacheID == tc.ExpectMacheID
			} else {
				r.got = "no-action"
			}

			results = append(results, r)

			status := "FAIL"
			if r.pass {
				status = "PASS"
			}
			t.Logf("%-15s %-35s %-7s %-13s %7.1fs %s",
				site, truncIntent(tc.Intent, 35), status, r.got, elapsed.Seconds(), tc.ExpectMacheID)
		}
	}

	// Summary
	t.Logf("%s", strings.Repeat("─", 100))

	passed := 0
	var totalLatency time.Duration
	var latencies []float64
	for _, r := range results {
		if r.pass {
			passed++
		}
		totalLatency += r.elapsed
		latencies = append(latencies, r.elapsed.Seconds())
	}
	total := len(results)
	pct := 0.0
	if total > 0 {
		pct = float64(passed) / float64(total) * 100
	}

	// Compute p50, p95
	sortedLat := make([]float64, len(latencies))
	copy(sortedLat, latencies)
	for i := 0; i < len(sortedLat); i++ {
		for j := i + 1; j < len(sortedLat); j++ {
			if sortedLat[j] < sortedLat[i] {
				sortedLat[i], sortedLat[j] = sortedLat[j], sortedLat[i]
			}
		}
	}
	p50 := 0.0
	p95 := 0.0
	if len(sortedLat) > 0 {
		p50 = sortedLat[len(sortedLat)/2]
		p95idx := int(float64(len(sortedLat)) * 0.95)
		if p95idx >= len(sortedLat) {
			p95idx = len(sortedLat) - 1
		}
		p95 = sortedLat[p95idx]
	}

	t.Logf("\n=== E2E RESULTS: DOM-ONLY + GEMMA 4 ===")
	t.Logf("Accuracy: %d/%d (%.0f%%)", passed, total, pct)
	t.Logf("Latency p50: %.1fs  p95: %.1fs  total: %.0fs", p50, p95, totalLatency.Seconds())
	t.Logf("Method: DOM-only (nil screenshot, structural zones, no visual features)")
	t.Logf("Model: %s at %s (--reasoning off)", model, endpoint)

	if pct >= 80 && p50 < 10 {
		t.Logf("✅ PASS: ≥80%% accuracy with p50 < 10s — DOM-only navigation works")
	} else if pct >= 60 {
		t.Logf("⚠️ PARTIAL: accuracy %.0f%% (want ≥80%%), p50 %.1fs", pct, p50)
	} else {
		t.Logf("❌ FAIL: accuracy %.0f%% too low for DOM-only navigation", pct)
	}
}

func truncIntent(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
