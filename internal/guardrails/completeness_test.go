package guardrails

import "testing"

func TestExtractItemCount(t *testing.T) {
	tests := []struct {
		text string
		want int
	}{
		{"12 Reviews", 12},
		{"Reviews (8)", 8},
		{"Reviews: 5", 5},
		{"Showing 1-10 of 47", 47},
		{"47 results", 47},
		{"12 items", 12},
		{"8 comments", 8},
		{"15 posts", 15},
		{"no count here", -1},
		{"0 reviews", -1}, // 0 is not useful
		{"", -1},
	}
	for _, tt := range tests {
		got := ExtractItemCount(tt.text)
		if got != tt.want {
			t.Errorf("ExtractItemCount(%q) = %d, want %d", tt.text, got, tt.want)
		}
	}
}

func TestCountScratchItems(t *testing.T) {
	tests := []struct {
		scratch string
		want    int
	}{
		{"", 0},
		{"Found: Alice\nFound: Bob\nFound: Charlie", 3},
		{"Found: Alice\n\nFound: Bob", 2},
		{"visited: page 1, 2\nFound: Alice", 1},
		{"PAGES VISITED: x\nFound: Alice\nFound: Bob", 2},
		// Working notes should NOT be counted.
		{"Found: Alice\nSearching page 2\nFound: Bob\nChecked tab", 2},
		// Bullet-style findings.
		{"- Alice\n- Bob\n- Charlie", 3},
		{"* Alice\n* Bob", 2},
	}
	for _, tt := range tests {
		got := CountScratchItems(tt.scratch)
		if got != tt.want {
			t.Errorf("CountScratchItems(%q) = %d, want %d", tt.scratch, got, tt.want)
		}
	}
}

func TestCheckCompleteness(t *testing.T) {
	gs := New(1)
	gs.Enabled = true

	// No expected count — should return empty.
	if msg := gs.CheckCompleteness("Found: Alice"); msg != "" {
		t.Errorf("expected empty with unknown count, got %q", msg)
	}

	// Set expected count.
	gs.UpdateItemCount("12 Reviews")

	// Incomplete.
	msg := gs.CheckCompleteness("Found: Alice\nFound: Bob")
	if msg == "" {
		t.Error("expected warning for 2/12 items")
	}

	// Complete.
	scratch := ""
	for i := 0; i < 12; i++ {
		scratch += "Found: item\n"
	}
	if msg := gs.CheckCompleteness(scratch); msg != "" {
		t.Errorf("expected empty when complete, got %q", msg)
	}
}

func TestCheckCompleteness_Disabled(t *testing.T) {
	gs := New(1)
	gs.Enabled = false
	gs.UpdateItemCount("12 Reviews")

	if msg := gs.CheckCompleteness("Found: Alice"); msg != "" {
		t.Error("guardrail should not fire when disabled")
	}
}
