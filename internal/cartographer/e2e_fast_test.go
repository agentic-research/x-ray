// e2e_fast_test.go — Prove DOM-only + minimal prompt + Gemma 4 = sub-second navigation.
//
// Requires: llama-server on localhost:8000 with --reasoning off
// Run: GOWORK=off go test ./internal/cartographer/ -run TestE2E_Fast -v -count=1 -timeout=300s 2>&1 | tee results/bench_local/e2e_fast.log
package cartographer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/agentic-research/mache/graph"
	"github.com/agentic-research/x-ray/internal/mache"
	"github.com/agentic-research/x-ray/internal/navigator"
	"google.golang.org/genai"
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
	if err := json.Unmarshal(casesData, &allCases); err != nil {
		t.Fatalf("parse bench_cases.json: %v", err)
	}

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

		// Build semantic projection from schema mounts
		var schemaOutput mache.CartographerOutput
		json.Unmarshal([]byte(schema), &schemaOutput)
		proj := navigator.NewSemanticProjection(schemaOutput.Mounts, string(summary))

		// Build semantic tree for the prompt — shows paths with roles
		var pathListing strings.Builder
		for _, pi := range proj.AllPaths() {
			if pi.Text == "" && pi.Role == "text" {
				continue // skip structural containers without text
			}
			actionTag := ""
			if pi.Action != "none" && pi.Action != "" {
				actionTag = " [" + pi.Action + "]"
			}
			text := pi.Text
			if len(text) > 60 {
				text = text[:57] + "..."
			}
			if text != "" {
				fmt.Fprintf(&pathListing, "%s%s \"%s\"\n", pi.Path, actionTag, text)
			} else {
				fmt.Fprintf(&pathListing, "%s%s\n", pi.Path, actionTag)
			}
		}

		// Log first few paths so we can see what the model receives
		allPaths := proj.AllPaths()
		t.Logf("  %s: %d semantic paths. First 5:", site, len(allPaths))
		for i, pi := range allPaths {
			if i >= 5 {
				break
			}
			t.Logf("    %s [%s] %q", pi.Path, pi.Action, truncIntent(pi.Text, 40))
		}

		for _, tc := range cases {
			engine := mache.NewEngine()
			if err := engine.ApplySchema(schema); err != nil {
				continue
			}
			engine.LoadChildren(string(summary), nil)

			composite := graph.NewCompositeGraph()
			composite.Mount("browser", engine)

			// Compact approach: zone overview + only interactive elements, capped at 40.
			// Parse summary for interactive elements only (links, buttons, inputs).
			type elem struct {
				id, tag, text string
				y             float64
			}
			var interactiveElems []elem
			for _, line := range strings.Split(string(summary), "\n") {
				if !strings.Contains(line, "ID: ") {
					continue
				}
				// Extract fields
				idStart := strings.Index(line, "ID: ") + 4
				idEnd := strings.Index(line[idStart:], " ")
				if idEnd < 0 {
					continue
				}
				id := line[idStart : idStart+idEnd]

				tag := ""
				if ti := strings.Index(line, "Tag: "); ti >= 0 {
					rest := line[ti+5:]
					if si := strings.Index(rest, " "); si > 0 {
						tag = rest[:si]
					}
				}
				// Only interactive tags
				switch tag {
				case "a", "button", "input", "select", "textarea":
				default:
					continue
				}

				// Filter zero-bounds elements (hidden/offscreen)
				if bi := strings.Index(line, "Bounds: "); bi >= 0 {
					boundsStr := line[bi+8:]
					if strings.HasPrefix(boundsStr, "0.0000,0.0000,0.0000,0.0000") {
						continue // hidden element
					}
				}

				text := ""
				if ti := strings.Index(line, "Text: \""); ti >= 0 {
					rest := line[ti+7:]
					if ei := strings.Index(rest, "\""); ei > 0 {
						text = rest[:ei]
					}
				}
				if text == "" || len(text) < 2 {
					continue
				}
				if len(text) > 50 {
					text = text[:47] + "..."
				}

				// Extract Y position for visual-salience sorting
				yPos := 0.0
				if bi := strings.Index(line, "Bounds: "); bi >= 0 {
					boundsStr := line[bi+8:]
					parts := strings.SplitN(boundsStr, ",", 3)
					if len(parts) >= 2 {
						fmt.Sscanf(parts[1], "%f", &yPos)
					}
				}

				interactiveElems = append(interactiveElems, elem{id: id, tag: tag, text: text, y: yPos})
			}

			// Sort by visual position (top-to-bottom) before capping
			sort.Slice(interactiveElems, func(i, j int) bool {
				return interactiveElems[i].y < interactiveElems[j].y
			})

			// Cap at 40 elements
			if len(interactiveElems) > 40 {
				interactiveElems = interactiveElems[:40]
			}

			// Build listing with position context for disambiguation.
			// Group by vertical region: header (y<0.15), main (0.15-0.85), footer (>0.85)
			var headerElems, mainElems, footerElems []elem
			for _, e := range interactiveElems {
				switch {
				case e.y < 0.15:
					headerElems = append(headerElems, e)
				case e.y > 0.85:
					footerElems = append(footerElems, e)
				default:
					mainElems = append(mainElems, e)
				}
			}

			var listing strings.Builder
			if len(headerElems) > 0 {
				listing.WriteString("HEADER (top of page):\n")
				for _, e := range headerElems {
					fmt.Fprintf(&listing, "  [%s] %s: %q\n", e.id, e.tag, e.text)
				}
			}
			if len(mainElems) > 0 {
				listing.WriteString("MAIN CONTENT:\n")
				for _, e := range mainElems {
					fmt.Fprintf(&listing, "  [%s] %s: %q\n", e.id, e.tag, e.text)
				}
			}
			if len(footerElems) > 0 {
				listing.WriteString("FOOTER (bottom of page):\n")
				for _, e := range footerElems {
					fmt.Fprintf(&listing, "  [%s] %s: %q\n", e.id, e.tag, e.text)
				}
			}

			sysPrompt := "You navigate web pages. Call act(element, action) to click elements. The element must be a mache-ID from the list. Elements are grouped by page region (HEADER/MAIN/FOOTER). Pick the most prominent match."
			userMsg := tc.Intent + "\n\nInteractive elements on this page:\n" + listing.String()

			history := []*genai.Content{
				{Role: "user", Parts: []*genai.Part{{Text: userMsg}}},
			}
			llmConfig := &genai.GenerateContentConfig{
				SystemInstruction: &genai.Content{
					Parts: []*genai.Part{{Text: sysPrompt}},
				},
				Tools: []*genai.Tool{{
					FunctionDeclarations: []*genai.FunctionDeclaration{
						{
							Name:        "act",
							Description: "Click, type, or focus on an element by its path",
							Parameters: &genai.Schema{
								Type: genai.TypeObject,
								Properties: map[string]*genai.Schema{
									"element": {Type: genai.TypeString, Description: "Element mache-ID from the page listing (e.g. mache-11)"},
									"action":  {Type: genai.TypeString, Description: "click, type, or focus", Enum: []string{"click", "type", "focus"}},
								},
								Required: []string{"element", "action"},
							},
						},
						{
							Name:        "answer",
							Description: "Return a text answer",
							Parameters: &genai.Schema{
								Type: genai.TypeObject,
								Properties: map[string]*genai.Schema{
									"text": {Type: genai.TypeString, Description: "Your answer"},
								},
								Required: []string{"text"},
							},
						},
					},
				}},
			}

			start := time.Now()
			resp, navErr := navGen.GenerateContent(ctx, model, history, llmConfig)
			elapsed := time.Since(start)

			r := result{
				site:    site,
				intent:  tc.Intent,
				expect:  tc.ExpectMacheID,
				elapsed: elapsed,
			}

			if navErr != nil {
				r.got = "ERR"
			} else if resp != nil && len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
				for _, part := range resp.Candidates[0].Content.Parts {
					if part.FunctionCall != nil && part.FunctionCall.Name == "act" {
						element, _ := part.FunctionCall.Args["element"].(string)
						r.got = element
						r.pass = element == tc.ExpectMacheID
						break
					}
					if part.Text != "" {
						r.got = "text:" + truncIntent(part.Text, 20)
					}
				}
			}
			if r.got == "" {
				r.got = "no-response"
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

	if total == 0 {
		t.Fatalf("no test cases ran — check bench_cases.json and testdata")
	}
	if pct >= 70 && p50 < 5 {
		t.Logf("✅ PASS: %.0f%% accuracy (≥70%%), p50 %.1fs (<5s)", pct, p50)
	} else {
		t.Errorf("❌ FAIL: accuracy %.0f%% (want ≥70%%), p50 %.1fs (want <5s)", pct, p50)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
