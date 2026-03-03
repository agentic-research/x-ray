package tasks

import (
	"testing"

	"github.com/agentic-research/mache/graph"
)

func TestGetNode_Root(t *testing.T) {
	g := New()
	node, err := g.GetNode("")
	if err != nil {
		t.Fatal(err)
	}
	if !node.Mode.IsDir() {
		t.Fatal("root should be directory")
	}
}

func TestGetNode_ActiveDir(t *testing.T) {
	g := New()
	node, err := g.GetNode("active")
	if err != nil {
		t.Fatal(err)
	}
	if !node.Mode.IsDir() {
		t.Fatal("active should be directory")
	}
	if len(node.Children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(node.Children))
	}
}

func TestSetTask_ReadBack(t *testing.T) {
	g := New()
	g.SetTask("Find reviewers who mention underwater")

	node, err := g.GetNode("active/task")
	if err != nil {
		t.Fatal(err)
	}
	if string(node.Data) != "Find reviewers who mention underwater" {
		t.Fatalf("unexpected task: %q", node.Data)
	}
}

func TestScratch_ActType(t *testing.T) {
	g := New()
	g.SetTask("test goal")

	// Write to scratch via Act
	result, err := g.Act("active/scratch", "type", "Found: Alice")
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "type" {
		t.Fatalf("unexpected action: %q", result.Action)
	}

	// Append more
	_, err = g.Act("active/scratch", "type", "Found: Bob")
	if err != nil {
		t.Fatal(err)
	}

	node, err := g.GetNode("active/scratch")
	if err != nil {
		t.Fatal(err)
	}
	expected := "Found: Alice\nFound: Bob"
	if string(node.Data) != expected {
		t.Fatalf("expected %q, got %q", expected, node.Data)
	}
}

func TestSetTask_ResetsScratch(t *testing.T) {
	g := New()
	g.SetTask("goal 1")
	g.AppendScratch("note 1")

	if g.Scratch() != "note 1" {
		t.Fatalf("expected scratch to have content")
	}

	// New goal clears scratch
	g.SetTask("goal 2")
	if g.Scratch() != "" {
		t.Fatalf("expected scratch to be cleared, got %q", g.Scratch())
	}
}

func TestClearTask(t *testing.T) {
	g := New()
	g.SetTask("some goal")
	g.AppendScratch("notes")
	g.ClearTask()

	node, _ := g.GetNode("active/task")
	if string(node.Data) != "" {
		t.Fatalf("expected empty task after clear")
	}
	node, _ = g.GetNode("active/scratch")
	if string(node.Data) != "" {
		t.Fatalf("expected empty scratch after clear")
	}
}

func TestAct_UnsupportedAction(t *testing.T) {
	g := New()
	_, err := g.Act("active/scratch", "click", "")
	if err != graph.ErrActNotSupported {
		t.Fatalf("expected ErrActNotSupported, got %v", err)
	}
}

func TestAct_UnsupportedPath(t *testing.T) {
	g := New()
	_, err := g.Act("active/task", "type", "text")
	if err != graph.ErrActNotSupported {
		t.Fatalf("expected ErrActNotSupported, got %v", err)
	}
}

func TestListChildren(t *testing.T) {
	g := New()

	// Root
	children, err := g.ListChildren("")
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 || children[0] != "active" {
		t.Fatalf("expected [active], got %v", children)
	}

	// Active
	children, err = g.ListChildren("active")
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(children))
	}
}

func TestReadContent(t *testing.T) {
	g := New()
	g.SetTask("hello world")

	buf := make([]byte, 100)
	n, err := g.ReadContent("active/task", buf, 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf[:n]) != "hello world" {
		t.Fatalf("unexpected: %q", buf[:n])
	}

	// Offset read
	n, err = g.ReadContent("active/task", buf, 6)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf[:n]) != "world" {
		t.Fatalf("unexpected offset read: %q", buf[:n])
	}
}

func TestGetNode_NotFound(t *testing.T) {
	g := New()
	_, err := g.GetNode("nonexistent")
	if err != graph.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
