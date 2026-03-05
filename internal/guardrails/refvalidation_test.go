package guardrails

import "testing"

func TestValidateActPath_AfterChildren(t *testing.T) {
	gs := New(1)
	gs.Enabled = true

	gs.RecordAction(0, "cat", "/browser/main/feed/children", "")

	msg := gs.ValidateActPath("mache-16")
	if msg == "" {
		t.Error("expected error for mache-16 after cat(children)")
	}
	if !contains(msg, "_c/N") {
		t.Errorf("expected _c/N suggestion, got %q", msg)
	}
}

func TestValidateActPath_AfterGrep(t *testing.T) {
	gs := New(1)
	gs.Enabled = true

	gs.RecordAction(0, "grep", "/browser/main", "mache-42: some text")

	msg := gs.ValidateActPath("mache-42")
	if msg != "" {
		t.Errorf("expected no error after grep, got %q", msg)
	}
}

func TestValidateActPath_AfterTextIndex(t *testing.T) {
	gs := New(1)
	gs.Enabled = true

	gs.RecordAction(0, "cat", "/browser/main/feed/text_index", "")

	msg := gs.ValidateActPath("mache-10")
	if msg != "" {
		t.Errorf("expected no error after cat(text_index), got %q", msg)
	}
}

func TestValidateActPath_VirtualPath(t *testing.T) {
	gs := New(1)
	gs.Enabled = true

	gs.RecordAction(0, "cat", "/browser/main/feed/children", "")

	// Virtual paths should always pass through.
	msg := gs.ValidateActPath("/browser/main/feed/_c/3")
	if msg != "" {
		t.Errorf("expected no error for virtual path, got %q", msg)
	}
}

func TestValidateActPath_Disabled(t *testing.T) {
	gs := New(1)
	gs.Enabled = false

	gs.RecordAction(0, "cat", "/browser/main/feed/children", "")

	if msg := gs.ValidateActPath("mache-16"); msg != "" {
		t.Error("guardrail should not fire when disabled")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
