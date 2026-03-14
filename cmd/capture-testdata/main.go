// cmd/capture-testdata/main.go — Capture frozen testdata from a running agentd.
//
// Connects to agentd, grabs the current page's DOM summary and screenshot,
// and writes them to testdata/<name>/ for offline replay testing.
//
// Prerequisites: agentd running + Chrome on the target page.
//
// Usage:
//
//	go run ./cmd/capture-testdata -name youtube
//	go run ./cmd/capture-testdata -name youtube_results
package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var (
	nameFlag = flag.String("name", "", "testdata directory name (required)")
	urlFlag  = flag.String("url", "ws://localhost:8080/ws", "agentd WebSocket URL")
)

func main() {
	flag.Parse()
	if *nameFlag == "" {
		log.Fatal("Usage: capture-testdata -name <name> (e.g. youtube)")
	}

	dir := fmt.Sprintf("testdata/%s", *nameFlag)
	os.MkdirAll(dir, 0o755)

	// Connect to agentd WebSocket (same as Chrome extension does).
	u, _ := url.Parse(*urlFlag)
	log.Printf("Connecting to %s...", u.String())

	dialer := websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
	}
	conn, _, err := dialer.Dial(u.String(), nil)
	if err != nil {
		log.Fatalf("WebSocket connect failed: %v", err)
	}
	defer conn.Close()
	log.Println("Connected")

	var (
		summary    string
		summaryURL string
		screenshot []byte
		mu         sync.Mutex
		done       = make(chan struct{})
	)

	// Read messages from agentd.
	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var m map[string]any
			if err := json.Unmarshal(msg, &m); err != nil {
				continue
			}
			msgType, _ := m["type"].(string)

			switch msgType {
			case "REQUEST_SUMMARY":
				// agentd is asking US for a summary — we're pretending to be the extension.
				// We can't provide one. Skip.
			case "DOM_SNAPSHOT":
				// This carries the enriched summary + screenshot.
				mu.Lock()
				if s, ok := m["summary"].(string); ok {
					summary = s
				}
				if u, ok := m["url"].(string); ok {
					summaryURL = u
				}
				if s, ok := m["screenshot"].(string); ok {
					// base64-encoded PNG
					if decoded, err := base64.StdEncoding.DecodeString(s); err == nil {
						screenshot = decoded
					}
				}
				mu.Unlock()
			}
		}
	}()

	// We can't easily trigger a capture from here since we're not the extension.
	// Instead, let's use the HTTP API to get what we need.
	conn.Close() // Close WS, use HTTP instead.

	log.Println("Using HTTP API to capture testdata...")

	// The screenshot is stored on the session. Let's add a capture endpoint.
	// For now: trigger a read-only doer intent which forces a capture cycle,
	// then read the engine state.

	// Actually, the simplest approach: connect to Chrome DevTools directly
	// and grab the screenshot + DOM summary ourselves.

	// But we already have agentd running with all the machinery.
	// Let's just read the files from the agentd session via HTTP.

	// Approach: use the extension's CAPTURE_SNAPSHOT + CDP screenshot path
	// by sending messages through a second WS connection as a "viewer".

	// Actually — let's just be practical. The agentd logs the summary during capture.
	// And we can get the screenshot from CDP. Let me use the CDP proxy.

	// Reconnect for real capture flow.
	conn2, _, err := dialer.Dial(u.String(), nil)
	if err != nil {
		log.Fatalf("WebSocket reconnect failed: %v", err)
	}
	defer conn2.Close()

	// Listen for messages in background.
	go func() {
		defer close(done)
		for {
			_, msg, err := conn2.ReadMessage()
			if err != nil {
				return
			}
			var m map[string]any
			if err := json.Unmarshal(msg, &m); err != nil {
				continue
			}
			msgType, _ := m["type"].(string)

			mu.Lock()
			switch msgType {
			case "SUMMARY_RESPONSE":
				if s, ok := m["summary"].(string); ok && s != "" {
					summary = s
				}
				if u, ok := m["url"].(string); ok {
					summaryURL = u
				}
				log.Printf("Got summary (%d chars, url=%s)", len(summary), summaryURL)
			case "DOM_SNAPSHOT":
				if s, ok := m["summary"].(string); ok && s != "" {
					summary = s
				}
				if u, ok := m["url"].(string); ok && u != "" {
					summaryURL = u
				}
				if s, ok := m["screenshot"].(string); ok && s != "" {
					if decoded, err := base64.StdEncoding.DecodeString(s); err == nil {
						screenshot = decoded
						log.Printf("Got screenshot (%d bytes)", len(screenshot))
					}
				}
			}
			mu.Unlock()
		}
	}()

	// Wait for data to arrive (agentd sends DOM_SNAPSHOT on tab connect).
	log.Println("Waiting for DOM_SNAPSHOT (open/refresh the target page in Chrome)...")
	timeout := time.After(60 * time.Second)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()

	for {
		select {
		case <-timeout:
			log.Println("Timeout waiting for data")
			mu.Lock()
			haveSummary := summary != ""
			haveScreenshot := len(screenshot) > 0
			mu.Unlock()
			if !haveSummary && !haveScreenshot {
				log.Fatal("No data received. Make sure Chrome is open with X-Ray extension on the target page.")
			}
			// Partial data — save what we have.
		case <-tick.C:
			mu.Lock()
			haveSummary := summary != ""
			haveScreenshot := len(screenshot) > 0
			mu.Unlock()
			if haveSummary && haveScreenshot {
				goto save
			}
		case <-done:
			goto save
		}
	}

save:
	mu.Lock()
	defer mu.Unlock()

	saved := 0

	if summary != "" {
		path := dir + "/page_summary.txt"
		if err := os.WriteFile(path, []byte(summary), 0o644); err != nil {
			log.Printf("ERROR writing summary: %v", err)
		} else {
			lines := strings.Count(summary, "\n")
			log.Printf("Saved summary: %s (%d lines)", path, lines)
			saved++
		}
	} else {
		log.Println("WARNING: no summary captured")
	}

	if len(screenshot) > 0 {
		path := dir + "/page.png"
		if err := os.WriteFile(path, screenshot, 0o644); err != nil {
			log.Printf("ERROR writing screenshot: %v", err)
		} else {
			log.Printf("Saved screenshot: %s (%d bytes)", path, len(screenshot))
			saved++
		}
	} else {
		log.Println("WARNING: no screenshot captured")
	}

	if summaryURL != "" {
		path := dir + "/url.txt"
		os.WriteFile(path, []byte(summaryURL+"\n"), 0o644)
		log.Printf("Saved URL: %s → %s", path, summaryURL)
	}

	if saved == 2 {
		fmt.Printf("\n✅ Testdata captured: %s/ (summary + screenshot)\n", dir)
		fmt.Printf("   URL: %s\n", summaryURL)
		fmt.Printf("   Now run: task replay -scenario testdata/replay_youtube.json\n")
	} else {
		fmt.Printf("\n⚠️  Partial capture: %d/2 files saved to %s/\n", saved, dir)
	}
}
