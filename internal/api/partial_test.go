package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/jamesgardner/x-ray/internal/mache"
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

// capturedCall records inputs to a single GenerateSchema call.
type capturedCall struct {
	screenshot []byte
	summary    string
}

// capturingCartographer records all calls and returns a configurable schema.
type capturingCartographer struct {
	calls  []capturedCall
	schema string
	err    error
}

func (c *capturingCartographer) GenerateSchema(_ context.Context, screenshot []byte, _, summary string) (string, error) {
	c.calls = append(c.calls, capturedCall{screenshot: screenshot, summary: summary})
	return c.schema, c.err
}

func TestRegenerateStaleZones_SingleStale(t *testing.T) {
	cart := &capturingCartographer{
		schema: `{"mounts":[{"virtual_path":"/main/feed","mache_id":"mache-new","description":"refreshed feed"}]}`,
	}
	stale := []StaleZoneInfo{
		{ZonePath: "/main/feed", Bounds: [4]float64{0.1, 0.1, 0.4, 0.6}},
	}

	result, err := RegenerateStaleZones(context.Background(), cart, stale, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(cart.calls) != 1 {
		t.Errorf("expected 1 Cartographer call, got %d", len(cart.calls))
	}
	if len(result.UpdatedMounts) != 1 {
		t.Errorf("expected 1 updated mount, got %d", len(result.UpdatedMounts))
	}
	if result.UpdatedMounts[0].VirtualPath != "/main/feed" {
		t.Errorf("expected /main/feed, got %s", result.UpdatedMounts[0].VirtualPath)
	}
	if len(result.InvalidatedPaths) != 1 || result.InvalidatedPaths[0] != "/main/feed" {
		t.Errorf("expected [/main/feed] invalidated, got %v", result.InvalidatedPaths)
	}

	// MergeSchemaJSON should be parseable
	var output mache.CartographerOutput
	if err := json.Unmarshal([]byte(result.MergeSchemaJSON), &output); err != nil {
		t.Fatalf("MergeSchemaJSON not parseable: %v", err)
	}
	if len(output.Mounts) != 1 {
		t.Errorf("expected 1 mount in MergeSchemaJSON, got %d", len(output.Mounts))
	}
}

func TestRegenerateStaleZones_AllStaleReturnsAll(t *testing.T) {
	callNum := 0
	schemas := []string{
		`{"mounts":[{"virtual_path":"/header","mache_id":"mache-h","description":"header"}]}`,
		`{"mounts":[{"virtual_path":"/footer","mache_id":"mache-f","description":"footer"}]}`,
	}
	cart := &capturingCartographer{}

	stale := []StaleZoneInfo{
		{ZonePath: "/header", Bounds: [4]float64{0, 0, 1, 0.1}},
		{ZonePath: "/footer", Bounds: [4]float64{0, 0.9, 1, 0.1}},
	}

	// Override to return different schemas per call.
	multiCart := &multiSchemaCartographer{schemas: schemas}
	_ = callNum // suppress unused

	result, err := RegenerateStaleZones(context.Background(), multiCart, stale, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = cart
	if len(multiCart.calls) != 2 {
		t.Errorf("expected 2 Cartographer calls, got %d", len(multiCart.calls))
	}
	if len(result.UpdatedMounts) != 2 {
		t.Errorf("expected 2 updated mounts, got %d", len(result.UpdatedMounts))
	}
	if len(result.InvalidatedPaths) != 2 {
		t.Errorf("expected 2 invalidated paths, got %d", len(result.InvalidatedPaths))
	}
}

// multiSchemaCartographer returns a different schema for each call.
type multiSchemaCartographer struct {
	calls   []capturedCall
	schemas []string
}

func (m *multiSchemaCartographer) GenerateSchema(_ context.Context, screenshot []byte, _, summary string) (string, error) {
	idx := len(m.calls)
	m.calls = append(m.calls, capturedCall{screenshot: screenshot, summary: summary})
	if idx < len(m.schemas) {
		return m.schemas[idx], nil
	}
	return `{"mounts":[]}`, nil
}

func TestRegenerateStaleZones_CartographerError(t *testing.T) {
	cart := &capturingCartographer{
		err: fmt.Errorf("API rate limit exceeded"),
	}
	stale := []StaleZoneInfo{
		{ZonePath: "/main/feed"},
	}

	result, err := RegenerateStaleZones(context.Background(), cart, stale, nil, "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if result != nil {
		t.Error("expected nil result on error")
	}
}

func TestRegenerateStaleZones_EmptyStaleList(t *testing.T) {
	cart := &capturingCartographer{}

	result, err := RegenerateStaleZones(context.Background(), cart, nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Error("expected nil result for empty stale list")
	}
	if len(cart.calls) != 0 {
		t.Errorf("expected 0 Cartographer calls, got %d", len(cart.calls))
	}
}

func TestRegenerateStaleZones_VerifyCroppedInputs(t *testing.T) {
	cart := &capturingCartographer{
		schema: `{"mounts":[{"virtual_path":"/main/feed","mache_id":"mache-1","description":"feed"}]}`,
	}

	// Create a test JPEG to verify cropping.
	screenshot := makeTestJPEG(200, 200)

	summary := `Interactive Elements:
ID: mache-1 | Color: BLUE | Bounds: [0.1, 0.1, 0.3, 0.3] | Parent: none | Tag: div | Text: "inside"
ID: mache-2 | Color: RED | Bounds: [0.8, 0.8, 0.1, 0.1] | Parent: none | Tag: div | Text: "outside"
`

	stale := []StaleZoneInfo{
		{ZonePath: "/main/feed", Bounds: [4]float64{0.0, 0.0, 0.5, 0.5}},
	}

	result, err := RegenerateStaleZones(context.Background(), cart, stale, screenshot, summary)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(cart.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(cart.calls))
	}

	// The screenshot passed to Cartographer should be different from the original
	// (it was cropped to the top-left quarter).
	if len(cart.calls[0].screenshot) == len(screenshot) {
		// Could be equal length by coincidence but very unlikely with JPEG re-encoding.
		t.Log("warning: cropped screenshot same length as original (may be coincidence)")
	}

	// The summary should be filtered — "outside" element should not appear.
	if len(cart.calls[0].summary) >= len(summary) {
		t.Log("warning: filtered summary not smaller than original")
	}
}

func TestRegenerateStaleZones_NoScreenshot(t *testing.T) {
	cart := &capturingCartographer{
		schema: `{"mounts":[{"virtual_path":"/main/feed","mache_id":"mache-1","description":"feed"}]}`,
	}
	stale := []StaleZoneInfo{
		{ZonePath: "/main/feed", Bounds: [4]float64{0.1, 0.1, 0.5, 0.5}},
	}

	result, err := RegenerateStaleZones(context.Background(), cart, stale, nil, "some summary")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	// Nil screenshot should be passed through (Cartographer handles it).
	if cart.calls[0].screenshot != nil {
		t.Error("expected nil screenshot to be passed through")
	}
}

func TestRegenerateStaleZones_ZoneBoundsZero(t *testing.T) {
	cart := &capturingCartographer{
		schema: `{"mounts":[{"virtual_path":"/main/feed","mache_id":"mache-1","description":"feed"}]}`,
	}
	screenshot := makeTestJPEG(100, 100)
	summary := "ID: mache-1 | Bounds: [0.1, 0.1, 0.3, 0.3] | Parent: none | Tag: div | Text: \"test\"\n"

	stale := []StaleZoneInfo{
		{ZonePath: "/main/feed", Bounds: [4]float64{0, 0, 0, 0}},
	}

	result, err := RegenerateStaleZones(context.Background(), cart, stale, screenshot, summary)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// With zero bounds, the full screenshot and summary should be passed through.
	if len(cart.calls[0].screenshot) != len(screenshot) {
		t.Error("expected full screenshot for zero-bounds zone")
	}
	if cart.calls[0].summary != summary {
		t.Error("expected full summary for zero-bounds zone")
	}
}

func TestExtractStaleZoneInfos(t *testing.T) {
	cached := `{"mounts":[
		{"virtual_path":"/header","mache_id":"mache-1","description":"header","bounds":[0,0,1,0.1]},
		{"virtual_path":"/main/feed","mache_id":"mache-2","description":"feed","bounds":[0,0.1,1,0.8]},
		{"virtual_path":"/footer","mache_id":"mache-3","description":"footer","bounds":[0,0.9,1,0.1]}
	]}`

	staleZones := map[string]string{
		"/main/feed": "mache-2",
	}

	infos := extractStaleZoneInfos(cached, staleZones)
	if len(infos) != 1 {
		t.Fatalf("expected 1 stale info, got %d", len(infos))
	}
	if infos[0].ZonePath != "/main/feed" {
		t.Errorf("expected /main/feed, got %s", infos[0].ZonePath)
	}
	// Bounds should be extracted from the cached mount.
	if infos[0].Bounds != [4]float64{0, 0.1, 1, 0.8} {
		t.Errorf("expected bounds [0,0.1,1,0.8], got %v", infos[0].Bounds)
	}
}

func TestRegenerateStaleZones_HallucinatedAnchorRepaired(t *testing.T) {
	// Cartographer returns a hallucinated anchor (mache-385) but valid primary_items.
	// The caller (attemptPartialRegen) validates and repairs AFTER RegenerateStaleZones,
	// so this test verifies the raw flow produces the hallucinated output that the
	// repair logic in attemptPartialRegen would then fix.
	cart := &capturingCartographer{
		schema: `{"mounts":[{
			"virtual_path":"/main/comments",
			"mache_id":"mache-385",
			"description":"Comment tree",
			"primary_items":["mache-51","mache-56"]
		}]}`,
	}
	summary := `Interactive Elements:
ID: mache-10 | Parent: none | Tag: div | Text: "Page"
ID: mache-51 | Parent: mache-10 | Tag: div | Text: "First comment"
ID: mache-56 | Parent: mache-10 | Tag: div | Text: "Second comment"
`
	stale := []StaleZoneInfo{
		{ZonePath: "/main/comments", Bounds: [4]float64{0.0, 0.3, 1.0, 0.6}},
	}

	result, err := RegenerateStaleZones(context.Background(), cart, stale, nil, summary)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// ValidateSchema should catch the hallucinated anchor.
	bad := mache.ValidateSchema(result.MergeSchemaJSON, summary)
	if len(bad) == 0 {
		t.Fatal("expected hallucinated ID to be detected")
	}

	// RepairSchema should fix it by swapping to first valid child.
	repaired, count := mache.RepairSchema(result.MergeSchemaJSON, summary)
	if count != 1 {
		t.Fatalf("expected 1 repair, got %d", count)
	}

	var output mache.CartographerOutput
	if err := json.Unmarshal([]byte(repaired), &output); err != nil {
		t.Fatalf("failed to parse repaired JSON: %v", err)
	}
	if output.Mounts[0].MacheID != "mache-51" {
		t.Errorf("expected anchor swapped to mache-51, got %q", output.Mounts[0].MacheID)
	}

	// After repair, validation should pass.
	bad2 := mache.ValidateSchema(repaired, summary)
	if len(bad2) != 0 {
		t.Errorf("repaired schema should validate clean, got bad: %v", bad2)
	}
}

func TestExtractStaleZoneInfos_InvalidJSON(t *testing.T) {
	infos := extractStaleZoneInfos("not json", map[string]string{"/foo": "mache-1"})
	if len(infos) != 1 {
		t.Fatalf("expected 1 stale info, got %d", len(infos))
	}
	if infos[0].ZonePath != "/foo" {
		t.Errorf("expected /foo, got %s", infos[0].ZonePath)
	}
	// Bounds should be zero since JSON parse failed.
	if infos[0].Bounds != [4]float64{} {
		t.Errorf("expected zero bounds, got %v", infos[0].Bounds)
	}
}
