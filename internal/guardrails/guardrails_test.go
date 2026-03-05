package guardrails

import (
	"sync"
	"testing"
)

func TestRecordAction_LastActions(t *testing.T) {
	gs := New(1)
	gs.Enabled = true

	gs.RecordAction(0, "cat", "/browser/main/children", "")
	gs.RecordAction(1, "grep", "/browser/main", "mache-42: text")
	gs.RecordAction(2, "act", "mache-42", "clicked")

	last := gs.LastActions(2)
	if len(last) != 2 {
		t.Fatalf("expected 2 actions, got %d", len(last))
	}
	if last[0].Tool != "grep" {
		t.Errorf("expected grep, got %s", last[0].Tool)
	}
	if last[1].Tool != "act" {
		t.Errorf("expected act, got %s", last[1].Tool)
	}
}

func TestLastActions_MoreThanHistory(t *testing.T) {
	gs := New(1)
	gs.RecordAction(0, "cat", "/path", "")

	last := gs.LastActions(10)
	if len(last) != 1 {
		t.Fatalf("expected 1 action, got %d", len(last))
	}
}

func TestLastActions_Empty(t *testing.T) {
	gs := New(1)
	last := gs.LastActions(3)
	if len(last) != 0 {
		t.Fatalf("expected 0 actions, got %d", len(last))
	}
}

// TestRecordAction_Race verifies concurrent RecordAction + LastActions
// don't race. Run with -race flag.
func TestRecordAction_Race(t *testing.T) {
	gs := New(1)
	gs.Enabled = true

	var wg sync.WaitGroup

	// 20 writers.
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			gs.RecordAction(n, "cat", "/path", "result")
		}(i)
	}

	// 20 readers.
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = gs.LastActions(3)
		}()
	}

	wg.Wait()

	last := gs.LastActions(100)
	if len(last) != 20 {
		t.Errorf("expected 20 recorded actions, got %d", len(last))
	}
}

// TestIsDuplicate_Race verifies concurrent IsDuplicate calls don't race
// on FoundItems. Run with -race flag.
func TestIsDuplicate_Race(t *testing.T) {
	gs := New(1)
	gs.Enabled = true

	var wg sync.WaitGroup
	items := []string{"Alice", "Bob", "Charlie", "Dave", "Eve"}

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			gs.IsDuplicate("", items[n%5])
		}(i)
	}
	wg.Wait()

	gs.mu.RLock()
	count := len(gs.FoundItems)
	gs.mu.RUnlock()

	// Each of 5 items should appear exactly once.
	if count != 5 {
		t.Errorf("expected 5 unique FoundItems, got %d", count)
	}
}

// TestRefValidation_WindowIntegrity verifies that the 3-action lookback
// window correctly reflects the most recent tools, not stale history.
func TestRefValidation_WindowIntegrity(t *testing.T) {
	gs := New(1)
	gs.Enabled = true

	// Simulate: cat(children), then 3 other tools, then act(mache-N).
	// The cat(children) should be pushed out of the 3-item window.
	gs.RecordAction(0, "cat", "/browser/main/feed/children", "")
	gs.RecordAction(1, "act", "/browser/main/feed/_c/3", "clicked")
	gs.RecordAction(2, "cat", "/browser/main/feed/_c/3/description", "text")
	gs.RecordAction(3, "grep", "/browser/main", "mache-50: result")

	// mache-50 should be allowed — cat(children) is outside the window.
	msg := gs.ValidateActPath("mache-50")
	if msg != "" {
		t.Errorf("expected no error (children outside window), got %q", msg)
	}

	// Now if we record cat(children) again, it should block.
	gs.RecordAction(4, "cat", "/browser/sidebar/nav/children", "")
	msg = gs.ValidateActPath("mache-99")
	if msg == "" {
		t.Error("expected error for mache-99 after fresh cat(children)")
	}
}
