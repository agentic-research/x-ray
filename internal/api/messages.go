package api

import "github.com/jamesgardner/x-ray/internal/navigator"

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
	MsgGotoURL           = "GOTO_URL"
	MsgVoiceLog          = "VOICE_LOG"
	MsgTabActivated      = "TAB_ACTIVATED"
	MsgRescan            = "RESCAN"
	MsgListTabs          = "LIST_TABS"
	MsgTabsListed        = "TABS_LISTED"
	MsgSwitchTab         = "SWITCH_TAB"
	MsgCreateTab         = "CREATE_TAB"
	MsgTabClosed         = "TAB_CLOSED"
	MsgDOMMutated        = "DOM_MUTATED"
	MsgPing              = "PING"
)

// TabInfo is an alias for navigator.TabInfo (canonical definition lives there).
type TabInfo = navigator.TabInfo

// InboundMessage is the envelope for all browser -> server messages.
type InboundMessage struct {
	Type          string              `json:"type"`
	TabID         int                 `json:"tab_id,omitempty"`
	URL           string              `json:"url,omitempty"`
	Summary       string              `json:"summary,omitempty"`
	Screenshot    string              `json:"screenshot,omitempty"` // base64-encoded JPEG (scaled)
	Intent        string              `json:"intent,omitempty"`
	ResolvedItems map[string][]string `json:"resolved_items,omitempty"` // zone mache-id → resolved child mache-ids
	Message       string              `json:"message,omitempty"`        // VOICE_LOG text
	IsRescan      bool                `json:"is_rescan,omitempty"`      // bypass schema cache on rescan
	Tabs          []TabInfo           `json:"tabs,omitempty"`           // TABS_LISTED response
}

// OutboundMessage is the envelope for all server -> browser messages.
type OutboundMessage struct {
	Type      string            `json:"type"`
	TabID     int               `json:"tab_id,omitempty"`
	Schema    any               `json:"schema,omitempty"`
	MacheID   string            `json:"mache_id,omitempty"`
	Action    string            `json:"action,omitempty"`
	Payload   string            `json:"payload,omitempty"` // text for "type" action
	Message   string            `json:"message,omitempty"`
	Stage     string            `json:"stage,omitempty"`
	Direction string            `json:"direction,omitempty"`
	URL       string            `json:"url,omitempty"`
	Selectors map[string]string `json:"selectors,omitempty"` // zone mache-id → CSS selector
	PixelX    int               `json:"pixel_x,omitempty"`   // viewport-relative X for CDP pixel click (cv-N)
	PixelY    int               `json:"pixel_y,omitempty"`   // viewport-relative Y for CDP pixel click (cv-N)
}
