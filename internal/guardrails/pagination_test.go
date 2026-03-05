package guardrails

import (
	"strings"
	"testing"
)

func TestRecordPageVisit_Basic(t *testing.T) {
	gs := New(1)
	gs.Enabled = true

	gs.RecordPageVisit("https://example.com/page", 0, 50)
	gs.RecordPageVisit("https://example.com/page", 50, 100)

	summary := gs.VisitedSummary()
	if !strings.Contains(summary, "PAGES VISITED:") {
		t.Errorf("expected PAGES VISITED prefix, got %q", summary)
	}
	if !strings.Contains(summary, "example.com/page") {
		t.Errorf("expected URL in summary, got %q", summary)
	}
}

func TestRecordPageVisit_Disabled(t *testing.T) {
	gs := New(1)
	gs.Enabled = false

	gs.RecordPageVisit("https://example.com/page", 0, 50)

	if gs.VisitedSummary() != "" {
		t.Error("expected empty summary when disabled")
	}
}

func TestVisitedSummary_Empty(t *testing.T) {
	gs := New(1)
	gs.Enabled = true

	if gs.VisitedSummary() != "" {
		t.Error("expected empty summary with no visits")
	}
}

func TestRecordPageVisit_Revisit(t *testing.T) {
	gs := New(1)
	gs.Enabled = true
	gs.debug = true // enable logging to verify revisit detection

	gs.RecordPageVisit("https://example.com/page", 0, 50)
	gs.RecordPageVisit("https://example.com/page", 0, 50) // revisit

	gs.mu.RLock()
	count := gs.VisitedPages["https://example.com/page#0-50"]
	gs.mu.RUnlock()

	if count != 2 {
		t.Errorf("expected visit count 2, got %d", count)
	}
}

func TestShortenURL(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"https://example.com/page", "example.com/page"},
		{"http://example.com/page", "example.com/page"},
		{"example.com/page", "example.com/page"},
	}
	for _, tt := range tests {
		got := shortenURL(tt.input)
		if got != tt.want {
			t.Errorf("shortenURL(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
