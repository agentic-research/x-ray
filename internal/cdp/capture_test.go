package cdp

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"
)

// --- LayoutMetrics ---

func TestLayoutMetrics_Success(t *testing.T) {
	ms := &mockSender{}
	p := New(ms)
	ctx := context.Background()

	var width, height float64
	var gotErr error
	done := make(chan struct{})
	go func() {
		width, height, gotErr = LayoutMetrics(ctx, p, 1)
		close(done)
	}()

	waitForMsg(t, ms)
	msg := ms.lastMsg()
	if msg["cdp_method"] != "Page.getLayoutMetrics" {
		t.Fatalf("expected Page.getLayoutMetrics, got %v", msg["cdp_method"])
	}
	p.HandleResult(msg["cdp_id"].(int64), json.RawMessage(`{
		"cssContentSize": {"width": 1440, "height": 5000}
	}`))
	waitDone(t, done)

	if gotErr != nil {
		t.Fatalf("unexpected error: %v", gotErr)
	}
	if width != 1440 {
		t.Errorf("expected width 1440, got %f", width)
	}
	if height != 5000 {
		t.Errorf("expected height 5000, got %f", height)
	}
}

func TestLayoutMetrics_CapsHeight(t *testing.T) {
	ms := &mockSender{}
	p := New(ms)
	ctx := context.Background()

	var height float64
	done := make(chan struct{})
	go func() {
		_, height, _ = LayoutMetrics(ctx, p, 1)
		close(done)
	}()

	waitForMsg(t, ms)
	msg := ms.lastMsg()
	p.HandleResult(msg["cdp_id"].(int64), json.RawMessage(`{
		"cssContentSize": {"width": 800, "height": 99999}
	}`))
	waitDone(t, done)

	if height != CDPMaxHeight {
		t.Errorf("expected height capped at %d, got %f", CDPMaxHeight, height)
	}
}

func TestLayoutMetrics_Error(t *testing.T) {
	ms := &mockSender{}
	p := New(ms)
	ctx := context.Background()

	var gotErr error
	done := make(chan struct{})
	go func() {
		_, _, gotErr = LayoutMetrics(ctx, p, 1)
		close(done)
	}()

	waitForMsg(t, ms)
	msg := ms.lastMsg()
	p.HandleError(msg["cdp_id"].(int64), "target closed")
	waitDone(t, done)

	if gotErr == nil {
		t.Fatal("expected error, got nil")
	}
}

// --- DocumentRoot ---

func TestDocumentRoot_Success(t *testing.T) {
	ms := &mockSender{}
	p := New(ms)
	ctx := context.Background()

	var nodeID int
	done := make(chan struct{})
	go func() {
		nodeID, _ = DocumentRoot(ctx, p, 1)
		close(done)
	}()

	waitForMsg(t, ms)
	msg := ms.lastMsg()
	if msg["cdp_method"] != "DOM.getDocument" {
		t.Fatalf("expected DOM.getDocument, got %v", msg["cdp_method"])
	}
	p.HandleResult(msg["cdp_id"].(int64), json.RawMessage(`{"root":{"nodeId":42}}`))
	waitDone(t, done)

	if nodeID != 42 {
		t.Errorf("expected nodeID 42, got %d", nodeID)
	}
}

func TestDocumentRoot_Error(t *testing.T) {
	ms := &mockSender{}
	p := New(ms)
	ctx := context.Background()

	var gotErr error
	done := make(chan struct{})
	go func() {
		_, gotErr = DocumentRoot(ctx, p, 1)
		close(done)
	}()

	waitForMsg(t, ms)
	msg := ms.lastMsg()
	p.HandleError(msg["cdp_id"].(int64), "DOM not ready")
	waitDone(t, done)

	if gotErr == nil {
		t.Fatal("expected error, got nil")
	}
}

// --- ElementBoxModel ---

func TestElementBoxModel_Found(t *testing.T) {
	ms := &mockSender{}
	p := New(ms)
	ctx := context.Background()

	var box *BoxModel
	done := make(chan struct{})
	go func() {
		box, _ = ElementBoxModel(ctx, p, 1, 42, "mache-5")
		close(done)
	}()

	// querySelector
	waitForMsg(t, ms)
	msg := ms.lastMsg()
	if msg["cdp_method"] != "DOM.querySelector" {
		t.Fatalf("expected DOM.querySelector, got %v", msg["cdp_method"])
	}
	p.HandleResult(msg["cdp_id"].(int64), json.RawMessage(`{"nodeId":100}`))

	// getBoxModel
	waitForMsgN(t, ms, 2)
	msg = ms.lastMsg()
	if msg["cdp_method"] != "DOM.getBoxModel" {
		t.Fatalf("expected DOM.getBoxModel, got %v", msg["cdp_method"])
	}
	// border quad: [10,20, 110,20, 110,120, 10,120]
	p.HandleResult(msg["cdp_id"].(int64), json.RawMessage(`{
		"model":{"border":[10,20,110,20,110,120,10,120]}
	}`))
	waitDone(t, done)

	if box == nil {
		t.Fatal("expected non-nil box")
	}
	if box.X != 10 || box.Y != 20 || box.W != 100 || box.H != 100 {
		t.Errorf("unexpected box: %+v", box)
	}
}

func TestElementBoxModel_NotFound(t *testing.T) {
	ms := &mockSender{}
	p := New(ms)
	ctx := context.Background()

	var box *BoxModel
	done := make(chan struct{})
	go func() {
		box, _ = ElementBoxModel(ctx, p, 1, 42, "mache-999")
		close(done)
	}()

	waitForMsg(t, ms)
	msg := ms.lastMsg()
	p.HandleResult(msg["cdp_id"].(int64), json.RawMessage(`{"nodeId":0}`))
	waitDone(t, done)

	if box != nil {
		t.Errorf("expected nil box for not-found element, got %+v", box)
	}
}

// --- BuildClip ---

func TestBuildClip_FullPage(t *testing.T) {
	clip := BuildClip(1440, 5000, nil)
	if clip.X != 0 || clip.Y != 0 {
		t.Errorf("expected origin (0,0), got (%f,%f)", clip.X, clip.Y)
	}
	if clip.Width != 1440 || clip.Height != 5000 {
		t.Errorf("expected (1440,5000), got (%f,%f)", clip.Width, clip.Height)
	}
	expectedScale := 800.0 / 1440.0
	if clip.Scale != expectedScale {
		t.Errorf("expected scale %f, got %f", expectedScale, clip.Scale)
	}
}

func TestBuildClip_NarrowPage(t *testing.T) {
	clip := BuildClip(600, 800, nil)
	if clip.Scale != 1.0 {
		t.Errorf("expected scale 1.0 for narrow page, got %f", clip.Scale)
	}
}

func TestBuildClip_WithTarget(t *testing.T) {
	box := &BoxModel{X: 100, Y: 200, W: 300, H: 400}
	clip := BuildClip(1440, 5000, box)
	// cx = max(0, 100-50) = 50, cy = max(0, 200-50) = 150
	// cw = min(1440-50, 300+100) = 400, ch = min(5000-150, 400+100) = 500
	if clip.X != 50 || clip.Y != 150 {
		t.Errorf("expected origin (50,150), got (%f,%f)", clip.X, clip.Y)
	}
	if clip.Width != 400 || clip.Height != 500 {
		t.Errorf("expected (400,500), got (%f,%f)", clip.Width, clip.Height)
	}
	expectedScale := 800.0 / 400.0
	if expectedScale > 1 {
		expectedScale = 1
	}
	if clip.Scale != expectedScale {
		t.Errorf("expected scale %f, got %f", expectedScale, clip.Scale)
	}
}

func TestBuildClip_TargetNearEdge(t *testing.T) {
	// Element near right edge: X=1400, W=100 → with pad, would exceed 1440
	box := &BoxModel{X: 1400, Y: 0, W: 100, H: 50}
	clip := BuildClip(1440, 800, box)
	// cx = max(0, 1400-50) = 1350, cy = max(0, 0-50) = 0
	// cw = min(1440-1350, 100+100) = 90, ch = min(800-0, 50+100) = 150
	if clip.X != 1350 {
		t.Errorf("expected X=1350, got %f", clip.X)
	}
	if clip.Width != 90 {
		t.Errorf("expected Width=90, got %f", clip.Width)
	}
}

// --- CaptureScreenshot ---

func TestCaptureScreenshot_Success(t *testing.T) {
	ms := &mockSender{}
	p := New(ms)
	ctx := context.Background()

	clip := ScreenshotClip{X: 0, Y: 0, Width: 800, Height: 600, Scale: 1}
	var b64 string
	done := make(chan struct{})
	go func() {
		b64, _ = CaptureScreenshot(ctx, p, 1, clip)
		close(done)
	}()

	waitForMsg(t, ms)
	msg := ms.lastMsg()
	if msg["cdp_method"] != "Page.captureScreenshot" {
		t.Fatalf("expected Page.captureScreenshot, got %v", msg["cdp_method"])
	}
	p.HandleResult(msg["cdp_id"].(int64), json.RawMessage(`{"data":"aGVsbG8="}`))
	waitDone(t, done)

	if b64 != "aGVsbG8=" {
		t.Errorf("expected 'aGVsbG8=', got %q", b64)
	}
}

func TestCaptureScreenshot_Error(t *testing.T) {
	ms := &mockSender{}
	p := New(ms)
	ctx := context.Background()

	clip := ScreenshotClip{X: 0, Y: 0, Width: 800, Height: 600, Scale: 1}
	var gotErr error
	done := make(chan struct{})
	go func() {
		_, gotErr = CaptureScreenshot(ctx, p, 1, clip)
		close(done)
	}()

	waitForMsg(t, ms)
	msg := ms.lastMsg()
	p.HandleError(msg["cdp_id"].(int64), "capture failed")
	waitDone(t, done)

	if gotErr == nil {
		t.Fatal("expected error")
	}
}

func TestCaptureScreenshot_Timeout(t *testing.T) {
	ms := &mockSender{}
	p := New(ms)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	clip := ScreenshotClip{X: 0, Y: 0, Width: 800, Height: 600, Scale: 1}
	_, err := CaptureScreenshot(ctx, p, 1, clip)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

// --- FullAXTree ---

func TestFullAXTree_Success(t *testing.T) {
	ms := &mockSender{}
	p := New(ms)
	ctx := context.Background()

	var nodes []axNode
	done := make(chan struct{})
	go func() {
		nodes, _ = FullAXTree(ctx, p, 1)
		close(done)
	}()

	waitForMsg(t, ms)
	msg := ms.lastMsg()
	if msg["cdp_method"] != "Accessibility.getFullAXTree" {
		t.Fatalf("expected Accessibility.getFullAXTree, got %v", msg["cdp_method"])
	}
	p.HandleResult(msg["cdp_id"].(int64), json.RawMessage(`{
		"nodes":[
			{"backendDOMNodeId":10,"role":{"value":"button"},"name":{"value":"Submit"}},
			{"backendDOMNodeId":20,"role":{"value":"textbox"},"name":{"value":"Email"}}
		]
	}`))
	waitDone(t, done)

	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(nodes))
	}
	if nodes[0].Role.Value != "button" {
		t.Errorf("expected role 'button', got %q", nodes[0].Role.Value)
	}
}

// --- MacheBackendMap ---

func TestMacheBackendMap_Success(t *testing.T) {
	ms := &mockSender{}
	p := New(ms)
	ctx := context.Background()

	var result map[string]int
	done := make(chan struct{})
	go func() {
		result, _ = MacheBackendMap(ctx, p, 1, 42)
		close(done)
	}()

	// querySelectorAll
	waitForMsg(t, ms)
	msg := ms.lastMsg()
	if msg["cdp_method"] != "DOM.querySelectorAll" {
		t.Fatalf("expected DOM.querySelectorAll, got %v", msg["cdp_method"])
	}
	p.HandleResult(msg["cdp_id"].(int64), json.RawMessage(`{"nodeIds":[100,200]}`))

	// Two describeNode calls (one per nodeID). Wait for all 3 messages.
	waitForMsgN(t, ms, 3) // querySelectorAll + 2 describeNode
	msgs := ms.allMsgs()
	for _, m := range msgs[1:] { // skip querySelectorAll
		if m["cdp_method"] == "DOM.describeNode" {
			cdpID := m["cdp_id"].(int64)
			// Use nodeId to determine which mache-id to return.
			// We just return different mache-ids for different requests.
			nodeIDFromParams := 0
			if params, ok := m["cdp_params"].(map[string]any); ok {
				if nid, ok := params["nodeId"].(int); ok {
					nodeIDFromParams = nid
				}
			}
			var macheID string
			if nodeIDFromParams == 100 {
				macheID = "mache-1"
			} else {
				macheID = "mache-2"
			}
			p.HandleResult(cdpID, json.RawMessage(
				`{"node":{"backendNodeId":`+itoa(nodeIDFromParams+500)+`,"attributes":["data-mache-id","`+macheID+`"]}}`,
			))
		}
	}
	waitDone(t, done)

	if len(result) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(result))
	}
	if result["mache-1"] != 600 {
		t.Errorf("expected mache-1 → 600, got %d", result["mache-1"])
	}
}

// --- JoinAXToMache ---

func TestJoinAXToMache_Basic(t *testing.T) {
	axNodes := []axNode{
		{
			BackendDOMNodeID: 100,
			Role: struct {
				Value string `json:"value"`
			}{Value: "button"},
			Name: struct {
				Value string `json:"value"`
			}{Value: "Submit"},
		},
		{
			BackendDOMNodeID: 200,
			Role: struct {
				Value string `json:"value"`
			}{Value: "textbox"},
			Name: struct {
				Value string `json:"value"`
			}{Value: "Email"},
		},
	}
	macheToBackend := map[string]int{
		"mache-1": 100,
		"mache-2": 200,
		"mache-3": 999, // no matching AX node
	}
	result := JoinAXToMache(axNodes, macheToBackend)

	if len(result) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(result))
	}
	if result["mache-1"].Role != "button" {
		t.Errorf("expected role 'button', got %q", result["mache-1"].Role)
	}
	if _, ok := result["mache-3"]; ok {
		t.Error("mache-3 should not be in result (no matching AX node)")
	}
}

func TestJoinAXToMache_FilteredProperties(t *testing.T) {
	axNodes := []axNode{
		{
			BackendDOMNodeID: 100,
			Role: struct {
				Value string `json:"value"`
			}{Value: "checkbox"},
			Name: struct {
				Value string `json:"value"`
			}{Value: "Agree"},
			Properties: []struct {
				Name  string `json:"name"`
				Value struct {
					Value any `json:"value"`
				} `json:"value"`
			}{
				{Name: "checked", Value: struct {
					Value any `json:"value"`
				}{Value: true}},
				{Name: "disabled", Value: struct {
					Value any `json:"value"`
				}{Value: false}},
				{Name: "focusable", Value: struct {
					Value any `json:"value"`
				}{Value: true}}, // should be filtered
				{Name: "readonly", Value: struct {
					Value any `json:"value"`
				}{Value: false}}, // should be filtered
			},
		},
	}
	macheToBackend := map[string]int{"mache-1": 100}
	result := JoinAXToMache(axNodes, macheToBackend)

	info := result["mache-1"]
	if len(info.Properties) != 2 {
		t.Fatalf("expected 2 filtered properties, got %d: %v", len(info.Properties), info.Properties)
	}
	// Should only have checked and disabled.
	for _, p := range info.Properties {
		if !strings.HasPrefix(p, "checked=") && !strings.HasPrefix(p, "disabled=") {
			t.Errorf("unexpected property: %s", p)
		}
	}
}

// --- CaptureLayerTree ---

func TestCaptureLayerTree_Success(t *testing.T) {
	ms := &mockSender{}
	p := New(ms)
	ctx := context.Background()

	macheToBackend := map[string]int{"mache-1": 10, "mache-2": 20}

	var result map[string]LayerInfo
	done := make(chan struct{})
	go func() {
		result = CaptureLayerTree(ctx, p, 1, macheToBackend)
		close(done)
	}()

	// LayerTree.enable
	waitForMsg(t, ms)
	msg := ms.lastMsg()
	if msg["cdp_method"] != "LayerTree.enable" {
		t.Fatalf("expected LayerTree.enable, got %v", msg["cdp_method"])
	}
	p.HandleResult(msg["cdp_id"].(int64), json.RawMessage(`{}`))

	// Simulate layerTreeDidChange event.
	p.HandleEvent(1, "LayerTree.layerTreeDidChange", json.RawMessage(`{
		"layers":[
			{"layerId":"L1","backendNodeId":10},
			{"layerId":"L2","parentLayerId":"L1","backendNodeId":20}
		]
	}`))

	// compositingReasons calls — 2 layers with backendNodeId → 2 calls (msgs 2-3).
	waitForMsgN(t, ms, 3) // enable + 2 compositingReasons
	msgs := ms.allMsgs()
	for _, m := range msgs {
		if m["cdp_method"] == "LayerTree.compositingReasons" {
			p.HandleResult(m["cdp_id"].(int64), json.RawMessage(`{"compositingReasons":["transform"]}`))
		}
	}

	// LayerTree.disable fires in defer after wg.Wait (msg 4).
	waitForMsgN(t, ms, 4)
	msgs = ms.allMsgs()
	for _, m := range msgs {
		if m["cdp_method"] == "LayerTree.disable" {
			p.HandleResult(m["cdp_id"].(int64), json.RawMessage(`{}`))
		}
	}

	waitDone(t, done)

	if len(result) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(result))
	}
	if !result["mache-1"].StackingRoot {
		t.Error("expected mache-1 to be stacking root (transform reason)")
	}
}

func TestCaptureLayerTree_EventTimeout(t *testing.T) {
	// Override timeout to be short for the test.
	oldTimeout := layerTreeTimeout
	layerTreeTimeout = 50 * time.Millisecond
	defer func() { layerTreeTimeout = oldTimeout }()

	ms := &mockSender{}
	p := New(ms)
	ctx := context.Background()

	macheToBackend := map[string]int{"mache-1": 10}

	var result map[string]LayerInfo
	done := make(chan struct{})
	go func() {
		result = CaptureLayerTree(ctx, p, 1, macheToBackend)
		close(done)
	}()

	// LayerTree.enable
	waitForMsg(t, ms)
	msg := ms.lastMsg()
	p.HandleResult(msg["cdp_id"].(int64), json.RawMessage(`{}`))

	// Don't send layerTreeDidChange event — should timeout gracefully.
	// LayerTree.disable will be called in defer.
	time.Sleep(100 * time.Millisecond)
	msgs := ms.allMsgs()
	for _, m := range msgs {
		if m["cdp_method"] == "LayerTree.disable" {
			p.HandleResult(m["cdp_id"].(int64), json.RawMessage(`{}`))
		}
	}

	waitDone(t, done)

	if len(result) != 0 {
		t.Errorf("expected empty result on timeout, got %d entries", len(result))
	}
}

// --- PixelClick ---

func TestPixelClick_Success(t *testing.T) {
	ms := &mockSender{}
	p := New(ms)
	ctx := context.Background()

	var gotErr error
	done := make(chan struct{})
	go func() {
		gotErr = PixelClick(ctx, p, 1, 400, 200)
		close(done)
	}()

	// Page.getLayoutMetrics
	waitForMsg(t, ms)
	msg := ms.lastMsg()
	if msg["cdp_method"] != "Page.getLayoutMetrics" {
		t.Fatalf("expected Page.getLayoutMetrics, got %v", msg["cdp_method"])
	}
	p.HandleResult(msg["cdp_id"].(int64), json.RawMessage(`{
		"cssContentSize":{"width":1440}
	}`))

	// mousePressed
	waitForMsgN(t, ms, 2)
	msg = ms.lastMsg()
	if msg["cdp_method"] != "Input.dispatchMouseEvent" {
		t.Fatalf("expected Input.dispatchMouseEvent, got %v", msg["cdp_method"])
	}
	p.HandleResult(msg["cdp_id"].(int64), json.RawMessage(`{}`))

	// mouseReleased
	waitForMsgN(t, ms, 3)
	msg = ms.lastMsg()
	p.HandleResult(msg["cdp_id"].(int64), json.RawMessage(`{}`))

	waitDone(t, done)
	if gotErr != nil {
		t.Fatalf("unexpected error: %v", gotErr)
	}
}

func TestPixelClick_ScaleMapping(t *testing.T) {
	// On a 1440px CSS-width page, screenshot is 800px wide (scale=800/1440=0.556).
	// Click at screenshot pixel (400, 200) should map to CSS pixel (720, 360).
	ms := &mockSender{}
	p := New(ms)
	ctx := context.Background()

	done := make(chan struct{})
	go func() {
		_ = PixelClick(ctx, p, 1, 400, 200)
		close(done)
	}()

	// Page.getLayoutMetrics → 1440px wide
	waitForMsg(t, ms)
	msg := ms.lastMsg()
	p.HandleResult(msg["cdp_id"].(int64), json.RawMessage(`{
		"cssContentSize":{"width":1440}
	}`))

	// mousePressed — check coordinates
	waitForMsgN(t, ms, 2)
	msg = ms.lastMsg()
	params := msg["cdp_params"].(map[string]any)
	x := params["x"].(float64)
	y := params["y"].(float64)

	// scale = 800/1440 ≈ 0.5556
	// viewportX = 400 / 0.5556 ≈ 720, viewportY = 200 / 0.5556 ≈ 360
	expectedX := 400.0 / (800.0 / 1440.0)
	expectedY := 200.0 / (800.0 / 1440.0)
	if diff := x - expectedX; diff < -0.01 || diff > 0.01 {
		t.Errorf("expected x=%f, got %f", expectedX, x)
	}
	if diff := y - expectedY; diff < -0.01 || diff > 0.01 {
		t.Errorf("expected y=%f, got %f", expectedY, y)
	}

	p.HandleResult(msg["cdp_id"].(int64), json.RawMessage(`{}`))

	// mouseReleased
	waitForMsgN(t, ms, 3)
	msg = ms.lastMsg()
	p.HandleResult(msg["cdp_id"].(int64), json.RawMessage(`{}`))
	waitDone(t, done)
}

func TestPixelClick_NarrowPage_NoScale(t *testing.T) {
	// Page narrower than CDPTargetWidth: scale=1, no coordinate transformation.
	ms := &mockSender{}
	p := New(ms)
	ctx := context.Background()

	done := make(chan struct{})
	go func() {
		_ = PixelClick(ctx, p, 1, 300, 150)
		close(done)
	}()

	// 600px wide → scale = min(1, 800/600) = 1
	waitForMsg(t, ms)
	msg := ms.lastMsg()
	p.HandleResult(msg["cdp_id"].(int64), json.RawMessage(`{
		"cssContentSize":{"width":600}
	}`))

	// mousePressed — coords should be unchanged
	waitForMsgN(t, ms, 2)
	msg = ms.lastMsg()
	params := msg["cdp_params"].(map[string]any)
	if params["x"].(float64) != 300 || params["y"].(float64) != 150 {
		t.Errorf("expected (300,150), got (%v,%v)", params["x"], params["y"])
	}

	p.HandleResult(msg["cdp_id"].(int64), json.RawMessage(`{}`))
	waitForMsgN(t, ms, 3)
	msg = ms.lastMsg()
	p.HandleResult(msg["cdp_id"].(int64), json.RawMessage(`{}`))
	waitDone(t, done)
}

// --- EnrichSummaryWithAX ---

func TestEnrichSummaryWithAX_Basic(t *testing.T) {
	summary := `ID: mache-1 | Tag: BUTTON Bounds: [0.1, 0.2, 0.3, 0.05]
ID: mache-2 | Tag: INPUT Bounds: [0.1, 0.3, 0.3, 0.05]
Some other line`

	axMap := map[string]AXInfo{
		"mache-1": {Role: "button", Name: "Submit"},
		"mache-2": {Role: "textbox", Name: "Email"},
	}

	result := EnrichSummaryWithAX(summary, axMap)
	lines := strings.Split(result, "\n")

	if !strings.Contains(lines[0], `AXRole: button`) {
		t.Errorf("line 0 missing AXRole: %s", lines[0])
	}
	if !strings.Contains(lines[0], `AXName: "Submit"`) {
		t.Errorf("line 0 missing AXName: %s", lines[0])
	}
	if !strings.Contains(lines[1], `AXRole: textbox`) {
		t.Errorf("line 1 missing AXRole: %s", lines[1])
	}
	if lines[2] != "Some other line" {
		t.Errorf("line 2 should be unchanged: %s", lines[2])
	}
}

func TestEnrichSummaryWithAX_TruncatedName(t *testing.T) {
	longName := strings.Repeat("A", 120)
	axMap := map[string]AXInfo{
		"mache-1": {Role: "link", Name: longName},
	}
	result := EnrichSummaryWithAX("ID: mache-1 | Tag: A", axMap)
	if strings.Contains(result, longName) {
		t.Error("name should be truncated to 80 chars")
	}
	if !strings.Contains(result, strings.Repeat("A", 80)) {
		t.Error("name should contain first 80 chars")
	}
}

func TestEnrichSummaryWithAX_NoMatch(t *testing.T) {
	summary := "ID: mache-99 | Tag: DIV"
	axMap := map[string]AXInfo{
		"mache-1": {Role: "button", Name: "X"},
	}
	result := EnrichSummaryWithAX(summary, axMap)
	if result != summary {
		t.Errorf("expected unchanged summary, got: %s", result)
	}
}

func TestEnrichSummaryWithAX_EmptyRole(t *testing.T) {
	axMap := map[string]AXInfo{
		"mache-1": {Role: "", Name: "Submit"},
	}
	result := EnrichSummaryWithAX("ID: mache-1 | Tag: BUTTON", axMap)
	if strings.Contains(result, "AXRole") {
		t.Error("should not include AXRole when empty")
	}
	if !strings.Contains(result, `AXName: "Submit"`) {
		t.Errorf("should include AXName: %s", result)
	}
}

// --- EnrichSummaryWithLayers ---

func TestEnrichSummaryWithLayers_Basic(t *testing.T) {
	summary := `ID: mache-1 | Tag: DIV
ID: mache-2 | Tag: CANVAS`
	layerMap := map[string]LayerInfo{
		"mache-1": {PaintOrder: 5, StackingRoot: true},
		"mache-2": {PaintOrder: 10, StackingRoot: false},
	}
	result := EnrichSummaryWithLayers(summary, layerMap)
	lines := strings.Split(result, "\n")
	if !strings.Contains(lines[0], "PaintOrder: 5") {
		t.Errorf("line 0 missing PaintOrder: %s", lines[0])
	}
	if !strings.Contains(lines[0], "StackingRoot: true") {
		t.Errorf("line 0 missing StackingRoot: %s", lines[0])
	}
	if !strings.Contains(lines[1], "PaintOrder: 10") {
		t.Errorf("line 1 missing PaintOrder: %s", lines[1])
	}
	if strings.Contains(lines[1], "StackingRoot") {
		t.Errorf("line 1 should not have StackingRoot: %s", lines[1])
	}
}

func TestEnrichSummaryWithLayers_NegativePaintOrder(t *testing.T) {
	layerMap := map[string]LayerInfo{
		"mache-1": {PaintOrder: -1, StackingRoot: false},
	}
	result := EnrichSummaryWithLayers("ID: mache-1 | Tag: DIV", layerMap)
	if strings.Contains(result, "PaintOrder") {
		t.Error("should not include PaintOrder when negative")
	}
}

// --- Test helpers ---

func waitForMsg(t *testing.T, ms *mockSender) {
	t.Helper()
	waitForMsgN(t, ms, 1)
}

func waitForMsgN(t *testing.T, ms *mockSender, n int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		msgs := ms.allMsgs()
		if len(msgs) >= n {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %d messages (got %d)", n, len(ms.allMsgs()))
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func waitDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for function to return")
	}
}

func itoa(n int) string {
	return strconv.Itoa(n)
}
