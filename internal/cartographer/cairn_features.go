// cairn_features.go — Extract 24D fused feature vectors from screenshot grid cells + DOM.
// Ported from cairn experiment (features.go). Adapted to accept image.Image directly.
//
// Dimensions 0-11: visual (biologically motivated by retinal/V1 cortex processing).
// Dimensions 12-23: semantic (DOM structure, interactivity, spatial position).
//
// Visual channels:
//
// Color processing (retina/LGN — opponent color channels):
//
//	[0] luminance      — ITU-R BT.601: 0.299R + 0.587G + 0.114B
//	[1] rgOpponent     — red-green opponent channel: R - G (L-M cone pathway)
//	[2] byOpponent     — blue-yellow opponent: B - (R+G)/2 (S-(L+M) pathway)
//	[3] saturation     — color purity: max(R,G,B) - min(R,G,B)
//
// Edge/orientation (V1 simple cells — orientation-selective columns):
//
//	[4] edgeDensity    — fraction of pixels with strong gradient
//	[5] horizEnergy    — energy in horizontal edges (text lines, dividers)
//	[6] vertEnergy     — energy in vertical edges (columns, borders)
//	[7] diagEnergy     — energy in diagonal edges (photos, graphics)
//	[8] dirVariance    — circular variance (low=text, high=photo)
//
// Spatial frequency (V1 complex cells):
//
//	[9]  contrast      — grayscale dynamic range
//	[10] peakStrength  — dominant spectral frequency peak
//	[11] entropy       — spectral complexity

package cartographer

import (
	"image"
	"math"
)

// CairnNumDims is the dimensionality of the optic nerve feature vector.
const CairnNumDims = 24

// CairnGridCell holds a cell's position and extracted features.
type CairnGridCell struct {
	Row, Col int                   // grid position
	X, Y     int                   // pixel top-left
	W, H     int                   // pixel dimensions
	Features [CairnNumDims]float64 // 24D fused visual+semantic vector
}

// CairnFeatureDimNames maps dimension index to human-readable name.
var CairnFeatureDimNames = []string{
	"luma", "rg", "by", "sat",
	"edgeDens", "hEdge", "vEdge", "dEdge", "dirVar",
	"contrast", "peakStr", "entropy",
	"area", "depth", "interact", "hasText",
	"container", "heading", "image", "xPos",
	"yPos", "childCount", "textDens", "aspect",
}

// ExtractFusedFeatures extracts 24D features per grid cell from an already-decoded image and DOM elements.
func ExtractFusedFeatures(img image.Image, elements []element, gridSize int) []CairnGridCell {
	bounds := img.Bounds()
	imgW := bounds.Max.X - bounds.Min.X
	imgH := bounds.Max.Y - bounds.Min.Y

	if gridSize < 1 {
		gridSize = 1
	}

	cellW := imgW / gridSize
	cellH := imgH / gridSize

	if cellW < 4 || cellH < 4 {
		gridSize = min(imgW/4, imgH/4)
		if gridSize < 1 {
			gridSize = 1
		}
		cellW = imgW / gridSize
		cellH = imgH / gridSize
	}

	var cells []CairnGridCell

	// Pre-compute depth for elements O(N)
	idToIdx := make(map[string]int, len(elements))
	for i, el := range elements {
		idToIdx[el.id] = i
	}
	depths := make([]float64, len(elements))
	for i, el := range elements {
		d := 0.0
		p := el.parentID
		for p != "" && p != "none" && d < 50 {
			d++
			if pIdx, ok := idToIdx[p]; ok {
				p = elements[pIdx].parentID
			} else {
				break
			}
		}
		depths[i] = d
	}

	// Map elements to grid cells O(N)
	cellElements := make([][]int, gridSize*gridSize)
	for i, el := range elements {
		if !el.hasBounds {
			continue
		}
		col := int(el.centerX * float64(gridSize))
		row := int(el.centerY * float64(gridSize))
		if col >= gridSize {
			col = gridSize - 1
		}
		if row >= gridSize {
			row = gridSize - 1
		}
		if col < 0 {
			col = 0
		}
		if row < 0 {
			row = 0
		}
		idx := row*gridSize + col
		cellElements[idx] = append(cellElements[idx], i)
	}

	for row := 0; row < gridSize; row++ {
		for col := 0; col < gridSize; col++ {
			x0 := bounds.Min.X + col*cellW
			y0 := bounds.Min.Y + row*cellH
			w := cellW
			h := cellH

			if x0+w > bounds.Max.X {
				w = bounds.Max.X - x0
			}
			if y0+h > bounds.Max.Y {
				h = bounds.Max.Y - y0
			}

			// RGB: average color
			r, g, b := cairnSampleCellRGB(img, x0, y0, w, h)

			// Color opponent channels (retina/LGN)
			luminance := 0.299*r + 0.587*g + 0.114*b
			rgOpponent := r - g
			byOpponent := b - (r+g)/2
			saturation := cairnMax3(r, g, b) - cairnMin3(r, g, b)

			// Grayscale for edge + spectral analysis
			gray := cairnExtractGrayscale(img, x0, y0, w, h)

			// Sobel edge features (V1 orientation-selective columns)
			edgeDensity, horizEnergy, vertEnergy, diagEnergy, dirVariance := cairnSobelFeatures(gray, w, h)

			// Contrast: max - min grayscale
			contrast := cairnGrayContrast(gray)

			// FFT spectral features (reuses existing AnalyzeRegion from fft.go)
			fft := AnalyzeRegion(gray, w, h)

			// Semantic Features
			var maxArea, maxDepth, interact, hasText float64
			var container, heading, imageFlag float64
			var sumTextDens, sumAspect float64

			cellIdx := row*gridSize + col
			els := cellElements[cellIdx]
			childCount := float64(len(els))

			for _, eIdx := range els {
				el := elements[eIdx]
				area := el.bounds[2] * el.bounds[3]
				if area > maxArea {
					maxArea = area
				}
				if depths[eIdx] > maxDepth {
					maxDepth = depths[eIdx]
				}

				if el.tag == "button" || el.tag == "a" || el.tag == "input" || el.interactive {
					interact = 1.0
				}
				if el.text != "" {
					hasText = 1.0
				}
				if el.tag == "div" || el.tag == "nav" || el.tag == "main" || el.tag == "section" || el.tag == "article" || el.tag == "header" || el.tag == "footer" || el.tag == "ul" || el.tag == "ol" {
					container = 1.0
				}
				if el.tag == "h1" || el.tag == "h2" || el.tag == "h3" || el.tag == "h4" || el.tag == "h5" || el.tag == "h6" {
					heading = 1.0
				}
				if el.tag == "img" || el.tag == "svg" || el.tag == "picture" {
					imageFlag = 1.0
				}

				sumTextDens += el.textDensity
				aspect := 0.0
				if el.bounds[3] > 0 {
					aspect = el.bounds[2] / el.bounds[3]
				}
				sumAspect += aspect
			}

			avgTextDens := 0.0
			avgAspect := 0.0
			if childCount > 0 {
				avgTextDens = sumTextDens / childCount
				avgAspect = sumAspect / childCount
			}

			xPos := float64(col) / float64(gridSize)
			yPos := float64(row) / float64(gridSize)

			features := [CairnNumDims]float64{
				luminance,        // 0
				rgOpponent,       // 1
				byOpponent,       // 2
				saturation,       // 3
				edgeDensity,      // 4
				horizEnergy,      // 5
				vertEnergy,       // 6
				diagEnergy,       // 7
				dirVariance,      // 8
				contrast,         // 9
				fft.PeakStrength, // 10
				fft.Entropy,      // 11
				maxArea,          // 12
				maxDepth,         // 13
				interact,         // 14
				hasText,          // 15
				container,        // 16
				heading,          // 17
				imageFlag,        // 18
				xPos,             // 19
				yPos,             // 20
				childCount,       // 21
				avgTextDens,      // 22
				avgAspect,        // 23
			}

			cells = append(cells, CairnGridCell{
				Row:      row,
				Col:      col,
				X:        x0,
				Y:        y0,
				W:        w,
				H:        h,
				Features: features,
			})
		}
	}

	return cells
}

// CairnNormalizeFeatures scales features so each dimension has roughly [0,1] range.
func CairnNormalizeFeatures(cells []CairnGridCell) {
	if len(cells) == 0 {
		return
	}

	var mins, maxs [CairnNumDims]float64
	for i := 0; i < CairnNumDims; i++ {
		mins[i] = math.Inf(1)
		maxs[i] = math.Inf(-1)
	}
	for _, c := range cells {
		for i := 0; i < CairnNumDims; i++ {
			if c.Features[i] < mins[i] {
				mins[i] = c.Features[i]
			}
			if c.Features[i] > maxs[i] {
				maxs[i] = c.Features[i]
			}
		}
	}

	for ci := range cells {
		for i := 0; i < CairnNumDims; i++ {
			rng := maxs[i] - mins[i]
			if rng > 1e-10 {
				cells[ci].Features[i] = (cells[ci].Features[i] - mins[i]) / rng
			} else {
				cells[ci].Features[i] = 0
			}
		}
	}
}

// --- Sobel Edge Detection ---

func cairnSobelFeatures(gray []float64, w, h int) (edgeDensity, horizEnergy, vertEnergy, diagEnergy, dirVariance float64) {
	if w < 3 || h < 3 {
		return 0, 0, 0, 0, 0
	}

	// Gaussian blur first (5x5, σ≈1.4)
	blurred := cairnGaussianBlur5x5(gray, w, h)

	type gradPixel struct {
		mag, dir float64
	}
	var pixels []gradPixel
	var maxMag float64

	for y := 1; y < h-1; y++ {
		for x := 1; x < w-1; x++ {
			gx := -blurred[(y-1)*w+(x-1)] + blurred[(y-1)*w+(x+1)] +
				-2*blurred[y*w+(x-1)] + 2*blurred[y*w+(x+1)] +
				-blurred[(y+1)*w+(x-1)] + blurred[(y+1)*w+(x+1)]
			gy := -blurred[(y-1)*w+(x-1)] - 2*blurred[(y-1)*w+x] - blurred[(y-1)*w+(x+1)] +
				blurred[(y+1)*w+(x-1)] + 2*blurred[(y+1)*w+x] + blurred[(y+1)*w+(x+1)]

			mag := math.Sqrt(gx*gx + gy*gy)
			dir := math.Atan2(gy, gx)
			pixels = append(pixels, gradPixel{mag, dir})
			if mag > maxMag {
				maxMag = mag
			}
		}
	}

	if len(pixels) == 0 || maxMag == 0 {
		return 0, 0, 0, 0, 0
	}

	threshold := maxMag * 0.15
	edgeCount := 0
	for _, p := range pixels {
		if p.mag > threshold {
			edgeCount++
		}
	}
	edgeDensity = float64(edgeCount) / float64(len(pixels))

	var hE, vE, dE, totalE float64
	for _, p := range pixels {
		energy := p.mag * p.mag
		totalE += energy

		d := p.dir
		if d < 0 {
			d += math.Pi
		}

		switch {
		case d < math.Pi/4 || d >= 3*math.Pi/4:
			vE += energy
		case d >= math.Pi/4 && d < math.Pi/2:
			dE += energy
		case d >= math.Pi/2 && d < 3*math.Pi/4:
			hE += energy
		default:
			dE += energy
		}
	}
	if totalE > 0 {
		horizEnergy = hE / totalE
		vertEnergy = vE / totalE
		diagEnergy = dE / totalE
	}

	var sinSum, cosSum, weightSum float64
	for _, p := range pixels {
		sinSum += p.mag * math.Sin(2*p.dir)
		cosSum += p.mag * math.Cos(2*p.dir)
		weightSum += p.mag
	}
	if weightSum > 0 {
		meanSin := sinSum / weightSum
		meanCos := cosSum / weightSum
		R := math.Sqrt(meanSin*meanSin + meanCos*meanCos)
		dirVariance = 1.0 - R
	}

	return edgeDensity, horizEnergy, vertEnergy, diagEnergy, dirVariance
}

func cairnGaussianBlur5x5(src []float64, w, h int) []float64 {
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
					sy := clampInt(y+ky, 0, h-1)
					sx := clampInt(x+kx, 0, w-1)
					sum += src[sy*w+sx] * kernel[ky+2][kx+2]
				}
			}
			dst[y*w+x] = sum / kSum
		}
	}
	return dst
}

// cairnGrayContrast computes (max - min) of grayscale values.
func cairnGrayContrast(gray []float64) float64 {
	if len(gray) == 0 {
		return 0
	}
	mn, mx := gray[0], gray[0]
	for _, v := range gray[1:] {
		if v < mn {
			mn = v
		}
		if v > mx {
			mx = v
		}
	}
	return mx - mn
}

// --- Color helpers ---

func cairnMax3(a, b, c float64) float64 {
	if a >= b && a >= c {
		return a
	}
	if b >= c {
		return b
	}
	return c
}

func cairnMin3(a, b, c float64) float64 {
	if a <= b && a <= c {
		return a
	}
	if b <= c {
		return b
	}
	return c
}

// cairnSampleCellRGB computes average RGB in [0,1] for a cell region.
func cairnSampleCellRGB(img image.Image, x0, y0, w, h int) (float64, float64, float64) {
	var rSum, gSum, bSum float64
	count := 0

	stepX := max(1, w/8)
	stepY := max(1, h/8)

	for y := y0; y < y0+h; y += stepY {
		for x := x0; x < x0+w; x += stepX {
			r, g, b, _ := img.At(x, y).RGBA()
			rSum += float64(r) / 65535.0
			gSum += float64(g) / 65535.0
			bSum += float64(b) / 65535.0
			count++
		}
	}

	if count == 0 {
		return 0, 0, 0
	}
	return rSum / float64(count), gSum / float64(count), bSum / float64(count)
}

// cairnExtractGrayscale converts a cell region to grayscale float64 slice.
func cairnExtractGrayscale(img image.Image, x0, y0, w, h int) []float64 {
	gray := make([]float64, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b, _ := img.At(x0+x, y0+y).RGBA()
			rf := float64(r) / 65535.0
			gf := float64(g) / 65535.0
			bf := float64(b) / 65535.0
			gray[y*w+x] = 0.299*rf + 0.587*gf + 0.114*bf
		}
	}
	return gray
}
