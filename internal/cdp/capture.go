package cdp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Default screenshot scaling constants. These are used by tests and serve as
// documentation of the defaults; production code receives values from config.
const (
	CDPTargetWidth = 800   // scaled-down width for Gemini (topology, not pixels)
	CDPMaxHeight   = 16384 // cap for infinite-scroll pages

	// ElementBoxPadding is the pixel padding around a target element in magnifying-glass mode.
	ElementBoxPadding = 50

	// AXNameMaxLen is the maximum AX name length before truncation in enriched summaries.
	AXNameMaxLen = 80

	// DescribeNodeConcurrency is the max in-flight DOM.describeNode calls.
	DescribeNodeConcurrency = 10

	// CompositingReasonsConcurrency is the max in-flight LayerTree.compositingReasons calls.
	CompositingReasonsConcurrency = 10
)

// ScreenshotClip defines the region and scale for Page.captureScreenshot.
type ScreenshotClip struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Scale  float64 `json:"scale"`
}

// BoxModel holds the bounding box of a DOM element (border quad).
type BoxModel struct {
	X float64
	Y float64
	W float64
	H float64
}

// AXInfo holds accessibility data for a single mache element.
type AXInfo struct {
	Role       string
	Name       string
	Properties []string // e.g. ["disabled=true", "checked=true"]
}

// LayerInfo holds layer tree data for a single mache element.
type LayerInfo struct {
	PaintOrder   int
	StackingRoot bool
}

// LayoutMetrics calls Page.getLayoutMetrics and returns CSS content dimensions.
// Height is capped at maxHeight.
func LayoutMetrics(ctx context.Context, p *Proxy, tabID int, maxHeight float64) (width, height float64, err error) {
	result, err := p.Send(ctx, tabID, "Page.getLayoutMetrics", nil)
	if err != nil {
		return 0, 0, err
	}
	var resp struct {
		CSSContentSize struct {
			Width  float64 `json:"width"`
			Height float64 `json:"height"`
		} `json:"cssContentSize"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return 0, 0, fmt.Errorf("LayoutMetrics: unmarshal: %w", err)
	}
	h := math.Min(resp.CSSContentSize.Height, maxHeight)
	return resp.CSSContentSize.Width, h, nil
}

// DocumentRoot calls DOM.getDocument(depth:0) and returns the root node ID.
func DocumentRoot(ctx context.Context, p *Proxy, tabID int) (int, error) {
	result, err := p.Send(ctx, tabID, "DOM.getDocument", map[string]int{"depth": 0})
	if err != nil {
		return 0, err
	}
	var resp struct {
		Root struct {
			NodeID int `json:"nodeId"`
		} `json:"root"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return 0, fmt.Errorf("DocumentRoot: unmarshal: %w", err)
	}
	return resp.Root.NodeID, nil
}

// ElementBoxModel queries the box model for a specific mache-ID element.
// Returns nil if the element is not found or has no box model.
func ElementBoxModel(ctx context.Context, p *Proxy, tabID, rootNodeID int, macheID string) (*BoxModel, error) {
	selector := fmt.Sprintf(`[data-mache-id="%s"]`, macheID)
	qResult, err := p.Send(ctx, tabID, "DOM.querySelector", map[string]any{
		"nodeId":   rootNodeID,
		"selector": selector,
	})
	if err != nil {
		return nil, err
	}
	var qResp struct {
		NodeID int `json:"nodeId"`
	}
	if err := json.Unmarshal(qResult, &qResp); err != nil {
		return nil, fmt.Errorf("ElementBoxModel: querySelector unmarshal: %w", err)
	}
	if qResp.NodeID == 0 {
		return nil, nil // element not found
	}

	bmResult, err := p.Send(ctx, tabID, "DOM.getBoxModel", map[string]any{
		"nodeId": qResp.NodeID,
	})
	if err != nil {
		return nil, err
	}
	var bmResp struct {
		Model struct {
			Border []float64 `json:"border"` // [x1,y1, x2,y1, x2,y2, x1,y2]
		} `json:"model"`
	}
	if err := json.Unmarshal(bmResult, &bmResp); err != nil {
		return nil, fmt.Errorf("ElementBoxModel: getBoxModel unmarshal: %w", err)
	}
	b := bmResp.Model.Border
	if len(b) < 6 {
		return nil, nil
	}
	return &BoxModel{
		X: b[0],
		Y: b[1],
		W: b[2] - b[0],
		H: b[5] - b[1],
	}, nil
}

// BuildClip computes a ScreenshotClip. If box is non-nil, crops to the element
// with 50px padding (magnifying glass mode). Otherwise full-page.
func BuildClip(pageWidth, pageHeight float64, box *BoxModel, targetWidth float64) ScreenshotClip {
	if box != nil {
		cx := math.Max(0, box.X-ElementBoxPadding)
		cy := math.Max(0, box.Y-ElementBoxPadding)
		cw := math.Min(pageWidth-cx, box.W+2*ElementBoxPadding)
		ch := math.Min(pageHeight-cy, box.H+2*ElementBoxPadding)
		scale := math.Min(1, targetWidth/cw)
		return ScreenshotClip{X: cx, Y: cy, Width: cw, Height: ch, Scale: scale}
	}
	scale := math.Min(1, targetWidth/pageWidth)
	return ScreenshotClip{X: 0, Y: 0, Width: pageWidth, Height: pageHeight, Scale: scale}
}

// CaptureScreenshot captures a PNG screenshot with the given clip.
// Returns the base64-encoded PNG data.
func CaptureScreenshot(ctx context.Context, p *Proxy, tabID int, clip ScreenshotClip) (string, error) {
	result, err := p.Send(ctx, tabID, "Page.captureScreenshot", map[string]any{
		"format":                "png",
		"captureBeyondViewport": true,
		"clip":                  clip,
	})
	if err != nil {
		return "", err
	}
	var resp struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return "", fmt.Errorf("CaptureScreenshot: unmarshal: %w", err)
	}
	return resp.Data, nil
}

// PageTextMaxLen caps page text to avoid excessive memory usage.
const PageTextMaxLen = 100_000

// PageText extracts all visible text from the page using Runtime.evaluate.
// Returns the text (truncated to PageTextMaxLen) or empty string on error.
func PageText(ctx context.Context, p *Proxy, tabID int) (string, error) {
	result, err := p.Send(ctx, tabID, "Runtime.evaluate", map[string]any{
		"expression":    "document.body.innerText",
		"returnByValue": true,
	})
	if err != nil {
		return "", fmt.Errorf("PageText: %w", err)
	}
	var resp struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return "", fmt.Errorf("PageText: unmarshal: %w", err)
	}
	text := resp.Result.Value
	if len(text) > PageTextMaxLen {
		text = text[:PageTextMaxLen]
	}
	return text, nil
}

// axNode represents a node from Accessibility.getFullAXTree.
type axNode struct {
	BackendDOMNodeID int `json:"backendDOMNodeId"`
	Role             struct {
		Value string `json:"value"`
	} `json:"role"`
	Name struct {
		Value string `json:"value"`
	} `json:"name"`
	Properties []struct {
		Name  string `json:"name"`
		Value struct {
			Value any `json:"value"`
		} `json:"value"`
	} `json:"properties"`
}

// FullAXTree calls Accessibility.getFullAXTree and returns all AX nodes.
func FullAXTree(ctx context.Context, p *Proxy, tabID int) ([]axNode, error) {
	result, err := p.Send(ctx, tabID, "Accessibility.getFullAXTree", nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Nodes []axNode `json:"nodes"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, fmt.Errorf("FullAXTree: unmarshal: %w", err)
	}
	return resp.Nodes, nil
}

// describeNodeResp is the response from DOM.describeNode.
type describeNodeResp struct {
	Node struct {
		BackendNodeID int      `json:"backendNodeId"`
		Attributes    []string `json:"attributes"`
	} `json:"node"`
}

// MacheBackendMap resolves [data-mache-id] elements to their backend node IDs.
// Uses concurrent DOM.describeNode calls (up to concurrency limit).
func MacheBackendMap(ctx context.Context, p *Proxy, tabID, rootNodeID int) (map[string]int, error) {
	qaResult, err := p.Send(ctx, tabID, "DOM.querySelectorAll", map[string]any{
		"nodeId":   rootNodeID,
		"selector": "[data-mache-id]",
	})
	if err != nil {
		return nil, err
	}
	var qaResp struct {
		NodeIDs []int `json:"nodeIds"`
	}
	if err := json.Unmarshal(qaResult, &qaResp); err != nil {
		return nil, fmt.Errorf("MacheBackendMap: querySelectorAll unmarshal: %w", err)
	}

	result := make(map[string]int)
	var mu sync.Mutex

	sem := make(chan struct{}, DescribeNodeConcurrency)
	var wg sync.WaitGroup

	for _, nid := range qaResp.NodeIDs {
		// Short-circuit: if context is cancelled, stop spawning goroutines.
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return result, ctx.Err()
		}
		wg.Add(1)
		go func(nodeID int) {
			defer wg.Done()
			defer func() { <-sem }()

			descResult, err := p.Send(ctx, tabID, "DOM.describeNode", map[string]any{
				"nodeId": nodeID,
			})
			if err != nil {
				return // skip on error, don't fail the whole batch
			}
			var desc describeNodeResp
			if err := json.Unmarshal(descResult, &desc); err != nil {
				return
			}
			attrs := desc.Node.Attributes
			for i := 0; i+1 < len(attrs); i += 2 {
				if attrs[i] == "data-mache-id" {
					mu.Lock()
					result[attrs[i+1]] = desc.Node.BackendNodeID
					mu.Unlock()
					break
				}
			}
		}(nid)
	}
	wg.Wait()
	return result, nil
}

// JoinAXToMache joins AX nodes to mache IDs via backendNodeID.
// Only includes disabled/expanded/checked/selected properties.
func JoinAXToMache(axNodes []axNode, macheToBackend map[string]int) map[string]AXInfo {
	// Build backendNodeID → AX node lookup.
	backendToAX := make(map[int]*axNode, len(axNodes))
	for i := range axNodes {
		if axNodes[i].BackendDOMNodeID != 0 {
			backendToAX[axNodes[i].BackendDOMNodeID] = &axNodes[i]
		}
	}

	allowedProps := map[string]bool{
		"disabled": true, "expanded": true,
		"checked": true, "selected": true,
	}

	result := make(map[string]AXInfo)
	for macheID, backendID := range macheToBackend {
		ax, ok := backendToAX[backendID]
		if !ok {
			continue
		}
		info := AXInfo{
			Role: ax.Role.Value,
			Name: ax.Name.Value,
		}
		for _, prop := range ax.Properties {
			if allowedProps[prop.Name] {
				info.Properties = append(info.Properties, fmt.Sprintf("%s=%v", prop.Name, prop.Value.Value))
			}
		}
		result[macheID] = info
	}
	return result
}

// layerTreeResp is the LayerTree.layerTreeDidChange event payload.
type layerTreeResp struct {
	Layers []layer `json:"layers"`
}

type layer struct {
	LayerID            string  `json:"layerId"`
	ParentLayerID      string  `json:"parentLayerId,omitempty"`
	BackendNodeID      int     `json:"backendNodeId,omitempty"`
	ResolvedPaintOrder int     // computed by DFS
	OffsetX            float64 `json:"offsetX"`
	OffsetY            float64 `json:"offsetY"`
	Width              float64 `json:"width"`
	Height             float64 `json:"height"`
}

// CaptureLayerTree enables LayerTree, waits for layerTreeDidChange event,
// then resolves paint order via DFS and compositing reasons.
// Returns map[macheID]LayerInfo. Degrades gracefully on errors/timeouts.
func CaptureLayerTree(ctx context.Context, p *Proxy, tabID int, macheToBackend map[string]int, timeout time.Duration) map[string]LayerInfo {
	result := make(map[string]LayerInfo)

	// Subscribe to events for this tab before enabling LayerTree.
	// Per-tab subscription avoids the global SetEventHandler race where
	// concurrent CaptureLayerTree calls on different tabs clobber each
	// other's handlers via save-and-restore.
	eventCh := p.SubscribeEvents(tabID)
	defer p.UnsubscribeEvents(tabID)

	layersCh := make(chan []layer, 1)
	go func() {
		for ev := range eventCh {
			if ev.Method == "LayerTree.layerTreeDidChange" {
				var resp layerTreeResp
				if err := json.Unmarshal(ev.Params, &resp); err == nil {
					layersCh <- resp.Layers
				} else {
					layersCh <- nil
				}
				return
			}
		}
	}()

	// Enable LayerTree.
	if _, err := p.Send(ctx, tabID, "LayerTree.enable", nil); err != nil {
		if !strings.Contains(err.Error(), "-32601") {
			log.Printf("CaptureLayerTree: LayerTree.enable failed (tab %d): %v", tabID, err)
		}
		return result
	}
	defer func() {
		_, _ = p.Send(ctx, tabID, "LayerTree.disable", nil)
	}()

	// Wait for event.
	var layers []layer
	select {
	case layers = <-layersCh:
	case <-ctx.Done():
		log.Printf("CaptureLayerTree: context cancelled (tab %d)", tabID)
		return result
	case <-time.After(timeout):
		log.Printf("CaptureLayerTree: layerTreeDidChange timeout after %v (tab %d)", timeout, tabID)
		return result
	}

	if len(layers) == 0 {
		log.Printf("CaptureLayerTree: event arrived but 0 layers (tab %d)", tabID)
		return result
	}

	// DFS to assign paint order.
	childrenByParent := make(map[string][]*layer)
	layerByID := make(map[string]*layer)
	for i := range layers {
		l := &layers[i]
		layerByID[l.LayerID] = l
		pid := l.ParentLayerID
		if pid == "" {
			pid = "root"
		}
		childrenByParent[pid] = append(childrenByParent[pid], l)
	}

	paintCounter := 0
	var dfsPaintOrder func(layerID string)
	dfsPaintOrder = func(layerID string) {
		if l, ok := layerByID[layerID]; ok {
			l.ResolvedPaintOrder = paintCounter
			paintCounter++
		}
		for _, child := range childrenByParent[layerID] {
			dfsPaintOrder(child.LayerID)
		}
	}
	for _, root := range childrenByParent["root"] {
		dfsPaintOrder(root.LayerID)
	}

	// Build backendNodeID → layer lookup.
	backendToLayer := make(map[int]*layer)
	for i := range layers {
		if layers[i].BackendNodeID != 0 {
			backendToLayer[layers[i].BackendNodeID] = &layers[i]
		}
	}

	// Batch compositingReasons for layers with backendNodeID.
	stackingReasons := map[string]bool{
		"transform": true, "opacity": true, "position: fixed": true,
		"position: sticky": true, "will-change": true, "filter": true,
		"backdrop-filter": true, "clip-path": true, "contain": true,
		"isolation": true, "mix-blend-mode": true, "perspective": true,
	}

	type reasonResult struct {
		backendID int
		reasons   []string
	}
	reasonsCh := make(chan reasonResult, len(layers))

	var wg sync.WaitGroup
	sem := make(chan struct{}, CompositingReasonsConcurrency)
	for i := range layers {
		if layers[i].BackendNodeID == 0 {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(l *layer) {
			defer wg.Done()
			defer func() { <-sem }()

			res, err := p.Send(ctx, tabID, "LayerTree.compositingReasons", map[string]any{
				"layerId": l.LayerID,
			})
			if err != nil {
				reasonsCh <- reasonResult{backendID: l.BackendNodeID}
				return
			}
			var resp struct {
				CompositingReasons []string `json:"compositingReasons"`
			}
			if err := json.Unmarshal(res, &resp); err != nil {
				reasonsCh <- reasonResult{backendID: l.BackendNodeID}
				return
			}
			reasonsCh <- reasonResult{backendID: l.BackendNodeID, reasons: resp.CompositingReasons}
		}(&layers[i])
	}
	wg.Wait()
	close(reasonsCh)

	reasonsByBackend := make(map[int][]string)
	for rr := range reasonsCh {
		reasonsByBackend[rr.backendID] = rr.reasons
	}

	// Join to mache IDs.
	for macheID, backendID := range macheToBackend {
		l, ok := backendToLayer[backendID]
		if !ok {
			continue
		}
		reasons := reasonsByBackend[backendID]
		isStackingRoot := false
		for _, r := range reasons {
			lower := strings.ToLower(r)
			if stackingReasons[lower] || strings.Contains(lower, "stacking") {
				isStackingRoot = true
				break
			}
		}
		result[macheID] = LayerInfo{
			PaintOrder:   l.ResolvedPaintOrder,
			StackingRoot: isStackingRoot,
		}
	}

	return result
}

// PixelClick dispatches a click at screenshot-relative coordinates.
// Unscales from screenshot pixels back to CSS pixels using the same formula as JS.
func PixelClick(ctx context.Context, p *Proxy, tabID int, scaledX, scaledY, targetWidth float64) error {
	// 1. Get CSS content width for scale computation.
	result, err := p.Send(ctx, tabID, "Page.getLayoutMetrics", nil)
	if err != nil {
		return fmt.Errorf("PixelClick: getLayoutMetrics: %w", err)
	}
	var resp struct {
		CSSContentSize struct {
			Width float64 `json:"width"`
		} `json:"cssContentSize"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return fmt.Errorf("PixelClick: unmarshal: %w", err)
	}

	// 2. Same scale formula as JS.
	actualWidth := resp.CSSContentSize.Width
	scale := math.Min(1, targetWidth/actualWidth)

	// 3. Unscale screenshot coords → CSS viewport coords.
	viewportX := scaledX / scale
	viewportY := scaledY / scale

	// 4. Dispatch mousePressed + mouseReleased.
	if _, err := p.Send(ctx, tabID, "Input.dispatchMouseEvent", map[string]any{
		"type":       "mousePressed",
		"x":          viewportX,
		"y":          viewportY,
		"button":     "left",
		"clickCount": 1,
	}); err != nil {
		return fmt.Errorf("PixelClick: mousePressed: %w", err)
	}
	if _, err := p.Send(ctx, tabID, "Input.dispatchMouseEvent", map[string]any{
		"type":       "mouseReleased",
		"x":          viewportX,
		"y":          viewportY,
		"button":     "left",
		"clickCount": 1,
	}); err != nil {
		return fmt.Errorf("PixelClick: mouseReleased: %w", err)
	}
	return nil
}

var macheIDRe = regexp.MustCompile(`^ID:\s*(mache-\d+)`)

// EnrichSummaryWithAX appends AX role and name to summary lines matching mache IDs.
func EnrichSummaryWithAX(summary string, axMap map[string]AXInfo) string {
	lines := strings.Split(summary, "\n")
	for i, line := range lines {
		m := macheIDRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		ax, ok := axMap[m[1]]
		if !ok {
			continue
		}
		if ax.Role != "" {
			lines[i] += " | AXRole: " + ax.Role
		}
		if ax.Name != "" {
			name := ax.Name
			if len(name) > AXNameMaxLen {
				name = name[:AXNameMaxLen]
			}
			lines[i] += fmt.Sprintf(` | AXName: "%s"`, name)
		}
	}
	return strings.Join(lines, "\n")
}

// EnrichSummaryWithLayers appends paint order and stacking context to summary lines.
func EnrichSummaryWithLayers(summary string, layerMap map[string]LayerInfo) string {
	lines := strings.Split(summary, "\n")
	for i, line := range lines {
		m := macheIDRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		info, ok := layerMap[m[1]]
		if !ok {
			continue
		}
		if info.PaintOrder >= 0 {
			lines[i] += fmt.Sprintf(" | PaintOrder: %d", info.PaintOrder)
		}
		if info.StackingRoot {
			lines[i] += " | StackingRoot: true"
		}
	}
	return strings.Join(lines, "\n")
}
