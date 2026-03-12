//go:build ignore

// compare_zones.go — Compare zone segmentation with and without sheaf/curvature.
// Run: go run ./cmd/bench/compare_zones.go [site]
//
// Prints side-by-side zone output for:
//   A) baseline (spatial proximity folding)
//   B) sheaf folding (H⁰)
//   C) sheaf + curvature (H⁰ + H¹)
//
// No LLM needed. Runs on testdata screenshots + summaries.
// Prefers page_summary_rich.txt (live CDP captures with Parent/Tag info),
// falls back to page_summary.txt.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/agentic-research/x-ray/internal/cartographer"
)

type mount struct {
	Path       string      `json:"path"`
	MacheID    string      `json:"mache_id"`
	Label      string      `json:"label"`
	Boundaries interface{} `json:"boundaries,omitempty"`
}

type schema struct {
	Mounts []mount `json:"mounts"`
}

func main() {
	sites := []string{"reddit", "github", "hackernews", "lobsters", "ecommerce", "wikipedia"}
	if len(os.Args) > 1 {
		sites = []string{os.Args[1]}
	}

	for _, site := range sites {
		dir := filepath.Join("testdata", site)

		// Prefer rich summary (from live CDP pipeline with Parent/Tag info)
		summaryPath := filepath.Join(dir, "page_summary_rich.txt")
		summary, err := os.ReadFile(summaryPath)
		if err != nil {
			summaryPath = filepath.Join(dir, "page_summary.txt")
			summary, err = os.ReadFile(summaryPath)
			if err != nil {
				log.Printf("skip %s: no summary file", site)
				continue
			}
		}

		screenshot, _ := os.ReadFile(filepath.Join(dir, "page.png"))

		// Count metadata richness
		lines := strings.Split(string(summary), "\n")
		var nParent, nBounds, nStruct int
		for _, l := range lines {
			if strings.Contains(l, "Parent:") {
				nParent++
			}
			if strings.Contains(l, "Bounds:") {
				nBounds++
			}
			for _, tag := range []string{"Tag: nav", "Tag: header", "Tag: section", "Tag: main", "Tag: footer", "Tag: article"} {
				if strings.Contains(l, tag) {
					nStruct++
					break
				}
			}
		}

		fmt.Printf("\n%s  (%s)\n", strings.ToUpper(site), filepath.Base(summaryPath))
		fmt.Printf("  metadata: %d parents, %d bounds, %d structural tags\n", nParent, nBounds, nStruct)
		fmt.Println(strings.Repeat("=", 70))

		configs := []struct {
			name string
			cart *cartographer.CairnCartographer
		}{
			{"A) baseline (spatial fold)", &cartographer.CairnCartographer{Gear: 5, Scale: 10.0}},
			{"B) sheaf H⁰ fold", &cartographer.CairnCartographer{Gear: 5, Scale: 10.0, SheafFolding: true}},
			{"C) sheaf H⁰ + curvature H¹", &cartographer.CairnCartographer{Gear: 5, Scale: 10.0, SheafFolding: true, CurvatureDetection: true}},
		}

		for _, cfg := range configs {
			ctx := context.Background()
			out, err := cfg.cart.GenerateSchema(ctx, screenshot, "image/png", string(summary))
			if err != nil {
				fmt.Printf("\n  %s: ERROR: %v\n", cfg.name, err)
				continue
			}

			var s schema
			if err := json.Unmarshal([]byte(out), &s); err != nil {
				fmt.Printf("\n  %s: JSON ERROR: %v\n", cfg.name, err)
				continue
			}

			fmt.Printf("\n  %s — %d zones:\n", cfg.name, len(s.Mounts))
			for _, m := range s.Mounts {
				boundary := ""
				if m.Boundaries != nil {
					b, _ := json.Marshal(m.Boundaries)
					boundary = fmt.Sprintf("  bounds=%s", string(b))
				}
				fmt.Printf("    %-35s %s %q%s\n", m.Path, m.MacheID, truncate(m.Label, 50), boundary)
			}
		}
	}
	fmt.Println()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
