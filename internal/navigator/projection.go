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

		// Register ALL descendant elements (not just direct children).
		// Walk the tree depth-first from the mount root.
		var walkDescendants func(parentID string)
		walkDescendants = func(parentID string) {
			for _, childID := range childrenOf[parentID] {
				child, ok := elemByID[childID]
				if !ok {
					continue
				}
				// Only register interactive/meaningful elements
				role := inferRole(child.tag)
				if role == "text" || role == "div" {
					// Skip structural containers but walk their children
					walkDescendants(childID)
					continue
				}
				childSlug := slugify(child.text, 30)
				if childSlug == "" {
					childSlug = role
				}
				childPath := zonePath + "/" + childSlug
				childPath = dedup(childPath, used)
				sp.register(childPath, childID, elemByID)
				// Also walk this element's children
				walkDescendants(childID)
			}
		}
		walkDescendants(m.MacheID)
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
//
//	ID: mache-11 | Parent: mache-10 | Tag: a | Text: "First Story Title"
//	ID: mache-11 | Parent: mache-10 | Tag: a | Role: link | Text: "Title" | Bounds: ...
//
// Matches ID, Parent, Tag, and Text fields regardless of other fields between them.
var summaryLineRe = regexp.MustCompile(
	`ID:\s*(\S+)\s*\|\s*Parent:\s*(\S+)\s*\|\s*Tag:\s*(\S+).*?\|\s*Text:\s*"([^"]*)"`,
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
