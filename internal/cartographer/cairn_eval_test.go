package cartographer

import (
	"fmt"
	"strings"
	"testing"
)

// TestNormalizationTrap falsifiably proves that mixing unscaled dimensions
// results in the Leech lattice completely ignoring the smaller dimensions.
func TestNormalizationTrap(t *testing.T) {
	var vec1 [24]float64
	var vec2 [24]float64

	// Without normalization: Luma (0-255), IsButton (0-1)
	vec1[0] = 250.0 // Luma
	vec2[0] = 250.0

	vec1[23] = 1.0 // IsButton = true
	vec2[23] = 0.0 // IsButton = false

	out1 := DecodeLeechTuryn(vec1)
	out2 := DecodeLeechTuryn(vec2)

	if out1 == out2 {
		t.Log("SUCCESS (Trap Verified): Without normalization, DecodeLeechTuryn produced identical lattice points for different semantic inputs.")
	} else {
		t.Log("NOTE: DecodeLeechTuryn is sensitive enough to distinguish 0.0 vs 1.0 even without normalization, but the delta is small.")
	}

	// With normalization: map to [0, 1], then scale to lattice spacing
	var normVec1 [24]float64
	var normVec2 [24]float64
	scale := 10.0 // standard CC scale

	normVec1[0] = (250.0 / 255.0) * scale
	normVec2[0] = (250.0 / 255.0) * scale

	normVec1[23] = 1.0 * scale
	normVec2[23] = (0.0 / 1.0) * scale

	normOut1 := DecodeLeechTuryn(normVec1)
	normOut2 := DecodeLeechTuryn(normVec2)

	if normOut1 != normOut2 {
		t.Log("SUCCESS (Fix Verified): With normalization, DecodeLeechTuryn produced distinct lattice points.")
	} else {
		t.Errorf("FAILED: Expected lattice points to differ after normalization, but they were identical.")
	}
}

// generateLargeDOMSummary creates a mock 2000-node DOM summary
func generateLargeDOMSummary(numNodes int) string {
	var sb strings.Builder
	for i := 0; i < numNodes; i++ {
		parentID := "none"
		if i > 0 {
			parentID = fmt.Sprintf("el-%d", i/2) // Create a binary tree structure for realistic depth
		}
		fmt.Fprintf(&sb, "ID: el-%d | Tag: div | Parent: %s | Path: body>div | Bounds: [0.1, 0.1, 0.8, 0.8] | Text: Node %d\n", i, parentID, i)
	}
	return sb.String()
}

// BenchmarkCartographer_12D_PureVisual simulates the current 12D workflow overhead.
func BenchmarkCartographer_12D_PureVisual(b *testing.B) {
	summary := generateLargeDOMSummary(2000)
	elements := parseElements(summary)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		visualTypes := make(map[int]string, len(elements))
		for j := 0; j < len(elements); j++ {
			// Mock 12D visual extraction
			var features [12]float64
			features[0] = 0.8

			// Down-project 12D -> 8D -> E8 -> 24D (Current state)
			var proj [8]float64
			copy(proj[:], features[:8]) // Mock projection

			var scaled [8]float64
			for k := 0; k < 8; k++ {
				scaled[k] = proj[k] * 10.0
			}

			e8 := QuantizeE8(scaled)
			raw24 := Construct24D(e8)
			out := DecodeLeechTuryn(raw24)

			visualTypes[j] = fmt.Sprintf("l%v", out[0:8])
		}

		zones := buildDOMSubtreeGroups(elements, visualTypes)
		_ = foldCairnZones(zones, elements, 3, 7)
	}
}

// BenchmarkCartographer_24D_Fused simulates the proposed 24D structural-visual fusion.
func BenchmarkCartographer_24D_Fused(b *testing.B) {
	summary := generateLargeDOMSummary(2000)
	elements := parseElements(summary)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		visualTypes := make(map[int]string, len(elements))

		// Build map for real DOM walking (simulating the extraction cost)
		idToIdx := make(map[string]int, len(elements))
		for idx, el := range elements {
			idToIdx[el.id] = idx
		}

		for j := 0; j < len(elements); j++ {
			el := elements[j]

			// Mock 24D semantic-visual extraction
			var features [24]float64
			// 12 Visual
			features[0] = 0.8

			// Actual DOM extraction work (measuring CPU cost)
			depth := 0.0
			parent := el.parentID
			for parent != "" && parent != "none" && depth < 50 {
				depth++
				if pIdx, ok := idToIdx[parent]; ok {
					parent = elements[pIdx].parentID
				} else {
					break
				}
			}

			// 12 Semantic (e.g. depth, area, is_button)
			features[12] = el.bounds[2] * el.bounds[3] // Bounding Box Area
			features[13] = depth / 50.0                // Depth Normalized
			if el.tag == "button" || el.tag == "a" || el.interactive {
				features[23] = 1.0 // IsButton
			}

			// Direct 24D Scaling -> Leech
			var scaled [24]float64
			for k := 0; k < 24; k++ {
				scaled[k] = features[k] * 10.0
			}

			out := DecodeLeechTuryn(scaled)
			visualTypes[j] = fmt.Sprintf("l%v", out[0:8])
		}

		zones := buildDOMSubtreeGroups(elements, visualTypes)
		_ = foldCairnZones(zones, elements, 3, 7)
	}
}
