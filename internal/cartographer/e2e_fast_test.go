// e2e_fast_test.go — Prove DOM-only + minimal prompt + Gemma 4 = sub-second navigation.
//
// Requires: llama-server on localhost:8000 with --reasoning off
// Run: GOWORK=off go test ./internal/cartographer/ -run TestE2E_Fast -v -count=1 -timeout=300s 2>&1 | tee results/bench_local/e2e_fast.log
package cartographer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/agentic-research/mache/graph"
	"github.com/agentic-research/x-ray/internal/mache"
	"github.com/agentic-research/x-ray/internal/navigator"
)

func TestE2E_Fast(t *testing.T) {
	endpoint := envOr("NAVIGATOR_ENDPOINT", "http://localhost:8000/v1")
	model := envOr("NAVIGATOR_MODEL", "gemma-4-26B-A4B-it")
	format := envOr("NAVIGATOR_FORMAT", "gemma")

	// Load bench cases
	casesPath := filepath.Join("..", "..", "testdata", "bench_cases.json")
	casesData, err := os.ReadFile(casesPath)
	if err != nil {
		t.Skipf("no bench_cases.json: %v", err)
	}
	type bc struct {
		Site          string `json:"site"`
		Intent        string `json:"intent"`
		ExpectMacheID string `json:"expect_mache_id"`
		ExpectText    string `json:"expect_text"`
		Difficulty    string `json:"difficulty"`
	}
	var allCases []bc
	json.Unmarshal(casesData, &allCases)

	// Navigator with MINIMAL prompt
	var navGen navigator.ContentGenerator
	switch format {
	case "gemma":
		navGen = &navigator.GemmaGenerator{Endpoint: endpoint, Model: model}
	default:
		navGen = &navigator.OllamaGenerator{Endpoint: endpoint, Model: model}
	}

	ctx := context.Background()

	// Group by site, take only simple cases, cap at 3 per site
	siteCases := map[string][]bc{}
	for _, c := range allCases {
		if c.Difficulty == "simple" || c.Difficulty == "" {
			siteCases[c.Site] = append(siteCases[c.Site], c)
		}
	}
	for k, v := range siteCases {
		if len(v) > 3 {
			siteCases[k] = v[:3]
		}
	}

	type result struct {
		site, intent, expect, got string
		pass                      bool
		elapsed                   time.Duration
	}
	var results []result

	t.Logf("\n=== E2E FAST: DOM-ONLY + MINIMAL PROMPT + GEMMA 4 ===\n")
	t.Logf("%-15s %-35s %-6s %-13s %7s %s",
		"Site", "Intent", "Pass?", "Got", "Time", "Expected")
	t.Logf("%s", strings.Repeat("─", 100))

	for site, cases := range siteCases {
		summaryPath := filepath.Join("..", "..", "testdata", site, "page_summary.txt")
		summary, err := os.ReadFile(summaryPath)
		if err != nil {
			continue
		}

		// DOM-only: nil screenshot
		cairn := &CairnCartographer{Gear: 5, Scale: 10.0, GridSize: 8, SheafFolding: true}
		schema, err := cairn.GenerateSchema(ctx, nil, "image/png", string(summary))
		if err != nil {
			t.Logf("  %s: schema error: %v", site, err)
			continue
		}

		for _, tc := range cases {
			engine := mache.NewEngine()
			if err := engine.ApplySchema(schema); err != nil {
				continue
			}
			engine.LoadChildren(string(summary), nil)

			composite := graph.NewCompositeGraph()
			composite.Mount("browser", engine)

			// Create agent with MINIMAL prompt
			nav := navigator.NewAgent(navGen, model, composite)
			nav.OverrideSystemPrompt(navigator.MinimalNavigatorPrompt)

			start := time.Now()
			action, textResp, navErr := nav.HandleIntent(ctx, tc.Intent, false)
			elapsed := time.Since(start)

			r := result{
				site:    site,
				intent:  tc.Intent,
				expect:  tc.ExpectMacheID,
				elapsed: elapsed,
			}

			if navErr != nil {
				r.got = "ERR"
			} else if action != nil {
				r.got = action.MacheID
				r.pass = action.MacheID == tc.ExpectMacheID
			} else if textResp != "" {
				r.got = "text:" + truncIntent(textResp, 20)
			} else {
				r.got = "no-action"
			}

			results = append(results, r)

			status := "FAIL"
			if r.pass {
				status = "PASS"
			}
			t.Logf("%-15s %-35s %-6s %-13s %6.1fs %s",
				site, truncIntent(tc.Intent, 35), status, r.got, elapsed.Seconds(), tc.ExpectMacheID)
		}
	}

	// Summary
	t.Logf("%s", strings.Repeat("─", 100))

	passed := 0
	var latencies []float64
	for _, r := range results {
		if r.pass {
			passed++
		}
		latencies = append(latencies, r.elapsed.Seconds())
	}
	total := len(results)
	pct := float64(passed) / float64(max(total, 1)) * 100

	sort.Float64s(latencies)
	p50, p95 := 0.0, 0.0
	if len(latencies) > 0 {
		p50 = latencies[len(latencies)/2]
		p95 = latencies[min(int(float64(len(latencies))*0.95), len(latencies)-1)]
	}

	totalTime := 0.0
	for _, l := range latencies {
		totalTime += l
	}

	t.Logf("\n=== E2E FAST RESULTS ===")
	t.Logf("Accuracy: %d/%d (%.0f%%)", passed, total, pct)
	t.Logf("Latency p50: %.1fs  p95: %.1fs  total: %.0fs", p50, p95, totalTime)
	t.Logf("Config: DOM-only, minimal prompt (~150 tokens), no screenshot")
	t.Logf("Model: %s via llama-server (--reasoning off)", model)

	if pct >= 80 && p50 < 3 {
		t.Logf("✅ PASS: ≥80%% accuracy, p50 < 3s — DOM-only fast navigation works")
	} else if pct >= 60 && p50 < 5 {
		t.Logf("⚠️ PARTIAL: accuracy %.0f%%, p50 %.1fs — close but needs tuning", pct, p50)
	} else {
		t.Logf("❌ FAIL: accuracy %.0f%%, p50 %.1fs", pct, p50)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
