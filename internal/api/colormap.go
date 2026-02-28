package api

import (
	"image"
	"image/color"
)

// OverlayColor represents a known semantic overlay color.
type OverlayColor struct {
	Name    string // "BLUE", "ORANGE", etc.
	R, G, B uint8  // exact RGB from content.js
	Type    string // "link", "button", "input", "clickable", "container", "other"
}

// SemanticColors — canonical palette matching content.js SEMANTIC_COLORS.
var SemanticColors = []OverlayColor{
	{"BLUE", 0, 0, 255, "link"},
	{"ORANGE", 255, 165, 0, "button"},
	{"GREEN", 0, 200, 0, "input"},
	{"PURPLE", 160, 32, 240, "container"},
	{"YELLOW", 255, 220, 0, "clickable"},
	{"RED", 255, 0, 0, "other"},
}

// OverlayMap is a per-pixel classification of the screenshot.
type OverlayMap struct {
	Width, Height     int
	Data              []int8 // per-pixel: -1 = background, 0-5 = SemanticColors index
	OverlayPixelCount int
}

// ClassifyOverlay scans a decoded image and classifies each pixel.
// maxDistSq is the maximum squared Euclidean distance for a color match.
// With PNG + 100% opacity borders: maxDistSq=0 works for borders.
// With alpha-blended fills or JPEG: use maxDistSq ~3600 (distance ~60).
func ClassifyOverlay(img image.Image, maxDistSq int) *OverlayMap {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	m := &OverlayMap{
		Width:  w,
		Height: h,
		Data:   make([]int8, w*h),
	}

	// Initialize all pixels to -1 (background).
	for i := range m.Data {
		m.Data[i] = -1
	}

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b, _ := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			// RGBA returns 16-bit pre-multiplied values; shift to 8-bit.
			pr := uint8(r >> 8)
			pg := uint8(g >> 8)
			pb := uint8(b >> 8)

			bestIdx := int8(-1)
			bestDist := maxDistSq + 1 // sentinel: worse than threshold

			for i, sc := range SemanticColors {
				dr := int(pr) - int(sc.R)
				dg := int(pg) - int(sc.G)
				db := int(pb) - int(sc.B)
				dist := dr*dr + dg*dg + db*db
				if dist <= maxDistSq && dist < bestDist {
					bestDist = dist
					bestIdx = int8(i)
				}
			}

			idx := y*w + x
			m.Data[idx] = bestIdx
			if bestIdx >= 0 {
				m.OverlayPixelCount++
			}
		}
	}

	return m
}

// CoverageRatio returns the fraction of pixels classified as overlay [0,1].
func (m *OverlayMap) CoverageRatio() float64 {
	total := m.Width * m.Height
	if total == 0 {
		return 0
	}
	return float64(m.OverlayPixelCount) / float64(total)
}

// MaskImage returns a grayscale copy of src with overlay pixels set to
// uniform gray (128), neutralizing them for edge detection.
func (m *OverlayMap) MaskImage(src image.Image) *image.Gray {
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	gray := image.NewGray(image.Rect(0, 0, w, h))

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			idx := y*w + x
			if idx < len(m.Data) && m.Data[idx] >= 0 {
				// Overlay pixel → uniform gray to eliminate edges.
				gray.SetGray(x, y, color.Gray{Y: 128})
			} else {
				// Non-overlay pixel → standard luminance conversion.
				r, g, b, _ := src.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
				lum := uint8((299*uint32(r>>8) + 587*uint32(g>>8) + 114*uint32(b>>8)) / 1000)
				gray.SetGray(x, y, color.Gray{Y: lum})
			}
		}
	}

	return gray
}

// Dilate expands the overlay mask by radius pixels to cover JPEG blur
// artifacts around overlay borders. Returns a new OverlayMap.
func (m *OverlayMap) Dilate(radius int) *OverlayMap {
	w, h := m.Width, m.Height
	out := &OverlayMap{
		Width:  w,
		Height: h,
		Data:   make([]int8, w*h),
	}

	// Copy original data.
	copy(out.Data, m.Data)

	// Expand: for each overlay pixel, mark all neighbors within radius.
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if m.Data[y*w+x] < 0 {
				continue
			}
			val := m.Data[y*w+x]
			for dy := -radius; dy <= radius; dy++ {
				ny := y + dy
				if ny < 0 || ny >= h {
					continue
				}
				for dx := -radius; dx <= radius; dx++ {
					nx := x + dx
					if nx < 0 || nx >= w {
						continue
					}
					ni := ny*w + nx
					if out.Data[ni] < 0 {
						out.Data[ni] = val
						out.OverlayPixelCount++
					}
				}
			}
		}
	}

	// Recount (original pixels were already counted during copy but OverlayPixelCount was 0).
	out.OverlayPixelCount = 0
	for _, v := range out.Data {
		if v >= 0 {
			out.OverlayPixelCount++
		}
	}

	return out
}
