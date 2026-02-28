package mache

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	_ "image/png" // register PNG decoder for format-agnostic image.Decode
)

// CropScreenshot extracts a sub-region from a screenshot (JPEG or PNG).
// The region is specified as [x, y, w, h] in normalized 0-1 coordinates.
// Returns the cropped region re-encoded as JPEG, or (nil, nil) if imgData is nil/empty.
func CropScreenshot(imgData []byte, region [4]float64) ([]byte, error) {
	if len(imgData) == 0 {
		return nil, nil
	}

	img, _, err := image.Decode(bytes.NewReader(imgData))
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}

	bounds := img.Bounds()
	imgW := float64(bounds.Dx())
	imgH := float64(bounds.Dy())

	x, y, w, h := region[0], region[1], region[2], region[3]

	// Compute pixel rectangle.
	px := int(x * imgW)
	py := int(y * imgH)
	pw := int(w * imgW)
	ph := int(h * imgH)

	// Clamp to image bounds: ensure the crop rectangle doesn't extend past the image.
	if px < 0 {
		px = 0
	}
	if py < 0 {
		py = 0
	}
	if px+pw > bounds.Dx() {
		pw = bounds.Dx() - px
	}
	if py+ph > bounds.Dy() {
		ph = bounds.Dy() - py
	}

	// Degenerate region check.
	if pw <= 0 || ph <= 0 {
		return nil, fmt.Errorf("degenerate crop region: computed %dx%d pixels", pw, ph)
	}

	// Create a new RGBA image and copy the sub-region.
	cropRect := image.Rect(0, 0, pw, ph)
	cropped := image.NewRGBA(cropRect)
	srcRect := image.Rect(bounds.Min.X+px, bounds.Min.Y+py, bounds.Min.X+px+pw, bounds.Min.Y+py+ph)
	draw.Draw(cropped, cropRect, img, srcRect.Min, draw.Src)

	// Re-encode as JPEG.
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, cropped, &jpeg.Options{Quality: 85}); err != nil {
		return nil, fmt.Errorf("encode cropped jpeg: %w", err)
	}

	return buf.Bytes(), nil
}
