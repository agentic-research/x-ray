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
	p := d * math.Exp(-z*z/2.0) * (t * (0.319381530 + t*(-0.356563782+t*(1.781477937+t*(-1.821255978+t*1.330274429)))))
	return p
}

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
