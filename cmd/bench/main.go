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

	"github.com/agentic-research/mache/graph"

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
	Difficulty    string `json:"difficulty"` // "simple", "medium", or "hard"
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
	modeFlag := flag.String("mode", "legacy", "Bench mode: unit|integration|e2e|legacy")
	outputFlag := flag.String("output", "", "JSON report output path (default: results/bench_{mode}/{timestamp}.json)")
	seedsFlag := flag.Int("seeds", 1, "Number of seeds/runs for reproducibility (integration/e2e modes)")
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
	fmt.Printf("%-13s %-26s %-8s %-9s %-13s %-8s %s\n",
		"Site", "Intent", "Diff", "Result", "MacheID", "Time", "Iters")
	fmt.Println(strings.Repeat("\u2500", 86))

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

		composite := graph.NewCompositeGraph()
		if err := composite.Mount("browser", engine); err != nil {
			r := benchResult{tc: tc, err: fmt.Errorf("mount: %w", err)}
			results = append(results, r)
			printRow(r)
			continue
		}

		nav := navigator.NewAgent(navGen, navModel, composite)

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

	fmt.Println(strings.Repeat("\u2500", 86))
	printSummary(results)
}

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
		var cErr error
		client, cErr = genai.NewClient(ctx, nil)
		if cErr != nil {
			log.Fatalf("Gemini client: %v", cErr)
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
