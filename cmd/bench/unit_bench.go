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
	Site     string  `json:"site"`
	LatencyA float64 `json:"latency_a_sec"`
	LatencyB float64 `json:"latency_b_sec"`
	ZonesA   int     `json:"zones_a"`
	ZonesB   int     `json:"zones_b"`
	Jaccard  float64 `json:"jaccard_similarity"`
	SchemaA  string  `json:"schema_a,omitempty"`
	SchemaB  string  `json:"schema_b,omitempty"`
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
