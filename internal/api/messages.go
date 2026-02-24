package api

// Message type constants for WebSocket communication.
const (
	MsgDOMSnapshot       = "DOM_SNAPSHOT"
	MsgSchemaReady       = "SCHEMA_READY"
	MsgExecuteAction     = "EXECUTE_ACTION"
	MsgNavigate          = "NAVIGATE"
	MsgStatus            = "STATUS"
	MsgScroll            = "SCROLL"
	MsgDOMUpdate         = "DOM_UPDATE"
	MsgResolveSelectors  = "RESOLVE_SELECTORS"
	MsgSelectorsResolved = "SELECTORS_RESOLVED"
)

// InboundMessage is the envelope for all browser -> server messages.
type InboundMessage struct {
	Type          string              `json:"type"`
	TabID         int                 `json:"tab_id,omitempty"`
	URL           string              `json:"url,omitempty"`
	Summary       string              `json:"summary,omitempty"`
	Screenshot    string              `json:"screenshot,omitempty"` // base64-encoded JPEG (scaled)
	Intent        string              `json:"intent,omitempty"`
	ResolvedItems map[string][]string `json:"resolved_items,omitempty"` // zone mache-id → resolved child mache-ids
}

// OutboundMessage is the envelope for all server -> browser messages.
type OutboundMessage struct {
	Type      string            `json:"type"`
	TabID     int               `json:"tab_id,omitempty"`
	Schema    any               `json:"schema,omitempty"`
	MacheID   string            `json:"mache_id,omitempty"`
	Action    string            `json:"action,omitempty"`
	Message   string            `json:"message,omitempty"`
	Stage     string            `json:"stage,omitempty"`
	Direction string            `json:"direction,omitempty"`
	Selectors map[string]string `json:"selectors,omitempty"` // zone mache-id → CSS selector
}
