// Command diag validates the structural consistency of x-ray's schema cache.
//
// It opens the mache graph SQLite at ~/.xray/schemas.db (or XRAY_DB),
// walks every cached URL → zone → section, and reports:
//   - orphaned sections (structural FP doesn't match parent zone)
//   - zones with missing mount data
//   - empty fingerprints
//   - section counts exceeding the per-zone cap
//
// Exit code 0 = clean, 1 = issues found.
//
// Usage:
//
//	go run ./cmd/diag
//	go run ./cmd/diag --db path/to/schemas.db
//	go run ./cmd/diag --verbose
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/agentic-research/mache/graph"
)

type issue struct {
	Level   string // "warn" or "error"
	URL     string
	Zone    string
	Section string
	Message string
}

func main() {
	dbFlag := flag.String("db", "", "Path to schemas.db (default: ~/.xray/schemas.db or XRAY_DB)")
	verbose := flag.Bool("verbose", false, "Print all zones/sections, not just issues")
	flag.Parse()

	dbPath := *dbFlag
	if dbPath == "" {
		dbPath = os.Getenv("XRAY_DB")
	}
	if dbPath == "" {
		home, _ := os.UserHomeDir()
		dbPath = filepath.Join(home, ".xray", "schemas.db")
	}

	if _, err := os.Stat(dbPath); err != nil {
		fmt.Fprintf(os.Stderr, "diag: %s not found\n", dbPath)
		os.Exit(1)
	}

	store, err := graph.ImportSQLite(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "diag: import %s: %v\n", dbPath, err)
		os.Exit(1)
	}

	roots := store.RootIDs()
	if len(roots) == 0 {
		fmt.Println("diag: schema cache is empty")
		os.Exit(0)
	}

	var issues []issue
	totalURLs := 0
	totalZones := 0
	totalSections := 0
	orphanedSections := 0

	for _, urlKey := range roots {
		totalURLs++

		// Check version marker.
		version := "1"
		if vn, err := store.GetNode(urlKey + "/meta/version"); err == nil {
			version = string(vn.Data)
		}

		if *verbose {
			fmt.Printf("URL: %s (v%s)\n", urlKey, version)
		}

		if version != "2" {
			// v1 monolithic — just check schema_json exists.
			if _, err := store.GetNode(urlKey + "/schema_json"); err != nil {
				issues = append(issues, issue{
					Level:   "error",
					URL:     urlKey,
					Message: "v1 entry missing schema_json",
				})
			}
			continue
		}

		// v2 per-zone format.
		zonesDir := urlKey + "/zones"
		zonesNode, err := store.GetNode(zonesDir)
		if err != nil {
			issues = append(issues, issue{
				Level:   "error",
				URL:     urlKey,
				Message: "v2 entry missing zones/ directory",
			})
			continue
		}

		for _, zoneID := range zonesNode.Children {
			totalZones++
			escaped := strings.TrimPrefix(zoneID, zonesDir+"/")
			zonePath := "/" + strings.ReplaceAll(escaped, "~", "/")

			// Check mount_json exists and is valid JSON.
			mountNode, err := store.GetNode(zoneID + "/mount_json")
			if err != nil {
				issues = append(issues, issue{
					Level:   "error",
					URL:     urlKey,
					Zone:    zonePath,
					Message: "zone missing mount_json",
				})
				continue
			}
			var mountData map[string]interface{}
			if err := json.Unmarshal(mountNode.Data, &mountData); err != nil {
				issues = append(issues, issue{
					Level:   "error",
					URL:     urlKey,
					Zone:    zonePath,
					Message: fmt.Sprintf("mount_json invalid JSON: %v", err),
				})
			}

			// Check fingerprints.
			zoneFP := ""
			if fpNode, err := store.GetNode(zoneID + "/fingerprint"); err == nil {
				zoneFP = string(fpNode.Data)
			}
			zoneSFP := ""
			if sfpNode, err := store.GetNode(zoneID + "/structural_fp"); err == nil {
				zoneSFP = string(sfpNode.Data)
			}

			if zoneFP == "" && zoneSFP == "" {
				issues = append(issues, issue{
					Level:   "warn",
					URL:     urlKey,
					Zone:    zonePath,
					Message: "zone has no fingerprints (content or structural)",
				})
			}

			if *verbose {
				fmt.Printf("  zone: %s  fp=%s  sfp=%s\n", zonePath, truncate(zoneFP, 12), truncate(zoneSFP, 12))
			}

			// Check sections.
			sectionsDir := zoneID + "/sections"
			sdNode, err := store.GetNode(sectionsDir)
			if err != nil {
				continue // no sections is fine
			}

			if len(sdNode.Children) > 5 {
				issues = append(issues, issue{
					Level:   "warn",
					URL:     urlKey,
					Zone:    zonePath,
					Message: fmt.Sprintf("section count %d exceeds cap of 5", len(sdNode.Children)),
				})
			}

			for _, secID := range sdNode.Children {
				totalSections++
				parts := strings.Split(secID, "/")
				goalHash := parts[len(parts)-1]

				secFP := ""
				if n, err := store.GetNode(secID + "/fingerprint"); err == nil {
					secFP = string(n.Data)
				}
				secSFP := ""
				if n, err := store.GetNode(secID + "/structural_fp"); err == nil {
					secSFP = string(n.Data)
				}

				// Check structural fingerprint match.
				matched := false
				if zoneSFP != "" && secSFP != "" {
					matched = secSFP == zoneSFP
				} else if zoneFP != "" {
					matched = secFP == zoneFP
				} else {
					matched = true // can't validate without zone FP
				}

				if !matched {
					orphanedSections++
					issues = append(issues, issue{
						Level:   "error",
						URL:     urlKey,
						Zone:    zonePath,
						Section: goalHash,
						Message: "section fingerprint does not match zone (orphaned)",
					})
				}

				// Check recorded_at is a valid timestamp.
				if raNode, err := store.GetNode(secID + "/recorded_at"); err == nil {
					if _, err := strconv.ParseInt(string(raNode.Data), 10, 64); err != nil {
						issues = append(issues, issue{
							Level:   "warn",
							URL:     urlKey,
							Zone:    zonePath,
							Section: goalHash,
							Message: fmt.Sprintf("invalid recorded_at: %q", string(raNode.Data)),
						})
					}
				}

				if *verbose {
					action := ""
					if n, err := store.GetNode(secID + "/action"); err == nil {
						action = string(n.Data)
					}
					text := ""
					if n, err := store.GetNode(secID + "/element_text"); err == nil {
						text = truncate(string(n.Data), 40)
					}
					matchStr := "OK"
					if !matched {
						matchStr = "ORPHAN"
					}
					fmt.Printf("    section: %s  action=%s  text=%q  [%s]\n", goalHash[:8], action, text, matchStr)
				}
			}
		}
	}

	// Summary.
	fmt.Printf("\ndiag: %d URLs, %d zones, %d sections\n", totalURLs, totalZones, totalSections)

	if len(issues) == 0 {
		fmt.Println("diag: all clean")
		os.Exit(0)
	}

	errors := 0
	warns := 0
	for _, iss := range issues {
		prefix := "WARN"
		if iss.Level == "error" {
			prefix = "ERROR"
			errors++
		} else {
			warns++
		}
		loc := iss.URL
		if iss.Zone != "" {
			loc += " " + iss.Zone
		}
		if iss.Section != "" {
			loc += " [" + iss.Section[:min(8, len(iss.Section))] + "]"
		}
		fmt.Printf("  %s: %s — %s\n", prefix, loc, iss.Message)
	}

	if orphanedSections > 0 {
		fmt.Printf("\ndiag: %d orphaned sections (FP mismatch)\n", orphanedSections)
	}
	fmt.Printf("diag: %d errors, %d warnings\n", errors, warns)

	if errors > 0 {
		os.Exit(1)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
