package api

import (
	"image"
	"image/color"
	"math"
	"testing"
)

// makeImage creates an RGBA image from a helper function.
func makeImage(w, h int, fill color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, fill)
		}
	}
	return img
}

func TestClassifyOverlay_ExactColors(t *testing.T) {
	// One 20x20 rect per semantic color on a white 200x100 background.
	img := makeImage(200, 100, color.RGBA{255, 255, 255, 255})

	for i, sc := range SemanticColors {
		x0 := i * 30
		c := color.RGBA{sc.R, sc.G, sc.B, 255}
		for y := 10; y < 30; y++ {
			for x := x0; x < x0+20; x++ {
				img.SetRGBA(x, y, c)
			}
		}
	}

	m := ClassifyOverlay(img, 0) // tolerance=0 for exact match

	// Verify each color block is correctly classified.
	for i, sc := range SemanticColors {
		x0 := i * 30
		// Check center pixel of each block.
		cx, cy := x0+10, 20
		idx := cy*m.Width + cx
		if m.Data[idx] != int8(i) {
			t.Errorf("color %s at (%d,%d): expected index %d, got %d", sc.Name, cx, cy, i, m.Data[idx])
		}
	}

	// Background pixels should be -1.
	bgIdx := 99*m.Width + 199 // bottom-right corner
	if m.Data[bgIdx] != -1 {
		t.Errorf("background pixel: expected -1, got %d", m.Data[bgIdx])
	}
}

func TestClassifyOverlay_AlphaBlended(t *testing.T) {
	// Simulate 85% opacity blue (0,0,255) blended on white (255,255,255).
	// Result: R = 0.85*0 + 0.15*255 ≈ 38, G = 38, B = 0.85*255 + 0.15*255 = 255
	blended := color.RGBA{38, 38, 255, 255}
	img := makeImage(50, 50, blended)

	// Squared distance from pure BLUE (0,0,255): (38² + 38² + 0²) = 2888
	// Find BLUE index by name (position-independent).
	blueIdx := int8(-1)
	for i, sc := range SemanticColors {
		if sc.Name == "BLUE" {
			blueIdx = int8(i)
			break
		}
	}
	if blueIdx < 0 {
		t.Fatal("BLUE not found in SemanticColors")
	}

	m := ClassifyOverlay(img, 3600)

	classified := 0
	for _, v := range m.Data {
		if v == blueIdx {
			classified++
		}
	}
	if classified != 50*50 {
		t.Errorf("expected all %d pixels classified as BLUE, got %d", 50*50, classified)
	}
}

func TestClassifyOverlay_Coverage(t *testing.T) {
	// 100x100 image: top 50 rows = blue, bottom 50 = white.
	img := makeImage(100, 100, color.RGBA{255, 255, 255, 255})
	blue := color.RGBA{0, 0, 255, 255}
	for y := 0; y < 50; y++ {
		for x := 0; x < 100; x++ {
			img.SetRGBA(x, y, blue)
		}
	}

	m := ClassifyOverlay(img, 0)
	cov := m.CoverageRatio()
	if math.Abs(cov-0.50) > 0.01 {
		t.Errorf("expected coverage ~0.50, got %.4f", cov)
	}
}

func TestClassifyOverlay_NoOverlay(t *testing.T) {
	// Plain white image → coverage ≈ 0, all pixels -1.
	img := makeImage(100, 100, color.RGBA{255, 255, 255, 255})
	m := ClassifyOverlay(img, 3600)

	if m.CoverageRatio() != 0 {
		t.Errorf("expected 0 coverage on white image, got %.4f", m.CoverageRatio())
	}
	for i, v := range m.Data {
		if v != -1 {
			t.Errorf("pixel %d: expected -1, got %d", i, v)
			break
		}
	}
}

func TestClassifyOverlay_BackgroundNotMisclassified(t *testing.T) {
	// Webpage-typical colors that must NOT be classified as overlay.
	bgColors := []struct {
		name string
		c    color.RGBA
	}{
		{"tan", color.RGBA{210, 180, 140, 255}},
		{"gray", color.RGBA{128, 128, 128, 255}},
		{"dark-blue", color.RGBA{0, 51, 102, 255}}, // #003366
		{"light-green", color.RGBA{144, 238, 144, 255}},
		{"black", color.RGBA{0, 0, 0, 255}},
		{"white", color.RGBA{255, 255, 255, 255}},
		{"reddit-orange", color.RGBA{255, 69, 0, 255}}, // reddit's brand
	}

	for _, bg := range bgColors {
		img := makeImage(10, 10, bg.c)
		m := ClassifyOverlay(img, 3600)
		if m.OverlayPixelCount > 0 {
			// Find which color it matched.
			matched := m.Data[0]
			t.Errorf("background color %s (%v) misclassified as %s (index %d)",
				bg.name, bg.c, SemanticColors[matched].Name, matched)
		}
	}
}

func TestClassifyOverlay_PaletteSeparation(t *testing.T) {
	// RGB cube vertices: every pair differs by 255 in at least one channel,
	// giving min pairwise dist² = 65025. With maxDistSq=900 (dist ~30),
	// nearest-neighbor resolves unambiguously even with JPEG artifacts.
	for i := 0; i < len(SemanticColors); i++ {
		for j := i + 1; j < len(SemanticColors); j++ {
			a, b := SemanticColors[i], SemanticColors[j]
			dr := int(a.R) - int(b.R)
			dg := int(a.G) - int(b.G)
			db := int(a.B) - int(b.B)
			dist := dr*dr + dg*dg + db*db
			// Minimum separation: must be > 900 (distance ~30) so even
			// JPEG-blurred pixels resolve unambiguously.
			if dist <= 900 {
				t.Errorf("palette pair %s↔%s too close: dist²=%d, need >900",
					a.Name, b.Name, dist)
			}
			t.Logf("  %s↔%s: dist²=%d (dist=%.0f)", a.Name, b.Name, dist, math.Sqrt(float64(dist)))
		}
	}
}

func TestOverlayMap_MaskImage(t *testing.T) {
	// 100x100 image with blue rectangle at top-left 50x50.
	img := makeImage(100, 100, color.RGBA{255, 255, 255, 255})
	blue := color.RGBA{0, 0, 255, 255}
	for y := 0; y < 50; y++ {
		for x := 0; x < 50; x++ {
			img.SetRGBA(x, y, blue)
		}
	}

	m := ClassifyOverlay(img, 0)
	masked := m.MaskImage(img)

	// Overlay pixels should be gray(128).
	for y := 0; y < 50; y++ {
		for x := 0; x < 50; x++ {
			g := masked.GrayAt(x, y)
			if g.Y != 128 {
				t.Errorf("overlay pixel (%d,%d): expected gray 128, got %d", x, y, g.Y)
				return
			}
		}
	}

	// Non-overlay pixels should be normal luminance (white → ~255).
	g := masked.GrayAt(75, 75)
	if g.Y < 250 {
		t.Errorf("white non-overlay pixel: expected ~255, got %d", g.Y)
	}
}

func TestOverlayMap_Dilate(t *testing.T) {
	// 20x20 image with a single overlay pixel at center (10,10).
	img := makeImage(20, 20, color.RGBA{255, 255, 255, 255})
	img.SetRGBA(10, 10, color.RGBA{0, 0, 255, 255})

	m := ClassifyOverlay(img, 0)
	if m.OverlayPixelCount != 1 {
		t.Fatalf("expected 1 overlay pixel, got %d", m.OverlayPixelCount)
	}

	dilated := m.Dilate(2)

	// Check that pixels within radius 2 (box) are now overlay.
	for dy := -2; dy <= 2; dy++ {
		for dx := -2; dx <= 2; dx++ {
			ny, nx := 10+dy, 10+dx
			idx := ny*20 + nx
			if dilated.Data[idx] < 0 {
				t.Errorf("dilated pixel (%d,%d): expected overlay, got -1", nx, ny)
			}
		}
	}

	// Pixels outside radius should still be background.
	idx := 0*20 + 0 // top-left corner
	if dilated.Data[idx] >= 0 {
		t.Errorf("far pixel (0,0): expected background, got %d", dilated.Data[idx])
	}

	// Expected count: 5x5 = 25 pixels in the dilated box.
	if dilated.OverlayPixelCount != 25 {
		t.Errorf("expected 25 dilated overlay pixels, got %d", dilated.OverlayPixelCount)
	}
}

func TestOverlayMap_FindUncoloredRegions(t *testing.T) {
	// 400x400 image with horizontal blue overlay band (rows 150-250).
	// Creates two uncolored regions: top (0-149) and bottom (251-399).
	img := makeImage(400, 400, color.RGBA{255, 255, 255, 255})
	blue := color.RGBA{0, 0, 255, 255}
	for y := 150; y < 250; y++ {
		for x := 0; x < 400; x++ {
			img.SetRGBA(x, y, blue)
		}
	}

	m := ClassifyOverlay(img, 0)
	regions := m.FindUncoloredRegions(100, 100, 0)

	if len(regions) != 2 {
		t.Fatalf("expected 2 uncolored regions, got %d", len(regions))
	}

	// Verify both regions are large enough.
	for i, r := range regions {
		if r.Dx() < 100 || r.Dy() < 100 {
			t.Errorf("region %d too small: %dx%d", i, r.Dx(), r.Dy())
		}
		t.Logf("region %d: (%d,%d)-(%d,%d) = %dx%d",
			i, r.Min.X, r.Min.Y, r.Max.X, r.Max.Y, r.Dx(), r.Dy())
	}
}

func TestOverlayMap_FindUncoloredRegions_SmallFiltered(t *testing.T) {
	// Overlay covers all but a 50x50 corner — too small for minW=100, minH=100.
	img := makeImage(200, 200, color.RGBA{0, 0, 255, 255}) // all blue overlay
	// Clear a 50x50 corner to white.
	for y := 150; y < 200; y++ {
		for x := 150; x < 200; x++ {
			img.SetRGBA(x, y, color.RGBA{255, 255, 255, 255})
		}
	}

	m := ClassifyOverlay(img, 0)
	regions := m.FindUncoloredRegions(100, 100, 0)

	if len(regions) != 0 {
		t.Errorf("expected 0 regions (50x50 < 100x100 threshold), got %d", len(regions))
	}
}
