package api

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"math"
)

// EdgeRegion describes a rectangular UI region detected via edge analysis inside
// canvas or other pixel-rendered content.
type EdgeRegion struct {
	ID                             string  // "cv-0", "cv-1", ...
	X, Y, W, H                     float64 // normalized 0.0–1.0 (matches summary Bounds format)
	PixelX, PixelY, PixelW, PixelH int     // absolute pixel coords for CDP click
}

// DetectCanvasRegions runs a minimal Canny edge detection pipeline on a JPEG
// screenshot, finds rectangular regions, filters out those overlapping existing
// mache bounds, and returns annotated regions + an annotated JPEG with cyan boxes.
//
// existingBounds contains [x, y, w, h] normalized coordinates of DOM-tagged elements.
func DetectCanvasRegions(jpegData []byte, existingBounds [][4]float64) ([]EdgeRegion, []byte, error) {
	// 1. Decode JPEG
	img, err := jpeg.Decode(bytes.NewReader(jpegData))
	if err != nil {
		return nil, nil, fmt.Errorf("decode jpeg: %w", err)
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w == 0 || h == 0 {
		return nil, nil, fmt.Errorf("empty image: %dx%d", w, h)
	}

	// 2. Convert to grayscale
	gray := toGrayscale(img)

	// 3. Gaussian blur (σ=2.0, kernel size 5)
	blurred := gaussianBlur(gray, w, h)

	// 4. Sobel edge detection → gradient magnitude + direction
	magnitude, direction := sobelEdges(blurred, w, h)

	// 5. Non-maximum suppression (thin edges to 1px)
	thinned := nonMaxSuppression(magnitude, direction, w, h)

	// 6. Double threshold + hysteresis
	edges := hysteresis(thinned, w, h, 50.0, 150.0)

	// 7. Find connected components → bounding boxes
	boxes := findBoundingBoxes(edges, w, h, 400, int(float64(w*h)/2))

	// 8. Overlap filtering — skip regions that overlap existing mache bounds
	var regions []EdgeRegion
	cvIdx := 0
	for _, box := range boxes {
		nx := float64(box.Min.X) / float64(w)
		ny := float64(box.Min.Y) / float64(h)
		nw := float64(box.Dx()) / float64(w)
		nh := float64(box.Dy()) / float64(h)

		if overlapsExisting(nx, ny, nw, nh, existingBounds, 0.3) {
			continue
		}

		regions = append(regions, EdgeRegion{
			ID:     fmt.Sprintf("cv-%d", cvIdx),
			X:      nx,
			Y:      ny,
			W:      nw,
			H:      nh,
			PixelX: box.Min.X,
			PixelY: box.Min.Y,
			PixelW: box.Dx(),
			PixelH: box.Dy(),
		})
		cvIdx++
	}

	if len(regions) == 0 {
		return nil, jpegData, nil
	}

	// 9. Annotate screenshot with cyan rectangles + labels
	annotated := annotateImage(img, regions)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, annotated, &jpeg.Options{Quality: 80}); err != nil {
		return regions, jpegData, nil // return regions with original image on encode failure
	}

	return regions, buf.Bytes(), nil
}

// toGrayscale converts an image to a flat float64 array of luminance values.
func toGrayscale(img image.Image) []float64 {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	gray := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b, _ := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			// ITU-R BT.601 luminance
			gray[y*w+x] = 0.299*float64(r>>8) + 0.587*float64(g>>8) + 0.114*float64(b>>8)
		}
	}
	return gray
}

// gaussianBlur applies a 5x5 Gaussian blur with σ≈1.4.
// Uses edge clamping to avoid artificial zero-borders that create false edges.
func gaussianBlur(src []float64, w, h int) []float64 {
	kernel := [5][5]float64{
		{1, 4, 7, 4, 1},
		{4, 16, 26, 16, 4},
		{7, 26, 41, 26, 7},
		{4, 16, 26, 16, 4},
		{1, 4, 7, 4, 1},
	}
	const kSum = 273.0
	dst := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var sum float64
			for ky := -2; ky <= 2; ky++ {
				for kx := -2; kx <= 2; kx++ {
					sy := clamp(y+ky, 0, h-1)
					sx := clamp(x+kx, 0, w-1)
					sum += src[sy*w+sx] * kernel[ky+2][kx+2]
				}
			}
			dst[y*w+x] = sum / kSum
		}
	}
	return dst
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// sobelEdges computes gradient magnitude and direction using 3x3 Sobel operators.
func sobelEdges(src []float64, w, h int) ([]float64, []float64) {
	mag := make([]float64, w*h)
	dir := make([]float64, w*h)
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			// Sobel X kernel: [[-1,0,1],[-2,0,2],[-1,0,1]]
			gx := -src[(y-1)*w+(x-1)] + src[(y-1)*w+(x+1)] +
				-2*src[y*w+(x-1)] + 2*src[y*w+(x+1)] +
				-src[(y+1)*w+(x-1)] + src[(y+1)*w+(x+1)]
			// Sobel Y kernel: [[-1,-2,-1],[0,0,0],[1,2,1]]
			gy := -src[(y-1)*w+(x-1)] - 2*src[(y-1)*w+x] - src[(y-1)*w+(x+1)] +
				src[(y+1)*w+(x-1)] + 2*src[(y+1)*w+x] + src[(y+1)*w+(x+1)]
			mag[y*w+x] = math.Sqrt(gx*gx + gy*gy)
			dir[y*w+x] = math.Atan2(gy, gx)
		}
	}
	return mag, dir
}

// nonMaxSuppression thins edges to 1px width by suppressing non-maximum
// pixels along the gradient direction.
func nonMaxSuppression(mag, dir []float64, w, h int) []float64 {
	out := make([]float64, w*h)
	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			idx := y*w + x
			angle := dir[idx] * 180 / math.Pi
			if angle < 0 {
				angle += 180
			}

			var p1, p2 float64
			switch {
			case angle < 22.5 || angle >= 157.5: // horizontal edge
				p1 = mag[y*w+(x-1)]
				p2 = mag[y*w+(x+1)]
			case angle < 67.5: // diagonal (45°)
				p1 = mag[(y-1)*w+(x+1)]
				p2 = mag[(y+1)*w+(x-1)]
			case angle < 112.5: // vertical edge
				p1 = mag[(y-1)*w+x]
				p2 = mag[(y+1)*w+x]
			default: // diagonal (135°)
				p1 = mag[(y-1)*w+(x-1)]
				p2 = mag[(y+1)*w+(x+1)]
			}

			if mag[idx] >= p1 && mag[idx] >= p2 {
				out[idx] = mag[idx]
			}
		}
	}
	return out
}

// hysteresis applies double threshold and edge tracking by hysteresis.
// Strong edges (>high) are always kept. Weak edges (low..high) are kept
// only if connected to a strong edge.
func hysteresis(thin []float64, w, h int, low, high float64) []bool {
	n := w * h
	edge := make([]bool, n)

	// Mark strong edges and queue them for flood-fill.
	var queue []int
	for i := 0; i < n; i++ {
		if thin[i] >= high {
			edge[i] = true
			queue = append(queue, i)
		}
	}

	// BFS: connect weak edges adjacent to strong edges.
	dx := [8]int{-1, 0, 1, -1, 1, -1, 0, 1}
	dy := [8]int{-1, -1, -1, 0, 0, 1, 1, 1}
	for len(queue) > 0 {
		idx := queue[0]
		queue = queue[1:]
		y, x := idx/w, idx%w
		for d := 0; d < 8; d++ {
			nx, ny := x+dx[d], y+dy[d]
			if nx < 0 || nx >= w || ny < 0 || ny >= h {
				continue
			}
			ni := ny*w + nx
			if !edge[ni] && thin[ni] >= low {
				edge[ni] = true
				queue = append(queue, ni)
			}
		}
	}
	return edge
}

// findBoundingBoxes uses flood-fill to find connected components in the binary
// edge image and returns their axis-aligned bounding boxes.
// Skips components with area < minArea or > maxArea pixels.
func findBoundingBoxes(edges []bool, w, h, minArea, maxArea int) []image.Rectangle {
	visited := make([]bool, w*h)
	var boxes []image.Rectangle

	dx := [8]int{-1, 0, 1, -1, 1, -1, 0, 1}
	dy := [8]int{-1, -1, -1, 0, 0, 1, 1, 1}

	for startY := 0; startY < h; startY++ {
		for startX := 0; startX < w; startX++ {
			idx := startY*w + startX
			if !edges[idx] || visited[idx] {
				continue
			}

			// Flood-fill to find connected component
			minX, minY := startX, startY
			maxX, maxY := startX, startY
			area := 0
			queue := []int{idx}
			visited[idx] = true

			for len(queue) > 0 {
				ci := queue[0]
				queue = queue[1:]
				cy, cx := ci/w, ci%w
				area++

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

				for d := 0; d < 8; d++ {
					nx, ny := cx+dx[d], cy+dy[d]
					if nx < 0 || nx >= w || ny < 0 || ny >= h {
						continue
					}
					ni := ny*w + nx
					if edges[ni] && !visited[ni] {
						visited[ni] = true
						queue = append(queue, ni)
					}
				}
			}

			bw := maxX - minX + 1
			bh := maxY - minY + 1
			bboxArea := bw * bh
			if bboxArea < minArea || bboxArea > maxArea {
				continue
			}

			boxes = append(boxes, image.Rect(minX, minY, maxX+1, maxY+1))
		}
	}

	return mergeOverlappingBoxes(boxes)
}

// mergeOverlappingBoxes merges bounding boxes that significantly overlap,
// preventing duplicate detections for the same visual element.
func mergeOverlappingBoxes(boxes []image.Rectangle) []image.Rectangle {
	if len(boxes) <= 1 {
		return boxes
	}

	merged := make([]bool, len(boxes))
	var result []image.Rectangle

	for i := 0; i < len(boxes); i++ {
		if merged[i] {
			continue
		}
		r := boxes[i]
		changed := true
		for changed {
			changed = false
			for j := i + 1; j < len(boxes); j++ {
				if merged[j] {
					continue
				}
				if boxIoU(r, boxes[j]) > 0.3 {
					r = r.Union(boxes[j])
					merged[j] = true
					changed = true
				}
			}
		}
		result = append(result, r)
	}
	return result
}

// boxIoU computes Intersection over Union for two rectangles.
func boxIoU(a, b image.Rectangle) float64 {
	inter := a.Intersect(b)
	if inter.Empty() {
		return 0
	}
	interArea := float64(inter.Dx() * inter.Dy())
	unionArea := float64(a.Dx()*a.Dy()+b.Dx()*b.Dy()) - interArea
	if unionArea <= 0 {
		return 0
	}
	return interArea / unionArea
}

// overlapsExisting checks if a normalized region [x, y, w, h] has IoU > threshold
// with any of the existing mache bounds.
func overlapsExisting(x, y, w, h float64, existing [][4]float64, threshold float64) bool {
	for _, b := range existing {
		iou := normalizedIoU(x, y, w, h, b[0], b[1], b[2], b[3])
		if iou > threshold {
			return true
		}
	}
	return false
}

// normalizedIoU computes IoU between two rectangles given as (x, y, w, h)
// in normalized 0.0–1.0 coordinates.
func normalizedIoU(x1, y1, w1, h1, x2, y2, w2, h2 float64) float64 {
	// Convert to (left, top, right, bottom)
	l1, t1, r1, b1 := x1, y1, x1+w1, y1+h1
	l2, t2, r2, b2 := x2, y2, x2+w2, y2+h2

	interL := math.Max(l1, l2)
	interT := math.Max(t1, t2)
	interR := math.Min(r1, r2)
	interB := math.Min(b1, b2)

	if interL >= interR || interT >= interB {
		return 0
	}
	interArea := (interR - interL) * (interB - interT)
	unionArea := w1*h1 + w2*h2 - interArea
	if unionArea <= 0 {
		return 0
	}
	return interArea / unionArea
}

// annotateImage draws cyan rectangles and cv-N labels onto the image.
func annotateImage(src image.Image, regions []EdgeRegion) *image.RGBA {
	bounds := src.Bounds()
	dst := image.NewRGBA(bounds)
	draw.Draw(dst, bounds, src, bounds.Min, draw.Src)

	cyan := color.RGBA{R: 0, G: 255, B: 255, A: 255}

	for _, r := range regions {
		drawRect(dst, r.PixelX, r.PixelY, r.PixelX+r.PixelW, r.PixelY+r.PixelH, cyan, 2)
		drawLabel(dst, r.PixelX, r.PixelY-12, r.ID, cyan)
	}
	return dst
}

// drawRect draws a rectangle outline with the given thickness.
func drawRect(img *image.RGBA, x1, y1, x2, y2 int, c color.RGBA, thick int) {
	bounds := img.Bounds()
	for t := 0; t < thick; t++ {
		// Top edge
		for x := x1; x < x2; x++ {
			if y1+t >= bounds.Min.Y && y1+t < bounds.Max.Y && x >= bounds.Min.X && x < bounds.Max.X {
				img.SetRGBA(x, y1+t, c)
			}
		}
		// Bottom edge
		for x := x1; x < x2; x++ {
			if y2-1-t >= bounds.Min.Y && y2-1-t < bounds.Max.Y && x >= bounds.Min.X && x < bounds.Max.X {
				img.SetRGBA(x, y2-1-t, c)
			}
		}
		// Left edge
		for y := y1; y < y2; y++ {
			if x1+t >= bounds.Min.X && x1+t < bounds.Max.X && y >= bounds.Min.Y && y < bounds.Max.Y {
				img.SetRGBA(x1+t, y, c)
			}
		}
		// Right edge
		for y := y1; y < y2; y++ {
			if x2-1-t >= bounds.Min.X && x2-1-t < bounds.Max.X && y >= bounds.Min.Y && y < bounds.Max.Y {
				img.SetRGBA(x2-1-t, y, c)
			}
		}
	}
}

// drawLabel draws a simple text label using a minimal 5x7 pixel font.
func drawLabel(img *image.RGBA, x, y int, text string, c color.RGBA) {
	bounds := img.Bounds()
	// Draw background
	labelW := len(text)*6 + 2
	labelH := 10
	bg := color.RGBA{R: 0, G: 0, B: 0, A: 200}
	for dy := 0; dy < labelH; dy++ {
		for dx := 0; dx < labelW; dx++ {
			px, py := x+dx, y+dy
			if px >= bounds.Min.X && px < bounds.Max.X && py >= bounds.Min.Y && py < bounds.Max.Y {
				img.SetRGBA(px, py, bg)
			}
		}
	}
	// Draw each character
	cx := x + 1
	for _, ch := range text {
		glyph := getGlyph(byte(ch))
		for row := 0; row < 7; row++ {
			for col := 0; col < 5; col++ {
				if glyph[row]&(1<<(4-col)) != 0 {
					px, py := cx+col, y+1+row
					if px >= bounds.Min.X && px < bounds.Max.X && py >= bounds.Min.Y && py < bounds.Max.Y {
						img.SetRGBA(px, py, c)
					}
				}
			}
		}
		cx += 6
	}
}

// getGlyph returns a 7-row bitmask for a character (5 pixels wide).
func getGlyph(ch byte) [7]byte {
	glyphs := map[byte][7]byte{
		'c': {0x00, 0x0E, 0x10, 0x10, 0x10, 0x0E, 0x00},
		'v': {0x00, 0x11, 0x11, 0x0A, 0x0A, 0x04, 0x00},
		'-': {0x00, 0x00, 0x00, 0x1F, 0x00, 0x00, 0x00},
		'0': {0x0E, 0x11, 0x13, 0x15, 0x19, 0x11, 0x0E},
		'1': {0x04, 0x0C, 0x04, 0x04, 0x04, 0x04, 0x0E},
		'2': {0x0E, 0x11, 0x01, 0x06, 0x08, 0x10, 0x1F},
		'3': {0x0E, 0x11, 0x01, 0x06, 0x01, 0x11, 0x0E},
		'4': {0x02, 0x06, 0x0A, 0x12, 0x1F, 0x02, 0x02},
		'5': {0x1F, 0x10, 0x1E, 0x01, 0x01, 0x11, 0x0E},
		'6': {0x06, 0x08, 0x10, 0x1E, 0x11, 0x11, 0x0E},
		'7': {0x1F, 0x01, 0x02, 0x04, 0x08, 0x08, 0x08},
		'8': {0x0E, 0x11, 0x11, 0x0E, 0x11, 0x11, 0x0E},
		'9': {0x0E, 0x11, 0x11, 0x0F, 0x01, 0x02, 0x0C},
	}
	if g, ok := glyphs[ch]; ok {
		return g
	}
	return [7]byte{0x1F, 0x11, 0x11, 0x11, 0x11, 0x11, 0x1F} // fallback: box
}
