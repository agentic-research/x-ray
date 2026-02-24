package api

import "testing"

func TestCacheKey(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://news.ycombinator.com/news?p=2", "news.ycombinator.com/news"},
		{"https://news.ycombinator.com/news?p=3", "news.ycombinator.com/news"},
		{"https://news.ycombinator.com/news", "news.ycombinator.com/news"},
		{"https://www.reddit.com/r/programming", "www.reddit.com/r/programming"},
		{"https://www.reddit.com/r/programming?sort=new", "www.reddit.com/r/programming"},
		{"https://example.com", "example.com/"},
		{"https://example.com/", "example.com/"},
		{"https://example.com/path/to/page#section", "example.com/path/to/page"},
		{"", ""},
		{"not-a-url", ""},
	}
	for _, tt := range tests {
		got := cacheKey(tt.url)
		if got != tt.want {
			t.Errorf("cacheKey(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestSchemaCacheHitMiss(t *testing.T) {
	c := newSchemaCache()

	// Miss on empty cache.
	if _, ok := c.Get("example.com/"); ok {
		t.Fatal("expected miss on empty cache")
	}

	// Put and hit.
	c.Put("example.com/", `{"mounts":[]}`)
	got, ok := c.Get("example.com/")
	if !ok {
		t.Fatal("expected hit after Put")
	}
	if got != `{"mounts":[]}` {
		t.Errorf("unexpected schema: %s", got)
	}

	// Overwrite same key.
	c.Put("example.com/", `{"mounts":[{"virtual_path":"/a"}]}`)
	got, _ = c.Get("example.com/")
	if got != `{"mounts":[{"virtual_path":"/a"}]}` {
		t.Errorf("overwrite failed: %s", got)
	}
}

func TestSchemaCacheEviction(t *testing.T) {
	c := newSchemaCache()

	// Fill to capacity.
	for i := range schemaCacheMaxSize {
		c.Put(cacheKey("https://example.com/"+string(rune('A'+i))), `{}`)
	}

	// All entries should be present.
	if _, ok := c.Get("example.com/A"); !ok {
		t.Fatal("expected oldest entry to exist at capacity")
	}

	// One more triggers eviction of the oldest ("A").
	c.Put("example.com/overflow", `{}`)

	if _, ok := c.Get("example.com/A"); ok {
		t.Fatal("expected oldest entry to be evicted")
	}
	if _, ok := c.Get("example.com/overflow"); !ok {
		t.Fatal("expected new entry to exist")
	}
}
