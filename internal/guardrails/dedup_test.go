package guardrails

import "testing"

func TestNormalize(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"Found: Alice Smith", "alice smith"},
		{"- Bob Jones", "bob jones"},
		{"* Charlie Brown", "charlie brown"},
		{"  hello   world  ", "hello world"},
		{"• bullet point", "bullet point"},
		{"", ""},
		{"  ", ""},
	}
	for _, tt := range tests {
		got := normalize(tt.input)
		if got != tt.want {
			t.Errorf("normalize(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestIsDuplicate(t *testing.T) {
	gs := New(1)
	gs.Enabled = true

	scratch := "Found: Alice Smith\nFound: Bob Jones"

	// Exact match after normalization.
	if !gs.IsDuplicate(scratch, "Found: Alice Smith") {
		t.Error("expected duplicate for exact match")
	}

	// Case + prefix variation.
	if !gs.IsDuplicate(scratch, "- alice smith") {
		t.Error("expected duplicate for normalized match")
	}

	// Not a duplicate.
	if gs.IsDuplicate(scratch, "Found: Charlie Brown") {
		t.Error("expected non-duplicate for new name")
	}

	// Now Charlie is tracked in FoundItems — duplicate even with empty scratch.
	if !gs.IsDuplicate("", "charlie brown") {
		t.Error("expected duplicate from FoundItems history")
	}
}

func TestIsDuplicate_Disabled(t *testing.T) {
	gs := New(1)
	gs.Enabled = false

	if gs.IsDuplicate("Found: Alice", "Found: Alice") {
		t.Error("guardrail should not fire when disabled")
	}
}

func TestIsDuplicate_Empty(t *testing.T) {
	gs := New(1)
	gs.Enabled = true

	if gs.IsDuplicate("some content", "") {
		t.Error("empty text should never be a duplicate")
	}
}
