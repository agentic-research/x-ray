# Eval Harness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a three-tier offline evaluation harness with publication-grade statistical rigor for measuring x-ray navigator performance.

**Architecture:** Three tiers — unit (cartographer speed/quality, no model), integration (navigator accuracy/iterations, model required), e2e (voice-to-action latency). Each tier runs against frozen testdata, produces JSON with raw measurements + bootstrap CI + p-values. A/B comparison between configurations with Bonferroni correction.

**Tech Stack:** Go, gonum/stat for statistical tests, existing bench infrastructure in cmd/bench/

**Beads:** x-ray-259a4b, x-ray-25af1a, x-ray-25c4a3, x-ray-25d510

---

## File Structure

| File | Action | Responsibility |
|------|--------|----------------|
| `cmd/bench/stats.go` | Create | Statistical utility functions (bootstrap CI, Wilcoxon, McNemar, Cohen's d, Bonferroni) |
| `cmd/bench/stats_test.go` | Create | Known-answer tests for all statistical functions |
| `cmd/bench/unit_bench.go` | Create | Tier 1: cartographer A/B comparison (no model) |
| `cmd/bench/integration_bench.go` | Create | Tier 2: navigator A/B comparison (model required) |
| `cmd/bench/main.go` | Modify | Add `--mode`, `--config-a`, `--config-b`, `--output`, `--seeds` flags |
| `testdata/bench_cases.json` | Modify | Add `difficulty` field, youtube cases, desktop cases |

---

### Task 1: stats.go — Statistical utility functions

Create `cmd/bench/stats.go` with pure-Go statistical functions. No external dependencies — use `math`, `sort`, and `math/rand/v2` from stdlib.

**Files:**
- Create: `cmd/bench/stats.go`
- Create: `cmd/bench/stats_test.go`

- [ ] **Step 1: Write failing tests for StatSummary and SummarizeLatencies**

Create `cmd/bench/stats_test.go`:

```go
package main

import (
	"math"
	"testing"
	"time"
)

func TestSummarizeLatencies_Basic(t *testing.T) {
	// 10 durations: 100ms to 1000ms in 100ms steps.
	durations := make([]time.Duration, 10)
	for i := range durations {
		durations[i] = time.Duration((i+1)*100) * time.Millisecond
	}

	s := SummarizeLatencies(durations)

	if s.N != 10 {
		t.Errorf("N = %d, want 10", s.N)
	}
	// Mean = (100+200+...+1000)/10 = 550ms = 0.55s
	if math.Abs(s.Mean-0.55) > 0.001 {
		t.Errorf("Mean = %f, want 0.55", s.Mean)
	}
	// Median of 10 values = average of 5th and 6th = (500+600)/2 = 550ms
	if math.Abs(s.Median-0.55) > 0.001 {
		t.Errorf("Median = %f, want 0.55", s.Median)
	}
	// P50 = Median
	if math.Abs(s.P50-s.Median) > 0.001 {
		t.Errorf("P50 = %f, want %f", s.P50, s.Median)
	}
	// P95 should be near 1.0s (950ms or 1000ms depending on interpolation)
	if s.P95 < 0.9 || s.P95 > 1.1 {
		t.Errorf("P95 = %f, want ~0.95-1.0", s.P95)
	}
	// P99 should be near 1.0s
	if s.P99 < 0.9 || s.P99 > 1.1 {
		t.Errorf("P99 = %f, want ~1.0", s.P99)
	}
}

func TestSummarizeLatencies_Single(t *testing.T) {
	s := SummarizeLatencies([]time.Duration{500 * time.Millisecond})
	if s.N != 1 {
		t.Errorf("N = %d, want 1", s.N)
	}
	if s.Mean != 0.5 {
		t.Errorf("Mean = %f, want 0.5", s.Mean)
	}
	if s.StdDev != 0 {
		t.Errorf("StdDev = %f, want 0", s.StdDev)
	}
}

func TestSummarizeLatencies_Empty(t *testing.T) {
	s := SummarizeLatencies(nil)
	if s.N != 0 {
		t.Errorf("N = %d, want 0", s.N)
	}
}
```

Run: `cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go test ./cmd/bench/ -run TestSummarizeLatencies -count=1`
Expect: compilation failure (functions not defined yet).

- [ ] **Step 2: Write failing tests for BootstrapCI**

Add to `cmd/bench/stats_test.go`:

```go
func TestBootstrapCI_Uniform(t *testing.T) {
	// 1000 samples from [0, 1) — mean should be ~0.5, CI should contain 0.5.
	data := make([]float64, 1000)
	for i := range data {
		data[i] = float64(i) / 1000.0
	}

	lo, hi := BootstrapCI(data, 10000, 0.05)

	if lo > 0.5 || hi < 0.5 {
		t.Errorf("95%% CI [%f, %f] does not contain true mean 0.5", lo, hi)
	}
	// CI width should be narrow for n=1000.
	if hi-lo > 0.1 {
		t.Errorf("CI width %f too wide for n=1000", hi-lo)
	}
}

func TestBootstrapCI_SmallSample(t *testing.T) {
	data := []float64{1, 2, 3, 4, 5}
	lo, hi := BootstrapCI(data, 10000, 0.05)

	// Mean = 3.0, CI should contain it.
	if lo > 3.0 || hi < 3.0 {
		t.Errorf("95%% CI [%f, %f] does not contain true mean 3.0", lo, hi)
	}
	// CI should be wider than for n=1000.
	if hi-lo < 0.1 {
		t.Errorf("CI width %f suspiciously narrow for n=5", hi-lo)
	}
}

func TestBootstrapCI_Degenerate(t *testing.T) {
	// All same value — CI should be [v, v].
	data := []float64{42, 42, 42, 42, 42}
	lo, hi := BootstrapCI(data, 1000, 0.05)
	if lo != 42 || hi != 42 {
		t.Errorf("CI [%f, %f], want [42, 42]", lo, hi)
	}
}
```

Run: `cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go test ./cmd/bench/ -run TestBootstrapCI -count=1`
Expect: compilation failure.

- [ ] **Step 3: Write failing tests for Wilcoxon, McNemar, CohensD, Bonferroni**

Add to `cmd/bench/stats_test.go`:

```go
func TestPairedWilcoxon_Identical(t *testing.T) {
	a := []float64{1, 2, 3, 4, 5}
	b := []float64{1, 2, 3, 4, 5}
	_, p := PairedWilcoxon(a, b)
	// Identical distributions — p should be 1.0 (no difference).
	if p < 0.99 {
		t.Errorf("p = %f, want ~1.0 for identical inputs", p)
	}
}

func TestPairedWilcoxon_ClearDifference(t *testing.T) {
	a := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	b := []float64{11, 12, 13, 14, 15, 16, 17, 18, 19, 20}
	_, p := PairedWilcoxon(a, b)
	// Clear shift — p should be very small.
	if p > 0.05 {
		t.Errorf("p = %f, want < 0.05 for shifted inputs", p)
	}
}

func TestPairedWilcoxon_LengthMismatch(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for mismatched lengths")
		}
	}()
	PairedWilcoxon([]float64{1, 2}, []float64{1})
}

func TestMcNemarTest_NoChange(t *testing.T) {
	// All pairs agree — b→c and c→b counts are zero.
	pairs := []PairedResult{
		{CaseID: "1", PassA: true, PassB: true},
		{CaseID: "2", PassA: false, PassB: false},
		{CaseID: "3", PassA: true, PassB: true},
	}
	_, p := McNemarTest(pairs)
	if p < 0.99 {
		t.Errorf("p = %f, want ~1.0 for no discordant pairs", p)
	}
}

func TestMcNemarTest_ClearDifference(t *testing.T) {
	// B is strictly better: 20 cases where A fails but B passes, 0 reverse.
	var pairs []PairedResult
	for i := 0; i < 20; i++ {
		pairs = append(pairs, PairedResult{
			CaseID: fmt.Sprintf("%d", i),
			PassA:  false,
			PassB:  true,
		})
	}
	_, p := McNemarTest(pairs)
	if p > 0.01 {
		t.Errorf("p = %f, want < 0.01 for 20 discordant pairs", p)
	}
}

func TestCohensD_Zero(t *testing.T) {
	a := []float64{5, 5, 5}
	b := []float64{5, 5, 5}
	d := CohensD(a, b)
	if d != 0 {
		t.Errorf("d = %f, want 0", d)
	}
}

func TestCohensD_Large(t *testing.T) {
	// Mean diff = 10, pooled SD ~ 1 → d ~ 10.
	a := []float64{0, 1, 0, 1, 0}
	b := []float64{10, 11, 10, 11, 10}
	d := CohensD(a, b)
	if d < 5.0 {
		t.Errorf("d = %f, want large effect size", d)
	}
}

func TestBonferroniCorrect(t *testing.T) {
	pValues := []float64{0.01, 0.03, 0.06}
	// alpha = 0.05 → corrected threshold = 0.05/3 ≈ 0.0167
	results := BonferroniCorrect(pValues, 0.05)
	if !results[0] {
		t.Error("p=0.01 should be significant after Bonferroni (threshold 0.0167)")
	}
	if results[1] {
		t.Error("p=0.03 should NOT be significant after Bonferroni (threshold 0.0167)")
	}
	if results[2] {
		t.Error("p=0.06 should NOT be significant after Bonferroni (threshold 0.0167)")
	}
}
```

Note: Add `"fmt"` to the imports if not already present.

Run: `cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go test ./cmd/bench/ -run "TestPairedWilcoxon|TestMcNemar|TestCohensD|TestBonferroni" -count=1`
Expect: compilation failure.

- [ ] **Step 4: Implement stats.go types and SummarizeLatencies**

Create `cmd/bench/stats.go`:

```go
package main

import (
	"math"
	"math/rand/v2"
	"sort"
	"time"
)

// PairedResult records pass/fail for the same case under two configs.
type PairedResult struct {
	CaseID string
	PassA  bool
	PassB  bool
}

// StatSummary holds descriptive statistics for a set of latency measurements.
type StatSummary struct {
	N      int     `json:"n"`
	Mean   float64 `json:"mean"`
	Median float64 `json:"median"`
	StdDev float64 `json:"std_dev"`
	P50    float64 `json:"p50"`
	P95    float64 `json:"p95"`
	P99    float64 `json:"p99"`
	CI95Lo float64 `json:"ci95_lo"`
	CI95Hi float64 `json:"ci95_hi"`
}

// SummarizeLatencies computes descriptive statistics from a slice of durations.
// All time values are reported in seconds (float64).
func SummarizeLatencies(durations []time.Duration) StatSummary {
	n := len(durations)
	if n == 0 {
		return StatSummary{}
	}

	vals := make([]float64, n)
	sum := 0.0
	for i, d := range durations {
		vals[i] = d.Seconds()
		sum += vals[i]
	}
	mean := sum / float64(n)

	sort.Float64s(vals)

	// Variance (population for descriptive stats).
	varSum := 0.0
	for _, v := range vals {
		diff := v - mean
		varSum += diff * diff
	}
	stdDev := 0.0
	if n > 1 {
		stdDev = math.Sqrt(varSum / float64(n-1))
	}

	lo, hi := BootstrapCI(vals, 10000, 0.05)

	return StatSummary{
		N:      n,
		Mean:   mean,
		Median: percentile(vals, 0.50),
		StdDev: stdDev,
		P50:    percentile(vals, 0.50),
		P95:    percentile(vals, 0.95),
		P99:    percentile(vals, 0.99),
		CI95Lo: lo,
		CI95Hi: hi,
	}
}

// percentile returns the p-th percentile of sorted data using linear interpolation.
func percentile(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n == 1 {
		return sorted[0]
	}
	idx := p * float64(n-1)
	lo := int(math.Floor(idx))
	hi := lo + 1
	if hi >= n {
		return sorted[n-1]
	}
	frac := idx - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}
```

Run: `cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go build ./cmd/bench/`
Expect: compilation failure (BootstrapCI not yet defined).

- [ ] **Step 5: Implement BootstrapCI**

Add to `cmd/bench/stats.go`:

```go
// BootstrapCI computes a bootstrap confidence interval for the mean of data.
// nResamples is typically 10000. alpha is the significance level (0.05 for 95% CI).
// Returns (lo, hi) bounds of the confidence interval.
func BootstrapCI(data []float64, nResamples int, alpha float64) (lo, hi float64) {
	n := len(data)
	if n == 0 {
		return 0, 0
	}
	if n == 1 {
		return data[0], data[0]
	}

	// Check if all values are identical (common in degenerate cases).
	allSame := true
	for i := 1; i < n; i++ {
		if data[i] != data[0] {
			allSame = false
			break
		}
	}
	if allSame {
		return data[0], data[0]
	}

	rng := rand.New(rand.NewPCG(42, 0)) // deterministic for reproducibility

	means := make([]float64, nResamples)
	for r := 0; r < nResamples; r++ {
		sum := 0.0
		for i := 0; i < n; i++ {
			sum += data[rng.IntN(n)]
		}
		means[r] = sum / float64(n)
	}

	sort.Float64s(means)

	loIdx := int(math.Floor(alpha / 2.0 * float64(nResamples)))
	hiIdx := int(math.Ceil((1.0 - alpha/2.0) * float64(nResamples)))
	if hiIdx >= nResamples {
		hiIdx = nResamples - 1
	}

	return means[loIdx], means[hiIdx]
}
```

Run: `cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go test ./cmd/bench/ -run "TestSummarizeLatencies|TestBootstrapCI" -count=1`
Expect: all pass.

- [ ] **Step 6: Implement PairedWilcoxon**

Add to `cmd/bench/stats.go`:

```go
// PairedWilcoxon computes the Wilcoxon signed-rank test statistic and
// approximate p-value for paired samples a and b. Panics if len(a) != len(b).
// Uses normal approximation for n >= 10, exact enumeration is not implemented.
// For small n, the p-value is approximate.
func PairedWilcoxon(a, b []float64) (stat, p float64) {
	if len(a) != len(b) {
		panic("PairedWilcoxon: mismatched lengths")
	}

	// Compute differences, discarding zeros.
	type ranked struct {
		absDiff float64
		sign    float64
	}
	var diffs []ranked
	for i := range a {
		d := a[i] - b[i]
		if d == 0 {
			continue
		}
		sign := 1.0
		if d < 0 {
			sign = -1.0
		}
		diffs = append(diffs, ranked{absDiff: math.Abs(d), sign: sign})
	}

	n := len(diffs)
	if n == 0 {
		// No differences — distributions are identical.
		return 0, 1.0
	}

	// Rank by absolute difference.
	sort.Slice(diffs, func(i, j int) bool {
		return diffs[i].absDiff < diffs[j].absDiff
	})

	// Assign ranks with average ties.
	ranks := make([]float64, n)
	i := 0
	for i < n {
		j := i + 1
		for j < n && diffs[j].absDiff == diffs[i].absDiff {
			j++
		}
		avgRank := float64(i+j+1) / 2.0 // 1-based average rank
		for k := i; k < j; k++ {
			ranks[k] = avgRank
		}
		i = j
	}

	// W+ = sum of ranks where sign is positive.
	wPlus := 0.0
	wMinus := 0.0
	for i, d := range diffs {
		if d.sign > 0 {
			wPlus += ranks[i]
		} else {
			wMinus += ranks[i]
		}
	}

	// Test statistic: smaller of W+ and W-.
	stat = math.Min(wPlus, wMinus)

	// Normal approximation for p-value.
	nf := float64(n)
	mu := nf * (nf + 1) / 4.0
	sigma := math.Sqrt(nf * (nf + 1) * (2*nf + 1) / 24.0)

	if sigma == 0 {
		return stat, 1.0
	}

	z := (stat - mu) / sigma
	// Two-tailed p-value using standard normal CDF approximation.
	p = 2.0 * normalCDF(z)
	if p > 1.0 {
		p = 1.0
	}
	return stat, p
}

// normalCDF approximates the cumulative distribution function of the
// standard normal distribution using the Abramowitz & Stegun formula.
func normalCDF(z float64) float64 {
	if z > 0 {
		return 1.0 - normalCDF(-z)
	}
	// For z <= 0, use the approximation.
	t := 1.0 / (1.0 + 0.2316419*math.Abs(z))
	d := 0.3989422804014327 // 1/sqrt(2*pi)
	p := d * math.Exp(-z*z/2.0) * (t*(0.319381530 + t*(-0.356563782 + t*(1.781477937 + t*(-1.821255978 + t*1.330274429)))))
	return p
}
```

Run: `cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go test ./cmd/bench/ -run TestPairedWilcoxon -count=1`
Expect: all pass.

- [ ] **Step 7: Implement McNemarTest, CohensD, BonferroniCorrect**

Add to `cmd/bench/stats.go`:

```go
// McNemarTest performs McNemar's test for paired nominal data.
// Returns chi-squared statistic and p-value.
// Tests whether the marginal frequencies of two binary outcomes differ.
func McNemarTest(paired []PairedResult) (chi2, p float64) {
	// b = A pass, B fail (A is better).
	// c = A fail, B pass (B is better).
	var b, c float64
	for _, pr := range paired {
		if pr.PassA && !pr.PassB {
			b++
		}
		if !pr.PassA && pr.PassB {
			c++
		}
	}

	if b+c == 0 {
		// No discordant pairs — no difference.
		return 0, 1.0
	}

	// McNemar's chi-squared with continuity correction (Edwards).
	diff := math.Abs(b-c) - 1.0
	if diff < 0 {
		diff = 0
	}
	chi2 = (diff * diff) / (b + c)

	// p-value from chi-squared distribution with 1 df.
	// Using the relationship: P(X > x) = erfc(sqrt(x/2)) for chi-sq(1).
	p = math.Erfc(math.Sqrt(chi2 / 2.0))
	return chi2, p
}

// CohensD computes Cohen's d effect size for two independent samples.
// Uses pooled standard deviation.
func CohensD(a, b []float64) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	meanA := mean(a)
	meanB := mean(b)
	varA := variance(a, meanA)
	varB := variance(b, meanB)

	nA := float64(len(a))
	nB := float64(len(b))

	// Pooled standard deviation.
	pooledVar := ((nA-1)*varA + (nB-1)*varB) / (nA + nB - 2)
	if pooledVar == 0 {
		if meanA == meanB {
			return 0
		}
		return math.Inf(1)
	}

	return (meanA - meanB) / math.Sqrt(pooledVar)
}

// BonferroniCorrect applies Bonferroni correction to a slice of p-values.
// Returns a bool slice: true means the test is still significant after correction.
func BonferroniCorrect(pValues []float64, alpha float64) []bool {
	m := float64(len(pValues))
	if m == 0 {
		return nil
	}
	correctedAlpha := alpha / m
	results := make([]bool, len(pValues))
	for i, p := range pValues {
		results[i] = p < correctedAlpha
	}
	return results
}

// mean computes the arithmetic mean.
func mean(data []float64) float64 {
	if len(data) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range data {
		sum += v
	}
	return sum / float64(len(data))
}

// variance computes sample variance given a precomputed mean.
func variance(data []float64, m float64) float64 {
	if len(data) < 2 {
		return 0
	}
	sum := 0.0
	for _, v := range data {
		d := v - m
		sum += d * d
	}
	return sum / float64(len(data)-1)
}
```

Run: `cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go test ./cmd/bench/ -run "TestMcNemar|TestCohensD|TestBonferroni" -count=1`
Expect: all pass.

- [ ] **Step 8: Run all stats tests, verify green**

Run: `cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go test ./cmd/bench/ -run "TestSummarize|TestBootstrap|TestPaired|TestMcNemar|TestCohensD|TestBonferroni" -v -count=1`
Expect: all pass.

Commit: `[x-ray-259a4b] feat(bench): add statistical utility functions — bootstrap CI, Wilcoxon, McNemar, Cohen's d, Bonferroni`

---

### Task 2: bench_cases.json expansion + difficulty field

Add a `difficulty` field to the bench case struct and to every case in the JSON file. Add new cases for youtube and desktop sites. Update the summary output to show difficulty breakdown.

**Files:**
- Modify: `cmd/bench/main.go`
- Modify: `testdata/bench_cases.json`

- [ ] **Step 1: Add difficulty field to benchCase struct**

Edit `cmd/bench/main.go` — add `Difficulty` field to the `benchCase` struct:

```go
type benchCase struct {
	Site          string `json:"site"`
	Intent        string `json:"intent"`
	ExpectMacheID string `json:"expect_mache_id"`
	ExpectText    string `json:"expect_text"`
	Difficulty    string `json:"difficulty"` // "simple", "medium", or "hard"
}
```

Run: `cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go build ./cmd/bench/`
Expect: compiles (JSON field is optional, existing cases without it unmarshal as "").

- [ ] **Step 2: Add difficulty labels to all existing 45 cases and add youtube/desktop cases**

Edit `testdata/bench_cases.json`. Assign difficulty based on:
- **simple**: exact text match in the UI (e.g., "click Sign in", "click the Search link") — direct element name
- **medium**: requires identifying ordinal or contextual position (e.g., "click the first story", "click the second story")
- **hard**: requires semantic understanding or ambiguous reference (e.g., "click the story about database transactions", "click the post about James Webb Telescope")

Add these youtube cases (testdata/youtube/ has page.png + page_summary.txt with IDs from the CDP summary):

```json
{"site": "youtube", "intent": "click the Search button", "expect_mache_id": "mache-2", "expect_text": "Search", "difficulty": "simple"},
{"site": "youtube", "intent": "click the Guide button", "expect_mache_id": "mache-0", "expect_text": "Guide", "difficulty": "simple"},
{"site": "youtube", "intent": "click the Create button", "expect_mache_id": "mache-13", "expect_text": "Create", "difficulty": "simple"},
{"site": "youtube", "intent": "click the Gaming filter", "expect_mache_id": "mache-17", "expect_text": "Gaming", "difficulty": "simple"},
{"site": "youtube", "intent": "click the Music filter", "expect_mache_id": "mache-18", "expect_text": "Music", "difficulty": "simple"},
{"site": "youtube", "intent": "click the All filter", "expect_mache_id": "mache-16", "expect_text": "All", "difficulty": "simple"},
{"site": "youtube", "intent": "click the Notifications button", "expect_mache_id": "mache-14", "expect_text": "Notifications", "difficulty": "medium"},
{"site": "youtube", "intent": "click Search with your voice", "expect_mache_id": "mache-12", "expect_text": "Search with your voice", "difficulty": "medium"}
```

Desktop testdata only has `page.png` (no `page_summary.txt`), so skip desktop cases for now. Add a comment in the JSON noting this.

The updated JSON file should have 53 cases total (45 existing + 8 youtube).

Run: `cd /Users/jamesgardner/remotes/art/x-ray && python3 -c "import json; cases=json.load(open('testdata/bench_cases.json')); print(f'{len(cases)} cases'); print({c['difficulty'] for c in cases}); print({c['site'] for c in cases})"`
Expect: 53 cases, difficulties {simple, medium, hard}, sites include youtube.

- [ ] **Step 3: Update printRow to show difficulty**

Edit `cmd/bench/main.go` — update `printRow` to include difficulty in the output and adjust the header:

```go
// In main(), update the header:
fmt.Printf("%-13s %-26s %-8s %-9s %-13s %-8s %s\n",
	"Site", "Intent", "Diff", "Result", "MacheID", "Time", "Iters")
fmt.Println(strings.Repeat("\u2500", 86))

// In printRow(), add difficulty:
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
	diff := r.tc.Difficulty
	if diff == "" {
		diff = "?"
	}

	if r.err != nil {
		result = "ERR"
		macheID = "-"
		timeStr = "-"
		itersStr = "-"
		log.Printf("  Error: %v", r.err)
	}

	fmt.Printf("%-13s %-26s %-8s %-9s %-13s %-8s %s\n",
		r.tc.Site, r.tc.Intent, diff, result, macheID, timeStr, itersStr)
}
```

- [ ] **Step 4: Update printSummary for difficulty breakdown**

Edit `cmd/bench/main.go` — update `printSummary` to show per-difficulty stats:

```go
func printSummary(results []benchResult) {
	passed := 0
	var totalLatency time.Duration
	totalIters := 0
	valid := 0

	// Per-difficulty tracking.
	type diffStats struct {
		total, passed, valid int
	}
	byDiff := map[string]*diffStats{}

	for _, r := range results {
		d := r.tc.Difficulty
		if d == "" {
			d = "unknown"
		}
		if byDiff[d] == nil {
			byDiff[d] = &diffStats{}
		}
		byDiff[d].total++

		if r.err != nil {
			continue
		}
		valid++
		byDiff[d].valid++
		if r.pass {
			passed++
			byDiff[d].passed++
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

	// Difficulty breakdown.
	for _, d := range []string{"simple", "medium", "hard"} {
		s := byDiff[d]
		if s == nil {
			continue
		}
		dpct := 0.0
		if s.total > 0 {
			dpct = float64(s.passed) / float64(s.total) * 100
		}
		fmt.Printf("  %-8s %d/%d (%.0f%%)\n", d+":", s.passed, s.total, dpct)
	}
}
```

Run: `cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go build ./cmd/bench/`
Expect: compiles.

Commit: `[x-ray-25af1a] feat(bench): add difficulty field to bench cases, expand to 53 cases with youtube`

---

### Task 3: Unit bench — cartographer A/B comparison

Create `cmd/bench/unit_bench.go` that compares two cartographers on the same testdata without needing any LLM for navigation. This tier measures cartographer latency and output quality.

**Files:**
- Create: `cmd/bench/unit_bench.go`

- [ ] **Step 1: Define the UnitBenchReport types**

Create `cmd/bench/unit_bench.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

// UnitSiteResult holds per-site comparison data for two cartographers.
type UnitSiteResult struct {
	Site      string      `json:"site"`
	LatencyA  float64     `json:"latency_a_sec"`
	LatencyB  float64     `json:"latency_b_sec"`
	ZonesA    int         `json:"zones_a"`
	ZonesB    int         `json:"zones_b"`
	Jaccard   float64     `json:"jaccard_similarity"`
	SchemaA   string      `json:"schema_a,omitempty"`
	SchemaB   string      `json:"schema_b,omitempty"`
}

// UnitBenchReport is the JSON-serializable output of a unit-tier comparison.
type UnitBenchReport struct {
	Timestamp   string           `json:"timestamp"`
	CartA       string           `json:"cart_a"`
	CartB       string           `json:"cart_b"`
	Sites       []UnitSiteResult `json:"sites"`
	LatencyA    StatSummary      `json:"latency_a_summary"`
	LatencyB    StatSummary      `json:"latency_b_summary"`
	WilcoxonP   float64          `json:"wilcoxon_p"`
	CohensD     float64          `json:"cohens_d"`
	MeanJaccard float64          `json:"mean_jaccard"`
}
```

Run: `cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go build ./cmd/bench/`
Expect: compiles (unused imports aside — will use them in next step).

- [ ] **Step 2: Implement ZoneJaccard**

Add to `cmd/bench/unit_bench.go`:

```go
// zoneMount is a minimal representation of a schema mount for Jaccard comparison.
type zoneMount struct {
	Path string `json:"path"`
	// Also accept virtual_path for backwards compat.
	VirtualPath string `json:"virtual_path"`
}

type zoneSchema struct {
	Mounts []zoneMount `json:"mounts"`
}

// ZoneJaccard computes the Jaccard similarity index between two schemas
// based on their zone virtual paths. Returns 0.0 for disjoint, 1.0 for identical.
func ZoneJaccard(schemaA, schemaB string) float64 {
	pathsA := extractPaths(schemaA)
	pathsB := extractPaths(schemaB)

	if len(pathsA) == 0 && len(pathsB) == 0 {
		return 1.0 // Both empty = identical.
	}

	// Intersection and union.
	setA := map[string]bool{}
	for _, p := range pathsA {
		setA[p] = true
	}
	setB := map[string]bool{}
	for _, p := range pathsB {
		setB[p] = true
	}

	intersection := 0
	for p := range setA {
		if setB[p] {
			intersection++
		}
	}

	union := len(setA)
	for p := range setB {
		if !setA[p] {
			union++
		}
	}

	if union == 0 {
		return 1.0
	}
	return float64(intersection) / float64(union)
}

func extractPaths(schema string) []string {
	var s zoneSchema
	if err := json.Unmarshal([]byte(schema), &s); err != nil {
		return nil
	}
	var paths []string
	for _, m := range s.Mounts {
		p := m.Path
		if p == "" {
			p = m.VirtualPath
		}
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}
```

- [ ] **Step 3: Implement RunUnitBench**

Add to `cmd/bench/unit_bench.go`:

```go
// RunUnitBench runs both cartographers on each site, measures latency, and
// compares zone output using Jaccard similarity.
func RunUnitBench(ctx context.Context, cartA, cartB schemaGenerator, nameA, nameB string, sites []string) UnitBenchReport {
	report := UnitBenchReport{
		Timestamp: time.Now().Format("2006-01-02T15:04:05Z07:00"),
		CartA:     nameA,
		CartB:     nameB,
	}

	var latenciesA, latenciesB []time.Duration
	jaccardSum := 0.0

	for _, site := range sites {
		dir := fmt.Sprintf("testdata/%s", site)
		summary, err := os.ReadFile(dir + "/page_summary.txt")
		if err != nil {
			log.Printf("unit_bench: skip %s: %v", site, err)
			continue
		}
		screenshot, err := os.ReadFile(dir + "/page.png")
		if err != nil {
			log.Printf("unit_bench: skip %s: no screenshot", site)
			continue
		}

		// Run cartographer A.
		startA := time.Now()
		schemaA, errA := cartA.GenerateSchema(ctx, screenshot, "image/png", string(summary))
		elapsedA := time.Since(startA)

		// Run cartographer B.
		startB := time.Now()
		schemaB, errB := cartB.GenerateSchema(ctx, screenshot, "image/png", string(summary))
		elapsedB := time.Since(startB)

		if errA != nil || errB != nil {
			log.Printf("unit_bench: %s: cartA err=%v, cartB err=%v", site, errA, errB)
			continue
		}

		jac := ZoneJaccard(schemaA, schemaB)
		zonesA := len(extractPaths(schemaA))
		zonesB := len(extractPaths(schemaB))

		result := UnitSiteResult{
			Site:     site,
			LatencyA: elapsedA.Seconds(),
			LatencyB: elapsedB.Seconds(),
			ZonesA:   zonesA,
			ZonesB:   zonesB,
			Jaccard:  jac,
		}

		report.Sites = append(report.Sites, result)
		latenciesA = append(latenciesA, elapsedA)
		latenciesB = append(latenciesB, elapsedB)
		jaccardSum += jac

		fmt.Printf("  %-15s A=%.3fs (%d zones)  B=%.3fs (%d zones)  Jaccard=%.3f\n",
			site, elapsedA.Seconds(), zonesA, elapsedB.Seconds(), zonesB, jac)
	}

	nSites := len(report.Sites)

	report.LatencyA = SummarizeLatencies(latenciesA)
	report.LatencyB = SummarizeLatencies(latenciesB)

	if nSites > 0 {
		report.MeanJaccard = jaccardSum / float64(nSites)
	}

	// Paired statistical tests on latencies.
	if nSites >= 3 {
		valsA := make([]float64, nSites)
		valsB := make([]float64, nSites)
		for i, s := range report.Sites {
			valsA[i] = s.LatencyA
			valsB[i] = s.LatencyB
		}
		_, report.WilcoxonP = PairedWilcoxon(valsA, valsB)
		report.CohensD = CohensD(valsA, valsB)
	}

	// Print summary.
	fmt.Println(strings.Repeat("\u2500", 78))
	fmt.Printf("Cart A (%s): mean=%.3fs  median=%.3fs  p95=%.3fs\n",
		nameA, report.LatencyA.Mean, report.LatencyA.Median, report.LatencyA.P95)
	fmt.Printf("Cart B (%s): mean=%.3fs  median=%.3fs  p95=%.3fs\n",
		nameB, report.LatencyB.Mean, report.LatencyB.Median, report.LatencyB.P95)
	fmt.Printf("Mean Jaccard similarity: %.3f\n", report.MeanJaccard)
	if nSites >= 3 {
		fmt.Printf("Wilcoxon p=%.4f  Cohen's d=%.3f\n", report.WilcoxonP, report.CohensD)
	}

	return report
}
```

Run: `cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go build ./cmd/bench/`
Expect: compiles.

Commit: `[x-ray-25af1a] feat(bench): add unit-tier cartographer A/B comparison with Jaccard similarity`

---

### Task 4: Integration bench — navigator A/B comparison

Create `cmd/bench/integration_bench.go` for comparing two full navigator configurations (cartographer + model) on the bench cases.

**Files:**
- Create: `cmd/bench/integration_bench.go`

- [ ] **Step 1: Define BenchConfig and IntegrationBenchReport types**

Create `cmd/bench/integration_bench.go`:

```go
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
	Name          string          `json:"name"`
	Cartographer  schemaGenerator `json:"-"`
	CartMode      string          `json:"cart_mode"` // "cairn", "tropical", "gemini"
	NavGenerator  navigator.ContentGenerator `json:"-"`
	NavModel      string          `json:"nav_model"`
	NavEndpoint   string          `json:"nav_endpoint,omitempty"`
	NavFormat     string          `json:"nav_format,omitempty"` // "openai", "gemma"
}

// IntCaseResult holds per-case A/B comparison data.
type IntCaseResult struct {
	CaseID     string  `json:"case_id"`
	Site       string  `json:"site"`
	Intent     string  `json:"intent"`
	Difficulty string  `json:"difficulty"`
	PassA      bool    `json:"pass_a"`
	PassB      bool    `json:"pass_b"`
	MacheIDA   string  `json:"mache_id_a"`
	MacheIDB   string  `json:"mache_id_b"`
	LatencyA   float64 `json:"latency_a_sec"`
	LatencyB   float64 `json:"latency_b_sec"`
	ItersA     int     `json:"iters_a"`
	ItersB     int     `json:"iters_b"`
	ToolTraceA []string `json:"tool_trace_a,omitempty"`
	ToolTraceB []string `json:"tool_trace_b,omitempty"`
	ErrorA     string  `json:"error_a,omitempty"`
	ErrorB     string  `json:"error_b,omitempty"`
}

// IntegrationBenchReport is the JSON-serializable output of an integration comparison.
type IntegrationBenchReport struct {
	Timestamp    string          `json:"timestamp"`
	ConfigA      string          `json:"config_a"`
	ConfigB      string          `json:"config_b"`
	Cases        []IntCaseResult `json:"cases"`

	// Accuracy.
	AccuracyA    float64         `json:"accuracy_a"`
	AccuracyB    float64         `json:"accuracy_b"`
	McNemarChi2  float64         `json:"mcnemar_chi2"`
	McNemarP     float64         `json:"mcnemar_p"`

	// Latency.
	LatencyA     StatSummary     `json:"latency_a_summary"`
	LatencyB     StatSummary     `json:"latency_b_summary"`
	LatencyWilcP float64         `json:"latency_wilcoxon_p"`
	LatencyCohD  float64         `json:"latency_cohens_d"`

	// Iterations.
	IterA        StatSummary     `json:"iter_a_summary"`
	IterB        StatSummary     `json:"iter_b_summary"`
	IterWilcP    float64         `json:"iter_wilcoxon_p"`

	// Multi-test correction.
	BonferroniSig []bool         `json:"bonferroni_significant"`
}
```

Run: `cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go build ./cmd/bench/`
Expect: compiles.

- [ ] **Step 2: Implement runOneCase helper**

Add to `cmd/bench/integration_bench.go`:

```go
// runOneCase runs a single bench case against a given config, returning pass/fail,
// mache ID, latency, iteration count, tool trace, and any error.
func runOneCase(ctx context.Context, cfg BenchConfig, tc benchCase, schemaCache map[string]string) (pass bool, macheID string, elapsed time.Duration, iters int, toolTrace []string, err error) {
	schema, err := getOrGenerateSchema(ctx, cfg.Cartographer, tc.Site, schemaCache, cfg.CartMode == "gemini")
	if err != nil {
		return false, "", 0, 0, nil, fmt.Errorf("schema: %w", err)
	}

	engine := mache.NewEngine()
	if err := engine.ApplySchema(schema); err != nil {
		return false, "", 0, 0, nil, fmt.Errorf("ApplySchema: %w", err)
	}

	summary := loadSummary(tc.Site)
	engine.LoadChildren(summary, nil)

	composite := graph.NewCompositeGraph()
	if err := composite.Mount("browser", engine); err != nil {
		return false, "", 0, 0, nil, fmt.Errorf("Mount: %w", err)
	}

	nav := navigator.NewAgent(cfg.NavGenerator, cfg.NavModel, composite)

	// Count tool calls via SetResultFunc callback.
	var iterCount atomic.Int32
	var trace []string
	nav.SetResultFunc(func(toolName string, args map[string]any, result string) {
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
```

- [ ] **Step 3: Implement RunIntegrationBench**

Add to `cmd/bench/integration_bench.go`:

```go
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
		if passA { pA = "PASS" }
		if passB { pB = "PASS" }
		if errA != nil { pA = "ERR" }
		if errB != nil { pB = "ERR" }

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
```

Run: `cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go build ./cmd/bench/`
Expect: compiles.

- [ ] **Step 4: Add writeReport helper**

Add to `cmd/bench/integration_bench.go`:

```go
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
```

Commit: `[x-ray-25c4a3] feat(bench): add integration-tier navigator A/B comparison with McNemar + Wilcoxon`

---

### Task 5: Wire into main.go with subcommands

Add `--mode`, `--config-a`, `--config-b`, `--output`, `--seeds` flags to `cmd/bench/main.go` so users can run different bench tiers.

**Files:**
- Modify: `cmd/bench/main.go`

- [ ] **Step 1: Add new flags and mode routing**

Edit `cmd/bench/main.go` — add the new flags before `flag.Parse()` and add a mode-routing switch after existing flag handling. The `legacy` mode preserves the current behavior as the default.

Update the `main()` function. Replace the existing flag block and add mode routing:

```go
func main() {
	dumpFlag := flag.Bool("dump", false, "Dump Navigator input (zone tree + children) without calling LLM")
	siteFlag := flag.String("site", "", "Only run bench cases for this site (e.g. hackernews)")
	modeFlag := flag.String("mode", "legacy", "Bench mode: unit|integration|e2e|legacy")
	outputFlag := flag.String("output", "", "JSON report output path (default: results/bench_{mode}/{timestamp}.json)")
	seedsFlag := flag.Int("seeds", 1, "Number of seeds/runs for reproducibility (integration/e2e modes)")
	_ = seedsFlag // Used in integration mode.
	flag.Parse()

	config.LoadEnv()

	switch *modeFlag {
	case "unit":
		runUnitMode(*outputFlag, *siteFlag)
		return
	case "integration":
		runIntegrationMode(*outputFlag, *siteFlag, *seedsFlag)
		return
	case "e2e":
		fmt.Println("e2e mode not yet implemented")
		os.Exit(1)
	case "legacy":
		// Fall through to existing behavior below.
	default:
		log.Fatalf("Unknown mode: %s (valid: unit, integration, e2e, legacy)", *modeFlag)
	}

	// ... rest of existing main() for legacy mode, starting from loadCases...
```

- [ ] **Step 2: Implement runUnitMode**

Add to `cmd/bench/main.go`:

```go
func runUnitMode(outputPath, siteFilter string) {
	ctx := context.Background()

	sites := []string{"hackernews", "lobsters", "github", "ecommerce", "wikipedia", "reddit", "youtube"}
	if siteFilter != "" {
		sites = []string{siteFilter}
	}

	// Filter to sites that have both page.png and page_summary.txt.
	var validSites []string
	for _, s := range sites {
		dir := fmt.Sprintf("testdata/%s", s)
		if _, err := os.Stat(dir + "/page.png"); err != nil {
			continue
		}
		if _, err := os.Stat(dir + "/page_summary.txt"); err != nil {
			continue
		}
		validSites = append(validSites, s)
	}

	// Cart A: Cairn baseline.
	cartA := &cartographer.CairnCartographer{Gear: 5, Scale: 10.0}
	nameA := "cairn-baseline"

	// Cart B: Cairn with sheaf + curvature.
	cartB := &cartographer.CairnCartographer{Gear: 5, Scale: 10.0, SheafFolding: true, CurvatureDetection: true}
	nameB := "cairn-sheaf"

	// Override from env if set.
	if v := os.Getenv("CART_A"); v != "" {
		nameA = v
	}
	if v := os.Getenv("CART_B"); v != "" {
		nameB = v
	}

	fmt.Printf("=== Unit Bench: %s vs %s ===\n", nameA, nameB)
	report := RunUnitBench(ctx, cartA, cartB, nameA, nameB, validSites)

	if outputPath == "" {
		ts := time.Now().Format("20060102_150405")
		outputPath = fmt.Sprintf("results/bench_unit/%s.json", ts)
	}
	if err := writeReport(outputPath, report); err != nil {
		log.Printf("Failed to write report: %v", err)
	}
}
```

Note: add `"github.com/agentic-research/x-ray/internal/cartographer"` to the imports in `main.go` (it is already present).

- [ ] **Step 3: Implement runIntegrationMode**

Add to `cmd/bench/main.go`:

```go
func runIntegrationMode(outputPath, siteFilter string, seeds int) {
	ctx := context.Background()

	cases, err := loadCases("testdata/bench_cases.json")
	if err != nil {
		log.Fatalf("Failed to load bench cases: %v", err)
	}

	if siteFilter != "" {
		var filtered []benchCase
		for _, tc := range cases {
			if tc.Site == siteFilter {
				filtered = append(filtered, tc)
			}
		}
		if len(filtered) == 0 {
			log.Fatalf("No bench cases for site %q", siteFilter)
		}
		cases = filtered
	}

	model := os.Getenv("GEMINI_MODEL")
	if model == "" {
		model = "gemini-2.5-flash"
	}

	// Build config A: current default.
	cartA, needsGeminiA := buildCartographer(model)
	var client *genai.Client
	if needsGeminiA {
		var err error
		client, err = genai.NewClient(ctx, nil)
		if err != nil {
			log.Fatalf("Gemini client: %v", err)
		}
		cartA = cartographer.NewAgent(client, model)
	}
	navGenA, navModelA := buildNavGenerator(client, model)

	cfgA := BenchConfig{
		Name:         "default",
		Cartographer: cartA,
		CartMode:     os.Getenv("CARTOGRAPHER_MODE"),
		NavGenerator: navGenA,
		NavModel:     navModelA,
	}

	// Config B: read from BENCH_CONFIG_B_* env vars (or defaults to same as A for baseline).
	// In practice, users set env vars to change one dimension at a time.
	cfgB := cfgA // shallow copy — same config unless overridden
	cfgB.Name = "variant"

	if v := os.Getenv("BENCH_CONFIG_B_CART"); v != "" {
		switch strings.ToLower(v) {
		case "cairn":
			cfgB.Cartographer = &cartographer.CairnCartographer{Gear: 5, Scale: 10.0}
			cfgB.CartMode = "cairn"
		case "cairn-sheaf":
			cfgB.Cartographer = &cartographer.CairnCartographer{Gear: 5, Scale: 10.0, SheafFolding: true, CurvatureDetection: true}
			cfgB.CartMode = "cairn"
		case "tropical":
			cfgB.Cartographer = &cartographer.TropicalCartographer{}
			cfgB.CartMode = "tropical"
		}
	}
	if v := os.Getenv("BENCH_CONFIG_B_NAME"); v != "" {
		cfgB.Name = v
	}

	for seed := 0; seed < seeds; seed++ {
		if seeds > 1 {
			fmt.Printf("\n=== Seed %d/%d ===\n", seed+1, seeds)
		}

		report := RunIntegrationBench(ctx, cfgA, cfgB, cases)

		outPath := outputPath
		if outPath == "" {
			ts := time.Now().Format("20060102_150405")
			outPath = fmt.Sprintf("results/bench_integration/%s_seed%d.json", ts, seed)
		} else if seeds > 1 {
			outPath = fmt.Sprintf("%s_seed%d.json", strings.TrimSuffix(outputPath, ".json"), seed)
		}
		if err := writeReport(outPath, report); err != nil {
			log.Printf("Failed to write report: %v", err)
		}
	}
}
```

Note: Add `"strings"` to imports if not already present (it already is).

- [ ] **Step 4: Verify build and test all modes parse**

Run:
```bash
cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go build ./cmd/bench/
cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go vet ./cmd/bench/
```

Expect: clean build and vet.

Then verify flag parsing works:
```bash
cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go run ./cmd/bench/ --mode unit --site hackernews
```

Expect: runs the unit bench for hackernews (Cairn cartographers are local, no API key needed).

- [ ] **Step 5: Run the full stats test suite one final time**

Run: `cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go test ./cmd/bench/ -v -count=1`
Expect: all tests pass.

Commit: `[x-ray-25d510] feat(bench): wire unit/integration modes into main.go with --mode, --output, --seeds flags`

---

## Usage Examples

After implementation, the harness supports these workflows:

```bash
# Legacy mode (unchanged from before):
GOWORK=off go run ./cmd/bench/ --site hackernews

# Unit tier — compare cairn baseline vs sheaf (no model needed):
GOWORK=off go run ./cmd/bench/ --mode unit

# Unit tier — single site:
GOWORK=off go run ./cmd/bench/ --mode unit --site hackernews

# Integration tier — same config (baseline measurement):
GOWORK=off go run ./cmd/bench/ --mode integration --seeds 3

# Integration tier — A/B compare cartographers:
BENCH_CONFIG_B_CART=tropical BENCH_CONFIG_B_NAME=tropical \
  GOWORK=off go run ./cmd/bench/ --mode integration

# Integration tier — custom output:
GOWORK=off go run ./cmd/bench/ --mode integration --output results/my_experiment.json
```

## JSON Report Schema

All reports follow this pattern for downstream consumption:

```json
{
  "timestamp": "2026-04-27T15:00:00Z",
  "config_a": "...",
  "config_b": "...",
  "cases": [...],
  "latency_a_summary": {
    "n": 53, "mean": 1.23, "median": 1.10, "std_dev": 0.45,
    "p50": 1.10, "p95": 2.30, "p99": 3.10,
    "ci95_lo": 1.10, "ci95_hi": 1.36
  },
  "mcnemar_p": 0.0312,
  "latency_wilcoxon_p": 0.0045,
  "bonferroni_significant": [true, true, false]
}
```

## Future Work (Not in This Plan)

- **E2E tier**: voice-to-action latency measurement (requires Gemini Live API session)
- **Regression detection**: CI workflow that compares new reports against a baseline JSON
- **Additional sites**: desktop (needs page_summary.txt), youtube_results (already has testdata)
- **gonum/stat migration**: Replace hand-rolled Wilcoxon/McNemar with gonum once needed for more tests
- **HTML report**: Generate a comparison dashboard from JSON reports
