package api

import (
	"image"
	"image/color"
	"sort"
)

// OverlayColor represents a known semantic overlay color.
type OverlayColor struct {
	Name    string // "BLUE", "ORANGE", etc.
	R, G, B uint8  // exact RGB from content.js
	Type    string // "link", "button", "input", "clickable", "container", "other"
}

// SemanticColors — canonical palette matching content.js SEMANTIC_COLORS.
// Uses RGB cube vertices for maximum pairwise separation (min dist = 255).
var SemanticColors = []OverlayColor{
	{"MAGENTA", 255, 0, 255, "link"},
	{"LIME", 0, 255, 0, "button"},
	{"CYAN", 0, 255, 255, "input"},
	{"YELLOW", 255, 255, 0, "clickable"},
	{"BLUE", 0, 0, 255, "container"},
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
// With cube-vertex colors, use maxDistSq ~900 (distance ~30).
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

// FindUncoloredRegions returns bounding boxes of connected regions of
// non-overlay pixels. Regions smaller than minW x minH are excluded.
// At most maxRegions are returned (largest by area), or all if maxRegions <= 0.
func (m *OverlayMap) FindUncoloredRegions(minW, minH, maxRegions int) []image.Rectangle {
	w, h := m.Width, m.Height
	visited := make([]bool, w*h)
	var boxes []image.Rectangle

	// 4-connected flood-fill (background regions are contiguous areas).
	dx := [4]int{-1, 1, 0, 0}
	dy := [4]int{0, 0, -1, 1}

	for startY := 0; startY < h; startY++ {
		for startX := 0; startX < w; startX++ {
			idx := startY*w + startX
			if visited[idx] || m.Data[idx] >= 0 {
				continue
			}

			// Flood-fill to find connected non-overlay component.
			minX, minY := startX, startY
			maxX, maxY := startX, startY
			queue := []int{idx}
			visited[idx] = true

			for len(queue) > 0 {
				ci := queue[0]
				queue = queue[1:]
				cy, cx := ci/w, ci%w

				if cx < minX {
					minX = cx
				}
				if cx > maxX {
					maxX = cx
				}
				if cy < minY {
					minY = cy
				}
				if cy > maxY {
					maxY = cy
				}

				for d := 0; d < 4; d++ {
					nx, ny := cx+dx[d], cy+dy[d]
					if nx < 0 || nx >= w || ny < 0 || ny >= h {
						continue
					}
					ni := ny*w + nx
					if !visited[ni] && m.Data[ni] < 0 {
						visited[ni] = true
						queue = append(queue, ni)
					}
				}
			}

			bw := maxX - minX + 1
			bh := maxY - minY + 1
			if bw >= minW && bh >= minH {
				boxes = append(boxes, image.Rect(minX, minY, maxX+1, maxY+1))
			}
		}
	}

	// Cap to maxRegions largest by area.
	if maxRegions > 0 && len(boxes) > maxRegions {
		sort.Slice(boxes, func(i, j int) bool {
			ai := boxes[i].Dx() * boxes[i].Dy()
			aj := boxes[j].Dx() * boxes[j].Dy()
			return ai > aj
		})
		boxes = boxes[:maxRegions]
	}

	return boxes
}
