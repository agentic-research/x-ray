package cartographer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"log"
	"math"
	"sort"
	"strconv"
	"strings"
)

// TropicalCartographer implements api.SchemaGenerator using tropical geometry
// instead of a vision language model. It produces deterministic, reproducible
// zone segmentation from the DOM summary and optional screenshot pixel data.
//
// The approach: treat each DOM element as a point with a multi-dimensional
// "fiber" (spatial bounds, visual RGB, structural CSS path, semantic role).
// Compute pairwise distances in the max-plus semiring (tropical addition = max),
// then extract the optimal tree topology via neighbor-joining — the algorithmic
// realization of the Grassmannian Gr(2,n) ≅ metric trees isomorphism
// (Speyer-Sturmfels 2004). Cut the tree into 3-7 zones.
type TropicalCartographer struct {
	MinZones int
	MaxZones int

	// MaxElements caps the number of DOM elements fed into the O(N^3)
	// neighbor-joining algorithm. Excess elements are pre-filtered by
	// removing those with empty text and no semantic color. Default: 500.
	MaxElements int

	// Layout thresholds (normalized 0-1). Override for sites with
	// large hero headers or non-standard layouts.
	HeaderMaxY float64 // elements with centerY < this are "header" (default 0.15)
	FooterMinY float64 // elements with centerY > this are "footer" (default 0.85)
	SidebarW   float64 // elements with centerX < this or > 1-this are "sidebar" (default 0.2)

	// CohomologyTolerance controls H^0 zone folding. Zones with fiber
	// similarity above this threshold are merged (identical text/tag
	// distribution + overlapping spatial bounds). Default: 0.7.
	// Set to 1.0 to disable folding entirely.
	CohomologyTolerance float64
}

// element is a parsed DOM summary line enriched with computed features.
type element struct {
	id        string
	parentID  string
	tag       string
	text      string
	path      string
	color     string
	bounds    [4]float64 // [x, y, w, h] normalized
	hasBounds bool
	source    string // "dom", "cv", or "ax" — origin of this element

	// Semantic fiber data (from content.js computed styles)
	fontSize    float64 // CSS font-size in px
	display     string  // CSS display value
	interactive bool    // element is focusable/clickable
	textDensity float64 // chars per normalized area [0,1]
	hasSemantic bool    // true if semantic fields were parsed

	// Stacking context fields (from content.js computed styles + LayerTree)
	zIndex       string  // "auto" or integer string
	opacity      float64 // 0.0 to 1.0
	paintOrder   int     // compositing paint order from LayerTree DFS (-1 = no layer)
	stackingRoot bool    // true if element creates a stacking context
	hasPaint     bool    // true if paintOrder was parsed

	// FFT fiber data (for cv-* regions — canvas/WebGL structure detection)
	fft    FFTFeatures
	hasFFT bool

	// Computed
	centerX   float64
	centerY   float64
	rgb       [3]float64
	hasRGB    bool
	pathParts []string
}

// treeNode is a node in the reconstructed metric tree.
type treeNode struct {
	children []*treeNode
	elements []int   // leaf element indices in this subtree
	dist     float64 // distance from parent
	isLeaf   bool
}

// zone is a cluster of elements from tree cutting.
type zone struct {
	rootIdx  int   // index of the representative element
	elems    []int // indices into the element slice
	centerX  float64
	centerY  float64
	isList   bool
	listIdxs []int  // primary item indices (if list zone)
	selector string // CSS item_selector (if list zone)
}

// tropicalMount matches mache.CartographerOutput.Mounts JSON.
type tropicalMount struct {
	VirtualPath  string           `json:"virtual_path"`
	MacheID      string           `json:"mache_id"`
	Description  string           `json:"description"`
	PrimaryItems []string         `json:"primary_items"`
	ItemSelector string           `json:"item_selector,omitempty"`
	Bounds       [4]float64       `json:"bounds,omitempty"`        // zone AABB [x,y,w,h] normalized
	Fingerprint  string           `json:"fingerprint,omitempty"`   // content hash for cache staleness
	StructuralFP string           `json:"structural_fp,omitempty"` // tag-shape hash — stable across same-layout pages
	Boundaries   *MountBoundaries `json:"boundaries,omitempty"`    // H¹ contour boundary strengths (ADR-004)
}

// GenerateSchema implements api.SchemaGenerator.
func (tc *TropicalCartographer) GenerateSchema(
	ctx context.Context,
	screenshot []byte,
	mimeType, summary string,
) (string, error) {
	log.Println("TropicalCartographer: generating schema from DOM topology + pixel fibers")

	minZ, maxZ := tc.MinZones, tc.MaxZones
	if minZ <= 0 {
		minZ = 3
	}
	if maxZ <= 0 {
		maxZ = 7
	}
	maxElems := tc.MaxElements
	if maxElems <= 0 {
		maxElems = 500
	}

	// Step 1: Parse DOM summary
	elements := parseElements(summary)
	if len(elements) == 0 {
		return "", fmt.Errorf("no elements found in summary")
	}

	// Pre-filter: cap element count for O(N^3) NJ algorithm.
	// Keep elements with text or semantic color; drop empty filler first.
	if len(elements) > maxElems {
		elements = prefilterElements(elements, maxElems)
		log.Printf("TropicalCartographer: pre-filtered to %d elements", len(elements))
	}

	// Step 2: Sample RGB fibers from screenshot
	if len(screenshot) > 0 {
		sampleRGB(screenshot, elements)
		// Step 2b: FFT analysis for cv-* regions (canvas/WebGL structure detection).
		sampleFFT(screenshot, elements)
	}

	// Step 3: Build tropical distance matrix
	dist := buildDistanceMatrix(elements)

	// Step 4: Extract metric tree via neighbor-joining (Gr(2,n) isomorphism)
	tree := neighborJoining(dist, len(elements))

	// Step 5: Cut tree into zones
	zones := cutTree(tree, elements, minZ, maxZ)
	if len(zones) == 0 {
		return "", fmt.Errorf("no zones produced from %d elements", len(elements))
	}

	// Step 5b: H^0 cohomology folding — merge zones with identical fibers.
	// Duplicate DOM subtrees (e.g. mobile + desktop nav bars) produce
	// separate zones that have the same text/tag signature and overlap
	// spatially. Folding them respects the sheaf consistency condition:
	// sections that agree on overlaps (within tolerance) are identified.
	cohTol := tc.CohomologyTolerance
	if cohTol <= 0 {
		cohTol = 0.7
	}
	if cohTol < 1.0 {
		folded := foldCoherentZones(zones, elements, cohTol)
		if len(folded) >= minZ {
			zones = folded
		}
		// If folding would drop below minZones, keep the pre-fold zones.
	}

	// Step 6: Build mounts
	layout := layoutThresholds{
		headerMaxY: tc.HeaderMaxY,
		footerMinY: tc.FooterMinY,
		sidebarW:   tc.SidebarW,
	}
	if layout.headerMaxY <= 0 {
		layout.headerMaxY = 0.15
	}
	if layout.footerMinY <= 0 {
		layout.footerMinY = 0.85
	}
	if layout.sidebarW <= 0 {
		layout.sidebarW = 0.2
	}
	mounts := buildMounts(zones, elements, layout)

	// Step 7: Marshal
	output := struct {
		Mounts []tropicalMount `json:"mounts"`
	}{Mounts: mounts}

	data, err := json.Marshal(output)
	if err != nil {
		return "", fmt.Errorf("marshal schema: %w", err)
	}

	log.Printf("TropicalCartographer: %d zones from %d elements", len(mounts), len(elements))
	return string(data), nil
}

// ---------------------------------------------------------------------------
// Pre-filtering for large DOMs
// ---------------------------------------------------------------------------

// structuralTags are HTML container elements that define page topology.
// These must survive prefiltering even when they lack text or color,
// because the NJ tree needs them to produce meaningful zone hierarchy.
var structuralTags = map[string]bool{
	"body": true, "main": true, "nav": true, "header": true,
	"footer": true, "section": true, "article": true, "aside": true,
	"form": true,
}

// prefilterElements reduces element count for the O(N^3) NJ algorithm.
// Priority: structural containers > text > color > rest.
// Structural containers get a reserved budget so they are never dropped.
func prefilterElements(elements []element, maxN int) []element {
	if len(elements) <= maxN {
		return elements
	}

	var structural, withText, withColor, rest []element
	for _, el := range elements {
		switch {
		case structuralTags[el.tag]:
			structural = append(structural, el)
		case el.text != "":
			withText = append(withText, el)
		case el.color != "":
			withColor = append(withColor, el)
		default:
			rest = append(rest, el)
		}
	}

	// Structural containers always go first (capped at 30 to leave room).
	structCap := 30
	if len(structural) < structCap {
		structCap = len(structural)
	}
	result := structural[:structCap]

	// Fill remaining budget with text > color > rest.
	remaining := maxN - len(result)
	for _, bucket := range [][]element{withText, withColor, rest} {
		if remaining <= 0 {
			break
		}
		take := len(bucket)
		if take > remaining {
			take = remaining
		}
		result = append(result, bucket[:take]...)
		remaining -= take
	}

	return result
}

// ---------------------------------------------------------------------------
// Parsing
// ---------------------------------------------------------------------------

func parseElements(summary string) []element {
	var elements []element
	for _, line := range strings.Split(summary, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ID: ") {
			continue
		}

		el := element{parentID: "none"}
		for _, seg := range strings.Split(line, " | ") {
			seg = strings.TrimSpace(seg)
			if v, ok := strings.CutPrefix(seg, "ID: "); ok {
				el.id = v
			} else if v, ok := strings.CutPrefix(seg, "Parent: "); ok {
				el.parentID = v
			} else if v, ok := strings.CutPrefix(seg, "Tag: "); ok {
				el.tag = v
			} else if v, ok := strings.CutPrefix(seg, "Color: "); ok {
				el.color = v
			} else if v, ok := strings.CutPrefix(seg, "Bounds: "); ok {
				if b, ok := parseBounds(v); ok {
					el.bounds = b
					el.hasBounds = true
					el.centerX = b[0] + b[2]/2
					el.centerY = b[1] + b[3]/2
				}
			} else if v, ok := strings.CutPrefix(seg, "Path: "); ok {
				el.path = v
				el.pathParts = strings.Split(v, " > ")
			} else if v, ok := strings.CutPrefix(seg, "Text: "); ok {
				el.text = strings.Trim(v, "\"")
			} else if v, ok := strings.CutPrefix(seg, "FontSize: "); ok {
				if fs, err := strconv.ParseFloat(v, 64); err == nil {
					el.fontSize = fs
					el.hasSemantic = true
				}
			} else if v, ok := strings.CutPrefix(seg, "Display: "); ok {
				el.display = v
				el.hasSemantic = true
			} else if v, ok := strings.CutPrefix(seg, "Interactive: "); ok {
				el.interactive = v == "true"
				el.hasSemantic = true
			} else if v, ok := strings.CutPrefix(seg, "TextDensity: "); ok {
				if td, err := strconv.ParseFloat(v, 64); err == nil {
					el.textDensity = td
					el.hasSemantic = true
				}
			} else if v, ok := strings.CutPrefix(seg, "ZIndex: "); ok {
				el.zIndex = v
			} else if v, ok := strings.CutPrefix(seg, "Opacity: "); ok {
				if f, err := strconv.ParseFloat(v, 64); err == nil {
					el.opacity = f
				}
			} else if v, ok := strings.CutPrefix(seg, "PaintOrder: "); ok {
				if n, err := strconv.Atoi(v); err == nil {
					el.paintOrder = n
					el.hasPaint = true
				}
			} else if v, ok := strings.CutPrefix(seg, "StackingRoot: "); ok {
				el.stackingRoot = v == "true"
			}
		}

		if el.id != "" {
			// Classify element source from ID prefix.
			switch {
			case strings.HasPrefix(el.id, "cv-"):
				el.source = "cv"
			case strings.HasPrefix(el.id, "ax-"):
				el.source = "ax"
			default:
				el.source = "dom"
			}
			elements = append(elements, el)
		}
	}

	// For old format without bounds: distribute Y positions sequentially.
	anyBounds := false
	for _, el := range elements {
		if el.hasBounds {
			anyBounds = true
			break
		}
	}
	if !anyBounds && len(elements) > 1 {
		for i := range elements {
			elements[i].centerX = 0.5
			elements[i].centerY = float64(i) / float64(len(elements)-1)
		}
	} else if !anyBounds && len(elements) == 1 {
		elements[0].centerX = 0.5
		elements[0].centerY = 0.5
	}

	return elements
}

func parseBounds(s string) ([4]float64, bool) {
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return [4]float64{}, false
	}
	var b [4]float64
	for i, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return [4]float64{}, false
		}
		b[i] = v
	}
	return b, true
}

// ---------------------------------------------------------------------------
// RGB fiber sampling from screenshot (JPEG or PNG)
// ---------------------------------------------------------------------------

func sampleRGB(screenshot []byte, elements []element) {
	img, _, err := image.Decode(bytes.NewReader(screenshot))
	if err != nil {
		log.Printf("TropicalCartographer: image decode failed: %v", err)
		return
	}

	bounds := img.Bounds()
	imgW := float64(bounds.Dx())
	imgH := float64(bounds.Dy())

	for i := range elements {
		el := &elements[i]
		if !el.hasBounds {
			continue
		}

		// Map normalized bounds to pixel coordinates
		px := int(el.bounds[0]*imgW) + bounds.Min.X
		py := int(el.bounds[1]*imgH) + bounds.Min.Y
		pw := int(el.bounds[2] * imgW)
		ph := int(el.bounds[3] * imgH)
		if pw <= 0 || ph <= 0 {
			continue
		}

		// Sample center 3x3 pixel region
		cx := px + pw/2
		cy := py + ph/2
		var rSum, gSum, bSum, count float64
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				sx := clampInt(cx+dx, bounds.Min.X, bounds.Max.X-1)
				sy := clampInt(cy+dy, bounds.Min.Y, bounds.Max.Y-1)
				r, g, b, _ := img.At(sx, sy).RGBA()
				rSum += float64(r >> 8)
				gSum += float64(g >> 8)
				bSum += float64(b >> 8)
				count++
			}
		}
		el.rgb = [3]float64{rSum / count, gSum / count, bSum / count}
		el.hasRGB = true
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// sampleFFT runs FFT analysis on cv-* regions to extract repeating visual
// patterns (grids, lists, table rows). Only processes elements with the
// "cv-" prefix since DOM elements already have structural data.
func sampleFFT(screenshot []byte, elements []element) {
	// Check if any cv-* elements exist to avoid unnecessary JPEG decode.
	hasCv := false
	for i := range elements {
		if strings.HasPrefix(elements[i].id, "cv-") && elements[i].hasBounds {
			hasCv = true
			break
		}
	}
	if !hasCv {
		return
	}

	img, _, err := image.Decode(bytes.NewReader(screenshot))
	if err != nil {
		return
	}

	bounds := img.Bounds()
	imgW := float64(bounds.Dx())
	imgH := float64(bounds.Dy())

	for i := range elements {
		el := &elements[i]
		if !strings.HasPrefix(el.id, "cv-") || !el.hasBounds {
			continue
		}

		// Map normalized bounds to pixel coordinates.
		px := int(el.bounds[0]*imgW) + bounds.Min.X
		py := int(el.bounds[1]*imgH) + bounds.Min.Y
		pw := int(el.bounds[2] * imgW)
		ph := int(el.bounds[3] * imgH)
		if pw < 8 || ph < 8 {
			continue
		}

		// Extract grayscale subimage (ITU-R BT.601).
		gray := make([]float64, pw*ph)
		for y := 0; y < ph; y++ {
			for x := 0; x < pw; x++ {
				sx := clampInt(px+x, bounds.Min.X, bounds.Max.X-1)
				sy := clampInt(py+y, bounds.Min.Y, bounds.Max.Y-1)
				r, g, b, _ := img.At(sx, sy).RGBA()
				gray[y*pw+x] = 0.299*float64(r>>8) + 0.587*float64(g>>8) + 0.114*float64(b>>8)
			}
		}

		el.fft = AnalyzeRegion(gray, pw, ph)
		el.hasFFT = true
	}
}

// ---------------------------------------------------------------------------
// Tropical distance computation (max-plus semiring)
// ---------------------------------------------------------------------------

func buildDistanceMatrix(elements []element) [][]float64 {
	n := len(elements)
	dist := make([][]float64, n)
	for i := range dist {
		dist[i] = make([]float64, n)
	}
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			d := tropicalDistance(&elements[i], &elements[j])
			dist[i][j] = d
			dist[j][i] = d
		}
	}
	return dist
}

// tropicalDistance: d(i,j) = max(d_spatial, d_visual, d_structural, d_semantic, d_frequency)
// In the max-plus semiring, tropical addition is max. When ANY dimension
// shows strong separation, the elements belong in different zones.
func tropicalDistance(a, b *element) float64 {
	ds := spatialDistance(a, b)
	dv := visualDistance(a, b)
	dt := structuralDistance(a, b)
	dm := semanticDistance(a, b)
	df := frequencyDistance(a, b)
	return math.Max(ds, math.Max(dv, math.Max(dt, math.Max(dm, df))))
}

// spatialDistance: normalized Euclidean distance of bbox centers.
func spatialDistance(a, b *element) float64 {
	if !a.hasBounds && !b.hasBounds {
		// Old format: use sequential Y positions assigned by parseElements.
		dy := a.centerY - b.centerY
		dx := a.centerX - b.centerX
		return math.Sqrt(dx*dx+dy*dy) / math.Sqrt(2)
	}
	dx := a.centerX - b.centerX
	dy := a.centerY - b.centerY
	return math.Sqrt(dx*dx+dy*dy) / math.Sqrt(2) // normalize: max diagonal = sqrt(2)
}

// visualDistance: normalized RGB L2 distance.
func visualDistance(a, b *element) float64 {
	if !a.hasRGB || !b.hasRGB {
		return 0.5 // neutral when no pixel data
	}
	dr := a.rgb[0] - b.rgb[0]
	dg := a.rgb[1] - b.rgb[1]
	db := a.rgb[2] - b.rgb[2]
	maxDist := math.Sqrt(3 * 255 * 255) // ~441.67
	return math.Sqrt(dr*dr+dg*dg+db*db) / maxDist
}

// structuralDistance: CSS path divergence.
func structuralDistance(a, b *element) float64 {
	if len(a.pathParts) == 0 && len(b.pathParts) == 0 {
		// Old format fallback: tag-based heuristic
		if a.tag == b.tag {
			return 0.2
		}
		return 0.8
	}
	if len(a.pathParts) == 0 || len(b.pathParts) == 0 {
		return 0.6
	}

	maxLen := len(a.pathParts)
	if len(b.pathParts) > maxLen {
		maxLen = len(b.pathParts)
	}
	common := 0
	minLen := len(a.pathParts)
	if len(b.pathParts) < minLen {
		minLen = len(b.pathParts)
	}
	for i := 0; i < minLen; i++ {
		if a.pathParts[i] == b.pathParts[i] {
			common++
		} else {
			break
		}
	}
	return 1.0 - float64(common)/float64(maxLen)
}

// semanticDistance: fiber distance from computed styles.
// Measures divergence in font size, display type, interactivity, and text density.
// Returns 0.5 (neutral) when semantic data is unavailable.
func semanticDistance(a, b *element) float64 {
	if !a.hasSemantic || !b.hasSemantic {
		return 0 // don't penalize when data unavailable
	}

	// Font size ratio: |log(fs_a/fs_b)| / log(maxRatio), capped at 1.0.
	// Elements with very different font sizes (heading vs body) are far apart.
	var fontDist float64
	if a.fontSize > 0 && b.fontSize > 0 {
		ratio := a.fontSize / b.fontSize
		if ratio < 1 {
			ratio = 1 / ratio
		}
		// log(4) ≈ 1.39 — a 4x size difference maps to 1.0
		fontDist = math.Min(1.0, math.Log(ratio)/math.Log(4))
	}

	// Display compatibility: same → 0, different → 0.5.
	var displayDist float64
	if a.display != b.display {
		displayDist = 0.5
	}

	// Interactivity divergence: both interactive or both not → 0, mixed → 0.4.
	var interactiveDist float64
	if a.interactive != b.interactive {
		interactiveDist = 0.4
	}

	// Text density difference (already [0,1]).
	textDensityDist := math.Abs(a.textDensity - b.textDensity)

	// Inner tropical max across semantic sub-fibers.
	return math.Max(fontDist, math.Max(displayDist, math.Max(interactiveDist, textDensityDist)))
}

// frequencyDistance: FFT-based visual structure divergence for cv-* regions.
// Elements with similar repeating patterns (same row spacing, similar entropy)
// are structurally similar. Returns 0 when FFT data is unavailable.
func frequencyDistance(a, b *element) float64 {
	if !a.hasFFT || !b.hasFFT {
		return 0 // don't penalize when no FFT data
	}

	// If neither region has meaningful structure (no frequencies, no grid),
	// they're both unstructured — treat as equivalent to keep them together.
	aEmpty := a.fft.DominantFreqX == 0 && a.fft.DominantFreqY == 0 && a.fft.GridScore == 0
	bEmpty := b.fft.DominantFreqX == 0 && b.fft.DominantFreqY == 0 && b.fft.GridScore == 0
	if aEmpty && bEmpty {
		return 0
	}

	// Dominant vertical frequency divergence (row spacing).
	maxFreq := math.Max(a.fft.DominantFreqY, b.fft.DominantFreqY)
	var freqYDist float64
	if maxFreq > 0 {
		freqYDist = math.Abs(a.fft.DominantFreqY-b.fft.DominantFreqY) / maxFreq
	}

	// Dominant horizontal frequency divergence (column spacing).
	maxFreqX := math.Max(a.fft.DominantFreqX, b.fft.DominantFreqX)
	var freqXDist float64
	if maxFreqX > 0 {
		freqXDist = math.Abs(a.fft.DominantFreqX-b.fft.DominantFreqX) / maxFreqX
	}

	// Spectral entropy divergence.
	entropyDist := math.Abs(a.fft.Entropy - b.fft.Entropy)

	// Grid score divergence.
	gridDist := math.Abs(a.fft.GridScore - b.fft.GridScore)

	return math.Max(freqYDist, math.Max(freqXDist, math.Max(entropyDist, gridDist)))
}

// ---------------------------------------------------------------------------
// Neighbor-joining tree construction
// Realizes the Gr(2,n) ≅ metric trees isomorphism (Speyer-Sturmfels 2004)
// ---------------------------------------------------------------------------

func neighborJoining(dist [][]float64, n int) *treeNode {
	if n <= 0 {
		return &treeNode{elements: []int{}}
	}
	if n == 1 {
		return &treeNode{isLeaf: true, elements: []int{0}}
	}
	if n == 2 {
		root := &treeNode{}
		left := &treeNode{isLeaf: true, elements: []int{0}, dist: dist[0][1] / 2}
		right := &treeNode{isLeaf: true, elements: []int{1}, dist: dist[0][1] / 2}
		root.children = []*treeNode{left, right}
		root.elements = []int{0, 1}
		return root
	}

	// Active indices and nodes
	active := make([]int, n)
	for i := range active {
		active[i] = i
	}

	nodes := make([]*treeNode, n)
	for i := 0; i < n; i++ {
		nodes[i] = &treeNode{isLeaf: true, elements: []int{i}}
	}

	// Working distance matrix
	D := make([][]float64, n)
	for i := range D {
		D[i] = make([]float64, n)
		copy(D[i], dist[i])
	}

	for len(active) > 2 {
		m := len(active)

		// Row sums
		rowSum := make([]float64, m)
		for i := 0; i < m; i++ {
			for j := 0; j < m; j++ {
				rowSum[i] += D[active[i]][active[j]]
			}
		}

		// Find minimum Q pair
		bestQ := math.Inf(1)
		bestI, bestJ := 0, 1
		for i := 0; i < m; i++ {
			for j := i + 1; j < m; j++ {
				q := float64(m-2)*D[active[i]][active[j]] - rowSum[i] - rowSum[j]
				if q < bestQ {
					bestQ = q
					bestI, bestJ = i, j
				}
			}
		}

		ai, aj := active[bestI], active[bestJ]
		dij := D[ai][aj]

		// Branch lengths
		var diU, djU float64
		if m > 2 {
			diU = dij/2 + (rowSum[bestI]-rowSum[bestJ])/(2*float64(m-2))
			djU = dij - diU
		} else {
			diU = dij / 2
			djU = dij / 2
		}
		diU = math.Max(diU, 0)
		djU = math.Max(djU, 0)

		// Create internal node
		u := &treeNode{}
		nodes[ai].dist = diU
		nodes[aj].dist = djU
		u.children = []*treeNode{nodes[ai], nodes[aj]}
		u.elements = append(append([]int{}, nodes[ai].elements...), nodes[aj].elements...)

		// Update distances: d(u,k) = (d(i,k) + d(j,k) - d(i,j)) / 2
		for _, k := range active {
			if k == ai || k == aj {
				continue
			}
			nd := (D[ai][k] + D[aj][k] - dij) / 2
			nd = math.Max(nd, 0)
			D[ai][k] = nd
			D[k][ai] = nd
		}
		D[ai][ai] = 0
		nodes[ai] = u

		// Remove aj from active (copy to avoid aliasing the backing array)
		newActive := make([]int, 0, len(active)-1)
		for k, v := range active {
			if k != bestJ {
				newActive = append(newActive, v)
			}
		}
		active = newActive
	}

	// Join final two
	root := &treeNode{}
	a0, a1 := active[0], active[1]
	d01 := D[a0][a1]
	nodes[a0].dist = d01 / 2
	nodes[a1].dist = d01 / 2
	root.children = []*treeNode{nodes[a0], nodes[a1]}
	root.elements = append(append([]int{}, nodes[a0].elements...), nodes[a1].elements...)
	return root
}

// ---------------------------------------------------------------------------
// Tree cutting → zones
// ---------------------------------------------------------------------------

type internalEdge struct {
	parent *treeNode
	child  *treeNode
	dist   float64
}

func cutTree(root *treeNode, elements []element, minZones, maxZones int) []zone {
	if len(elements) == 0 {
		return nil
	}
	if len(elements) <= maxZones {
		// Few enough elements to make each its own zone
		return singleElementZones(elements)
	}

	// Collect all edges
	var edges []internalEdge
	var walk func(parent, n *treeNode)
	walk = func(parent, n *treeNode) {
		if parent != nil {
			edges = append(edges, internalEdge{parent: parent, child: n, dist: n.dist})
		}
		for _, c := range n.children {
			walk(n, c)
		}
	}
	walk(nil, root)

	// Sort by distance descending (longest first)
	sort.Slice(edges, func(i, j int) bool {
		return edges[i].dist > edges[j].dist
	})

	// Greedy cutting
	cutSet := map[*treeNode]bool{}
	numZones := 1

	for _, e := range edges {
		if numZones >= maxZones {
			break
		}
		if len(e.child.elements) > 0 && len(e.child.elements) < len(elements) {
			cutSet[e.child] = true
			numZones++
		}
	}

	// Extract zones from the cut tree
	zones := extractZones(root, cutSet, elements)

	// If we have too few zones, split the largest zone at its widest spatial gap.
	for len(zones) < minZones && len(zones) > 0 {
		largest := 0
		for i, z := range zones {
			if len(z.elems) > len(zones[largest].elems) {
				largest = i
			}
		}
		if len(zones[largest].elems) <= 2 {
			break
		}
		z1, z2 := splitZoneBySpatialGap(zones[largest], elements)
		zones = append(zones[:largest], append([]zone{z1, z2}, zones[largest+1:]...)...)
	}

	// Compute zone centers and detect list zones
	for i := range zones {
		computeZoneFeatures(&zones[i], elements)
	}

	return zones
}

func extractZones(root *treeNode, cutSet map[*treeNode]bool, elements []element) []zone {
	var zones []zone

	var collect func(n *treeNode) []int
	collect = func(n *treeNode) []int {
		if cutSet[n] {
			// This subtree is a separate zone
			var elems []int
			var gatherAll func(node *treeNode)
			gatherAll = func(node *treeNode) {
				if node.isLeaf {
					elems = append(elems, node.elements...)
					return
				}
				for _, c := range node.children {
					gatherAll(c)
				}
			}
			gatherAll(n)
			if len(elems) > 0 {
				zones = append(zones, zone{elems: elems})
			}
			return nil
		}

		if n.isLeaf {
			return n.elements
		}

		var remaining []int
		for _, c := range n.children {
			remaining = append(remaining, collect(c)...)
		}
		return remaining
	}

	// Root zone is everything not in a cut subtree
	rootElems := collect(root)
	if len(rootElems) > 0 {
		zones = append(zones, zone{elems: rootElems})
	}

	return zones
}

// splitZoneBySpatialGap splits a zone into two by finding the largest gap
// along whichever axis (X or Y) has greater spread, then cutting there.
func splitZoneBySpatialGap(z zone, elements []element) (zone, zone) {
	// Determine dominant axis by range
	var minX, maxX, minY, maxY float64
	minX, minY = math.Inf(1), math.Inf(1)
	maxX, maxY = math.Inf(-1), math.Inf(-1)
	for _, idx := range z.elems {
		el := elements[idx]
		if el.centerX < minX {
			minX = el.centerX
		}
		if el.centerX > maxX {
			maxX = el.centerX
		}
		if el.centerY < minY {
			minY = el.centerY
		}
		if el.centerY > maxY {
			maxY = el.centerY
		}
	}

	// Sort by the dominant axis and find the largest gap
	useY := (maxY - minY) >= (maxX - minX)
	sorted := make([]int, len(z.elems))
	copy(sorted, z.elems)
	sort.Slice(sorted, func(i, j int) bool {
		if useY {
			return elements[sorted[i]].centerY < elements[sorted[j]].centerY
		}
		return elements[sorted[i]].centerX < elements[sorted[j]].centerX
	})

	bestGap := -1.0
	bestSplit := len(sorted) / 2 // fallback to midpoint
	for i := 0; i < len(sorted)-1; i++ {
		var gap float64
		if useY {
			gap = elements[sorted[i+1]].centerY - elements[sorted[i]].centerY
		} else {
			gap = elements[sorted[i+1]].centerX - elements[sorted[i]].centerX
		}
		if gap > bestGap {
			bestGap = gap
			bestSplit = i + 1
		}
	}

	return zone{elems: sorted[:bestSplit]}, zone{elems: sorted[bestSplit:]}
}

func singleElementZones(elements []element) []zone {
	zones := make([]zone, len(elements))
	for i := range elements {
		zones[i] = zone{elems: []int{i}, rootIdx: i, centerX: elements[i].centerX, centerY: elements[i].centerY}
	}
	return zones
}

func computeZoneFeatures(z *zone, elements []element) {
	if len(z.elems) == 0 {
		return
	}

	// Compute center
	var sx, sy float64
	for _, idx := range z.elems {
		sx += elements[idx].centerX
		sy += elements[idx].centerY
	}
	z.centerX = sx / float64(len(z.elems))
	z.centerY = sy / float64(len(z.elems))

	// Find representative element (nearest to center)
	bestDist := math.Inf(1)
	for _, idx := range z.elems {
		dx := elements[idx].centerX - z.centerX
		dy := elements[idx].centerY - z.centerY
		d := dx*dx + dy*dy
		if d < bestDist {
			bestDist = d
			z.rootIdx = idx
		}
	}
	// Prefer a structural tag element over centroid-nearest, so
	// inferCategory sees the semantic signal (nav, aside, etc.).
	for _, idx := range z.elems {
		if structuralTags[elements[idx].tag] {
			z.rootIdx = idx
			break
		}
	}

	// Detect list zones
	z.isList, z.listIdxs, z.selector = detectListZone(z.elems, elements)
}

// detectListZone checks for repeating structural patterns.
func detectListZone(elems []int, elements []element) (bool, []int, string) {
	if len(elems) < 3 {
		return false, nil, ""
	}

	// Strategy 1: group by CSS path prefix + tag
	type pathGroup struct {
		prefix  string
		tag     string
		indices []int
	}
	groups := map[string]*pathGroup{}

	for _, idx := range elems {
		el := elements[idx]
		if len(el.pathParts) >= 2 {
			parentPath := strings.Join(el.pathParts[:len(el.pathParts)-1], " > ")
			key := parentPath + "|" + el.tag
			if g, ok := groups[key]; ok {
				g.indices = append(g.indices, idx)
			} else {
				groups[key] = &pathGroup{prefix: parentPath, tag: el.tag, indices: []int{idx}}
			}
		}
	}

	var best *pathGroup
	for _, g := range groups {
		if len(g.indices) >= 3 && (best == nil || len(g.indices) > len(best.indices)) {
			best = g
		}
	}

	// Strategy 2 fallback: same tag repeated
	if best == nil {
		tagGroups := map[string][]int{}
		for _, idx := range elems {
			t := elements[idx].tag
			tagGroups[t] = append(tagGroups[t], idx)
		}
		for tag, indices := range tagGroups {
			if len(indices) >= 3 && (best == nil || len(indices) > len(best.indices)) {
				best = &pathGroup{tag: tag, indices: indices}
			}
		}
	}

	if best == nil {
		return false, nil, ""
	}

	// Build CSS selector
	selector := ""
	if best.prefix != "" && len(best.indices) > 0 {
		el := elements[best.indices[0]]
		if len(el.pathParts) > 0 {
			selector = best.prefix + " > " + el.pathParts[len(el.pathParts)-1]
		}
	}

	// Filter to items with non-empty text
	var filtered []int
	for _, idx := range best.indices {
		if elements[idx].text != "" {
			filtered = append(filtered, idx)
		}
	}
	if len(filtered) < 3 {
		filtered = best.indices
	}

	return true, filtered, selector
}

// ---------------------------------------------------------------------------
// H^0 Cohomology folding
// ---------------------------------------------------------------------------

// foldCoherentZones merges zones whose fiber signatures (text content and
// tag distribution) are similar within tolerance. This collapses duplicate
// DOM subtrees — e.g. mobile/desktop nav bars that produce identical link
// text but live in different parts of the tree.
//
// Mathematically this computes H^0 of the Čech complex: zones are open sets
// in the cover, their text/tag distributions are local sections, and we
// identify sections that agree on overlaps (spatial proximity) within the
// given tolerance.
// fiberSig is the local section data over a zone — its "fiber" in the sheaf.
// Text content and tag distribution form the observable section; two zones
// with similar fiberSigs agree on overlaps and can be identified in H^0.
type fiberSig struct {
	tagDist  map[string]float64 // normalized tag frequency
	textSet  map[string]bool    // set of non-empty text values
	textHash uint64             // cheap hash for fast comparison
	nText    int
}

func foldCoherentZones(zones []zone, elements []element, tolerance float64) []zone {
	if len(zones) <= 1 {
		return zones
	}

	sigs := make([]fiberSig, len(zones))
	for i, z := range zones {
		tags := map[string]int{}
		texts := map[string]bool{}
		var h uint64
		for _, idx := range z.elems {
			el := elements[idx]
			tags[el.tag]++
			if el.text != "" {
				// Use first 40 chars to avoid long-text noise.
				key := el.text
				if len(key) > 40 {
					key = key[:40]
				}
				texts[key] = true
				// FNV-like hash accumulation for fast equality check.
				for _, b := range key {
					h ^= uint64(b)
					h *= 0x100000001b3
				}
			}
		}
		// Normalize tag distribution.
		tagDist := map[string]float64{}
		n := float64(len(z.elems))
		if n > 0 {
			for t, c := range tags {
				tagDist[t] = float64(c) / n
			}
		}
		sigs[i] = fiberSig{
			tagDist:  tagDist,
			textSet:  texts,
			textHash: h,
			nText:    len(texts),
		}
	}

	// Union-find for merging.
	parent := make([]int, len(zones))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[rb] = ra
		}
	}

	// Compare all zone pairs.
	for i := 0; i < len(zones); i++ {
		for j := i + 1; j < len(zones); j++ {
			if find(i) == find(j) {
				continue
			}
			sim := zoneFiberSimilarity(sigs[i], sigs[j], zones[i], zones[j])
			if sim >= tolerance {
				union(i, j)
			}
		}
	}

	// Collect merged zones.
	merged := map[int]*zone{}
	for i, z := range zones {
		root := find(i)
		if m, ok := merged[root]; ok {
			m.elems = append(m.elems, z.elems...)
		} else {
			cp := zone{elems: make([]int, len(z.elems))}
			copy(cp.elems, z.elems)
			merged[root] = &cp
		}
	}

	result := make([]zone, 0, len(merged))
	for _, z := range merged {
		computeZoneFeatures(z, elements)
		result = append(result, *z)
	}

	if len(result) < len(zones) {
		log.Printf("TropicalCartographer: H^0 folding merged %d → %d zones (tol=%.2f)",
			len(zones), len(result), tolerance)
	}

	return result
}

// zoneFiberSimilarity computes the similarity between two zones' fiber
// signatures. Returns a value in [0, 1] where 1 = identical fibers.
//
// Components:
//   - textSim: Jaccard similarity of text content sets
//   - tagSim: 1 - L1 distance of normalized tag distributions
//   - spatialSim: overlap of spatial bounding boxes (zones in the same
//     screen region are candidates for folding)
func zoneFiberSimilarity(a, b fiberSig, za, zb zone) float64 {
	// Fast path: if neither zone has text, compare tags only.
	if a.nText == 0 && b.nText == 0 {
		return tagDistSimilarity(a.tagDist, b.tagDist)
	}

	// Text Jaccard similarity.
	var textSim float64
	if a.nText > 0 || b.nText > 0 {
		inter := 0
		for t := range a.textSet {
			if b.textSet[t] {
				inter++
			}
		}
		union := len(a.textSet) + len(b.textSet) - inter
		if union > 0 {
			textSim = float64(inter) / float64(union)
		}
	}

	// Tag distribution similarity (1 - L1/2).
	tagSim := tagDistSimilarity(a.tagDist, b.tagDist)

	// Spatial proximity: do the zones occupy a similar screen region?
	// Use distance between zone centers (normalized to [0, 1]).
	dx := za.centerX - zb.centerX
	dy := za.centerY - zb.centerY
	dist := math.Sqrt(dx*dx + dy*dy)
	spatialSim := math.Max(0, 1-dist/0.5) // 0 if centers > 0.5 apart

	// Weighted combination.
	//
	// Key insight: duplicate DOM subtrees (mobile/desktop nav bars) get
	// interleaved by NJ because their elements have identical text. After
	// tree cutting, the resulting zones contain different subsets of the
	// same links, so text Jaccard is low. But they share the same tag
	// distribution (all <a> tags) and the same spatial band (both at Y≈0).
	// When structure and position strongly agree, that's sufficient evidence
	// for folding — the sections are "cohomologically equivalent" even if
	// their text fibers differ.
	if tagSim > 0.85 && spatialSim > 0.7 {
		// Structurally equivalent zones in the same region: boost.
		return 0.2*textSim + 0.3*tagSim + 0.5*spatialSim
	}
	if a.nText > 0 && b.nText > 0 {
		return 0.5*textSim + 0.2*tagSim + 0.3*spatialSim
	}
	return 0.4*tagSim + 0.6*spatialSim
}

func tagDistSimilarity(a, b map[string]float64) float64 {
	allTags := map[string]bool{}
	for t := range a {
		allTags[t] = true
	}
	for t := range b {
		allTags[t] = true
	}
	if len(allTags) == 0 {
		return 1.0
	}
	var l1 float64
	for t := range allTags {
		l1 += math.Abs(a[t] - b[t])
	}
	return math.Max(0, 1-l1/2)
}

// ---------------------------------------------------------------------------
// Mount generation
// ---------------------------------------------------------------------------

type layoutThresholds struct {
	headerMaxY float64
	footerMinY float64
	sidebarW   float64
}

// computeZoneBounds returns the axis-aligned bounding box of all elements in
// the zone. This defines the spatial "open set" for the sheaf section.
func computeZoneBounds(z zone, elements []element) [4]float64 {
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, idx := range z.elems {
		el := elements[idx]
		if !el.hasBounds {
			continue
		}
		x0, y0 := el.bounds[0], el.bounds[1]
		x1, y1 := x0+el.bounds[2], y0+el.bounds[3]
		if x0 < minX {
			minX = x0
		}
		if y0 < minY {
			minY = y0
		}
		if x1 > maxX {
			maxX = x1
		}
		if y1 > maxY {
			maxY = y1
		}
	}
	if math.IsInf(minX, 1) {
		return [4]float64{0, 0, 1, 1}
	}
	return [4]float64{minX, minY, maxX - minX, maxY - minY}
}

// computeZoneFingerprint produces a content hash for cache staleness detection.
// Uses (tag, text[:30]) pairs — NOT mache_id — because mache-IDs are temporal
// (idCounter resets per page load and element order can shift). Tag+text is
// reload-stable: identical DOM produces identical fingerprint.
func computeZoneFingerprint(z zone, elements []element) string {
	type pair struct{ tag, text string }
	pairs := make([]pair, 0, len(z.elems))
	for _, idx := range z.elems {
		el := elements[idx]
		text := el.text
		if len(text) > 30 {
			text = text[:30]
		}
		pairs = append(pairs, pair{el.tag, text})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].tag != pairs[j].tag {
			return pairs[i].tag < pairs[j].tag
		}
		return pairs[i].text < pairs[j].text
	})
	h := sha256.New()
	for _, p := range pairs {
		h.Write([]byte(p.tag))
		h.Write([]byte{0})
		h.Write([]byte(p.text))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// computeStructuralFingerprint hashes the tag-shape of a zone (tag names +
// interactive flag), ignoring text content. Two zones with the same DOM
// structure but different link labels/post titles produce the same hash.
// This enables NavSection transfer across pages on the same site.
func computeStructuralFingerprint(z zone, elements []element) string {
	// Count (tag, interactive) pairs — order-independent.
	type key struct {
		tag         string
		interactive bool
	}
	counts := map[key]int{}
	for _, idx := range z.elems {
		el := elements[idx]
		counts[key{el.tag, el.interactive}]++
	}
	// Sort keys for determinism.
	type kv struct {
		k key
		n int
	}
	sorted := make([]kv, 0, len(counts))
	for k, n := range counts {
		sorted = append(sorted, kv{k, n})
	}
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].k.tag != sorted[j].k.tag {
			return sorted[i].k.tag < sorted[j].k.tag
		}
		return sorted[i].k.interactive && !sorted[j].k.interactive
	})
	h := sha256.New()
	for _, s := range sorted {
		_, _ = fmt.Fprintf(h, "%s:%v:%d\x00", s.k.tag, s.k.interactive, s.n)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func buildMounts(zones []zone, elements []element, lt layoutThresholds) []tropicalMount {
	// Sort zones by position: top-to-bottom, then left-to-right
	sort.Slice(zones, func(i, j int) bool {
		if math.Abs(zones[i].centerY-zones[j].centerY) > 0.15 {
			return zones[i].centerY < zones[j].centerY
		}
		return zones[i].centerX < zones[j].centerX
	})

	usedPaths := map[string]bool{}
	mounts := make([]tropicalMount, 0, len(zones))

	for i, z := range zones {
		if len(z.elems) == 0 {
			continue
		}
		// Pick a real DOM element as root — skip synthetic cv-* and ax-*
		// IDs that don't exist in the browser DOM summary. cv-* comes from
		// edge detection, ax-* from macOS Accessibility. Both are valid
		// cartographer elements but can't be used as mache_ids in mounts
		// (ValidateSchema checks against the DOM summary).
		rootEl := elements[z.rootIdx]
		if rootEl.source == "cv" || rootEl.source == "ax" {
			found := false
			for _, idx := range z.elems {
				if elements[idx].source == "dom" {
					rootEl = elements[idx]
					found = true
					break
				}
			}
			if !found {
				continue // all-synthetic zone, skip entirely
			}
		}

		cat := inferCategory(z, elements, lt)
		subcat := inferSubcategory(z, elements, lt)
		vpath := "/" + cat + "/" + subcat
		if usedPaths[vpath] {
			vpath = fmt.Sprintf("%s_%d", vpath, i)
		}
		usedPaths[vpath] = true

		m := tropicalMount{
			VirtualPath:  vpath,
			MacheID:      rootEl.id,
			Description:  inferDescription(z, elements, lt),
			PrimaryItems: []string{},
			Bounds:       computeZoneBounds(z, elements),
			Fingerprint:  computeZoneFingerprint(z, elements),
			StructuralFP: computeStructuralFingerprint(z, elements),
		}

		if z.isList && len(z.listIdxs) > 0 {
			for _, idx := range z.listIdxs {
				if elements[idx].source != "dom" {
					continue
				}
				m.PrimaryItems = append(m.PrimaryItems, elements[idx].id)
			}
			m.ItemSelector = z.selector
		} else {
			// Non-list zones: expose DOM elements as primary items so
			// LoadChildren creates _c/ directories for clicking.
			// Include elements with text OR interactive elements (icon-only
			// buttons like search/hamburger that have no text but are clickable).
			for _, idx := range z.elems {
				el := elements[idx]
				if el.source != "dom" {
					continue
				}
				if el.id == rootEl.id {
					continue // skip zone root itself
				}
				if el.text == "" && !el.interactive {
					continue // skip non-text, non-interactive elements
				}
				m.PrimaryItems = append(m.PrimaryItems, el.id)
			}
		}

		mounts = append(mounts, m)
	}

	return mounts
}

func inferCategory(z zone, elements []element, lt layoutThresholds) string {
	// Prefer semantic signal from the zone's structural ancestor tag.
	if z.rootIdx >= 0 && z.rootIdx < len(elements) {
		switch elements[z.rootIdx].tag {
		case "nav":
			if z.centerY < lt.headerMaxY {
				return "header"
			}
			return "sidebar"
		case "aside":
			return "sidebar"
		case "header":
			return "header"
		case "footer":
			return "footer"
		case "main", "article":
			return "main"
		}
	}
	// Fall back to spatial heuristics.
	// Sidebar before footer: a narrow strip is a stronger signal than Y position.
	if z.centerX < lt.sidebarW || z.centerX > 1-lt.sidebarW {
		return "sidebar"
	}
	if z.centerY < lt.headerMaxY {
		return "header"
	}
	if z.centerY > lt.footerMinY {
		return "footer"
	}
	return "main"
}

func inferSubcategory(z zone, elements []element, lt layoutThresholds) string {
	tagCounts := map[string]int{}
	colorCounts := map[string]int{}
	hasInput := false

	for _, idx := range z.elems {
		el := elements[idx]
		tagCounts[el.tag]++
		if el.color != "" {
			colorCounts[el.color]++
		}
		if el.tag == "input" || el.tag == "textarea" || el.tag == "select" {
			hasInput = true
		}
	}

	if hasInput {
		return "search"
	}
	if z.centerY < lt.headerMaxY && tagCounts["a"]*2 > len(z.elems) {
		return "nav"
	}
	if z.isList {
		return "feed"
	}
	if (colorCounts["ORANGE"]+colorCounts["YELLOW"])*3 > len(z.elems) {
		return "actions"
	}
	if tagCounts["a"]*3 > len(z.elems)*2 {
		return "links"
	}
	return "content"
}

func inferDescription(z zone, elements []element, lt layoutThresholds) string {
	cat := inferCategory(z, elements, lt)
	subcat := inferSubcategory(z, elements, lt)

	n := len(z.elems)
	switch {
	case cat == "header" && subcat == "nav":
		return fmt.Sprintf("Navigation bar with %d links", n)
	case cat == "footer":
		return fmt.Sprintf("Footer section with %d elements", n)
	case subcat == "feed":
		if cat == "header" {
			return fmt.Sprintf("Top content feed with %d items", len(z.listIdxs))
		}
		if cat == "footer" {
			return fmt.Sprintf("Footer feed with %d items", len(z.listIdxs))
		}
		return fmt.Sprintf("Content feed with %d items", len(z.listIdxs))
	case subcat == "search":
		return fmt.Sprintf("Search/input area with %d elements", n)
	case subcat == "actions":
		return fmt.Sprintf("Action buttons with %d elements", n)
	default:
		// Use first non-empty text as hint
		for _, idx := range z.elems {
			if t := elements[idx].text; t != "" {
				if len(t) > 40 {
					t = t[:40] + "..."
				}
				return fmt.Sprintf("Section containing \"%s\" and %d more elements", t, n-1)
			}
		}
		return fmt.Sprintf("Content section with %d elements", n)
	}
}
