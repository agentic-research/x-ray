package guardrails

import "testing"

func TestNormalizeAnswer(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		// JSON array passthrough.
		{`["Alice", "Bob"]`, `["Alice","Bob"]`},
		// Python-style single quotes.
		{`['Alice', 'Bob']`, `["Alice","Bob"]`},
		// Comma-separated.
		{"Alice, Bob, Charlie", `["Alice","Bob","Charlie"]`},
		// "and"-separated (single line).
		{"Rachel and T. Gannon", `["Rachel","T. Gannon"]`},
		// Bullet list.
		{"* Alice\n* Bob", `["Alice","Bob"]`},
		{"- Alice\n- Bob", `["Alice","Bob"]`},
		// Newline-separated.
		{"Alice\nBob", `["Alice","Bob"]`},
		// Single value — trailing period stripped.
		{"Alice.", "Alice"},
		// Single value — no change needed.
		{"Alice", "Alice"},
		// Empty.
		{"", ""},
	}
	for _, tt := range tests {
		got := NormalizeAnswer(tt.input, true)
		if got != tt.want {
			t.Errorf("NormalizeAnswer(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestNormalizeAnswer_Disabled(t *testing.T) {
	raw := "['Alice', 'Bob']"
	got := NormalizeAnswer(raw, false)
	if got != raw {
		t.Errorf("expected passthrough when disabled, got %q", got)
	}
}

func TestNormalizeAnswer_MultiLineWithAnd(t *testing.T) {
	// CRITICAL-2 regression: "and" in a multi-line answer should NOT split on "and".
	// Newline-split should take priority.
	input := "Rachel and T. Gannon\nJohn Smith"
	got := NormalizeAnswer(input, true)
	want := `["Rachel and T. Gannon","John Smith"]`
	if got != want {
		t.Errorf("NormalizeAnswer(%q) = %q, want %q", input, got, want)
	}
}

func TestNormalizeAnswer_CommaSuffix(t *testing.T) {
	// HIGH-2: "Smith, Jr." should NOT be split on comma.
	input := "Smith, Jr."
	got := NormalizeAnswer(input, true)
	want := "Smith, Jr" // trailing period stripped, but not split
	if got != want {
		t.Errorf("NormalizeAnswer(%q) = %q, want %q", input, got, want)
	}
}
