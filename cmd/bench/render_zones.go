//go:build ignore

// render_zones.go — Render zone bounding boxes on testdata screenshots.
// Run: go run ./cmd/bench/render_zones.go
//
// Produces output images in testdata/<site>/zones_baseline.png and zones_sheaf.png

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	"image/png"
	"log"
	"os"
	"path/filepath"

	"github.com/agentic-research/x-ray/internal/cartographer"
)

type mount struct {
	Path        string     `json:"virtual_path"`
	MacheID     string     `json:"mache_id"`
	Description string     `json:"description"`
	Bounds      [4]float64 `json:"bounds"`
}

type schema struct {
	Mounts []mount `json:"mounts"`
}

var zoneColors = []color.RGBA{
	{255, 0, 0, 180},    // red
	{0, 180, 0, 180},    // green
	{0, 80, 255, 180},   // blue
	{255, 165, 0, 180},  // orange
	{148, 0, 211, 180},  // purple
	{0, 206, 209, 180},  // teal
	{255, 20, 147, 180}, // pink
}

func main() {
	sites := []string{"hackernews", "lobsters", "github", "ecommerce", "wikipedia", "reddit"}

	for _, site := range sites {
		dir := filepath.Join("testdata", site)

		// Try rich summary first
		summaryPath := filepath.Join(dir, "page_summary_rich.txt")
		summary, err := os.ReadFile(summaryPath)
		if err != nil {
			summaryPath = filepath.Join(dir, "page_summary.txt")
			summary, err = os.ReadFile(summaryPath)
			if err != nil {
				continue
			}
		}

		screenshot, err := os.ReadFile(filepath.Join(dir, "page.png"))
		if err != nil {
			// Try jpeg
			screenshot, err = os.ReadFile(filepath.Join(dir, "page.jpg"))
			if err != nil {
				log.Printf("skip %s: no screenshot", site)
				continue
			}
		}

		img, _, err := image.Decode(bytes.NewReader(screenshot))
		if err != nil {
			log.Printf("skip %s: decode error: %v", site, err)
			continue
		}

		configs := []struct {
			name string
			cart *cartographer.CairnCartographer
			file string
		}{
			{"baseline", &cartographer.CairnCartographer{Gear: 5, Scale: 10.0}, "zones_baseline.png"},
			{"sheaf", &cartographer.CairnCartographer{Gear: 5, Scale: 10.0, SheafFolding: true, CurvatureDetection: true}, "zones_sheaf.png"},
		}

		for _, cfg := range configs {
			ctx := context.Background()
			out, err := cfg.cart.GenerateSchema(ctx, screenshot, "image/png", string(summary))
			if err != nil {
				log.Printf("%s/%s: ERROR: %v", site, cfg.name, err)
				continue
			}

			var s schema
			if err := json.Unmarshal([]byte(out), &s); err != nil {
				log.Printf("%s/%s: JSON ERROR: %v", site, cfg.name, err)
				continue
			}

			// Draw zones on image
			rendered := drawZones(img, s.Mounts)
			outPath := filepath.Join(dir, cfg.file)
			if err := savePNG(outPath, rendered); err != nil {
				log.Printf("%s/%s: save error: %v", site, cfg.name, err)
				continue
			}
			fmt.Printf("%s/%s: %d zones → %s\n", site, cfg.name, len(s.Mounts), outPath)
			for i, m := range s.Mounts {
				fmt.Printf("  [%d] %-25s %s bounds=[%.2f,%.2f,%.2f,%.2f] %q\n",
					i, m.Path, m.MacheID, m.Bounds[0], m.Bounds[1], m.Bounds[2], m.Bounds[3], truncate(m.Description, 40))
			}
		}
		fmt.Println()
	}
}

func drawZones(src image.Image, mounts []mount) image.Image {
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	dst := image.NewRGBA(bounds)
	draw.Draw(dst, bounds, src, bounds.Min, draw.Src)

	for i, m := range mounts {
		c := zoneColors[i%len(zoneColors)]

		// Convert normalized bounds to pixel coords
		x := int(m.Bounds[0] * float64(w))
		y := int(m.Bounds[1] * float64(h))
		bw := int(m.Bounds[2] * float64(w))
		bh := int(m.Bounds[3] * float64(h))

		// Clamp
		if x < 0 {
			x = 0
		}
		if y < 0 {
			y = 0
		}
		if x+bw > w {
			bw = w - x
		}
		if y+bh > h {
			bh = h - y
		}
		if bw <= 0 || bh <= 0 {
			continue
		}

		// Draw 3px border
		thickness := 3
		for t := 0; t < thickness; t++ {
			// Top edge
			for px := x; px < x+bw; px++ {
				if y+t < h {
					dst.Set(px, y+t, c)
				}
			}
			// Bottom edge
			for px := x; px < x+bw; px++ {
				if y+bh-1-t >= 0 && y+bh-1-t < h {
					dst.Set(px, y+bh-1-t, c)
				}
			}
			// Left edge
			for py := y; py < y+bh; py++ {
				if x+t < w {
					dst.Set(x+t, py, c)
				}
			}
			// Right edge
			for py := y; py < y+bh; py++ {
				if x+bw-1-t >= 0 && x+bw-1-t < w {
					dst.Set(x+bw-1-t, py, c)
				}
			}
		}
	}
	return dst
}

func savePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
