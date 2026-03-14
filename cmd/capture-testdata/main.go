// cmd/capture-testdata/main.go — Capture frozen testdata from a running agentd.
//
// Hits the /capture-testdata HTTP endpoint to grab the current tab's
// DOM summary + screenshot and save to testdata/<name>/.
//
// Prerequisites: agentd running + Chrome on the target page.
//
// Usage:
//
//	go run ./cmd/capture-testdata -name youtube
//	go run ./cmd/capture-testdata -name youtube_results
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
)

var (
	nameFlag = flag.String("name", "", "testdata directory name (required)")
	urlFlag  = flag.String("url", "http://localhost:8080", "agentd URL")
)

func main() {
	flag.Parse()
	if *nameFlag == "" {
		log.Fatal("Usage: capture-testdata -name <name> (e.g. youtube)")
	}

	endpoint := fmt.Sprintf("%s/capture-testdata?name=%s", *urlFlag, *nameFlag)
	log.Printf("Capturing testdata from %s...", endpoint)

	resp, err := http.Get(endpoint)
	if err != nil {
		log.Fatalf("HTTP request failed: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		log.Fatalf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result map[string]any
	json.Unmarshal(body, &result)

	dir, _ := result["dir"].(string)
	saved, _ := result["saved"].(map[string]any)

	fmt.Printf("\n✅ Testdata captured: %s/\n", dir)
	for k, v := range saved {
		fmt.Printf("   %s: %v\n", k, v)
	}
	fmt.Printf("\nNow run: task replay\n")
}
