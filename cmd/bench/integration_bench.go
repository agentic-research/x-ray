package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/agentic-research/mache/graph"

	"github.com/agentic-research/x-ray/internal/mache"
	"github.com/agentic-research/x-ray/internal/navigator"
)

// BenchConfig describes a navigator configuration for A/B testing.
type BenchConfig struct {
	Name         string                     `json:"name"`
	Cartographer schemaGenerator            `json:"-"`
	CartMode     string                     `json:"cart_mode"` // "cairn", "tropical", "gemini"
	NavGenerator navigator.ContentGenerator `json:"-"`
	NavModel     string                     `json:"nav_model"`
	NavEndpoint  string                     `json:"nav_endpoint,omitempty"`
	NavFormat    string                     `json:"nav_format,omitempty"` // "openai", "gemma"
}

// IntCaseResult holds per-case A/B comparison data.
type IntCaseResult struct {
	CaseID     string   `json:"case_id"`
	Site       string   `json:"site"`
	Intent     string   `json:"intent"`
	Difficulty string   `json:"difficulty"`
	PassA      bool     `json:"pass_a"`
	PassB      bool     `json:"pass_b"`
	MacheIDA   string   `json:"mache_id_a"`
	MacheIDB   string   `json:"mache_id_b"`
	LatencyA   float64  `json:"latency_a_sec"`
	LatencyB   float64  `json:"latency_b_sec"`
	ItersA     int      `json:"iters_a"`
	ItersB     int      `json:"iters_b"`
	ToolTraceA []string `json:"tool_trace_a,omitempty"`
	ToolTraceB []string `json:"tool_trace_b,omitempty"`
	ErrorA     string   `json:"error_a,omitempty"`
	ErrorB     string   `json:"error_b,omitempty"`
}

// IntegrationBenchReport is the JSON-serializable output of an integration comparison.
type IntegrationBenchReport struct {
	Timestamp string          `json:"timestamp"`
	ConfigA   string          `json:"config_a"`
	ConfigB   string          `json:"config_b"`
	Cases     []IntCaseResult `json:"cases"`

	// Accuracy.
	AccuracyA   float64 `json:"accuracy_a"`
	AccuracyB   float64 `json:"accuracy_b"`
	McNemarChi2 float64 `json:"mcnemar_chi2"`
	McNemarP    float64 `json:"mcnemar_p"`

	// Latency.
	LatencyA     StatSummary `json:"latency_a_summary"`
	LatencyB     StatSummary `json:"latency_b_summary"`
	LatencyWilcP float64     `json:"latency_wilcoxon_p"`
	LatencyCohD  float64     `json:"latency_cohens_d"`

	// Iterations.
	IterA     StatSummary `json:"iter_a_summary"`
	IterB     StatSummary `json:"iter_b_summary"`
	IterWilcP float64     `json:"iter_wilcoxon_p"`

	// Multi-test correction.
	BonferroniSig []bool `json:"bonferroni_significant"`
}

// runOneCase runs a single bench case against a given config, returning pass/fail,
// mache ID, latency, iteration count, tool trace, and any error.
func runOneCase(ctx context.Context, cfg BenchConfig, tc benchCase, schemaCache map[string]string) (pass bool, macheID string, elapsed time.Duration, iters int, toolTrace []string, err error) {
	schema, err := getOrGenerateSchema(ctx, cfg.Cartographer, tc.Site, schemaCache, cfg.CartMode == "gemini")
	if err != nil {
		return false, "", 0, 0, nil, fmt.Errorf("schema: %w", err)
	}

	engine := mache.NewEngine()
	if err := engine.ApplySchema(schema); err != nil {
		return false, "", 0, 0, nil, fmt.Errorf("apply schema: %w", err)
	}

	summary := loadSummary(tc.Site)
	engine.LoadChildren(summary, nil)

	composite := graph.NewCompositeGraph()
	if err := composite.Mount("browser", engine); err != nil {
		return false, "", 0, 0, nil, fmt.Errorf("mount: %w", err)
	}

	nav := navigator.NewAgent(cfg.NavGenerator, cfg.NavModel, composite)

	// Count tool calls via SetResultFunc callback.
	var iterCount atomic.Int32
	var trace []string
	nav.SetResultFunc(func(toolName string, _ map[string]any, _ string) {
		iterCount.Add(1)
		trace = append(trace, toolName)
	})

	start := time.Now()
	action, _, navErr := nav.HandleIntent(ctx, tc.Intent, false)
	elapsed = time.Since(start)

	iters = int(iterCount.Load())
	toolTrace = trace

	if navErr != nil {
		return false, "", elapsed, iters, toolTrace, navErr
	}

	if action != nil {
		macheID = action.MacheID
		pass = (macheID == tc.ExpectMacheID)
	}

	return pass, macheID, elapsed, iters, toolTrace, nil
}

// RunIntegrationBench runs all cases through both configs and produces a comparison report.
func RunIntegrationBench(ctx context.Context, cfgA, cfgB BenchConfig, cases []benchCase) IntegrationBenchReport {
	report := IntegrationBenchReport{
		Timestamp: time.Now().Format("2006-01-02T15:04:05Z07:00"),
		ConfigA:   cfgA.Name,
		ConfigB:   cfgB.Name,
	}

	schemaCacheA := map[string]string{}
	schemaCacheB := map[string]string{}

	var latenciesA, latenciesB []time.Duration
	var itersA, itersB []float64
	var pairedResults []PairedResult
	var passCountA, passCountB, validCount int

	fmt.Printf("=== Integration Bench: %s vs %s ===\n", cfgA.Name, cfgB.Name)
	fmt.Printf("%-13s %-26s %-6s %-6s %-8s %-8s\n",
		"Site", "Intent", "PassA", "PassB", "IterA", "IterB")
	fmt.Println(strings.Repeat("\u2500", 70))

	for i, tc := range cases {
		caseID := fmt.Sprintf("%s_%d", tc.Site, i)

		// Run config A.
		passA, midA, elapA, itA, traceA, errA := runOneCase(ctx, cfgA, tc, schemaCacheA)

		// Run config B.
		passB, midB, elapB, itB, traceB, errB := runOneCase(ctx, cfgB, tc, schemaCacheB)

		result := IntCaseResult{
			CaseID:     caseID,
			Site:       tc.Site,
			Intent:     tc.Intent,
			Difficulty: tc.Difficulty,
			PassA:      passA,
			PassB:      passB,
			MacheIDA:   midA,
			MacheIDB:   midB,
			LatencyA:   elapA.Seconds(),
			LatencyB:   elapB.Seconds(),
			ItersA:     itA,
			ItersB:     itB,
			ToolTraceA: traceA,
			ToolTraceB: traceB,
		}
		if errA != nil {
			result.ErrorA = errA.Error()
		}
		if errB != nil {
			result.ErrorB = errB.Error()
		}

		report.Cases = append(report.Cases, result)

		// Aggregate stats (skip if either errored).
		if errA == nil && errB == nil {
			validCount++
			latenciesA = append(latenciesA, elapA)
			latenciesB = append(latenciesB, elapB)
			itersA = append(itersA, float64(itA))
			itersB = append(itersB, float64(itB))
			pairedResults = append(pairedResults, PairedResult{
				CaseID: caseID,
				PassA:  passA,
				PassB:  passB,
			})
			if passA {
				passCountA++
			}
			if passB {
				passCountB++
			}
		}

		pA, pB := "FAIL", "FAIL"
		if passA {
			pA = "PASS"
		}
		if passB {
			pB = "PASS"
		}
		if errA != nil {
			pA = "ERR"
		}
		if errB != nil {
			pB = "ERR"
		}

		fmt.Printf("%-13s %-26s %-6s %-6s %-8d %-8d\n",
			tc.Site, tc.Intent, pA, pB, itA, itB)
	}

	// Compute stats.
	if validCount > 0 {
		report.AccuracyA = float64(passCountA) / float64(validCount)
		report.AccuracyB = float64(passCountB) / float64(validCount)
	}

	report.LatencyA = SummarizeLatencies(latenciesA)
	report.LatencyB = SummarizeLatencies(latenciesB)

	// Convert iterations to durations for SummarizeLatencies (reuse the stats infra).
	iterDurA := make([]time.Duration, len(itersA))
	iterDurB := make([]time.Duration, len(itersB))
	for i := range itersA {
		iterDurA[i] = time.Duration(itersA[i]) * time.Second
		iterDurB[i] = time.Duration(itersB[i]) * time.Second
	}
	report.IterA = SummarizeLatencies(iterDurA)
	report.IterB = SummarizeLatencies(iterDurB)

	// Statistical tests.
	if len(pairedResults) >= 3 {
		report.McNemarChi2, report.McNemarP = McNemarTest(pairedResults)

		valsA := make([]float64, len(latenciesA))
		valsB := make([]float64, len(latenciesB))
		for i := range latenciesA {
			valsA[i] = latenciesA[i].Seconds()
			valsB[i] = latenciesB[i].Seconds()
		}
		_, report.LatencyWilcP = PairedWilcoxon(valsA, valsB)
		report.LatencyCohD = CohensD(valsA, valsB)
		_, report.IterWilcP = PairedWilcoxon(itersA, itersB)

		// Bonferroni correction on the three tests.
		pValues := []float64{report.McNemarP, report.LatencyWilcP, report.IterWilcP}
		report.BonferroniSig = BonferroniCorrect(pValues, 0.05)
	}

	// Print summary.
	fmt.Println(strings.Repeat("\u2500", 70))
	fmt.Printf("Config A (%s): accuracy=%.1f%%  mean_latency=%.2fs  mean_iters=%.1f\n",
		cfgA.Name, report.AccuracyA*100, report.LatencyA.Mean, report.IterA.Mean)
	fmt.Printf("Config B (%s): accuracy=%.1f%%  mean_latency=%.2fs  mean_iters=%.1f\n",
		cfgB.Name, report.AccuracyB*100, report.LatencyB.Mean, report.IterB.Mean)
	if len(pairedResults) >= 3 {
		fmt.Printf("McNemar p=%.4f  Latency Wilcoxon p=%.4f (d=%.3f)  Iter Wilcoxon p=%.4f\n",
			report.McNemarP, report.LatencyWilcP, report.LatencyCohD, report.IterWilcP)
		fmt.Printf("Bonferroni significant (accuracy, latency, iters): %v\n", report.BonferroniSig)
	}

	return report
}

// writeReport writes a JSON report to the given path, creating parent directories.
func writeReport(path string, report any) error {
	dir := path[:strings.LastIndex(path, "/")]
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	log.Printf("Report written to %s (%d bytes)", path, len(data))
	return nil
}
