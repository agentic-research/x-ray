// cmd/analyze-replay/main.go — Extract site primers from replay results.
//
// Reads all replay JSON results, aggregates tool chains per (site, intent),
// identifies winning patterns, and outputs site primers for the Navigator.
//
// Usage:
//
//	go run ./cmd/analyze-replay                    # analyze all results
//	go run ./cmd/analyze-replay -dir results/replay/20260314_145941  # specific run
//	go run ./cmd/analyze-replay -emit              # output site_primers.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type stepResult struct {
	Step struct {
		Site                  string `json:"site"`
		Intent                string `json:"intent"`
		ExpectAction          string `json:"expect_action"`
		ExpectPayloadContains string `json:"expect_payload_contains"`
	} `json:"step"`
	Pass     bool `json:"pass"`
	Action   *struct {
		MacheID string `json:"mache_id"`
		Action  string `json:"action"`
		Path    string `json:"path"`
		Payload string `json:"payload"`
	} `json:"action"`
	TextResp  string     `json:"text_response"`
	ToolCalls []toolCall `json:"tool_calls"`
	Duration  int64      `json:"duration"`
	Reason    string     `json:"reason"`
	Err       string     `json:"error"`
}

type toolCall struct {
	Tool   string `json:"tool"`
	Args   string `json:"args"`
	Result string `json:"result"`
}

// pattern is a normalized tool chain signature.
type pattern struct {
	Site      string
	Intent    string
	ToolChain string // e.g. "grep→act(type)→act(enter)"
	Action    string // final action type
	Target    string // mache_id or path
	Payload   string
	Passes    int
	Fails     int
	AvgDurMs  float64
}

var (
	dirFlag  = flag.String("dir", "results/replay", "directory containing replay results")
	emitFlag = flag.Bool("emit", false, "output site_primers.json")
)

func main() {
	flag.Parse()

	// Find all run_*.json files.
	var files []string
	filepath.Walk(*dirFlag, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if strings.HasPrefix(info.Name(), "run_") && strings.HasSuffix(info.Name(), ".json") {
			files = append(files, path)
		}
		return nil
	})

	if len(files) == 0 {
		fmt.Println("No replay results found in", *dirFlag)
		return
	}

	fmt.Printf("Analyzing %d result files from %s\n\n", len(files), *dirFlag)

	// Load all steps.
	var allSteps []stepResult
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var steps []stepResult
		if err := json.Unmarshal(data, &steps); err != nil {
			continue
		}
		allSteps = append(allSteps, steps...)
	}

	// Group by (site, intent) and extract patterns.
	type key struct {
		Site   string
		Intent string
	}
	groups := map[key][]stepResult{}
	for _, s := range allSteps {
		k := key{s.Step.Site, s.Step.Intent}
		groups[k] = append(groups[k], s)
	}

	// Sort keys for stable output.
	var keys []key
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Site != keys[j].Site {
			return keys[i].Site < keys[j].Site
		}
		return keys[i].Intent < keys[j].Intent
	})

	// Analyze each group.
	patterns := map[key][]pattern{}
	for _, k := range keys {
		steps := groups[k]
		// Group by tool chain signature.
		chainGroups := map[string][]stepResult{}
		for _, s := range steps {
			chain := extractChain(s)
			chainGroups[chain] = append(chainGroups[chain], s)
		}

		for chain, chainSteps := range chainGroups {
			p := pattern{
				Site:      k.Site,
				Intent:    k.Intent,
				ToolChain: chain,
			}
			var totalDur int64
			for _, s := range chainSteps {
				if s.Pass {
					p.Passes++
				} else {
					p.Fails++
				}
				totalDur += s.Duration
				if s.Action != nil {
					p.Action = s.Action.Action
					p.Target = s.Action.Path
					if p.Target == "" {
						p.Target = s.Action.MacheID
					}
					p.Payload = s.Action.Payload
				}
			}
			p.AvgDurMs = float64(totalDur) / float64(len(chainSteps)) / 1e6
			patterns[k] = append(patterns[k], p)
		}
	}

	// Print analysis.
	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Println("  SITE PRIMER ANALYSIS")
	fmt.Println("═══════════════════════════════════════════════════════════")

	primers := map[string]map[string]string{} // site → intent_key → primer text

	for _, k := range keys {
		pats := patterns[k]
		sort.Slice(pats, func(i, j int) bool {
			// Sort by pass rate desc, then speed.
			ri := float64(pats[i].Passes) / float64(pats[i].Passes+pats[i].Fails)
			rj := float64(pats[j].Passes) / float64(pats[j].Passes+pats[j].Fails)
			if ri != rj {
				return ri > rj
			}
			return pats[i].AvgDurMs < pats[j].AvgDurMs
		})

		total := 0
		for _, p := range pats {
			total += p.Passes + p.Fails
		}

		fmt.Printf("\n  Site: %s\n", k.Site)
		fmt.Printf("  Intent: %s\n", k.Intent)
		fmt.Printf("  Samples: %d\n", total)
		fmt.Println("  ┌──────────────────────────────────────────┬──────┬──────────┐")
		fmt.Println("  │ Tool Chain                               │ Pass │ Avg Time │")
		fmt.Println("  ├──────────────────────────────────────────┼──────┼──────────┤")

		for _, p := range pats {
			rate := fmt.Sprintf("%d/%d", p.Passes, p.Passes+p.Fails)
			chain := p.ToolChain
			if len(chain) > 40 {
				chain = chain[:37] + "..."
			}
			fmt.Printf("  │ %-40s │ %-4s │ %6.0fms │\n", chain, rate, p.AvgDurMs)
		}
		fmt.Println("  └──────────────────────────────────────────┴──────┴──────────┘")

		// Extract best pattern as primer.
		if len(pats) > 0 {
			best := pats[0]
			if best.Passes > 0 {
				intentKey := normalizeIntent(k.Intent)
				if primers[k.Site] == nil {
					primers[k.Site] = map[string]string{}
				}

				var primer string
				switch best.Action {
				case "type":
					primer = fmt.Sprintf("To %s: %s → act(%s, type, {query})",
						intentKey, best.ToolChain, best.Target)
				case "enter":
					primer = fmt.Sprintf("To %s: %s → act(%s, enter)",
						intentKey, best.ToolChain, best.Target)
				case "click":
					primer = fmt.Sprintf("To %s: %s → act(%s, click)",
						intentKey, best.ToolChain, best.Target)
				default:
					primer = fmt.Sprintf("To %s: %s", intentKey, best.ToolChain)
				}

				primers[k.Site][intentKey] = primer
				fmt.Printf("  ★ Best: %s (%.0f%% pass, %.0fms)\n",
					primer,
					float64(best.Passes)/float64(best.Passes+best.Fails)*100,
					best.AvgDurMs)
			}
		}
	}

	// Emit site_primers.json if requested.
	if *emitFlag && len(primers) > 0 {
		fmt.Println("\n═══════════════════════════════════════════════════════════")
		fmt.Println("  GENERATED SITE PRIMERS")
		fmt.Println("═══════════════════════════════════════════════════════════")

		out, _ := json.MarshalIndent(primers, "", "  ")
		fmt.Println(string(out))

		path := "testdata/site_primers.json"
		os.WriteFile(path, out, 0o644)
		fmt.Printf("\nWritten to: %s\n", path)
	}
}

func extractChain(s stepResult) string {
	var parts []string
	for _, tc := range s.ToolCalls {
		var args map[string]any
		json.Unmarshal([]byte(tc.Args), &args)

		switch tc.Tool {
		case "act":
			action, _ := args["action"].(string)
			parts = append(parts, fmt.Sprintf("act(%s)", action))
		case "grep":
			pattern, _ := args["pattern"].(string)
			if len(pattern) > 15 {
				pattern = pattern[:12] + "..."
			}
			parts = append(parts, fmt.Sprintf("grep(%s)", pattern))
		case "ls":
			path, _ := args["path"].(string)
			parts = append(parts, fmt.Sprintf("ls(%s)", path))
		case "cat":
			path, _ := args["path"].(string)
			// Shorten path for readability.
			if idx := strings.LastIndex(path, "/"); idx > 10 {
				path = "..." + path[idx:]
			}
			parts = append(parts, fmt.Sprintf("cat(%s)", path))
		default:
			parts = append(parts, tc.Tool)
		}
	}
	return strings.Join(parts, " → ")
}

func normalizeIntent(intent string) string {
	intent = strings.ToLower(intent)
	if strings.Contains(intent, "search") {
		return "search"
	}
	if strings.Contains(intent, "enter") || strings.Contains(intent, "submit") {
		return "submit_search"
	}
	if strings.Contains(intent, "click") && strings.Contains(intent, "first") {
		return "click_first_result"
	}
	if strings.Contains(intent, "click") {
		return "click"
	}
	// Fallback: first few words.
	words := strings.Fields(intent)
	if len(words) > 4 {
		words = words[:4]
	}
	return strings.Join(words, "_")
}
