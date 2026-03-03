package api

import (
	"encoding/json"

	"github.com/agentic-research/x-ray/internal/navigator"
)

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

	// Go-driven capture orchestration.
	MsgPageReady         = "PAGE_READY"             // ext → server: tab ready for capture
	MsgRequestSummary    = "REQUEST_SUMMARY"        // server → ext: build registry + return summary
	MsgSummaryResponse   = "SUMMARY_RESPONSE"       // ext → server: summary + url
	MsgDrawOverlayCmd    = "DRAW_OVERLAY_CMD"       // server → ext: draw machine overlay
	MsgOverlayDrawn      = "OVERLAY_DRAWN"          // ext → server: overlay drawn ack
	MsgRemoveOverlayCmd  = "REMOVE_OVERLAY_CMD"     // server → ext: remove machine overlay
	MsgOverlayRemoved    = "OVERLAY_REMOVED"        // ext → server: overlay removed ack
	MsgDrawHumanOverlay  = "DRAW_HUMAN_OVERLAY_CMD" // server → ext: draw human-friendly overlay
	MsgHumanOverlayDrawn = "HUMAN_OVERLAY_DRAWN"    // ext → server: human overlay drawn ack

	// CDP proxy message types (Dumb Pipe architecture).
	MsgCDPAttach       = "CDP_ATTACH"
	MsgCDPAttached     = "CDP_ATTACHED"
	MsgCDPAttachFailed = "CDP_ATTACH_FAILED"
	MsgCDPSend         = "CDP_SEND"
	MsgCDPResult       = "CDP_RESULT"
	MsgCDPError        = "CDP_ERROR"
	MsgCDPEvent        = "CDP_EVENT"
	MsgCDPDetach       = "CDP_DETACH"
	MsgCDPDetached     = "CDP_DETACHED"
)

// TabInfo is an alias for navigator.TabInfo (canonical definition lives there).
type TabInfo = navigator.TabInfo

// InboundMessage is the envelope for all browser -> server messages.
type InboundMessage struct {
	Type           string              `json:"type"`
	TabID          int                 `json:"tab_id,omitempty"`
	URL            string              `json:"url,omitempty"`
	Summary        string              `json:"summary,omitempty"`
	Screenshot     string              `json:"screenshot,omitempty"` // base64-encoded JPEG (scaled)
	Intent         string              `json:"intent,omitempty"`
	ResolvedItems  map[string][]string `json:"resolved_items,omitempty"`  // zone mache-id → resolved child mache-ids
	Message        string              `json:"message,omitempty"`         // VOICE_LOG text
	IsRescan       bool                `json:"is_rescan,omitempty"`       // bypass schema cache on rescan
	TargetMacheID  string              `json:"target_mache_id,omitempty"` // PAGE_READY: magnifying glass target
	AtBottom       bool                `json:"at_bottom,omitempty"`       // DOM_UPDATE: page scrolled to bottom
	AtTop          bool                `json:"at_top,omitempty"`          // DOM_UPDATE: page scrolled to top
	ScrollMoved    bool                `json:"scroll_moved,omitempty"`    // DOM_UPDATE: scroll actually changed position
	ScrollY        float64             `json:"scroll_y,omitempty"`        // DOM_UPDATE: current scroll position (px)
	ScrollHeight   float64             `json:"scroll_height,omitempty"`   // DOM_UPDATE: total document height (px)
	ViewportHeight float64             `json:"viewport_height,omitempty"` // DOM_UPDATE: viewport height (px)
	PageText       string              `json:"page_text,omitempty"`       // CDP Runtime.evaluate body text
	Tabs           []TabInfo           `json:"tabs,omitempty"`            // TABS_LISTED response

	// CDP proxy fields (Dumb Pipe architecture).
	CDPRequestID int64           `json:"cdp_id,omitempty"`
	CDPMethod    string          `json:"cdp_method,omitempty"`
	CDPResult    json.RawMessage `json:"cdp_result,omitempty"`
	CDPError     string          `json:"cdp_error,omitempty"`
	CDPParams    json.RawMessage `json:"cdp_params,omitempty"`
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
