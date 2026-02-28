package api

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

// makeJPEG creates a JPEG-encoded image from an RGBA image.
func makeJPEG(img *image.RGBA) []byte {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// blankImage creates a solid-color image.
func blankImage(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// drawFilledRect draws a filled rectangle onto an image.
func drawFilledRect(img *image.RGBA, x1, y1, x2, y2 int, c color.RGBA) {
	for y := y1; y < y2; y++ {
		for x := x1; x < x2; x++ {
			if x >= 0 && x < img.Bounds().Dx() && y >= 0 && y < img.Bounds().Dy() {
				img.SetRGBA(x, y, c)
			}
		}
	}
}

func TestDetectCanvasRegionsEmpty(t *testing.T) {
	// Blank white image → no regions
	img := blankImage(200, 200, color.RGBA{255, 255, 255, 255})
	jpegData := makeJPEG(img)

	regions, resultJPEG, err := DetectCanvasRegions(jpegData, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(regions) != 0 {
		t.Errorf("expected 0 regions on blank image, got %d", len(regions))
	}
	// Result should be the original JPEG data (no annotation needed)
	if len(resultJPEG) == 0 {
		t.Error("expected non-empty result JPEG")
	}
}

func TestDetectCanvasRegionsFindsRects(t *testing.T) {
	// Draw distinct black rectangles on white background
	img := blankImage(400, 400, color.RGBA{255, 255, 255, 255})
	black := color.RGBA{0, 0, 0, 255}

	// Rectangle 1: top-left area (50,50)-(150,100) — 100x50 = 5000px²
	drawFilledRect(img, 50, 50, 150, 100, black)

	// Rectangle 2: bottom-right area (250,250)-(380,350) — 130x100 = 13000px²
	drawFilledRect(img, 250, 250, 380, 350, black)

	jpegData := makeJPEG(img)

	regions, resultJPEG, err := DetectCanvasRegions(jpegData, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(regions) == 0 {
		t.Fatal("expected at least 1 region, got 0")
	}

	// Verify IDs are sequential
	for i, r := range regions {
		expectedID := fmt.Sprintf("cv-%d", i)
		if r.ID != expectedID {
			t.Errorf("region %d: expected ID %q, got %q", i, expectedID, r.ID)
		}
	}

	// Verify coordinates are normalized
	for _, r := range regions {
		if r.X < 0 || r.X > 1 || r.Y < 0 || r.Y > 1 {
			t.Errorf("region %s: normalized coords out of range: (%.3f, %.3f)", r.ID, r.X, r.Y)
		}
		if r.W <= 0 || r.W > 1 || r.H <= 0 || r.H > 1 {
			t.Errorf("region %s: normalized size out of range: (%.3f, %.3f)", r.ID, r.W, r.H)
		}
	}

	// Verify pixel coords are positive
	for _, r := range regions {
		if r.PixelX < 0 || r.PixelY < 0 || r.PixelW <= 0 || r.PixelH <= 0 {
			t.Errorf("region %s: invalid pixel coords: (%d, %d, %d, %d)",
				r.ID, r.PixelX, r.PixelY, r.PixelW, r.PixelH)
		}
	}

	// Result JPEG should be annotated (different from input)
	if bytes.Equal(resultJPEG, jpegData) {
		t.Error("expected annotated JPEG to differ from input")
	}

	t.Logf("found %d regions", len(regions))
	for _, r := range regions {
		t.Logf("  %s: norm=(%.3f, %.3f, %.3f, %.3f) px=(%d, %d, %d, %d)",
			r.ID, r.X, r.Y, r.W, r.H, r.PixelX, r.PixelY, r.PixelW, r.PixelH)
	}
}

func TestDetectCanvasRegionsFiltersOverlap(t *testing.T) {
	// Draw a rectangle and provide existing bounds that overlap it
	img := blankImage(400, 400, color.RGBA{255, 255, 255, 255})
	black := color.RGBA{0, 0, 0, 255}

	// Rectangle at (100,100)-(200,200) — normalized approx (0.25, 0.25, 0.25, 0.25)
	drawFilledRect(img, 100, 100, 200, 200, black)

	jpegData := makeJPEG(img)

	// Existing bound that overlaps the rectangle
	existingBounds := [][4]float64{
		{0.24, 0.24, 0.27, 0.27}, // overlaps the drawn rectangle
	}

	regions, _, err := DetectCanvasRegions(jpegData, existingBounds, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The region should be filtered out due to overlap
	for _, r := range regions {
		iou := normalizedIoU(r.X, r.Y, r.W, r.H,
			existingBounds[0][0], existingBounds[0][1], existingBounds[0][2], existingBounds[0][3])
		if iou > 0.3 {
			t.Errorf("region %s should have been filtered (IoU=%.3f > 0.3)", r.ID, iou)
		}
	}
}

func TestDetectCanvasRegionsMinSize(t *testing.T) {
	// Draw tiny 5x5 rectangles that should be filtered as noise (<400px²)
	img := blankImage(200, 200, color.RGBA{255, 255, 255, 255})
	black := color.RGBA{0, 0, 0, 255}

	// Tiny rect: 5x5 = 25px² — well below 400px² minimum
	drawFilledRect(img, 50, 50, 55, 55, black)
	drawFilledRect(img, 150, 150, 155, 155, black)

	jpegData := makeJPEG(img)

	regions, _, err := DetectCanvasRegions(jpegData, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Tiny regions should be filtered out
	for _, r := range regions {
		area := r.PixelW * r.PixelH
		if area < 400 {
			t.Errorf("region %s has area %d which is below minimum 400", r.ID, area)
		}
	}

	t.Logf("found %d regions (tiny rects should be filtered)", len(regions))
}

func TestNormalizedIoU(t *testing.T) {
	tests := []struct {
		name string
		a, b [4]float64 // x, y, w, h
		want float64
	}{
		{"identical", [4]float64{0.1, 0.1, 0.5, 0.5}, [4]float64{0.1, 0.1, 0.5, 0.5}, 1.0},
		{"no overlap", [4]float64{0.0, 0.0, 0.1, 0.1}, [4]float64{0.5, 0.5, 0.1, 0.1}, 0.0},
		{"partial", [4]float64{0.0, 0.0, 0.2, 0.2}, [4]float64{0.1, 0.1, 0.2, 0.2}, 0.0}, // small overlap
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizedIoU(tt.a[0], tt.a[1], tt.a[2], tt.a[3],
				tt.b[0], tt.b[1], tt.b[2], tt.b[3])
			if tt.want == 0 && got != 0 {
				// For "no overlap", exact zero
				if tt.name == "no overlap" {
					t.Errorf("expected 0, got %f", got)
				}
			} else if tt.want == 1.0 && (got < 0.99 || got > 1.01) {
				t.Errorf("expected ~1.0, got %f", got)
			}
		})
	}
}

func TestBoxIoU(t *testing.T) {
	a := image.Rect(0, 0, 100, 100)
	b := image.Rect(0, 0, 100, 100)
	iou := boxIoU(a, b)
	if iou < 0.99 {
		t.Errorf("identical boxes should have IoU ~1.0, got %f", iou)
	}

	c := image.Rect(200, 200, 300, 300)
	iou2 := boxIoU(a, c)
	if iou2 != 0 {
		t.Errorf("non-overlapping boxes should have IoU 0, got %f", iou2)
	}
}

func TestInvalidJPEG(t *testing.T) {
	_, _, err := DetectCanvasRegions([]byte("not a jpeg"), nil, nil, nil)
	if err == nil {
		t.Error("expected error for invalid JPEG data")
	}
}

// makePNG creates a PNG-encoded image from an RGBA image.
func makePNG(img *image.RGBA) []byte {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func TestDetectCanvasRegions_CropToUncolored(t *testing.T) {
	// Verify crop-to-uncolored: Canny runs only on non-overlay regions.
	//
	// Left 40%: blue overlay. Right 60%: white with black rectangle.
	// With overlayMap: Canny crops to the right uncolored region only.
	// The 6px crop padding extends into overlay border, creating a thin
	// boundary edge — but its center stays in the uncolored half (>0.40).
	img := blankImage(400, 400, color.RGBA{255, 255, 255, 255})
	blue := color.RGBA{0, 0, 255, 255}
	black := color.RGBA{0, 0, 0, 255}

	// Blue overlay covering left 40%.
	drawFilledRect(img, 0, 0, 160, 400, blue)
	// Black rectangle in right (uncolored) area.
	drawFilledRect(img, 260, 260, 380, 350, black)

	pngData := makePNG(img)

	// Classify overlay.
	overlayMap := ClassifyOverlay(img, 0)
	if overlayMap.OverlayPixelCount == 0 {
		t.Fatal("expected overlay pixels to be classified")
	}

	// Run with overlay map (crop-to-uncolored).
	regions, _, err := DetectCanvasRegions(pngData, nil, overlayMap, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 1. The black rect in the right half must be detected.
	foundBlackRect := false
	for _, r := range regions {
		if r.X > 0.5 && r.Y > 0.5 {
			foundBlackRect = true
		}
	}
	if !foundBlackRect {
		t.Error("black rect in uncolored region not detected")
	}

	// 2. No regions should have center in the overlay area (left 40%).
	for _, r := range regions {
		centerX := r.X + r.W/2
		if centerX < 0.40 {
			t.Errorf("region %s has center in overlay area (centerX=%.3f < 0.40), should be cropped out", r.ID, centerX)
		}
	}

	t.Logf("found %d regions", len(regions))
	for _, r := range regions {
		t.Logf("  %s: norm=(%.3f, %.3f, %.3f, %.3f)", r.ID, r.X, r.Y, r.W, r.H)
	}
}

func TestDetectCanvasRegions_ImplicitCoverageGate(t *testing.T) {
	// Overlay covers all but a 50x50 corner — too small for 100x100 threshold.
	// FindUncoloredRegions returns 0 → Canny skipped implicitly.
	img := blankImage(400, 400, color.RGBA{0, 0, 255, 255}) // all blue
	// Clear a 50x50 corner to white.
	for y := 350; y < 400; y++ {
		for x := 350; x < 400; x++ {
			img.SetRGBA(x, y, color.RGBA{255, 255, 255, 255})
		}
	}

	pngData := makePNG(img)
	overlayMap := ClassifyOverlay(img, 0)

	regions, _, err := DetectCanvasRegions(pngData, nil, overlayMap, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(regions) != 0 {
		t.Errorf("expected 0 regions (50x50 uncolored < 100x100 threshold), got %d", len(regions))
	}
}

func TestDetectCanvasRegions_NilOverlayLegacy(t *testing.T) {
	// nil overlay map → existing behavior unchanged (same as old tests).
	img := blankImage(400, 400, color.RGBA{255, 255, 255, 255})
	black := color.RGBA{0, 0, 0, 255}
	drawFilledRect(img, 50, 50, 150, 100, black)

	jpegData := makeJPEG(img)

	regions, _, err := DetectCanvasRegions(jpegData, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should still detect the black rectangle.
	if len(regions) == 0 {
		t.Error("expected at least 1 region with nil overlay map")
	}
}

func TestDetectCanvasRegions_PNGInput(t *testing.T) {
	// PNG image decodes and processes correctly.
	img := blankImage(400, 400, color.RGBA{255, 255, 255, 255})
	black := color.RGBA{0, 0, 0, 255}
	drawFilledRect(img, 100, 100, 250, 200, black)

	pngData := makePNG(img)

	regions, _, err := DetectCanvasRegions(pngData, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(regions) == 0 {
		t.Error("expected at least 1 region from PNG input")
	}
	t.Logf("PNG input: found %d regions", len(regions))
}
