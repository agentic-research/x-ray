package navigator

import (
	"testing"

	"github.com/agentic-research/x-ray/internal/mache"
)

func TestNewSemanticProjection_BasicMounts(t *testing.T) {
	mounts := []mache.Mount{
		{
			VirtualPath: "/header/nav",
			MacheID:     "mache-0",
			Description: "Top navigation bar",
			Bounds:      [4]float64{0, 0, 1.0, 0.1}, // y < 0.15 -> header
		},
		{
			VirtualPath: "/main/stories",
			MacheID:     "mache-10",
			Description: "Main story listing",
			Bounds:      [4]float64{0.1, 0.2, 0.7, 0.6}, // middle -> main
		},
		{
			VirtualPath: "/footer/links",
			MacheID:     "mache-50",
			Description: "Footer navigation",
			Bounds:      [4]float64{0, 0.9, 1.0, 0.1}, // y > 0.85 -> footer
		},
	}

	summary := `Interactive Elements:
ID: mache-0 | Parent: none | Tag: nav | Text: "Navigation"
ID: mache-1 | Parent: mache-0 | Tag: a | Text: "Home"
ID: mache-2 | Parent: mache-0 | Tag: a | Text: "About"
ID: mache-10 | Parent: none | Tag: div | Text: "Stories"
ID: mache-11 | Parent: mache-10 | Tag: a | Text: "First Story Title"
ID: mache-13 | Parent: mache-10 | Tag: a | Text: "Second Story"
ID: mache-50 | Parent: none | Tag: footer | Text: "Footer"
`

	sp := NewSemanticProjection(mounts, summary)

	// Each mount should have a semantic path.
	if got := sp.SemanticPath("mache-0"); got == "" {
		t.Error("mache-0 should have a semantic path")
	}
	if got := sp.SemanticPath("mache-10"); got == "" {
		t.Error("mache-10 should have a semantic path")
	}
	if got := sp.SemanticPath("mache-50"); got == "" {
		t.Error("mache-50 should have a semantic path")
	}

	// Reverse lookup should work.
	for _, mid := range []string{"mache-0", "mache-10", "mache-50"} {
		sp1 := sp.SemanticPath(mid)
		got := sp.MacheID(sp1)
		if got != mid {
			t.Errorf("round-trip failed: MacheID(%q) = %q, want %q", sp1, got, mid)
		}
	}

	// Child elements from summary should also be projected.
	if got := sp.SemanticPath("mache-1"); got == "" {
		t.Error("mache-1 (child element 'Home' link) should have a semantic path")
	}
}

func TestNewSemanticProjection_RegionInference(t *testing.T) {
	tests := []struct {
		name   string
		bounds [4]float64
		want   string // expected region prefix
	}{
		{"top of page", [4]float64{0, 0, 1.0, 0.1}, "header"},
		{"bottom of page", [4]float64{0, 0.9, 1.0, 0.1}, "footer"},
		{"left column", [4]float64{0, 0.2, 0.2, 0.5}, "sidebar"},
		{"center content", [4]float64{0.25, 0.2, 0.5, 0.6}, "main"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inferRegion(tt.bounds)
			if got != tt.want {
				t.Errorf("inferRegion(%v) = %q, want %q", tt.bounds, got, tt.want)
			}
		})
	}
}

func TestNewSemanticProjection_Slugify(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"First Story Title", "first-story-title"},
		{"About Us & More", "about-us-more"},
		{"", "element"},
		{"A Very Long Label That Exceeds Thirty Characters Limit", "a-very-long-label-that-exceeds"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := slugify(tt.input, 30)
			if got != tt.want {
				t.Errorf("slugify(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNewSemanticProjection_CollisionResolution(t *testing.T) {
	// Two elements with the same text under the same parent zone.
	mounts := []mache.Mount{
		{
			VirtualPath: "/main/feed",
			MacheID:     "mache-10",
			Description: "Feed",
			Bounds:      [4]float64{0.1, 0.2, 0.7, 0.6},
		},
	}

	// Two "Read more" links under the same zone.
	summary := `Interactive Elements:
ID: mache-10 | Parent: none | Tag: div | Text: "Feed"
ID: mache-11 | Parent: mache-10 | Tag: a | Text: "Read more"
ID: mache-12 | Parent: mache-10 | Tag: a | Text: "Read more"
`

	sp := NewSemanticProjection(mounts, summary)

	path1 := sp.SemanticPath("mache-11")
	path2 := sp.SemanticPath("mache-12")

	if path1 == "" || path2 == "" {
		t.Fatal("both elements should have paths")
	}
	if path1 == path2 {
		t.Errorf("collision not resolved: both got %q", path1)
	}

	// One should have -2 suffix.
	t.Logf("path1=%q path2=%q", path1, path2)
}

func TestNewSemanticProjection_AllPaths(t *testing.T) {
	mounts := []mache.Mount{
		{
			VirtualPath: "/header/nav",
			MacheID:     "mache-0",
			Description: "Navigation",
			Bounds:      [4]float64{0, 0, 1.0, 0.1},
		},
	}

	summary := `Interactive Elements:
ID: mache-0 | Parent: none | Tag: nav | Text: "Navigation"
ID: mache-1 | Parent: mache-0 | Tag: a | Text: "Home"
ID: mache-2 | Parent: mache-0 | Tag: input | Text: "Search"
`

	sp := NewSemanticProjection(mounts, summary)
	paths := sp.AllPaths()

	if len(paths) == 0 {
		t.Fatal("AllPaths returned empty")
	}

	// Check that PathInfo fields are populated.
	for _, pi := range paths {
		if pi.Path == "" {
			t.Error("PathInfo.Path is empty")
		}
		if pi.Role == "" {
			t.Error("PathInfo.Role is empty for path:", pi.Path)
		}
	}

	// The input element should have role "input" and action "typeable".
	var foundInput bool
	for _, pi := range paths {
		if pi.Role == "input" {
			foundInput = true
			if pi.Action != "typeable" {
				t.Errorf("input element action = %q, want 'typeable'", pi.Action)
			}
		}
	}
	if !foundInput {
		t.Error("expected to find an input element in AllPaths")
	}
}

func TestSemanticProjection_UnknownIDReturnsEmpty(t *testing.T) {
	sp := NewSemanticProjection(nil, "")
	if got := sp.SemanticPath("mache-999"); got != "" {
		t.Errorf("unknown ID should return empty, got %q", got)
	}
	if got := sp.MacheID("/nonexistent/path"); got != "" {
		t.Errorf("unknown path should return empty, got %q", got)
	}
}

func TestInferRole(t *testing.T) {
	tests := []struct {
		tag  string
		want string
	}{
		{"a", "link"},
		{"A", "link"},
		{"input", "input"},
		{"button", "button"},
		{"select", "input"},
		{"textarea", "input"},
		{"h1", "heading"},
		{"h2", "heading"},
		{"h6", "heading"},
		{"img", "image"},
		{"svg", "image"},
		{"video", "media"},
		{"audio", "media"},
		{"div", "text"},
		{"span", "text"},
		{"nav", "text"},
		{"", "text"},
	}

	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			got := inferRole(tt.tag)
			if got != tt.want {
				t.Errorf("inferRole(%q) = %q, want %q", tt.tag, got, tt.want)
			}
		})
	}
}

func TestInferAction(t *testing.T) {
	tests := []struct {
		role string
		want string
	}{
		{"link", "clickable"},
		{"button", "clickable"},
		{"input", "typeable"},
		{"heading", "none"},
		{"text", "none"},
		{"image", "none"},
		{"media", "clickable"},
	}

	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			got := inferAction(tt.role)
			if got != tt.want {
				t.Errorf("inferAction(%q) = %q, want %q", tt.role, got, tt.want)
			}
		})
	}
}
