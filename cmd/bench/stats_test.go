package main

import (
	"fmt"
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
	// Mean diff = 10, pooled SD ~ 1 → |d| ~ 10.
	a := []float64{0, 1, 0, 1, 0}
	b := []float64{10, 11, 10, 11, 10}
	d := CohensD(a, b)
	if math.Abs(d) < 5.0 {
		t.Errorf("d = %f, want large effect size (|d| > 5)", d)
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
