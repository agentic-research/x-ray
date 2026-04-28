// cmd/album/main.go — Generate zone segmentation JSON for all sites × cartographer modes.
// Outputs one JSON line per (site, mode) with the schema. A Python script renders overlays.
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

type output struct {
	Site   string `json:"site"`
	Mode   string `json:"mode"`
	Schema string `json:"schema"`
}

func main() {
	modes := []string{"cairn", "tropical", "progressive"}
	dirs, _ := filepath.Glob("testdata/*/page_summary.txt")

	ctx := context.Background()

	for _, mode := range modes {
		for _, d := range dirs {
			site := filepath.Base(filepath.Dir(d))
			summary, err := os.ReadFile(d)
			if err != nil {
				continue
			}
			screenshotPath := filepath.Join("testdata", site, "page.png")
			screenshot, err := os.ReadFile(screenshotPath)
			if err != nil {
				continue
			}

			var gen interface {
				GenerateSchema(ctx context.Context, screenshot []byte, mimeType, summary string) (string, error)
			}
			switch strings.ToLower(mode) {
			case "tropical":
				gen = &cartographer.TropicalCartographer{}
			case "progressive":
				gen = &cartographer.ProgressiveCartographer{Scale: 10.0, GridSize: 12}
			default:
				gen = &cartographer.CairnCartographer{Gear: 5, Scale: 10.0, GridSize: 12, SheafFolding: true}
			}

			schema, err := gen.GenerateSchema(ctx, screenshot, "image/png", string(summary))
			if err != nil {
				log.Printf("WARN: %s/%s: %v", mode, site, err)
				continue
			}

			out := output{Site: site, Mode: mode, Schema: schema}
			b, _ := json.Marshal(out)
			fmt.Println(string(b))
		}
	}
}
