package api

import "testing"

func TestIntentDedupKey(t *testing.T) {
	// Same intent + url = same key
	k1 := intentDedupKey("click the search box", "https://youtube.com/")
	k2 := intentDedupKey("click the search box", "https://youtube.com/")
	if k1 != k2 {
		t.Fatal("identical inputs should produce same key")
	}

	// Different intent = different key
	k3 := intentDedupKey("scroll down", "https://youtube.com/")
	if k1 == k3 {
		t.Fatal("different intents should produce different keys")
	}

	// Different URL = different key
	k4 := intentDedupKey("click the search box", "https://google.com/")
	if k1 == k4 {
		t.Fatal("different URLs should produce different keys")
	}

	// Case insensitive
	k5 := intentDedupKey("Click The Search Box", "https://youtube.com/")
	if k1 != k5 {
		t.Fatal("should be case insensitive")
	}

	// Whitespace trimmed
	k6 := intentDedupKey("  click the search box  ", "https://youtube.com/")
	if k1 != k6 {
		t.Fatal("should trim whitespace")
	}
}
