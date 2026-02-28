// Package ax provides access to the macOS Accessibility tree via a
// pre-compiled Swift CLI (cmd/axdump). The Go core stays pure — no CGo.
//
// The pattern follows the existing codebase convention:
//   - focus.GetFrontmostApp() calls osascript via os/exec
//   - iterm.Client communicates via WebSocket IPC
//   - ax.GetAppTree() calls axdump via os/exec
package ax

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// AXNode represents a single node in the macOS Accessibility tree.
type AXNode struct {
	Role     string     `json:"role"`
	Label    string     `json:"label,omitempty"`
	Value    string     `json:"value,omitempty"`
	Bounds   [4]float64 `json:"bounds"` // [x,y,w,h] normalized to screen
	Children []AXNode   `json:"children"`
}

// DefaultBinary is the name/path of the axdump binary.
// Override in tests or when the binary is installed elsewhere.
var DefaultBinary = "axdump"

// DefaultTimeout is the maximum time to wait for axdump to complete.
var DefaultTimeout = 5 * time.Second

// GetAppTree returns the Accessibility tree for the given PID.
// maxDepth caps recursion depth in the AX hierarchy (0 = app element only).
//
// Errors:
//   - Binary not found: axdump not installed or not in PATH
//   - Permission denied: Accessibility permission not granted (exit code 2)
//   - Invalid PID: process doesn't exist or has no AX tree
//   - Timeout: target app is hung
func GetAppTree(pid, maxDepth int) ([]AXNode, error) {
	return GetAppTreeWithContext(context.Background(), pid, maxDepth)
}

// GetAppTreeWithContext is like GetAppTree but accepts a context for cancellation.
func GetAppTreeWithContext(ctx context.Context, pid, maxDepth int) ([]AXNode, error) {
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx,
		DefaultBinary,
		"--pid", strconv.Itoa(pid),
		"--max-depth", strconv.Itoa(maxDepth),
	)

	out, err := cmd.Output()
	if err != nil {
		return nil, classifyError(err, ctx)
	}

	return ParseAXJSON(out)
}

// ParseAXJSON parses the JSON output from axdump into a slice of AXNode.
func ParseAXJSON(data []byte) ([]AXNode, error) {
	data = []byte(strings.TrimSpace(string(data)))
	if len(data) == 0 {
		return nil, fmt.Errorf("ax: empty output")
	}

	var nodes []AXNode
	if err := json.Unmarshal(data, &nodes); err != nil {
		return nil, fmt.Errorf("ax: parse JSON: %w", err)
	}
	return nodes, nil
}

// FlattenTree converts a tree of AXNodes into a flat slice with hierarchy
// paths suitable for the TropicalCartographer's element format.
// Each flattened node gets:
//   - id: "ax-N" (sequential)
//   - tag: AXRole lowercased
//   - text: Label or Value
//   - bounds: normalized [x,y,w,h]
//   - path: AX hierarchy (e.g., "AXApplication > AXWindow > AXButton")
type FlatNode struct {
	ID     string
	Tag    string
	Text   string
	Bounds [4]float64
	Path   string
}

// FlattenTree walks the AXNode tree depth-first and produces a flat slice.
func FlattenTree(roots []AXNode) []FlatNode {
	var result []FlatNode
	counter := 0

	var walk func(node AXNode, parentPath string)
	walk = func(node AXNode, parentPath string) {
		path := node.Role
		if parentPath != "" {
			path = parentPath + " > " + node.Role
		}

		text := node.Label
		if text == "" {
			text = node.Value
		}

		result = append(result, FlatNode{
			ID:     fmt.Sprintf("ax-%d", counter),
			Tag:    strings.ToLower(node.Role),
			Text:   text,
			Bounds: node.Bounds,
			Path:   path,
		})
		counter++

		for _, child := range node.Children {
			walk(child, path)
		}
	}

	for _, root := range roots {
		walk(root, "")
	}
	return result
}

// ToSummaryLines converts flattened AX nodes into the DOM summary format
// consumed by TropicalCartographer's parseElements(). Each line matches:
//
//	ID: ax-N | Tag: <role> | Text: "<text>" | Bounds: [x,y,w,h] | Path: <hierarchy>
func ToSummaryLines(nodes []FlatNode) string {
	var sb strings.Builder
	for _, n := range nodes {
		fmt.Fprintf(&sb,
			"ID: %s | Tag: %s | Text: %q | Bounds: [%.3f, %.3f, %.3f, %.3f] | Path: %s | Parent: none\n",
			n.ID, n.Tag, n.Text,
			n.Bounds[0], n.Bounds[1], n.Bounds[2], n.Bounds[3],
			n.Path,
		)
	}
	return sb.String()
}

// classifyError converts exec errors into descriptive messages.
func classifyError(err error, ctx context.Context) error {
	if ctx.Err() != nil {
		return fmt.Errorf("ax: timeout waiting for axdump (target app may be hung)")
	}

	if exitErr, ok := err.(*exec.ExitError); ok {
		switch exitErr.ExitCode() {
		case 1:
			return fmt.Errorf("ax: invalid arguments or PID")
		case 2:
			return fmt.Errorf("ax: accessibility permission not granted — enable in System Settings > Privacy & Security > Accessibility")
		case 3:
			return fmt.Errorf("ax: JSON encoding failed")
		default:
			stderr := strings.TrimSpace(string(exitErr.Stderr))
			if stderr != "" {
				return fmt.Errorf("ax: axdump failed (exit %d): %s", exitErr.ExitCode(), stderr)
			}
			return fmt.Errorf("ax: axdump failed (exit %d)", exitErr.ExitCode())
		}
	}

	if execErr, ok := err.(*exec.Error); ok {
		if execErr.Err == exec.ErrNotFound {
			return fmt.Errorf("ax: axdump binary not found — build with: swiftc cmd/axdump/main.swift -o axdump -framework ApplicationServices")
		}
	}

	return fmt.Errorf("ax: %w", err)
}
