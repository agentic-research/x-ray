package cartographer

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// Reuse cairnTestSummary from cairn_test.go (same package).

func TestProgressiveCartographer_NilProgress(t *testing.T) {
	pc := &ProgressiveCartographer{
		Scale:    10.0,
		GridSize: 12,
	}

	schema, err := pc.GenerateSchema(context.Background(), nil, "", cairnTestSummary)
	if err != nil {
		t.Fatalf("GenerateSchema failed: %v", err)
	}

	var result struct {
		Mounts []tropicalMount `json:"mounts"`
	}
	if err := json.Unmarshal([]byte(schema), &result); err != nil {
		t.Fatalf("invalid JSON: %v\nSchema: %s", err, schema)
	}
	if len(result.Mounts) == 0 {
		t.Fatal("expected at least 1 mount")
	}
	for _, m := range result.Mounts {
		if m.VirtualPath == "" {
			t.Error("mount has empty VirtualPath")
		}
	}
}

func TestProgressiveCartographer_ChannelEmission(t *testing.T) {
	ch := make(chan ProgressResult, 10) // buffered to avoid blocking
	pc := &ProgressiveCartographer{
		Scale:    10.0,
		GridSize: 12,
		Progress: ch,
	}

	schema, err := pc.GenerateSchema(context.Background(), nil, "", cairnTestSummary)
	if err != nil {
		t.Fatalf("GenerateSchema failed: %v", err)
	}

	close(ch) // signal no more results

	var results []ProgressResult
	for r := range ch {
		results = append(results, r)
	}

	if len(results) < 1 {
		t.Fatalf("expected at least 1 progress result, got %d", len(results))
	}

	// Last result must be final
	last := results[len(results)-1]
	if !last.IsFinal {
		t.Error("last progress result should have IsFinal=true")
	}

	// Final schema from channel must match return value
	if last.Schema != schema {
		t.Error("final progress schema differs from GenerateSchema return value")
	}

	// All intermediate results must be valid JSON
	for i, r := range results {
		var output struct {
			Mounts []tropicalMount `json:"mounts"`
		}
		if err := json.Unmarshal([]byte(r.Schema), &output); err != nil {
			t.Errorf("result[%d] (stage=%d) is not valid JSON: %v", i, r.Stage, err)
		}
	}

	// Stages must be monotonically increasing
	for i := 1; i < len(results); i++ {
		if results[i].Stage < results[i-1].Stage {
			t.Errorf("stages not monotonic: result[%d].Stage=%d < result[%d].Stage=%d",
				i, results[i].Stage, i-1, results[i-1].Stage)
		}
	}
}

func TestProgressiveCartographer_Stage0_DOMOnly(t *testing.T) {
	ch := make(chan ProgressResult, 10)
	pc := &ProgressiveCartographer{
		Scale:    10.0,
		GridSize: 12,
		Progress: ch,
	}

	// DOM-only: no screenshot
	_, err := pc.GenerateSchema(context.Background(), nil, "", cairnTestSummary)
	if err != nil {
		t.Fatalf("GenerateSchema failed: %v", err)
	}
	close(ch)

	var results []ProgressResult
	for r := range ch {
		results = append(results, r)
	}

	// With no screenshot, only stage 0 should emit (and be final)
	if len(results) != 1 {
		t.Fatalf("expected exactly 1 result for DOM-only, got %d", len(results))
	}
	if results[0].Stage != 0 {
		t.Errorf("expected stage 0, got %d", results[0].Stage)
	}
	if !results[0].IsFinal {
		t.Error("DOM-only result should be final")
	}

	// Verify zone count in range [2, 7]
	var output struct {
		Mounts []tropicalMount `json:"mounts"`
	}
	if err := json.Unmarshal([]byte(results[0].Schema), &output); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(output.Mounts) < 2 || len(output.Mounts) > 7 {
		t.Errorf("expected 2-7 mounts, got %d", len(output.Mounts))
	}
}

func TestProgressiveCartographer_Stage1_WithScreenshot(t *testing.T) {
	// Create a 64x64 synthetic screenshot: top half blue, bottom half red
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			if y < 32 {
				img.Set(x, y, color.RGBA{0, 0, 255, 255})
			} else {
				img.Set(x, y, color.RGBA{255, 0, 0, 255})
			}
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode test image: %v", err)
	}

	ch := make(chan ProgressResult, 20)
	pc := &ProgressiveCartographer{
		Scale:    10.0,
		GridSize: 4, // small grid for test speed
		Progress: ch,
	}

	schema, err := pc.GenerateSchema(context.Background(), buf.Bytes(), "image/png", cairnTestSummary)
	if err != nil {
		t.Fatalf("GenerateSchema failed: %v", err)
	}
	close(ch)

	var results []ProgressResult
	for r := range ch {
		results = append(results, r)
	}

	// Should have stage 0 (DOM) through stage 5 (sheaf)
	if len(results) < 2 {
		t.Fatalf("expected at least 2 results with screenshot, got %d", len(results))
	}

	// Stage 0 should be present
	if results[0].Stage != 0 {
		t.Errorf("first result should be stage 0, got %d", results[0].Stage)
	}

	// Final result must match return value
	last := results[len(results)-1]
	if last.Schema != schema {
		t.Error("final channel schema differs from return value")
	}

	// All schemas must be valid CartographerOutput JSON
	for i, r := range results {
		var output struct {
			Mounts []tropicalMount `json:"mounts"`
		}
		if err := json.Unmarshal([]byte(r.Schema), &output); err != nil {
			t.Errorf("result[%d] stage=%d: invalid JSON: %v", i, r.Stage, err)
			continue
		}
		for j, m := range output.Mounts {
			if m.VirtualPath == "" {
				t.Errorf("result[%d] stage=%d mount[%d]: empty VirtualPath", i, r.Stage, j)
			}
		}
	}

	t.Logf("Got %d progressive results, stages: %v",
		len(results), func() []int {
			s := make([]int, len(results))
			for i, r := range results {
				s[i] = r.Stage
			}
			return s
		}())
}

func TestProgressiveCartographer_Stages2And3_GearProgression(t *testing.T) {
	// Create a more complex test image: 4 colored quadrants
	img := image.NewRGBA(image.Rect(0, 0, 128, 128))
	colors := [4]color.RGBA{
		{255, 0, 0, 255},   // top-left: red
		{0, 255, 0, 255},   // top-right: green
		{0, 0, 255, 255},   // bottom-left: blue
		{255, 255, 0, 255}, // bottom-right: yellow
	}
	for y := 0; y < 128; y++ {
		for x := 0; x < 128; x++ {
			qi := 0
			if x >= 64 {
				qi += 1
			}
			if y >= 64 {
				qi += 2
			}
			img.Set(x, y, colors[qi])
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode test image: %v", err)
	}

	ch := make(chan ProgressResult, 20)
	pc := &ProgressiveCartographer{
		Scale:    10.0,
		GridSize: 8,
		Progress: ch,
	}

	_, err := pc.GenerateSchema(context.Background(), buf.Bytes(), "image/png", cairnTestSummary)
	if err != nil {
		t.Fatalf("GenerateSchema failed: %v", err)
	}
	close(ch)

	stageSchemas := make(map[int]string)
	for r := range ch {
		stageSchemas[r.Stage] = r.Schema
	}

	// Stages 2 (Gear 3) and 3 (Gear 5) must both be present
	for _, stage := range []int{2, 3} {
		s, ok := stageSchemas[stage]
		if !ok {
			t.Errorf("missing stage %d (%s)", stage, StageName(stage))
			continue
		}
		var output struct {
			Mounts []tropicalMount `json:"mounts"`
		}
		if err := json.Unmarshal([]byte(s), &output); err != nil {
			t.Errorf("stage %d: invalid JSON: %v", stage, err)
			continue
		}
		if len(output.Mounts) == 0 {
			t.Errorf("stage %d: 0 mounts", stage)
		}
		t.Logf("Stage %d (%s): %d mounts", stage, StageName(stage), len(output.Mounts))
	}
}

func BenchmarkProgressiveCartographer_ThroughGear5(b *testing.B) {
	// 64x64 minimal image
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		b.Fatal(err)
	}
	screenshot := buf.Bytes()

	pc := &ProgressiveCartographer{
		Scale:    10.0,
		GridSize: 4,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := pc.GenerateSchema(context.Background(), screenshot, "image/png", cairnTestSummary)
		if err != nil {
			b.Fatal(err)
		}
	}
}
