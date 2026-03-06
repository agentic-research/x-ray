package interactions

import (
	"fmt"
	"sync"
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
	if len(node.Children) != 6 {
		t.Fatalf("expected 6 children (id, intent, task, status, scratch, steps), got %d: %v", len(node.Children), node.Children)
	}
}

func TestStartAndFinish(t *testing.T) {
	g := New()
	g.StartInteraction("ix-1", "find reviewers")

	// Verify active fields.
	if s := g.Status(); s != "in_progress" {
		t.Fatalf("expected in_progress, got %q", s)
	}

	node, err := g.GetNode("active/intent")
	if err != nil {
		t.Fatal(err)
	}
	if string(node.Data) != "find reviewers" {
		t.Fatalf("unexpected intent: %q", node.Data)
	}

	node, err = g.GetNode("active/id")
	if err != nil {
		t.Fatal(err)
	}
	if string(node.Data) != "ix-1" {
		t.Fatalf("unexpected id: %q", node.Data)
	}

	// Finish moves to history.
	g.FinishInteraction("Found 3 reviewers")

	if s := g.Status(); s != "" {
		t.Fatalf("expected empty status after finish, got %q", s)
	}

	// Check history.
	children, err := g.ListChildren("history")
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 {
		t.Fatalf("expected 1 history entry, got %d", len(children))
	}

	node, err = g.GetNode("history/ix-1/summary")
	if err != nil {
		t.Fatal(err)
	}
	if string(node.Data) != "Found 3 reviewers" {
		t.Fatalf("unexpected summary: %q", node.Data)
	}
}

func TestStatus_NavigatorCompleted(t *testing.T) {
	g := New()
	g.StartInteraction("ix-1", "find X")

	_, err := g.Act("active/status", "type", "completed")
	if err != nil {
		t.Fatal(err)
	}

	if s := g.Status(); s != "completed" {
		t.Fatalf("expected completed, got %q", s)
	}
}

func TestStatus_NavigatorFailed(t *testing.T) {
	g := New()
	g.StartInteraction("ix-1", "find X")

	_, err := g.Act("active/status", "type", "failed:not found on page")
	if err != nil {
		t.Fatal(err)
	}

	if s := g.Status(); s != "failed:not found on page" {
		t.Fatalf("expected failed status, got %q", s)
	}
}

func TestStatus_InvalidValue(t *testing.T) {
	g := New()
	g.StartInteraction("ix-1", "find X")

	_, err := g.Act("active/status", "type", "garbage")
	if err == nil {
		t.Fatal("expected error for invalid status value")
	}

	// Status should be unchanged.
	if s := g.Status(); s != "in_progress" {
		t.Fatalf("expected in_progress after invalid set, got %q", s)
	}
}

func TestScratch_ActType(t *testing.T) {
	g := New()
	g.StartInteraction("ix-1", "test goal")

	result, err := g.Act("active/scratch", "type", "Found: Alice")
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "type" {
		t.Fatalf("unexpected action: %q", result.Action)
	}

	_, err = g.Act("active/scratch", "type", "Found: Bob")
	if err != nil {
		t.Fatal(err)
	}

	expected := "Found: Alice\nFound: Bob"
	if got := g.Scratch(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestScratch_DedupGuard(t *testing.T) {
	g := New()
	g.StartInteraction("ix-1", "test goal")

	blocked := 0
	g.SetDedupFunc(func(scratch, text string) bool {
		for _, line := range splitLines(scratch) {
			if line == text {
				blocked++
				return true
			}
		}
		return false
	})

	_, _ = g.Act("active/scratch", "type", "Found: Alice")
	_, _ = g.Act("active/scratch", "type", "Found: Alice") // duplicate
	_, _ = g.Act("active/scratch", "type", "Found: Bob")

	expected := "Found: Alice\nFound: Bob"
	if got := g.Scratch(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
	if blocked != 1 {
		t.Fatalf("expected 1 blocked write, got %d", blocked)
	}
}

func TestScratch_ClearedOnNewInteraction(t *testing.T) {
	g := New()
	g.StartInteraction("ix-1", "goal 1")
	_, _ = g.Act("active/scratch", "type", "note 1")

	if g.Scratch() != "note 1" {
		t.Fatal("expected scratch to have content")
	}

	g.StartInteraction("ix-2", "goal 2")
	if g.Scratch() != "" {
		t.Fatalf("expected scratch to be cleared, got %q", g.Scratch())
	}
}

func TestHistory_RingBuffer(t *testing.T) {
	g := New()
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("ix-%d", i)
		g.StartInteraction(id, "task "+id)
		g.FinishInteraction("done " + id)
	}

	children, err := g.ListChildren("history")
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != maxHistory {
		t.Fatalf("expected %d history entries, got %d", maxHistory, len(children))
	}

	// First entry should be ix-2 (0 and 1 evicted).
	node, err := g.GetNode("history/ix-2/intent")
	if err != nil {
		t.Fatal(err)
	}
	if string(node.Data) != "task ix-2" {
		t.Fatalf("expected oldest to be ix-2, got %q", node.Data)
	}
}

func TestSteps_AuditTrail(t *testing.T) {
	g := New()
	g.StartInteraction("ix-1", "find X")

	g.RecordStep("cat /browser/main/children")
	g.RecordStep("act _c/3 click")

	children, err := g.ListChildren("active/steps")
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(children))
	}

	node, err := g.GetNode("active/steps/0")
	if err != nil {
		t.Fatal(err)
	}
	if string(node.Data) != "cat /browser/main/children" {
		t.Fatalf("unexpected step 0: %q", node.Data)
	}
}

func TestBackwardsCompat_TaskAlias(t *testing.T) {
	g := New()
	g.StartInteraction("ix-1", "find reviewers")

	node, err := g.GetNode("active/task")
	if err != nil {
		t.Fatal(err)
	}
	if string(node.Data) != "find reviewers" {
		t.Fatalf("expected task alias to return intent, got %q", node.Data)
	}
}

func TestFilesystemLayout(t *testing.T) {
	g := New()
	g.StartInteraction("ix-1", "find X")

	// Root has active (no history yet).
	children, err := g.ListChildren("")
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 || children[0] != "active" {
		t.Fatalf("expected [active], got %v", children)
	}

	// Finish to create history.
	g.FinishInteraction("done")
	children, err = g.ListChildren("")
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 2 {
		t.Fatalf("expected [active, history], got %v", children)
	}
}

func TestReset(t *testing.T) {
	g := New()
	g.StartInteraction("ix-1", "goal")
	_, _ = g.Act("active/scratch", "type", "notes")
	g.FinishInteraction("done")

	g.Reset()

	if g.Status() != "" {
		t.Fatal("expected empty status after reset")
	}
	children, _ := g.ListChildren("")
	if len(children) != 1 {
		t.Fatalf("expected no history after reset, got children: %v", children)
	}
}

func TestReadContent(t *testing.T) {
	g := New()
	g.StartInteraction("ix-1", "hello world")

	buf := make([]byte, 100)
	n, err := g.ReadContent("active/intent", buf, 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf[:n]) != "hello world" {
		t.Fatalf("unexpected: %q", buf[:n])
	}

	// Offset read.
	n, err = g.ReadContent("active/intent", buf, 6)
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

func TestAct_UnsupportedAction(t *testing.T) {
	g := New()
	g.StartInteraction("ix-1", "test")
	_, err := g.Act("active/scratch", "click", "")
	if err != graph.ErrActNotSupported {
		t.Fatalf("expected ErrActNotSupported, got %v", err)
	}
}

func TestAct_UnsupportedPath(t *testing.T) {
	g := New()
	g.StartInteraction("ix-1", "test")
	_, err := g.Act("active/intent", "type", "text")
	if err != graph.ErrActNotSupported {
		t.Fatalf("expected ErrActNotSupported, got %v", err)
	}
}

func TestDedup_ClearedOnNil(t *testing.T) {
	g := New()
	g.StartInteraction("ix-1", "test goal")

	g.SetDedupFunc(func(_, _ string) bool { return true }) // block everything
	_, _ = g.Act("active/scratch", "type", "should be blocked")
	if g.Scratch() != "" {
		t.Fatal("expected empty scratch when all writes blocked")
	}

	g.SetDedupFunc(nil) // clear
	_, _ = g.Act("active/scratch", "type", "should succeed")
	if g.Scratch() != "should succeed" {
		t.Fatalf("expected write after clearing dedup, got %q", g.Scratch())
	}
}

func TestDedup_Race(t *testing.T) {
	g := New()
	g.StartInteraction("ix-race", "race test")

	g.SetDedupFunc(func(scratch, text string) bool {
		for _, line := range splitLines(scratch) {
			if line == text {
				return true
			}
		}
		return false
	})

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			text := []string{"Alice", "Bob", "Charlie", "Dave", "Eve"}[n%5]
			_, _ = g.Act("active/scratch", "type", text)
		}(i)
	}
	wg.Wait()

	lines := splitLines(g.Scratch())
	seen := make(map[string]int)
	for _, line := range lines {
		seen[line]++
	}

	if len(seen) != 5 {
		t.Errorf("expected 5 unique items, got %d: %v", len(seen), seen)
	}
	for name, count := range seen {
		if count != 1 {
			t.Errorf("item %q appeared %d times (expected 1)", name, count)
		}
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	lines = append(lines, s[start:])
	return lines
}
