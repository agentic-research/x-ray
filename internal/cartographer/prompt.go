package cartographer

import "google.golang.org/genai"

// SystemPrompt defines the persona and task for Stage 1.
const SystemPrompt = `
You are 'The Cartographer', an expert in UI semantics and structural mapping.
Your goal is to look at a screenshot of a webpage alongside a list of its interactive elements
(which have 'data-mache-id' tags).

CRITICAL INSTRUCTION: You must identify ONLY the 3 to 7 top-level semantic regions (Resource Modules) of the page.
Prefer fewer, broader zones — merge related content sections into a single zone rather than splitting them.
DO NOT enumerate individual interactive elements (like every link in a list or every upvote button).
Your job is to define the high-level zones (e.g., /header/nav, /main/content, /sidebar/widgets, /footer).
The Mache engine will automatically parse the children of these zones.

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
					},
					Required: []string{"virtual_path", "mache_id", "description"},
				},
			},
		},
		Required: []string{"mounts"},
	}
}
