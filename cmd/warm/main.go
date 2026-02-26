// Command warm pre-generates mache schemas for given URLs using Gemini's URL
// context tool. This allows cache-warming for known sites WITHOUT requiring
// the Chrome extension, a screenshot, or a DOM summary.
//
// Valid schemas are persisted to ~/.xray/schemas.db (the same SQLite graph
// used by the main x-ray server), so a subsequent server start sees them
// as cache hits.
//
// Usage:
//
//	go run cmd/warm/main.go https://news.ycombinator.com [more urls...]
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jamesgardner/x-ray/internal/api"
	"github.com/jamesgardner/x-ray/internal/mache"
	"github.com/joho/godotenv"
	"google.golang.org/genai"
)

// ---------------------------------------------------------------------------
// Prompt (same as experiments/url-schema-gen)
// ---------------------------------------------------------------------------

const systemPrompt = `You are 'The Cartographer', an expert in UI semantics and structural mapping.
Your goal is to analyze a public webpage (fetched via URL) and map its structure into a virtual
semantic filesystem.

CRITICAL INSTRUCTION: You must identify ONLY the 3 to 7 top-level semantic regions (Resource Modules) of the page.
Prefer fewer, broader zones -- merge related content sections into a single zone rather than splitting them.
DO NOT enumerate individual interactive elements (like every link in a list or every upvote button).
Your job is to define the high-level zones (e.g., /header/nav, /main/content, /sidebar/widgets, /footer).

CRITICAL: If there are pagination controls, 'Next Page' buttons, or 'Load More' links, you MUST map them into their own distinct semantic zone (e.g., /main/pagination). Do not skip them.

For zones that contain repeating content (e.g., a list of stories, products, search results):
- Include 'primary_items' as an empty array [] (since we have no tagged elements yet).
- Include 'item_selector' -- a CSS selector that matches EXACTLY ONE element per repeating item:
  the primary title/link the user would click. It MUST NOT match metadata links (subreddit names,
  author links, comment counts, domain labels). Use the most specific selector possible -- prefer
  child combinators (>) and specific classes/attributes over broad descendant selectors.
  Leave empty for non-list zones or when no clear repeating pattern exists.

For css_selector: provide a standard CSS selector that identifies the ROOT element of each zone.
This selector will be used later by the browser extension to locate and tag elements.

For mache_id: use placeholder IDs in the format "url-0", "url-1", "url-2", etc. The real DOM
mapping happens when the extension actually loads the page. What we are caching is the STRUCTURE
(virtual paths + CSS selectors + descriptions).

Output a valid JSON object with a top-level "mounts" array. Each mount must have:
- virtual_path (string): the semantic filesystem path, e.g., /header/nav
- mache_id (string): placeholder ID like "url-0"
- description (string): semantic description of the zone's purpose
- css_selector (string): CSS selector for the zone root element
- primary_items (array of strings): empty [] for now
- item_selector (string): CSS selector for repeating item titles/links, or "" if not a list zone
`

// ---------------------------------------------------------------------------
// Gemini URL context call
// ---------------------------------------------------------------------------

// result holds the output of a single URL schema generation run.
type result struct {
	URL           string
	Schema        *mache.CartographerOutput
	RawJSON       string
	InputTokens   int32
	OutputTokens  int32
	TotalTokens   int32
	Duration      time.Duration
	Valid         bool
	ValidationMsg string
	Error         error
}

func generateSchema(ctx context.Context, client *genai.Client, model, targetURL string) result {
	start := time.Now()
	r := result{URL: targetURL}

	userPrompt := fmt.Sprintf(
		"Analyze the following URL and generate a mache schema mapping its page structure into a "+
			"virtual semantic filesystem.\n\nURL: %s\n\nGenerate the semantic filesystem schema.",
		targetURL,
	)

	config := &genai.GenerateContentConfig{
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{
				{Text: systemPrompt},
			},
		},
		Tools: []*genai.Tool{
			{URLContext: &genai.URLContext{}},
		},
		ResponseMIMEType: "application/json",
		ResponseSchema:   getSchemaDefinition(),
		Temperature:      genai.Ptr(float32(0.1)),
	}

	resp, err := client.Models.GenerateContent(ctx, model, genai.Text(userPrompt), config)
	r.Duration = time.Since(start)
	if err != nil {
		r.Error = fmt.Errorf("GenerateContent failed: %w", err)
		return r
	}

	// Extract usage metadata.
	if resp.UsageMetadata != nil {
		r.InputTokens = resp.UsageMetadata.PromptTokenCount
		r.OutputTokens = resp.UsageMetadata.CandidatesTokenCount
		r.TotalTokens = resp.UsageMetadata.TotalTokenCount
	}

	if len(resp.Candidates) == 0 {
		r.Error = fmt.Errorf("no candidates returned")
		return r
	}

	// Extract text from the first candidate.
	candidate := resp.Candidates[0]
	if candidate.Content == nil || len(candidate.Content.Parts) == 0 {
		r.Error = fmt.Errorf("empty response content")
		return r
	}

	text := candidate.Content.Parts[0].Text
	if text == "" {
		r.Error = fmt.Errorf("no text in response")
		return r
	}

	r.RawJSON = text

	// Parse and validate.
	var output mache.CartographerOutput
	if err := json.Unmarshal([]byte(text), &output); err != nil {
		r.Error = fmt.Errorf("JSON parse error: %w", err)
		return r
	}
	r.Schema = &output

	// Structural validation.
	r.Valid, r.ValidationMsg = validateSchema(&output)
	return r
}

// getSchemaDefinition returns the structured output schema for Gemini.
func getSchemaDefinition() *genai.Schema {
	return &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"mounts": {
				Type: genai.TypeArray,
				Items: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"virtual_path": {
							Type:        genai.TypeString,
							Description: "Semantic filesystem path, e.g., /header/nav",
						},
						"mache_id": {
							Type:        genai.TypeString,
							Description: "Placeholder ID like url-0, url-1, etc.",
						},
						"description": {
							Type:        genai.TypeString,
							Description: "Semantic description of the zone's purpose",
						},
						"css_selector": {
							Type:        genai.TypeString,
							Description: "CSS selector identifying the root element of this zone",
						},
						"primary_items": {
							Type:        genai.TypeArray,
							Items:       &genai.Schema{Type: genai.TypeString},
							Description: "Empty array for URL-based schema (no DOM tags yet)",
						},
						"item_selector": {
							Type:        genai.TypeString,
							Description: "CSS selector matching primary clickable elements in list zones. Empty for non-list zones.",
						},
					},
					Required: []string{"virtual_path", "mache_id", "description", "css_selector"},
				},
			},
		},
		Required: []string{"mounts"},
	}
}

// validateSchema checks structural validity of the generated schema.
func validateSchema(output *mache.CartographerOutput) (bool, string) {
	var issues []string

	if len(output.Mounts) == 0 {
		return false, "FAIL: no mounts in schema"
	}

	if len(output.Mounts) < 3 {
		issues = append(issues, fmt.Sprintf("WARN: only %d mounts (expected 3-7)", len(output.Mounts)))
	}
	if len(output.Mounts) > 10 {
		issues = append(issues, fmt.Sprintf("WARN: %d mounts exceeds expected range (3-7)", len(output.Mounts)))
	}

	hasCSS := 0
	seenIDs := make(map[string]bool)
	seenPaths := make(map[string]bool)

	for i, m := range output.Mounts {
		if m.VirtualPath == "" {
			issues = append(issues, fmt.Sprintf("FAIL: mount[%d] missing virtual_path", i))
		}
		if m.MacheID == "" {
			issues = append(issues, fmt.Sprintf("FAIL: mount[%d] missing mache_id", i))
		}
		if m.Description == "" {
			issues = append(issues, fmt.Sprintf("WARN: mount[%d] missing description", i))
		}
		if m.CSSSelector != "" {
			hasCSS++
		} else {
			issues = append(issues, fmt.Sprintf("WARN: mount[%d] (%s) missing css_selector", i, m.VirtualPath))
		}
		if seenIDs[m.MacheID] {
			issues = append(issues, fmt.Sprintf("WARN: duplicate mache_id %q", m.MacheID))
		}
		seenIDs[m.MacheID] = true
		if seenPaths[m.VirtualPath] {
			issues = append(issues, fmt.Sprintf("WARN: duplicate virtual_path %q", m.VirtualPath))
		}
		seenPaths[m.VirtualPath] = true
	}

	if hasCSS == 0 {
		issues = append(issues, "FAIL: no mounts have css_selector")
	}

	// Check for hierarchy: at least some paths should have depth > 1.
	hasDepth := false
	for _, m := range output.Mounts {
		parts := strings.Split(strings.Trim(m.VirtualPath, "/"), "/")
		if len(parts) >= 2 {
			hasDepth = true
			break
		}
	}
	if !hasDepth {
		issues = append(issues, "WARN: no paths have depth > 1 (expected /zone/subzone structure)")
	}

	valid := true
	for _, issue := range issues {
		if strings.HasPrefix(issue, "FAIL:") {
			valid = false
		}
	}

	msg := "OK"
	if len(issues) > 0 {
		msg = strings.Join(issues, "; ")
	}

	return valid, msg
}

// ---------------------------------------------------------------------------
// Output helpers
// ---------------------------------------------------------------------------

func printResult(r result) {
	if r.Error != nil {
		log.Printf("ERROR [%s]: %v (took %s)", r.URL, r.Error, r.Duration)
		return
	}

	// Pretty-print the schema JSON to stdout.
	pretty, err := json.MarshalIndent(r.Schema, "", "  ")
	if err != nil {
		fmt.Println(r.RawJSON)
	} else {
		fmt.Println(string(pretty))
	}

	// Metadata goes to stderr so stdout is clean JSON.
	log.Printf("OK [%s]: %d mounts, %d tokens, %s",
		r.URL, len(r.Schema.Mounts), r.TotalTokens, r.Duration)
	if r.ValidationMsg != "OK" {
		log.Printf("  validation: %s", r.ValidationMsg)
	}
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func main() {
	log.SetFlags(log.Ltime | log.Lmsgprefix)
	log.SetPrefix("[warm] ")

	// Load environment.
	if err := godotenv.Load(".envrc"); err != nil {
		log.Println("Note: No .envrc file found, using environment")
	}

	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <url> [url...]\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "\nPre-generate mache schemas for URLs using Gemini URL context.\n")
		fmt.Fprintf(os.Stderr, "Schemas are printed to stdout as JSON.\n")
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  %s https://news.ycombinator.com\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s https://news.ycombinator.com https://github.com/trending\n", os.Args[0])
		os.Exit(1)
	}

	urls := os.Args[1:]

	ctx := context.Background()

	client, err := genai.NewClient(ctx, nil)
	if err != nil {
		log.Fatalf("Failed to initialize Gemini client: %v", err)
	}

	model := os.Getenv("GEMINI_MODEL")
	if model == "" {
		model = "gemini-2.5-flash"
	}

	// Schema cache — same default path as the main server.
	home, _ := os.UserHomeDir()
	dbPath := filepath.Join(home, ".xray", "schemas.db")
	cache := api.NewSchemaCache(dbPath)
	defer func() { _ = cache.Close() }()

	log.Printf("Model: %s", model)
	log.Printf("Warming %d URL(s)...", len(urls))

	exitCode := 0
	for i, u := range urls {
		log.Printf("Generating schema for: %s", u)
		r := generateSchema(ctx, client, model, u)
		printResult(r)

		if r.Error != nil || !r.Valid {
			exitCode = 1
		} else if key := api.CacheKey(r.URL); key != "" {
			cache.Put(key, r.RawJSON)
			log.Printf("Cached schema for %q → %s", key, dbPath)
		}

		// Small delay between requests to avoid rate limits.
		if i < len(urls)-1 {
			time.Sleep(2 * time.Second)
		}
	}

	os.Exit(exitCode)
}
