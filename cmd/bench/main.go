package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/agentic-research/x-ray/internal/cartographer"
	"github.com/agentic-research/x-ray/internal/config"
	"github.com/agentic-research/x-ray/internal/mache"
	"github.com/agentic-research/x-ray/internal/navigator"
	"google.golang.org/genai"
)

// schemaGenerator matches the GenerateSchema method on all cartographer backends.
type schemaGenerator interface {
	GenerateSchema(ctx context.Context, screenshot []byte, mimeType, summary string) (string, error)
}

type benchCase struct {
	Site          string `json:"site"`
	Intent        string `json:"intent"`
	ExpectMacheID string `json:"expect_mache_id"`
	ExpectText    string `json:"expect_text"`
}

type benchResult struct {
	tc      benchCase
	pass    bool
	macheID string
	elapsed time.Duration
	iters   int
	err     error
}

func main() {
	dumpFlag := flag.Bool("dump", false, "Dump Navigator input (zone tree + children) without calling LLM")
	siteFlag := flag.String("site", "", "Only run bench cases for this site (e.g. hackernews)")
	flag.Parse()

	config.LoadEnv()

	cases, err := loadCases("testdata/bench_cases.json")
	if err != nil {
		log.Fatalf("Failed to load bench cases: %v", err)
	}

	// Filter by site if requested.
	if *siteFlag != "" {
		var filtered []benchCase
		for _, tc := range cases {
			if tc.Site == *siteFlag {
				filtered = append(filtered, tc)
			}
		}
		if len(filtered) == 0 {
			log.Fatalf("No bench cases for site %q", *siteFlag)
		}
		cases = filtered
	}

	ctx := context.Background()

	model := os.Getenv("GEMINI_MODEL")
	if model == "" {
		model = "gemini-2.5-flash"
	}

	cartMode := os.Getenv("CARTOGRAPHER_MODE")
	if cartMode == "" {
		cartMode = "gemini"
	}

	cart, needsGemini := buildCartographer(model)

	// Only create Gemini client if the cartographer or navigator needs it.
	var client *genai.Client
	navEndpoint := os.Getenv("NAVIGATOR_ENDPOINT")
	navMode := os.Getenv("NAVIGATOR_MODE")
	needsGeminiClient := needsGemini || (navEndpoint == "" && navMode != "gemini-live")
	if needsGeminiClient {
		var err error
		client, err = genai.NewClient(ctx, nil)
		if err != nil {
			log.Fatalf("Failed to initialize Gemini client: %v", err)
		}
	}

	// Gemini cartographer needs the client, which is only available now.
	if needsGemini {
		cart = cartographer.NewAgent(client, model)
	}

	schemaCache := map[string]string{} // site → schemaJSON

	// Dump mode: show what Navigator sees without calling LLM.
	if *dumpFlag {
		runDump(ctx, cart, cases, schemaCache, cartMode)
		return
	}

	navGen, navModel := buildNavGenerator(client, model)

	var results []benchResult

	fmt.Println("=== X-Ray Navigation Benchmark ===")
	fmt.Printf("Cartographer: %s\n", cartMode)
	fmt.Println()
	fmt.Printf("%-13s %-26s %-9s %-13s %-8s %s\n",
		"Site", "Intent", "Result", "MacheID", "Time", "Iters")
	fmt.Println(strings.Repeat("\u2500", 78))

	for _, tc := range cases {
		schema, err := getOrGenerateSchema(ctx, cart, tc.Site, schemaCache, cartMode == "gemini")
		if err != nil {
			r := benchResult{tc: tc, err: err}
			results = append(results, r)
			printRow(r)
			continue
		}

		engine := mache.NewEngine()
		if err := engine.ApplySchema(schema); err != nil {
			r := benchResult{tc: tc, err: fmt.Errorf("ApplySchema: %w", err)}
			results = append(results, r)
			printRow(r)
			continue
		}

		summary := loadSummary(tc.Site)
		engine.LoadChildren(summary, nil)

		nav := navigator.NewAgent(navGen, navModel, engine)

		start := time.Now()
		action, _, err := nav.HandleIntent(ctx, tc.Intent, false)
		elapsed := time.Since(start)

		r := benchResult{
			tc:      tc,
			elapsed: elapsed,
		}

		if err != nil {
			r.err = err
		} else if action != nil {
			r.macheID = action.MacheID
			r.pass = action.MacheID == tc.ExpectMacheID
		}

		// Count iterations from log output isn't feasible; use a rough estimate
		// based on latency (typically ~1s per iteration for cloud, less for local).
		r.iters = estimateIters(elapsed)

		results = append(results, r)
		printRow(r)
	}

	fmt.Println(strings.Repeat("\u2500", 78))
	printSummary(results)
}

func loadCases(path string) ([]benchCase, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cases []benchCase
	if err := json.Unmarshal(data, &cases); err != nil {
		return nil, err
	}
	return cases, nil
}

func buildNavGenerator(client *genai.Client, defaultModel string) (navigator.ContentGenerator, string) {
	// Gemini Live API mode.
	if os.Getenv("NAVIGATOR_MODE") == "gemini-live" {
		liveClient, err := genai.NewClient(context.Background(), &genai.ClientConfig{
			HTTPOptions: genai.HTTPOptions{APIVersion: "v1alpha"},
		})
		if err != nil {
			log.Fatalf("Failed to create Gemini Live client: %v", err)
		}
		navModel := defaultModel
		log.Printf("Navigator: using Gemini Live API (model %s)", navModel)
		return &navigator.GeminiLiveGenerator{Client: liveClient, Model: navModel}, navModel
	}

	// Local SLM (Ollama/Gemma).
	if ep := os.Getenv("NAVIGATOR_ENDPOINT"); ep != "" {
		navModel := os.Getenv("NAVIGATOR_MODEL")
		if navModel == "" {
			navModel = "functiongemma:270m"
		}
		format := os.Getenv("NAVIGATOR_FORMAT")
		if format == "gemma" {
			log.Printf("Navigator: using Gemma model %s at %s", navModel, ep)
			return &navigator.GemmaGenerator{Endpoint: ep, Model: navModel}, navModel
		}
		log.Printf("Navigator: using local model %s at %s (OpenAI format)", navModel, ep)
		return &navigator.OllamaGenerator{Endpoint: ep, Model: navModel}, navModel
	}

	// Gemini REST fallback.
	return &navigator.GeminiGenerator{Client: client}, defaultModel
}

// runDump prints what the Navigator would see for each site without calling any LLM.
func runDump(ctx context.Context, cart schemaGenerator, cases []benchCase, schemaCache map[string]string, cartMode string) {
	// Deduplicate sites.
	seen := map[string]bool{}
	var sites []string
	for _, tc := range cases {
		if !seen[tc.Site] {
			seen[tc.Site] = true
			sites = append(sites, tc.Site)
		}
	}

	for _, site := range sites {
		schema, err := getOrGenerateSchema(ctx, cart, site, schemaCache, cartMode == "gemini")
		if err != nil {
			fmt.Printf("=== %s === ERROR: %v\n\n", site, err)
			continue
		}

		engine := mache.NewEngine()
		if err := engine.ApplySchema(schema); err != nil {
			fmt.Printf("=== %s === ERROR: ApplySchema: %v\n\n", site, err)
			continue
		}

		summary := loadSummary(site)
		engine.LoadChildren(summary, nil)

		fmt.Printf("=== %s ===\n", site)

		// Show root listing (what Navigator sees from ls("/")).
		entries, err := engine.ListDir("/")
		if err != nil {
			fmt.Printf("  ls(\"/\"): ERROR: %v\n\n", err)
			continue
		}

		fmt.Println("ls(\"/\"):")
		for _, entry := range entries {
			desc, _ := engine.ReadFile(entry + "/description")
			if desc != "" && len(desc) > 80 {
				desc = desc[:77] + "..."
			}
			fmt.Printf("  %-30s %s\n", entry+"/", desc)
		}
		fmt.Println()

		// Show formatted children for each zone.
		dumpZoneChildren(engine, "", entries)
	}
}

// dumpZoneChildren walks zone paths and prints any "children" files found.
// parentPath is the parent directory's full path (empty string for root).
// entries are the base names returned by ListDir (e.g., "main/", "search/").
func dumpZoneChildren(engine *mache.Engine, parentPath string, entries []string) {
	for _, entry := range entries {
		// Build full path: parent + entry (strip trailing slash for path joining).
		name := strings.TrimSuffix(entry, "/")
		fullPath := name
		if parentPath != "" {
			fullPath = parentPath + "/" + name
		}

		// Check if this path has a "children" file (meaning it's a populated zone).
		children, _ := engine.ReadFile(fullPath + "/children")
		if children != "" {
			desc, _ := engine.ReadFile(fullPath + "/description")
			fmt.Printf("Zone: %s/\n", fullPath)
			if desc != "" {
				fmt.Printf("  Description: %s\n", desc)
			}
			fmt.Println("  Children:")
			for _, line := range strings.Split(children, "\n") {
				if line != "" {
					fmt.Printf("    %s\n", line)
				}
			}
			fmt.Println()
		}

		// Recurse into subdirectories.
		if strings.HasSuffix(entry, "/") {
			subEntries, err := engine.ListDir(fullPath)
			if err == nil && len(subEntries) > 0 {
				dumpZoneChildren(engine, fullPath, subEntries)
			}
		}
	}
}

func buildCartographer(defaultModel string) (schemaGenerator, bool) {
	mode := os.Getenv("CARTOGRAPHER_MODE")
	switch strings.ToLower(mode) {
	case "cairn":
		gear := 5
		if g, err := strconv.Atoi(os.Getenv("CAIRN_GEAR")); err == nil {
			gear = g
		}
		scale := 10.0
		if s, err := strconv.ParseFloat(os.Getenv("CAIRN_SCALE"), 64); err == nil {
			scale = s
		}
		cairnCart := &cartographer.CairnCartographer{Gear: gear, Scale: scale}
		if v := os.Getenv("CAIRN_SHEAF"); v != "" && v != "0" && v != "false" {
			cairnCart.SheafFolding = true
		}
		if v := os.Getenv("CAIRN_CURVATURE"); v != "" && v != "0" && v != "false" {
			cairnCart.CurvatureDetection = true
		}
		log.Printf("Cartographer: cairn (gear=%d, scale=%.1f, sheaf=%v, curvature=%v)", gear, scale, cairnCart.SheafFolding, cairnCart.CurvatureDetection)
		return cairnCart, false
	case "tropical":
		log.Printf("Cartographer: tropical")
		return &cartographer.TropicalCartographer{}, false
	default:
		log.Printf("Cartographer: gemini (%s)", defaultModel)
		return nil, true // placeholder — filled after client init
	}
}

func getOrGenerateSchema(ctx context.Context, cart schemaGenerator, site string, cache map[string]string, validateIDs bool) (string, error) {
	if s, ok := cache[site]; ok {
		return s, nil
	}

	summary := loadSummary(site)
	screenshot, err := os.ReadFile(fmt.Sprintf("testdata/%s/page.png", site))
	if err != nil {
		return "", fmt.Errorf("read screenshot: %w", err)
	}

	log.Printf("Generating schema for %s...", site)
	schema, err := cart.GenerateSchema(ctx, screenshot, "image/png", summary)
	if err != nil {
		return "", fmt.Errorf("GenerateSchema: %w", err)
	}

	// Validate no hallucinated IDs (only relevant for VLM-based cartographers).
	if validateIDs {
		if bad := mache.ValidateSchema(schema, summary); len(bad) > 0 {
			return "", fmt.Errorf("hallucinated IDs in schema: %v", bad)
		}
	}

	cache[site] = schema
	return schema, nil
}

func loadSummary(site string) string {
	data, err := os.ReadFile(fmt.Sprintf("testdata/%s/page_summary.txt", site))
	if err != nil {
		log.Fatalf("Failed to read summary for %s: %v", site, err)
	}
	return string(data)
}

func estimateIters(d time.Duration) int {
	// The navigator pre-fills ls("/") as iteration 0, then does tool calls.
	// Minimum 2 iterations (cat children + act). Rough estimate from latency.
	n := int(d.Seconds()/1.0) + 1
	if n < 2 {
		n = 2
	}
	if n > 8 {
		n = 8
	}
	return n
}

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

	if r.err != nil {
		result = "ERR"
		macheID = "-"
		timeStr = "-"
		itersStr = "-"
		log.Printf("  Error: %v", r.err)
	}

	fmt.Printf("%-13s %-26s %-9s %-13s %-8s %s\n",
		r.tc.Site, r.tc.Intent, result, macheID, timeStr, itersStr)
}

func printSummary(results []benchResult) {
	passed := 0
	var totalLatency time.Duration
	totalIters := 0
	valid := 0

	for _, r := range results {
		if r.err != nil {
			continue
		}
		valid++
		if r.pass {
			passed++
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
}
