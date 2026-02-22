package mache

import (
	"testing"
)

const sampleSchema = `{
  "mounts": [
    {
      "virtual_path": "/header/global_nav",
      "mache_id": "mache-0",
      "description": "Top navigation bar with logo and search"
    },
    {
      "virtual_path": "/main/news_feed",
      "mache_id": "mache-15",
      "description": "Main news article listing"
    },
    {
      "virtual_path": "/footer/links",
      "mache_id": "mache-200",
      "description": "Footer with legal and help links"
    }
  ]
}`

func TestApplySchema(t *testing.T) {
	e := NewEngine()
	if err := e.ApplySchema(sampleSchema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	// Root should have 3 top-level dirs
	entries, err := e.ListDir("/")
	if err != nil {
		t.Fatalf("ListDir / failed: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 root entries, got %d: %v", len(entries), entries)
	}
}

func TestListDir(t *testing.T) {
	e := NewEngine()
	if err := e.ApplySchema(sampleSchema); err != nil {
		t.Fatal(err)
	}

	entries, err := e.ListDir("/header/global_nav")
	if err != nil {
		t.Fatalf("ListDir failed: %v", err)
	}

	// Should contain mache_id and description files
	found := map[string]bool{}
	for _, name := range entries {
		found[name] = true
	}
	if !found["mache_id"] {
		t.Error("missing mache_id file")
	}
	if !found["description"] {
		t.Error("missing description file")
	}
}

func TestReadFile(t *testing.T) {
	e := NewEngine()
	if err := e.ApplySchema(sampleSchema); err != nil {
		t.Fatal(err)
	}

	content, err := e.ReadFile("/main/news_feed/description")
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if content != "Main news article listing" {
		t.Errorf("unexpected description: %q", content)
	}

	id, err := e.ReadFile("/main/news_feed/mache_id")
	if err != nil {
		t.Fatalf("ReadFile mache_id failed: %v", err)
	}
	if id != "mache-15" {
		t.Errorf("unexpected mache_id: %q", id)
	}
}

func TestResolveMacheID(t *testing.T) {
	e := NewEngine()
	if err := e.ApplySchema(sampleSchema); err != nil {
		t.Fatal(err)
	}

	id, err := e.ResolveMacheID("/header/global_nav")
	if err != nil {
		t.Fatalf("ResolveMacheID failed: %v", err)
	}
	if id != "mache-0" {
		t.Errorf("expected mache-0, got %q", id)
	}

	id, err = e.ResolveMacheID("/footer/links")
	if err != nil {
		t.Fatalf("ResolveMacheID footer failed: %v", err)
	}
	if id != "mache-200" {
		t.Errorf("expected mache-200, got %q", id)
	}
}

func TestResolveNotFound(t *testing.T) {
	e := NewEngine()
	if err := e.ApplySchema(sampleSchema); err != nil {
		t.Fatal(err)
	}

	_, err := e.ListDir("/nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent path")
	}

	_, err = e.ReadFile("/header/global_nav")
	if err == nil {
		t.Error("expected error reading a directory")
	}
}

func TestToTopology(t *testing.T) {
	e := NewEngine()
	if err := e.ApplySchema(sampleSchema); err != nil {
		t.Fatal(err)
	}

	topo := e.ToTopology()
	if topo.Version != "v1" {
		t.Errorf("expected v1, got %q", topo.Version)
	}
	if len(topo.Nodes) != 3 {
		t.Errorf("expected 3 nodes, got %d", len(topo.Nodes))
	}
}
