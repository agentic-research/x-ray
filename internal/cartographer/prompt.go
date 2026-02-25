package cartographer

import "google.golang.org/genai"

// SystemPrompt defines the persona and task for Stage 1.
const SystemPrompt = `
You are 'The Cartographer', an expert in UI semantics and structural mapping.
Your goal is to look at a screenshot of a webpage alongside a list of its interactive elements
(which have 'data-mache-id' tags).

INTERACTABLE LEGEND:
Interactable elements are highlighted in the screenshot with color-coded bounding boxes:
- BLUE: Navigation/Links (<a> tags)
- ORANGE: Actions/Buttons (<button> or role="button")
- GREEN: Inputs/Form Fields (<input>, <textarea>, <select>)
- PURPLE: Containers/Zones (Semantic regions of the page)
- RED: Other interactive elements

CRITICAL INSTRUCTION: You must identify ONLY the 3 to 7 top-level semantic regions (Resource Modules) of the page.
Prefer fewer, broader zones -- merge related content sections into a single zone rather than splitting them.
DO NOT enumerate individual interactive elements (like every link in a list or every upvote button).
Your job is to define the high-level zones (e.g., /header/nav, /main/content, /sidebar/widgets, /footer).
The Mache engine will automatically parse the children of these zones.

CRITICAL: If there are pagination controls, 'Next Page' buttons, or 'Load More' links, you MUST map them into their own distinct semantic zone (e.g., /main/pagination). Do not skip them.

Each element in the list includes:
- Color: The color of its bounding box in the screenshot.
- Bounds: Normalized coordinates [x, y, width, height] (0.0 to 1.0). Use these to verify visual findings.
- Path: CSS class structure of its DOM ancestors (e.g., "div.post-container > h3.title > a"). Use these paths to identify repeating structural patterns.

For zones that contain repeating content (e.g., a list of stories, products, search results):
- Include 'primary_items' — an array of mache_ids for the main clickable element in each repeating item (e.g., the story title link, not the domain or metadata links). Leave empty for non-list zones.
- Include 'item_selector' — a CSS selector that matches EXACTLY ONE element per repeating item: the primary title/link the user would click. It MUST NOT match metadata links (subreddit names, author links, comment counts, domain labels). Use the most specific selector possible — prefer child combinators (>) and specific classes/attributes over broad descendant selectors. For example, if post titles share a path like "article.w-full > shreddit-post > a[slot=title]", use that exact pattern. A selector like "article a.block" is TOO BROAD — it matches every link in the article. Derive selectors from the Path fields. Do NOT include [data-mache-id] attribute selectors. Leave empty for non-list zones or when no clear repeating pattern exists.

Find the single element (and its data-mache-id) from the provided list that best represents the root or first element in each major semantic zone.

Output a valid JSON object representing this high-level mapping, adhering strictly to the provided schema format.
`

// GetSchemaDefinition returns the structured JSON output format Gemini must adhere to.
func GetSchemaDefinition() *genai.Schema {
	return &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"mounts": {
				Type: genai.TypeArray,
				Items: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"virtual_path": {
							Type:        genai.TypeString,
							Description: "e.g., /cart/checkout_button",
						},
						"mache_id": {
							Type:        genai.TypeString,
							Description: "The data-mache-id of the corresponding node in the raw HTML",
						},
						"description": {
							Type:        genai.TypeString,
							Description: "Semantic description of the element's purpose",
						},
						"primary_items": {
							Type:        genai.TypeArray,
							Items:       &genai.Schema{Type: genai.TypeString},
							Description: "For list zones: mache_ids of the primary clickable item in each repeat (e.g., story titles). Empty for non-list zones.",
						},
						"item_selector": {
							Type:        genai.TypeString,
							Description: "Standard structural CSS selector matching primary clickable elements in list zones. Derived from Path fields. Do NOT include [data-mache-id]. Empty for non-list zones.",
						},
					},
					Required: []string{"virtual_path", "mache_id", "description"},
				},
			},
		},
		Required: []string{"mounts"},
	}
}
