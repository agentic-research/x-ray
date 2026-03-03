package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/agentic-research/x-ray/internal/cartographer"
	"github.com/agentic-research/x-ray/internal/config"
	"google.golang.org/genai"
)

func main() {
	log.Println("--- X-Ray Cartographer 48-Hour Gate Test ---")

	config.LoadEnv()

	htmlPath := "testdata/dummy.html"
	summaryPath := "testdata/dummy_summary.txt"
	pngPath := "testdata/dummy.png"

	if len(os.Args) >= 3 {
		htmlPath = os.Args[1]
		summaryPath = htmlPath[:len(htmlPath)-5] + "_summary.txt" // naive replace .html with _summary.txt
		pngPath = os.Args[2]
	}

	// 1. Initialize Gemini Client
	ctx := context.Background()
	client, err := genai.NewClient(ctx, nil) // Reads GEMINI_API_KEY from environment
	if err != nil {
		log.Fatalf("Failed to initialize Gemini client: %v", err)
	}

	// 2. Read test assets (sanitized HTML summary and screenshot)
	log.Printf("Reading HTML Summary: %s", summaryPath)
	summaryBytes, err := os.ReadFile(summaryPath)
	if err != nil {
		log.Fatalf("Failed to read summary: %v", err)
	}

	log.Printf("Reading Screenshot: %s", pngPath)
	screenshotBytes, err := os.ReadFile(pngPath)
	if err != nil {
		log.Fatalf("Failed to read screenshot: %v", err)
	}

	// 3. Initialize Agent
	model := os.Getenv("GEMINI_MODEL")
	if model == "" {
		model = "gemini-2.5-flash"
	}
	agent := cartographer.NewAgent(client, model)

	// 4. Run the Cartographer
	startTime := time.Now()
	schemaJSON, err := agent.GenerateSchema(ctx, screenshotBytes, "image/png", string(summaryBytes))
	if err != nil {
		log.Fatalf("Cartographer failed to generate schema: %v", err)
	}
	latency := time.Since(startTime)

	// 5. Output the result
	fmt.Println("\n--- Generated Mache Topology Schema ---")
	fmt.Println(schemaJSON)
	fmt.Printf("\n--- Latency: %s ---\n", latency)

	// 6. Validate IDs (Zero Hallucination Criterion)
	var schema struct {
		Mounts []struct {
			MacheID string `json:"mache_id"`
		} `json:"mounts"`
	}

	if err := json.Unmarshal([]byte(schemaJSON), &schema); err != nil {
		log.Fatalf("Failed to parse JSON for validation: %v", err)
	}

	summaryStr := string(summaryBytes)
	for _, m := range schema.Mounts {
		if !strings.Contains(summaryStr, "ID: "+m.MacheID) {
			log.Fatalf("❌ HALLUCINATION DETECTED: mache_id '%s' does not exist in the source summary.", m.MacheID)
		}
	}
	fmt.Println("✅ Validation Passed: 0 Hallucinated IDs.")
	fmt.Println("---------------------------------------")
}
