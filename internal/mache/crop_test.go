package mache

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

// makeTestJPEG creates a solid red JPEG image of the given dimensions.
func makeTestJPEG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 255, G: 0, B: 0, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90})
	return buf.Bytes()
}

func TestCropScreenshot_FullRegion(t *testing.T) {
	jpegData := makeTestJPEG(200, 100)
	cropped, err := CropScreenshot(jpegData, [4]float64{0, 0, 1, 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cropped == nil {
		t.Fatal("expected non-nil result for full region crop")
	}
	// Decode the cropped image and verify dimensions match the original.
	img, err := jpeg.Decode(bytes.NewReader(cropped))
	if err != nil {
		t.Fatalf("failed to decode cropped JPEG: %v", err)
	}
	bounds := img.Bounds()
	if bounds.Dx() != 200 || bounds.Dy() != 100 {
		t.Errorf("expected 200x100, got %dx%d", bounds.Dx(), bounds.Dy())
	}
}

func TestCropScreenshot_QuarterTopLeft(t *testing.T) {
	jpegData := makeTestJPEG(200, 100)
	cropped, err := CropScreenshot(jpegData, [4]float64{0, 0, 0.5, 0.5})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cropped == nil {
		t.Fatal("expected non-nil result for quarter crop")
	}
	img, err := jpeg.Decode(bytes.NewReader(cropped))
	if err != nil {
		t.Fatalf("failed to decode cropped JPEG: %v", err)
	}
	bounds := img.Bounds()
	if bounds.Dx() != 100 || bounds.Dy() != 50 {
		t.Errorf("expected 100x50, got %dx%d", bounds.Dx(), bounds.Dy())
	}
}

func TestCropScreenshot_ClampToBounds(t *testing.T) {
	jpegData := makeTestJPEG(200, 100)
	// Region extends past the image boundary: starts at (0.8, 0.8) with size (0.5, 0.5)
	// would go to (1.3, 1.3), which should be clamped to (1.0, 1.0).
	cropped, err := CropScreenshot(jpegData, [4]float64{0.8, 0.8, 0.5, 0.5})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cropped == nil {
		t.Fatal("expected non-nil result for clamped crop")
	}
	img, err := jpeg.Decode(bytes.NewReader(cropped))
	if err != nil {
		t.Fatalf("failed to decode cropped JPEG: %v", err)
	}
	bounds := img.Bounds()
	// Start at pixel (160, 80). Unclamped end would be (260, 130) but image is 200x100.
	// Clamped width = 200-160 = 40, clamped height = 100-80 = 20.
	if bounds.Dx() != 40 || bounds.Dy() != 20 {
		t.Errorf("expected 40x20 (clamped), got %dx%d", bounds.Dx(), bounds.Dy())
	}
}

func TestCropScreenshot_ZeroArea(t *testing.T) {
	jpegData := makeTestJPEG(200, 100)
	_, err := CropScreenshot(jpegData, [4]float64{0.5, 0.5, 0, 0})
	if err == nil {
		t.Fatal("expected error for zero-area region")
	}
}

func TestCropScreenshot_InvalidJPEG(t *testing.T) {
	garbage := []byte("not a jpeg at all")
	_, err := CropScreenshot(garbage, [4]float64{0, 0, 1, 1})
	if err == nil {
		t.Fatal("expected error for invalid JPEG data")
	}
}

func TestCropScreenshot_NilBytes(t *testing.T) {
	cropped, err := CropScreenshot(nil, [4]float64{0, 0, 1, 1})
	if err != nil {
		t.Fatalf("unexpected error for nil input: %v", err)
	}
	if cropped != nil {
		t.Fatal("expected nil result for nil input")
	}

	// Also test empty slice.
	cropped, err = CropScreenshot([]byte{}, [4]float64{0, 0, 1, 1})
	if err != nil {
		t.Fatalf("unexpected error for empty input: %v", err)
	}
	if cropped != nil {
		t.Fatal("expected nil result for empty input")
	}
}
