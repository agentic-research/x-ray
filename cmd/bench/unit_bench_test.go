package main

import (
	"math"
	"testing"
)

func TestZoneJaccard_Identical(t *testing.T) {
	schema := `{"mounts": [{"path": "/a"}, {"path": "/b"}, {"path": "/c"}]}`
	j := ZoneJaccard(schema, schema)
	if j != 1.0 {
		t.Errorf("Jaccard = %f, want 1.0 for identical schemas", j)
	}
}

func TestZoneJaccard_Disjoint(t *testing.T) {
	a := `{"mounts": [{"path": "/a"}, {"path": "/b"}]}`
	b := `{"mounts": [{"path": "/c"}, {"path": "/d"}]}`
	j := ZoneJaccard(a, b)
	if j != 0.0 {
		t.Errorf("Jaccard = %f, want 0.0 for disjoint schemas", j)
	}
}

func TestZoneJaccard_Overlap(t *testing.T) {
	a := `{"mounts": [{"path": "/a"}, {"path": "/b"}, {"path": "/c"}]}`
	b := `{"mounts": [{"path": "/b"}, {"path": "/c"}, {"path": "/d"}]}`
	j := ZoneJaccard(a, b)
	// Intersection: {/b, /c} = 2; Union: {/a, /b, /c, /d} = 4; J = 0.5
	if math.Abs(j-0.5) > 0.001 {
		t.Errorf("Jaccard = %f, want 0.5", j)
	}
}

func TestZoneJaccard_BothEmpty(t *testing.T) {
	j := ZoneJaccard(`{"mounts": []}`, `{"mounts": []}`)
	if j != 1.0 {
		t.Errorf("Jaccard = %f, want 1.0 for both empty", j)
	}
}

func TestZoneJaccard_InvalidJSON(t *testing.T) {
	j := ZoneJaccard("not json", `{"mounts": [{"path": "/a"}]}`)
	if j != 0.0 {
		t.Errorf("Jaccard = %f, want 0.0 for invalid JSON input", j)
	}
}

func TestZoneJaccard_VirtualPath(t *testing.T) {
	a := `{"mounts": [{"virtual_path": "/a"}, {"virtual_path": "/b"}]}`
	b := `{"mounts": [{"path": "/a"}, {"path": "/b"}]}`
	j := ZoneJaccard(a, b)
	if j != 1.0 {
		t.Errorf("Jaccard = %f, want 1.0 for matching virtual_path/path", j)
	}
}
