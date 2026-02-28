package ax

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Unit tests: JSON parsing (no binary required)
// ---------------------------------------------------------------------------

func TestParseAXJSON_SingleNode(t *testing.T) {
	input := `[{"role":"AXApplication","label":"Finder","bounds":[0,0,1,1],"children":[]}]`
	nodes, err := ParseAXJSON([]byte(input))
	if err != nil {
		t.Fatalf("ParseAXJSON: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(nodes))
	}
	if nodes[0].Role != "AXApplication" {
		t.Errorf("Role = %q, want AXApplication", nodes[0].Role)
	}
	if nodes[0].Label != "Finder" {
		t.Errorf("Label = %q, want Finder", nodes[0].Label)
	}
	if nodes[0].Bounds != [4]float64{0, 0, 1, 1} {
		t.Errorf("Bounds = %v, want [0,0,1,1]", nodes[0].Bounds)
	}
}

func TestParseAXJSON_NestedTree(t *testing.T) {
	input := `[{
		"role": "AXApplication",
		"label": "Finder",
		"bounds": [0, 0, 1, 1],
		"children": [
			{
				"role": "AXWindow",
				"label": "Documents",
				"bounds": [0.1, 0.1, 0.8, 0.8],
				"children": [
					{
						"role": "AXButton",
						"label": "Close",
						"bounds": [0.1, 0.1, 0.02, 0.02],
						"children": []
					},
					{
						"role": "AXButton",
						"label": "Minimize",
						"bounds": [0.13, 0.1, 0.02, 0.02],
						"children": []
					}
				]
			}
		]
	}]`

	nodes, err := ParseAXJSON([]byte(input))
	if err != nil {
		t.Fatalf("ParseAXJSON: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 root, got %d", len(nodes))
	}

	app := nodes[0]
	if len(app.Children) != 1 {
		t.Fatalf("expected 1 window, got %d children", len(app.Children))
	}

	window := app.Children[0]
	if window.Role != "AXWindow" {
		t.Errorf("window.Role = %q, want AXWindow", window.Role)
	}
	if len(window.Children) != 2 {
		t.Fatalf("expected 2 buttons, got %d", len(window.Children))
	}
	if window.Children[0].Label != "Close" {
		t.Errorf("first button = %q, want Close", window.Children[0].Label)
	}
}

func TestParseAXJSON_OptionalFields(t *testing.T) {
	// label and value can be omitted
	input := `[{"role":"AXGroup","bounds":[0,0,0.5,0.5],"children":[]}]`
	nodes, err := ParseAXJSON([]byte(input))
	if err != nil {
		t.Fatalf("ParseAXJSON: %v", err)
	}
	if nodes[0].Label != "" {
		t.Errorf("Label should be empty, got %q", nodes[0].Label)
	}
	if nodes[0].Value != "" {
		t.Errorf("Value should be empty, got %q", nodes[0].Value)
	}
}

func TestParseAXJSON_Empty(t *testing.T) {
	_, err := ParseAXJSON([]byte(""))
	if err == nil {
		t.Error("expected error for empty input")
	}
}

func TestParseAXJSON_InvalidJSON(t *testing.T) {
	_, err := ParseAXJSON([]byte("{not json"))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

// ---------------------------------------------------------------------------
// FlattenTree + ToSummaryLines
// ---------------------------------------------------------------------------

func TestFlattenTree(t *testing.T) {
	tree := []AXNode{
		{
			Role:   "AXApplication",
			Label:  "Finder",
			Bounds: [4]float64{0, 0, 1, 1},
			Children: []AXNode{
				{
					Role:   "AXWindow",
					Label:  "Home",
					Bounds: [4]float64{0.1, 0.1, 0.8, 0.8},
					Children: []AXNode{
						{
							Role:   "AXButton",
							Label:  "Close",
							Bounds: [4]float64{0.1, 0.1, 0.02, 0.02},
						},
					},
				},
			},
		},
	}

	flat := FlattenTree(tree)
	if len(flat) != 3 {
		t.Fatalf("expected 3 flat nodes, got %d", len(flat))
	}

	// Check IDs are sequential
	for i, n := range flat {
		expected := "ax-" + strings.TrimPrefix(n.ID, "ax-")
		if n.ID != expected {
			t.Errorf("node %d: ID = %q", i, n.ID)
		}
	}

	// Check paths
	if flat[0].Path != "AXApplication" {
		t.Errorf("root path = %q, want AXApplication", flat[0].Path)
	}
	if flat[1].Path != "AXApplication > AXWindow" {
		t.Errorf("window path = %q, want AXApplication > AXWindow", flat[1].Path)
	}
	if flat[2].Path != "AXApplication > AXWindow > AXButton" {
		t.Errorf("button path = %q", flat[2].Path)
	}

	// Check tag is lowercased role
	if flat[2].Tag != "axbutton" {
		t.Errorf("button tag = %q, want axbutton", flat[2].Tag)
	}

	// Check text falls back to value
	tree2 := []AXNode{
		{Role: "AXTextField", Value: "hello", Bounds: [4]float64{0, 0, 0.5, 0.05}},
	}
	flat2 := FlattenTree(tree2)
	if flat2[0].Text != "hello" {
		t.Errorf("text should fall back to value, got %q", flat2[0].Text)
	}
}

func TestToSummaryLines(t *testing.T) {
	nodes := []FlatNode{
		{ID: "ax-0", Tag: "axbutton", Text: "Close", Bounds: [4]float64{0.1, 0.1, 0.02, 0.02}, Path: "AXApplication > AXWindow > AXButton"},
	}
	lines := ToSummaryLines(nodes)
	if !strings.Contains(lines, "ID: ax-0") {
		t.Error("missing ID in summary line")
	}
	if !strings.Contains(lines, "Tag: axbutton") {
		t.Error("missing Tag in summary line")
	}
	if !strings.Contains(lines, `Text: "Close"`) {
		t.Error("missing Text in summary line")
	}
	if !strings.Contains(lines, "Bounds: [0.100, 0.100, 0.020, 0.020]") {
		t.Error("missing Bounds in summary line")
	}
	if !strings.Contains(lines, "Path: AXApplication > AXWindow > AXButton") {
		t.Error("missing Path in summary line")
	}
}

// ---------------------------------------------------------------------------
// Error classification
// ---------------------------------------------------------------------------

func TestClassifyError_BinaryNotFound(t *testing.T) {
	err := classifyError(&exec.Error{Name: "axdump", Err: exec.ErrNotFound}, context.Background())
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got: %v", err)
	}
}

func TestClassifyError_Timeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	time.Sleep(2 * time.Millisecond) // ensure timeout fires
	err := classifyError(ctx.Err(), ctx)
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("expected timeout error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Graceful failure: GetAppTree with nonexistent binary
// ---------------------------------------------------------------------------

func TestGetAppTree_NoBinary(t *testing.T) {
	old := DefaultBinary
	DefaultBinary = "/nonexistent/axdump-does-not-exist"
	defer func() { DefaultBinary = old }()

	_, err := GetAppTree(1, 10)
	if err == nil {
		t.Fatal("expected error when binary doesn't exist")
	}
	// Error may be "not found" (exec.Error) or "no such file" (os.PathError)
	errStr := err.Error()
	if !strings.Contains(errStr, "not found") && !strings.Contains(errStr, "no such file") {
		t.Errorf("expected path error, got: %v", err)
	}
}

func TestGetAppTree_BadPID(t *testing.T) {
	// PID -1 is invalid on all systems
	old := DefaultBinary
	DefaultBinary = "/nonexistent/axdump-does-not-exist"
	defer func() { DefaultBinary = old }()

	_, err := GetAppTree(-1, 10)
	if err == nil {
		t.Fatal("expected error for invalid PID")
	}
}

// ---------------------------------------------------------------------------
// Integration test: requires macOS + accessibility permission + axdump built
// ---------------------------------------------------------------------------

func TestIntegration_FinderTree(t *testing.T) {
	// Skip unless axdump is available and we're on macOS.
	if _, err := exec.LookPath("axdump"); err != nil {
		t.Skip("axdump binary not found in PATH — build with: swiftc cmd/axdump/main.swift -o axdump -framework ApplicationServices")
	}

	// Find Finder's PID (it's always running on macOS).
	out, err := exec.Command("pgrep", "-x", "Finder").Output()
	if err != nil {
		t.Skip("Finder not running (not macOS?)")
	}
	pidStr := strings.TrimSpace(string(out))
	pid, err := strconv.Atoi(strings.Split(pidStr, "\n")[0])
	if err != nil {
		t.Skipf("cannot parse Finder PID: %v", err)
	}

	nodes, err := GetAppTree(pid, 3)
	if err != nil {
		if strings.Contains(err.Error(), "permission") {
			t.Skip("accessibility permission not granted")
		}
		t.Fatalf("GetAppTree(Finder): %v", err)
	}

	if len(nodes) == 0 {
		t.Fatal("expected at least one AXNode from Finder")
	}

	// Finder's root should be AXApplication
	if nodes[0].Role != "AXApplication" {
		t.Errorf("root role = %q, want AXApplication", nodes[0].Role)
	}

	// Flatten and verify
	flat := FlattenTree(nodes)
	if len(flat) < 2 {
		t.Errorf("expected at least 2 flattened nodes from Finder, got %d", len(flat))
	}

	t.Logf("Finder AX tree: %d nodes flattened from %d roots", len(flat), len(nodes))
	for i, n := range flat {
		if i >= 5 {
			t.Logf("  ... and %d more", len(flat)-5)
			break
		}
		t.Logf("  %s [%s] %q", n.ID, n.Tag, n.Text)
	}
}
