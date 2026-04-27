# Semantic Path Projection Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace opaque mache-IDs with semantic paths (`/browser/header/search-input` instead of `mache-42`) and simplify the tool vocabulary from 12 to 5, so the model can navigate without excavating its own data structure.

**Architecture:** New projection layer between CartographerOutput and Navigator. Transforms zone paths to encode region + role + label. New simplified tool set (find, act, scroll, answer, look) behind a config flag. Both opt-in, existing behavior unchanged by default.

**Tech Stack:** Go, existing navigator/mache abstractions

**Beads:** x-ray-263555, x-ray-264d9c

---

## File Structure

| File | Action | Responsibility |
|------|--------|----------------|
| `internal/navigator/projection.go` | Create | SemanticProjection: Mount[] + summary -> semantic PathInfo[] with bidirectional lookup |
| `internal/navigator/projection_test.go` | Create | Unit tests for projection rules, collision handling, region inference |
| `internal/navigator/tools_simplified.go` | Create | FindTool, LookTool, SemanticActTool wrappers |
| `internal/navigator/tools_simplified_test.go` | Create | Unit tests for simplified tools against known projections |
| `internal/config/config.go` | Modify | Add `Tools` and `Projection` fields to NavigatorConfig |
| `internal/navigator/agent.go` | Modify | Conditional tool registration, projection lifecycle on SetGraph |
| `internal/navigator/agent_test.go` | Modify | Add test cases for simplified tool set |

---

### Task 1: Projection layer -- transform Mount paths to semantic paths

Create `internal/navigator/projection.go` with the core data structure that maps between mache-IDs and human-readable semantic paths.

**Files:**
- Create: `internal/navigator/projection.go`
- Create: `internal/navigator/projection_test.go`

- [ ] **Step 1: Write failing tests for NewSemanticProjection with known Mount data**

Create `internal/navigator/projection_test.go`:

```go
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
		{"A Very Long Label That Exceeds Thirty Characters Limit", "a-very-long-label-that-exceed"},
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
```

Run: `cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go test ./internal/navigator/ -run TestNewSemanticProjection -count=1`
Expect: compilation failure (types not defined yet).

- [ ] **Step 2: Write failing tests for role and action inference from DOM tags**

Add to `internal/navigator/projection_test.go`:

```go
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
```

Run: `cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go test ./internal/navigator/ -run "TestInferRole|TestInferAction" -count=1`
Expect: compilation failure.

- [ ] **Step 3: Implement SemanticProjection, inferRegion, inferRole, inferAction, slugify**

Create `internal/navigator/projection.go`:

```go
package navigator

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/agentic-research/x-ray/internal/mache"
)

// PathInfo describes a single element in the semantic projection.
type PathInfo struct {
	Path   string `json:"path"`
	Text   string `json:"text"`
	Role   string `json:"role"`   // link, input, button, heading, image, media, text
	Action string `json:"action"` // clickable, focusable, typeable, none
}

// SemanticProjection maps between opaque mache-IDs and human-readable
// semantic paths like /browser/header/search-input. Built from Cartographer
// Mounts plus the DOM summary.
type SemanticProjection struct {
	pathToMacheID map[string]string // semantic path -> mache-ID
	macheIDToPath map[string]string // mache-ID -> semantic path
	allPaths      []PathInfo
}

// parsedElement holds a single element parsed from the DOM summary.
type parsedElement struct {
	id     string
	parent string
	tag    string
	text   string
}

// NewSemanticProjection builds a bidirectional mapping from Mounts and DOM summary.
//
// Projection pipeline:
//  1. Parse DOM summary into elements (id, parent, tag, text).
//  2. For each Mount, infer region from bounds (header/footer/sidebar/main).
//  3. Assign the mount's root element a semantic path: /<region>/<slugified-description>.
//  4. For each child element under that mount's mache-ID, build:
//     /<region>/<zone-slug>/<slugified-text>
//  5. Resolve collisions by appending -2, -3, etc.
func NewSemanticProjection(mounts []mache.Mount, summary string) *SemanticProjection {
	sp := &SemanticProjection{
		pathToMacheID: make(map[string]string),
		macheIDToPath: make(map[string]string),
	}

	elements := parseSummaryElements(summary)
	elemByID := make(map[string]*parsedElement, len(elements))
	for i := range elements {
		elemByID[elements[i].id] = &elements[i]
	}

	// Build parent -> children index.
	childrenOf := make(map[string][]string)
	for _, e := range elements {
		if e.parent != "" && e.parent != "none" {
			childrenOf[e.parent] = append(childrenOf[e.parent], e.id)
		}
	}

	// Track used paths for collision resolution.
	used := make(map[string]int)

	for _, m := range mounts {
		region := inferRegion(m.Bounds)
		zoneSlug := slugify(m.Description, 30)
		if zoneSlug == "element" && m.VirtualPath != "" {
			// Fall back to last path segment.
			parts := strings.Split(strings.Trim(m.VirtualPath, "/"), "/")
			zoneSlug = slugify(parts[len(parts)-1], 30)
		}

		// Register the zone root.
		zonePath := "/browser/" + region + "/" + zoneSlug
		zonePath = dedup(zonePath, used)
		sp.register(zonePath, m.MacheID, elemByID)

		// Register child elements.
		for _, childID := range childrenOf[m.MacheID] {
			child, ok := elemByID[childID]
			if !ok {
				continue
			}
			childSlug := slugify(child.text, 30)
			childPath := zonePath + "/" + childSlug
			childPath = dedup(childPath, used)
			sp.register(childPath, childID, elemByID)
		}
	}

	return sp
}

// register adds a bidirectional mapping and builds the PathInfo entry.
func (sp *SemanticProjection) register(path, macheID string, elemByID map[string]*parsedElement) {
	sp.pathToMacheID[path] = macheID
	sp.macheIDToPath[macheID] = path

	pi := PathInfo{Path: path}
	if e, ok := elemByID[macheID]; ok {
		pi.Text = e.text
		pi.Role = inferRole(e.tag)
		pi.Action = inferAction(pi.Role)
	} else {
		pi.Role = "text"
		pi.Action = "none"
	}
	sp.allPaths = append(sp.allPaths, pi)
}

// SemanticPath returns the semantic path for a mache-ID, or "" if unknown.
func (sp *SemanticProjection) SemanticPath(macheID string) string {
	return sp.macheIDToPath[macheID]
}

// MacheID returns the mache-ID for a semantic path, or "" if unknown.
func (sp *SemanticProjection) MacheID(semanticPath string) string {
	return sp.pathToMacheID[semanticPath]
}

// AllPaths returns all projected paths with metadata.
func (sp *SemanticProjection) AllPaths() []PathInfo {
	return sp.allPaths
}

// --- Helper functions ---

// inferRegion determines the page region from normalized zone bounds [x,y,w,h].
func inferRegion(bounds [4]float64) string {
	y := bounds[1]
	h := bounds[3]
	x := bounds[0]
	w := bounds[2]

	// Use the vertical center of the zone for region classification.
	centerY := y + h/2
	centerX := x + w/2

	switch {
	case centerY < 0.15:
		return "header"
	case centerY > 0.85:
		return "footer"
	case centerX < 0.25 && w < 0.35:
		return "sidebar"
	default:
		return "main"
	}
}

// inferRole maps a DOM tag name to a semantic role.
func inferRole(tag string) string {
	switch strings.ToLower(tag) {
	case "a":
		return "link"
	case "button":
		return "button"
	case "input", "select", "textarea":
		return "input"
	case "h1", "h2", "h3", "h4", "h5", "h6":
		return "heading"
	case "img", "svg":
		return "image"
	case "video", "audio":
		return "media"
	default:
		return "text"
	}
}

// inferAction determines the primary interaction for a role.
func inferAction(role string) string {
	switch role {
	case "link", "button", "media":
		return "clickable"
	case "input":
		return "typeable"
	default:
		return "none"
	}
}

// slugify converts text to a URL-safe slug: lowercase, spaces to hyphens,
// non-alphanumeric stripped, truncated to maxLen. Returns "element" for empty input.
func slugify(s string, maxLen int) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "element"
	}

	// Replace non-alphanumeric with hyphens.
	re := regexp.MustCompile(`[^a-z0-9]+`)
	s = re.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")

	if s == "" {
		return "element"
	}

	if len(s) > maxLen {
		s = s[:maxLen]
		// Don't end on a hyphen.
		s = strings.TrimRight(s, "-")
	}
	return s
}

// dedup appends -2, -3, etc. if the path is already used.
func dedup(path string, used map[string]int) string {
	used[path]++
	if used[path] == 1 {
		return path
	}
	return fmt.Sprintf("%s-%d", path, used[path])
}

// summaryLineRe parses lines like:
// ID: mache-11 | Parent: mache-10 | Tag: a | Text: "First Story Title"
var summaryLineRe = regexp.MustCompile(
	`ID:\s*(\S+)\s*\|\s*Parent:\s*(\S+)\s*\|\s*Tag:\s*(\S+)\s*\|\s*Text:\s*"([^"]*)"`,
)

// parseSummaryElements extracts structured elements from the DOM summary text.
func parseSummaryElements(summary string) []parsedElement {
	var elems []parsedElement
	for _, line := range strings.Split(summary, "\n") {
		m := summaryLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		elems = append(elems, parsedElement{
			id:     m[1],
			parent: m[2],
			tag:    m[3],
			text:   m[4],
		})
	}
	return elems
}
```

Run: `cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go test ./internal/navigator/ -run "TestNewSemanticProjection|TestInferRole|TestInferAction|TestSemanticProjection" -count=1 -v`
Expect: all tests pass.

Commit: `[x-ray-263555] feat(navigator): add SemanticProjection with region/role/label inference`

---

### Task 2: FindTool -- ranked search returning PathInfo results

Create the `FindTool` that searches all projected paths by fuzzy text match. Returns top 5 ranked results as formatted text the model can directly use with `act()`.

**Files:**
- Create: `internal/navigator/tools_simplified.go`
- Create: `internal/navigator/tools_simplified_test.go`

- [ ] **Step 1: Write failing tests for FindTool**

Create `internal/navigator/tools_simplified_test.go`:

```go
package navigator

import (
	"context"
	"strings"
	"testing"

	"github.com/agentic-research/x-ray/internal/mache"
)

// testProjection builds a SemanticProjection from the standard test fixtures.
func testProjection() *SemanticProjection {
	mounts := []mache.Mount{
		{
			VirtualPath: "/header/nav",
			MacheID:     "mache-0",
			Description: "Top navigation bar",
			Bounds:      [4]float64{0, 0, 1.0, 0.1},
		},
		{
			VirtualPath: "/main/stories",
			MacheID:     "mache-10",
			Description: "Main story listing",
			Bounds:      [4]float64{0.1, 0.2, 0.7, 0.6},
		},
	}

	summary := `Interactive Elements:
ID: mache-0 | Parent: none | Tag: nav | Text: "Navigation"
ID: mache-1 | Parent: mache-0 | Tag: a | Text: "Home"
ID: mache-2 | Parent: mache-0 | Tag: a | Text: "About"
ID: mache-3 | Parent: mache-0 | Tag: input | Text: "Search"
ID: mache-10 | Parent: none | Tag: div | Text: "Stories"
ID: mache-11 | Parent: mache-10 | Tag: a | Text: "First Story Title"
ID: mache-13 | Parent: mache-10 | Tag: a | Text: "Second Story"
`
	return NewSemanticProjection(mounts, summary)
}

func TestFindTool_BasicSearch(t *testing.T) {
	ft := &FindTool{projection: testProjection()}

	result, action := ft.Execute(context.Background(), map[string]any{
		"query": "search",
	})

	if action != nil {
		t.Fatal("find should not return an action")
	}
	if result == "" {
		t.Fatal("expected non-empty result")
	}
	// Should match the search input element.
	if !strings.Contains(strings.ToLower(result), "search") {
		t.Errorf("result should contain 'search': %s", result)
	}
	// Should include a semantic path.
	if !strings.Contains(result, "/browser/") {
		t.Errorf("result should contain a semantic path: %s", result)
	}
	t.Logf("find result:\n%s", result)
}

func TestFindTool_NoMatch(t *testing.T) {
	ft := &FindTool{projection: testProjection()}

	result, _ := ft.Execute(context.Background(), map[string]any{
		"query": "zzzznonexistent",
	})

	if !strings.Contains(result, "No matches") {
		t.Errorf("expected 'No matches' message, got: %s", result)
	}
}

func TestFindTool_EmptyQuery(t *testing.T) {
	ft := &FindTool{projection: testProjection()}

	result, _ := ft.Execute(context.Background(), map[string]any{
		"query": "",
	})

	if !strings.Contains(result, "Error") {
		t.Errorf("expected error for empty query, got: %s", result)
	}
}

func TestFindTool_RoleFilter(t *testing.T) {
	ft := &FindTool{projection: testProjection()}

	// Searching for "home" should find the Home link.
	result, _ := ft.Execute(context.Background(), map[string]any{
		"query": "home",
	})

	if !strings.Contains(result, "link") {
		t.Errorf("expected result to show 'link' role: %s", result)
	}
}

func TestFindTool_TopNLimit(t *testing.T) {
	ft := &FindTool{projection: testProjection()}

	// Broad query that matches many things.
	result, _ := ft.Execute(context.Background(), map[string]any{
		"query": "a",
	})

	// Count result lines (non-empty).
	lines := 0
	for _, line := range strings.Split(result, "\n") {
		if strings.TrimSpace(line) != "" {
			lines++
		}
	}
	// Should not exceed 5 results (1 line each + header possible).
	if lines > 6 {
		t.Errorf("expected at most 5 results, got %d lines:\n%s", lines, result)
	}
}
```

Run: `cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go test ./internal/navigator/ -run TestFindTool -count=1`
Expect: compilation failure (FindTool not defined).

- [ ] **Step 2: Implement FindTool**

Add to `internal/navigator/tools_simplified.go`:

```go
package navigator

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"google.golang.org/genai"
)

// FindTool searches the semantic projection for elements matching a query.
// Returns top 5 ranked matches with path, role, action, and text.
type FindTool struct {
	projection *SemanticProjection
}

func (f *FindTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "find",
		Description: "Search for page elements by text or role. Returns the top 5 matches with their semantic paths, roles, and actions. Use the returned path with act() to interact.",
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"query": {Type: genai.TypeString, Description: "Text to search for (case-insensitive). Matches against element text, path segments, and roles."},
			},
			Required: []string{"query"},
		},
	}
}

// scored pairs a PathInfo with a relevance score for ranking.
type scored struct {
	pi    PathInfo
	score int
}

func (f *FindTool) Execute(_ context.Context, args map[string]any) (string, *ActionResult) {
	query, _ := args["query"].(string)
	if query == "" {
		return "Error: query is required", nil
	}

	lower := strings.ToLower(query)
	var results []scored

	for _, pi := range f.projection.AllPaths() {
		score := 0
		// Text match (strongest signal).
		if strings.Contains(strings.ToLower(pi.Text), lower) {
			score += 10
		}
		// Path match.
		if strings.Contains(strings.ToLower(pi.Path), lower) {
			score += 5
		}
		// Role match.
		if strings.Contains(strings.ToLower(pi.Role), lower) {
			score += 3
		}
		// Action match.
		if strings.Contains(strings.ToLower(pi.Action), lower) {
			score += 2
		}
		if score > 0 {
			results = append(results, scored{pi: pi, score: score})
		}
	}

	if len(results) == 0 {
		return fmt.Sprintf("No matches for %q", query), nil
	}

	// Sort by score descending, then path ascending for stability.
	sort.Slice(results, func(i, j int) bool {
		if results[i].score != results[j].score {
			return results[i].score > results[j].score
		}
		return results[i].pi.Path < results[j].pi.Path
	})

	// Cap at 5 results.
	if len(results) > 5 {
		results = results[:5]
	}

	var sb strings.Builder
	for _, r := range results {
		fmt.Fprintf(&sb, "%s  [%s, %s]", r.pi.Path, r.pi.Role, r.pi.Action)
		if r.pi.Text != "" {
			fmt.Fprintf(&sb, " %q", r.pi.Text)
		}
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}
```

Run: `cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go test ./internal/navigator/ -run TestFindTool -count=1 -v`
Expect: all tests pass.

Commit: `[x-ray-263555] feat(navigator): add FindTool with ranked fuzzy search over semantic paths`

---

### Task 3: LookTool -- zone exploration

Create the `LookTool` that shows all children of a zone (or top-level zones if omitted). Replaces `ls` + `cat` + `stat` in a single call.

**Files:**
- Modify: `internal/navigator/tools_simplified.go`
- Modify: `internal/navigator/tools_simplified_test.go`

- [ ] **Step 1: Write failing tests for LookTool**

Add to `internal/navigator/tools_simplified_test.go`:

```go
func TestLookTool_TopLevel(t *testing.T) {
	lt := &LookTool{projection: testProjection()}

	// Omit zone -> show top-level zones.
	result, action := lt.Execute(context.Background(), map[string]any{})

	if action != nil {
		t.Fatal("look should not return an action")
	}
	if result == "" {
		t.Fatal("expected non-empty result")
	}
	// Should contain the regions from our test data.
	if !strings.Contains(result, "header") {
		t.Errorf("expected 'header' zone in result: %s", result)
	}
	if !strings.Contains(result, "main") {
		t.Errorf("expected 'main' zone in result: %s", result)
	}
	t.Logf("look top-level:\n%s", result)
}

func TestLookTool_ZoneChildren(t *testing.T) {
	sp := testProjection()
	lt := &LookTool{projection: sp}

	// Find a zone path that exists (the header zone).
	var headerZone string
	for _, pi := range sp.AllPaths() {
		if strings.Contains(pi.Path, "header") && !strings.Contains(pi.Path[len("/browser/header/"):], "/") {
			headerZone = pi.Path
			break
		}
	}
	if headerZone == "" {
		t.Skip("no header zone found in projection")
	}

	result, _ := lt.Execute(context.Background(), map[string]any{
		"zone": headerZone,
	})

	// Should list children of the header zone.
	if !strings.Contains(result, "Home") || !strings.Contains(result, "About") {
		t.Errorf("expected header children (Home, About) in result: %s", result)
	}
	t.Logf("look zone:\n%s", result)
}

func TestLookTool_UnknownZone(t *testing.T) {
	lt := &LookTool{projection: testProjection()}

	result, _ := lt.Execute(context.Background(), map[string]any{
		"zone": "/browser/nonexistent/zone",
	})

	// Should return a helpful message, not crash.
	if !strings.Contains(result, "No elements found") && !strings.Contains(result, "not found") {
		t.Errorf("expected error-ish message for unknown zone: %s", result)
	}
}
```

Run: `cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go test ./internal/navigator/ -run TestLookTool -count=1`
Expect: compilation failure (LookTool not defined).

- [ ] **Step 2: Implement LookTool**

Add to `internal/navigator/tools_simplified.go`:

```go
// LookTool shows the children of a zone (or top-level zones if omitted).
// Replaces ls + cat + stat with a single call that returns semantic paths,
// roles, and element text.
type LookTool struct {
	projection *SemanticProjection
}

func (l *LookTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "look",
		Description: "Show page zones and their elements. Without a zone path, shows all top-level zones. With a zone path, shows all elements inside that zone with their roles and text.",
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"zone": {Type: genai.TypeString, Description: "Optional: semantic zone path (e.g., '/browser/header/nav'). Omit to see all zones."},
			},
		},
	}
}

func (l *LookTool) Execute(_ context.Context, args map[string]any) (string, *ActionResult) {
	zone, _ := args["zone"].(string)

	allPaths := l.projection.AllPaths()
	if len(allPaths) == 0 {
		return "No page elements available. Use browser.goto() to load a page first.", nil
	}

	if zone == "" {
		return l.showTopLevel(allPaths), nil
	}

	return l.showZoneChildren(zone, allPaths), nil
}

// showTopLevel groups paths by their zone (first two path segments after /browser/)
// and shows a summary of each zone.
func (l *LookTool) showTopLevel(allPaths []PathInfo) string {
	// Group by zone: /browser/<region>/<zone-slug>
	zones := make(map[string][]PathInfo)
	var zoneOrder []string
	for _, pi := range allPaths {
		parts := strings.Split(pi.Path, "/")
		// /browser/region/zone -> parts = ["", "browser", "region", "zone", ...]
		if len(parts) < 4 {
			continue
		}
		zoneKey := strings.Join(parts[:4], "/")
		if _, exists := zones[zoneKey]; !exists {
			zoneOrder = append(zoneOrder, zoneKey)
		}
		zones[zoneKey] = append(zones[zoneKey], pi)
	}

	var sb strings.Builder
	for _, zk := range zoneOrder {
		children := zones[zk]
		// Count interactive elements.
		interactive := 0
		for _, pi := range children {
			if pi.Action != "none" {
				interactive++
			}
		}
		fmt.Fprintf(&sb, "%s  (%d elements, %d interactive)\n", zk, len(children), interactive)
	}

	if sb.Len() == 0 {
		return "No zones found."
	}
	return strings.TrimRight(sb.String(), "\n")
}

// showZoneChildren lists all elements whose path starts with the given zone prefix.
func (l *LookTool) showZoneChildren(zone string, allPaths []PathInfo) string {
	// Normalize: ensure zone doesn't end with /
	zone = strings.TrimRight(zone, "/")

	var sb strings.Builder
	count := 0
	for _, pi := range allPaths {
		if pi.Path == zone || strings.HasPrefix(pi.Path, zone+"/") {
			fmt.Fprintf(&sb, "%s  [%s, %s]", pi.Path, pi.Role, pi.Action)
			if pi.Text != "" {
				fmt.Fprintf(&sb, " %q", pi.Text)
			}
			sb.WriteByte('\n')
			count++
		}
	}

	if count == 0 {
		return fmt.Sprintf("No elements found under %q. Use look() without arguments to see available zones.", zone)
	}
	return strings.TrimRight(sb.String(), "\n")
}
```

Run: `cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go test ./internal/navigator/ -run TestLookTool -count=1 -v`
Expect: all tests pass.

Commit: `[x-ray-263555] feat(navigator): add LookTool for single-call zone exploration`

---

### Task 4: SemanticActTool -- accepts semantic paths and resolves to mache-ID

Wrap the existing `ActTool` so it accepts semantic paths like `/browser/header/search-input` and resolves them to the underlying mache-ID before delegating.

**Files:**
- Modify: `internal/navigator/tools_simplified.go`
- Modify: `internal/navigator/tools_simplified_test.go`

- [ ] **Step 1: Write failing tests for SemanticActTool**

Add to `internal/navigator/tools_simplified_test.go`:

```go
func TestSemanticActTool_ResolvesPath(t *testing.T) {
	sp := testProjection()

	// Build a minimal ActTool backed by the standard test agent's NavFS.
	agent := newTestAgent()
	inner := agent.actTool

	sat := &SemanticActTool{
		inner:      inner,
		projection: sp,
	}

	// Find the semantic path for the "Home" link (mache-1).
	homePath := sp.SemanticPath("mache-1")
	if homePath == "" {
		t.Fatal("mache-1 should have a semantic path")
	}

	result, action := sat.Execute(context.Background(), map[string]any{
		"path":   homePath,
		"action": "click",
	})

	// Should resolve to mache-1 and attempt the click action.
	t.Logf("result=%q action=%+v", result, action)

	if action == nil {
		// Browser MemoryStore returns ErrActNotSupported, so ActTool falls through
		// to ActionResult dispatch. That path calls ResolveMacheID which needs
		// the mache-ID, not the semantic path. Our wrapper should have resolved it.
		t.Fatal("expected an ActionResult for browser element click")
	}
	if action.MacheID != "mache-1" {
		t.Errorf("expected mache-1, got %q", action.MacheID)
	}
}

func TestSemanticActTool_FallsBackToBareID(t *testing.T) {
	sp := testProjection()
	agent := newTestAgent()

	sat := &SemanticActTool{
		inner:      agent.actTool,
		projection: sp,
	}

	// Bare mache-ID should still work (passthrough).
	result, action := sat.Execute(context.Background(), map[string]any{
		"path":   "mache-10",
		"action": "click",
	})

	t.Logf("result=%q action=%+v", result, action)

	if action == nil {
		t.Fatal("expected ActionResult for bare mache-ID")
	}
	if action.MacheID != "mache-10" {
		t.Errorf("expected mache-10, got %q", action.MacheID)
	}
}

func TestSemanticActTool_UnknownPath(t *testing.T) {
	sp := testProjection()
	agent := newTestAgent()

	sat := &SemanticActTool{
		inner:      agent.actTool,
		projection: sp,
	}

	result, action := sat.Execute(context.Background(), map[string]any{
		"path":   "/browser/nonexistent/element",
		"action": "click",
	})

	// Should return an error, not panic.
	if action != nil {
		t.Fatal("expected nil action for unknown path")
	}
	if !strings.Contains(result, "Error") && !strings.Contains(result, "not found") {
		t.Errorf("expected error message, got: %s", result)
	}
}

func TestSemanticActTool_TypeAction(t *testing.T) {
	sp := testProjection()
	agent := newTestAgent()

	sat := &SemanticActTool{
		inner:      agent.actTool,
		projection: sp,
	}

	// Find the search input.
	searchPath := sp.SemanticPath("mache-3")
	if searchPath == "" {
		t.Skip("mache-3 not projected (no search input in test data)")
	}

	result, action := sat.Execute(context.Background(), map[string]any{
		"path":    searchPath,
		"action":  "type",
		"payload": "hello world",
	})

	t.Logf("result=%q action=%+v", result, action)
	if action == nil {
		t.Fatal("expected ActionResult for type action")
	}
	if action.Payload != "hello world" {
		t.Errorf("expected payload 'hello world', got %q", action.Payload)
	}
}
```

Run: `cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go test ./internal/navigator/ -run TestSemanticActTool -count=1`
Expect: compilation failure (SemanticActTool not defined).

- [ ] **Step 2: Implement SemanticActTool**

Add to `internal/navigator/tools_simplified.go`:

```go
// SemanticActTool wraps the existing ActTool to accept semantic paths.
// If the path is a known semantic path, it resolves to the mache-ID first.
// If the path is already a bare mache-ID or a NavFS path, it passes through.
type SemanticActTool struct {
	inner      *ActTool
	projection *SemanticProjection
}

func (s *SemanticActTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "act",
		Description: "Execute an action on a page element. Accepts semantic paths (from find/look results) or bare mache IDs. Actions: 'click', 'focus', 'type', 'enter'.",
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"path":    {Type: genai.TypeString, Description: "Semantic path (e.g., '/browser/header/search-input') or bare mache ID (e.g., 'mache-42')"},
				"action":  {Type: genai.TypeString, Description: "Action: 'click', 'focus', 'type', or 'enter'"},
				"payload": {Type: genai.TypeString, Description: "Text to type (required for 'type' action)"},
			},
			Required: []string{"path", "action"},
		},
	}
}

func (s *SemanticActTool) Execute(ctx context.Context, args map[string]any) (string, *ActionResult) {
	path, _ := args["path"].(string)

	// Try to resolve semantic path -> mache-ID.
	if macheID := s.projection.MacheID(path); macheID != "" {
		// Replace the semantic path with the actual NavFS path or bare mache-ID
		// that the inner ActTool understands.
		args = copyArgs(args)
		args["path"] = macheID
	}

	return s.inner.Execute(ctx, args)
}

// copyArgs returns a shallow copy of the args map to avoid mutating the original.
func copyArgs(args map[string]any) map[string]any {
	cp := make(map[string]any, len(args))
	for k, v := range args {
		cp[k] = v
	}
	return cp
}
```

Run: `cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go test ./internal/navigator/ -run TestSemanticActTool -count=1 -v`
Expect: all tests pass.

Commit: `[x-ray-263555] feat(navigator): add SemanticActTool wrapping ActTool with path resolution`

---

### Task 5: Config flags and conditional tool registration

Wire everything together behind config flags so the simplified tool set is opt-in. Existing behavior is unchanged when flags are at their defaults.

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/navigator/agent.go`
- Modify: `internal/navigator/agent_test.go`

- [ ] **Step 1: Add Tools and Projection fields to NavigatorConfig**

Edit `internal/config/config.go`. Add two new fields to `NavigatorConfig`:

```go
type NavigatorConfig struct {
	Endpoint   string `yaml:"endpoint"`
	Model      string `yaml:"model"`
	Format     string `yaml:"format"`
	CLI        bool   `yaml:"cli"`
	Mode       string `yaml:"mode"`
	Speed      string `yaml:"speed"`
	Tools      string `yaml:"tools"`      // "full" (default) or "simplified"
	Projection string `yaml:"projection"` // "mache" (default) or "semantic"
}
```

Add helper methods:

```go
// IsSimplifiedTools returns true when the simplified 5-tool set is enabled.
func (n NavigatorConfig) IsSimplifiedTools() bool {
	return n.Tools == "simplified"
}

// IsSemanticProjection returns true when semantic path projection is enabled.
func (n NavigatorConfig) IsSemanticProjection() bool {
	return n.Projection == "semantic"
}
```

Add env var overrides in `applyEnvOverrides`:

```go
if v := os.Getenv("NAVIGATOR_TOOLS"); v != "" {
	cfg.Navigator.Tools = v
}
if v := os.Getenv("NAVIGATOR_PROJECTION"); v != "" {
	cfg.Navigator.Projection = v
}
```

Run: `cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go build ./...`
Expect: compiles.

- [ ] **Step 2: Write failing tests for simplified tool registration in Agent**

Add to `internal/navigator/agent_test.go`:

```go
func TestNewAgent_DefaultToolSet(t *testing.T) {
	// Default: full tool set — should have ls, cat, stat, act, grep, etc.
	agent := newTestAgent()
	decls := agent.registry.Definitions()
	if len(decls) == 0 || len(decls[0].FunctionDeclarations) == 0 {
		t.Fatal("expected tool declarations")
	}

	names := make(map[string]bool)
	for _, d := range decls[0].FunctionDeclarations {
		names[d.Name] = true
	}

	for _, want := range []string{"ls", "cat", "stat", "act", "grep"} {
		if !names[want] {
			t.Errorf("default tool set missing %q", want)
		}
	}
	// Should NOT have simplified tools.
	for _, unwanted := range []string{"find", "look"} {
		if names[unwanted] {
			t.Errorf("default tool set should not have %q", unwanted)
		}
	}
}

func TestNewAgentSimplified_ToolSet(t *testing.T) {
	engine := mache.NewEngine()
	if err := engine.ApplySchema(testSchema); err != nil {
		t.Fatal(err)
	}
	engine.LoadChildren(testSummary, nil)

	agent := NewAgentWithConfig(nil, "test", engine, config.NavigatorConfig{
		Tools:      "simplified",
		Projection: "semantic",
	})

	decls := agent.registry.Definitions()
	if len(decls) == 0 || len(decls[0].FunctionDeclarations) == 0 {
		t.Fatal("expected tool declarations")
	}

	names := make(map[string]bool)
	for _, d := range decls[0].FunctionDeclarations {
		names[d.Name] = true
	}

	// Simplified set: find, act, browser.scroll, answer, look
	for _, want := range []string{"find", "act", "browser.scroll", "answer", "look"} {
		if !names[want] {
			t.Errorf("simplified tool set missing %q", want)
		}
	}
	// Should NOT have full tools.
	for _, unwanted := range []string{"ls", "cat", "stat", "grep"} {
		if names[unwanted] {
			t.Errorf("simplified tool set should not have %q", unwanted)
		}
	}
}

func TestNewAgentSimplified_ProjectionUpdatesOnSetGraph(t *testing.T) {
	engine := mache.NewEngine()
	if err := engine.ApplySchema(testSchema); err != nil {
		t.Fatal(err)
	}
	engine.LoadChildren(testSummary, nil)

	agent := NewAgentWithConfig(nil, "test", engine, config.NavigatorConfig{
		Tools:      "simplified",
		Projection: "semantic",
	})

	// Initial projection should have paths.
	if agent.projection == nil {
		t.Fatal("expected projection to be set")
	}
	initialPaths := len(agent.projection.AllPaths())
	if initialPaths == 0 {
		t.Fatal("expected non-zero initial paths")
	}

	// Build a new engine with different content.
	engine2 := mache.NewEngine()
	schema2 := `{
		"mounts": [
			{
				"virtual_path": "/header/brand",
				"mache_id": "mache-100",
				"description": "Brand logo",
				"bounds": [0, 0, 0.3, 0.1]
			}
		]
	}`
	if err := engine2.ApplySchema(schema2); err != nil {
		t.Fatal(err)
	}
	engine2.LoadChildren(`Interactive Elements:
ID: mache-100 | Parent: none | Tag: img | Text: "Logo"
`, nil)

	// Swap the graph.
	agent.SetGraphWithProjection(engine2, []mache.Mount{
		{VirtualPath: "/header/brand", MacheID: "mache-100", Description: "Brand logo", Bounds: [4]float64{0, 0, 0.3, 0.1}},
	}, `Interactive Elements:
ID: mache-100 | Parent: none | Tag: img | Text: "Logo"
`)

	// Projection should be rebuilt.
	newPaths := len(agent.projection.AllPaths())
	if newPaths == initialPaths {
		t.Errorf("projection should have changed after SetGraphWithProjection")
	}
}
```

Run: `cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go test ./internal/navigator/ -run "TestNewAgent_DefaultToolSet|TestNewAgentSimplified" -count=1`
Expect: compilation failure (NewAgentWithConfig, SetGraphWithProjection not defined).

- [ ] **Step 3: Implement NewAgentWithConfig and conditional tool registration**

Edit `internal/navigator/agent.go`.

Add `projection` field and `navConfig` to the Agent struct:

```go
type Agent struct {
	// ... existing fields ...
	navConfig  config.NavigatorConfig
	projection *SemanticProjection // non-nil when projection = "semantic"
}
```

Add `NewAgentWithConfig` constructor alongside existing `NewAgent`:

```go
// NewAgentWithConfig creates a Navigator agent with explicit config for tool
// set and projection mode. When config.Tools == "simplified", only the 5-tool
// simplified set is registered. When config.Projection == "semantic", a
// SemanticProjection is built from the initial graph state.
func NewAgentWithConfig(gen ContentGenerator, model string, g graph.Graph, navCfg config.NavigatorConfig) *Agent {
	if model == "" {
		model = "gemini-2.5-flash"
	}

	hs := graph.NewHotSwapGraph(g)
	fs := NewNavFS(hs)

	reg := NewToolRegistry()

	scroll := &ScrollTool{}
	act := &ActTool{fs: fs}
	answerTool := &AnswerTool{}
	listTabs := &ListTabsTool{}

	var rescan *RescanTool
	var newWindow *NewWindowTool
	var newTab *NewTabTool
	var lsTool *LsTool
	var catTool *CatTool
	var projection *SemanticProjection

	if navCfg.IsSimplifiedTools() {
		// Simplified: find, act (semantic), scroll, answer, look.
		projection = &SemanticProjection{
			pathToMacheID: make(map[string]string),
			macheIDToPath: make(map[string]string),
		}

		findTool := &FindTool{projection: projection}
		lookTool := &LookTool{projection: projection}
		semanticAct := &SemanticActTool{inner: act, projection: projection}

		reg.Register(findTool)
		reg.Register(semanticAct)
		reg.Register(scroll)
		reg.Register(answerTool)
		reg.Register(lookTool)
	} else {
		// Full: existing 12-tool set.
		lsTool = &LsTool{fs: fs}
		catTool = &CatTool{fs: fs}
		stat := &StatTool{fs: fs}
		grepTool := &GrepTool{fs: fs}
		goTo := &GotoTool{}
		rescan = &RescanTool{fs: fs}
		switchTab := &SwitchTabTool{}
		newWindow = &NewWindowTool{fs: fs}
		newTab = &NewTabTool{fs: fs}

		reg.Register(lsTool)
		reg.Register(catTool)
		reg.Register(stat)
		reg.Register(act)
		reg.Register(grepTool)
		reg.Register(scroll)
		reg.Register(goTo)
		reg.Register(rescan)
		reg.Register(listTabs)
		reg.Register(switchTab)
		reg.Register(newWindow)
		reg.Register(newTab)
		reg.Register(answerTool)
	}

	a := &Agent{
		generator:     gen,
		model:         model,
		fs:            fs,
		hotswap:       hs,
		registry:      reg,
		scrollTool:    scroll,
		listTabs:      listTabs,
		lsTool:        lsTool,
		catTool:       catTool,
		actTool:       act,
		rescanTool:    rescan,
		newWindowTool: newWindow,
		newTabTool:    newTab,
		navConfig:     navCfg,
		projection:    projection,
	}
	scroll.getViewport = a.viewportString
	return a
}
```

Add `SetGraphWithProjection` method:

```go
// SetGraphWithProjection atomically swaps the graph and rebuilds the
// SemanticProjection. Only meaningful when projection mode is "semantic";
// otherwise it delegates to SetGraph.
func (a *Agent) SetGraphWithProjection(g graph.Graph, mounts []mache.Mount, summary string) {
	a.hotswap.Swap(g)
	if a.navConfig.IsSemanticProjection() && a.projection != nil {
		newProj := NewSemanticProjection(mounts, summary)
		// Update the projection in place so existing tool references stay valid.
		a.projection.pathToMacheID = newProj.pathToMacheID
		a.projection.macheIDToPath = newProj.macheIDToPath
		a.projection.allPaths = newProj.allPaths
	}
}
```

Ensure `NewAgent` delegates to `NewAgentWithConfig` with zero-value config:

```go
func NewAgent(gen ContentGenerator, model string, g graph.Graph) *Agent {
	return NewAgentWithConfig(gen, model, g, config.NavigatorConfig{})
}
```

Add the config import at the top of `agent.go`:

```go
import (
	// ... existing imports ...
	"github.com/agentic-research/x-ray/internal/config"
)
```

Run: `cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go test ./internal/navigator/ -run "TestNewAgent|TestExecuteToolLs|TestHandleIntent" -count=1 -v`
Expect: all tests pass. The existing `NewAgent` path is unchanged and all prior tests continue to work.

- [ ] **Step 4: Verify full test suite passes with both configurations**

Run the complete navigator test suite to confirm nothing is broken:

```bash
cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go test ./internal/navigator/ -count=1 -v
```

Expect: all existing tests pass. New tests also pass.

Then run the broader build to catch any import cycles or compilation issues:

```bash
cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go build ./...
```

Expect: clean build.

Commit: `[x-ray-264d9c] feat(navigator): conditional simplified tool set behind config flags`

- [ ] **Step 5: Add config documentation to template**

Edit the `writeDefaultConfig` function in `internal/config/config.go` to include the new fields in the generated YAML template:

Add after the `cli: false` line in the navigator section:

```yaml
  # Tool set: "full" (12 tools, default) or "simplified" (5 tools: find, act, scroll, answer, look).
  tools: ""
  # Path projection: "mache" (default, opaque IDs) or "semantic" (human-readable paths).
  projection: ""
```

Add env var documentation comment (no code change needed -- the overrides are already wired in step 1).

Run: `cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go build ./...`
Expect: clean build.

Commit: `[x-ray-264d9c] docs(config): add tools and projection fields to generated config template`

---

## Verification Checklist

After all tasks are complete, run these commands to confirm everything works:

```bash
# Full test suite.
cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go test ./internal/navigator/ -count=1 -v

# Build check.
cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go build ./...

# Vet check.
cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go vet ./...

# Confirm default behavior unchanged (no config flags set).
cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go test ./internal/navigator/ -run "TestExecuteToolLs|TestHandleIntent" -count=1

# Confirm simplified mode works.
cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go test ./internal/navigator/ -run "TestNewAgentSimplified|TestFindTool|TestLookTool|TestSemanticActTool" -count=1

# Config package compiles with new fields.
cd /Users/jamesgardner/remotes/art/x-ray && GOWORK=off go test ./internal/config/ -count=1
```

All commands should exit 0.
