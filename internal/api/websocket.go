package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agentic-research/mache/graph"
	"github.com/agentic-research/x-ray/internal/focus"
	"github.com/agentic-research/x-ray/internal/mache"
	"github.com/agentic-research/x-ray/internal/navigator"
	"github.com/gorilla/websocket"
	"google.golang.org/genai"
)

var boundsRe = regexp.MustCompile(`Bounds: \[([\d.]+), ([\d.]+), ([\d.]+), ([\d.]+)\]`)

const (
	schemaWaitTimeout = 30 * time.Second
	scrollWaitTimeout = 10 * time.Second
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Handler holds the dependencies for the WebSocket handler.
type Handler struct {
	Cartographer  SchemaGenerator
	NavGen        navigator.ContentGenerator // for creating per-tab Navigators
	LiveClient    *genai.Client              // Live API client (voice mode)
	PlannerClient *genai.Client              // regular (non-Live) Gemini client for Planner
	NavModel      string                     // model name for creating per-tab Navigators
	LiveModel     string                     // model name for voice sessions
	PlannerModel  string                     // model name for Planner (e.g., "gemini-2.5-flash")

	mu             sync.Mutex
	conn           *websocket.Conn
	pending        []pendingAction
	sessions       map[int]*TabSession
	schemas        *SchemaCache     // domain+path → schema JSON
	activeVoiceTab int              // tab ID for native voice mode (set by TAB_ACTIVATED)
	openBrowserFn  func(url string) // fallback when no WS connection; nil = no-op (tests)
	termBridge     graph.Graph      // global iTerm2 bridge (nil if iTerm not available)
	planner        *Planner         // Planner for /agent/task (non-voice Talker)
}

func NewHandler(cart SchemaGenerator, navGen navigator.ContentGenerator, client, liveClient *genai.Client, navModel, liveModel, plannerModel, dbPath string) *Handler {
	h := &Handler{
		Cartographer:  cart,
		NavGen:        navGen,
		PlannerClient: client,
		LiveClient:    liveClient,
		NavModel:      navModel,
		LiveModel:     liveModel,
		PlannerModel:  plannerModel,
		sessions:      make(map[int]*TabSession),
		schemas:       NewSchemaCache(dbPath),
	}
	if client != nil && plannerModel != "" {
		h.planner = &Planner{handler: h, client: client, model: plannerModel}
	}
	return h
}

// SetOpenBrowserFunc injects the logic for opening a browser when no extension is connected.
func (h *Handler) SetOpenBrowserFunc(fn func(string)) {
	h.openBrowserFn = fn
}

// SetTermBridge registers the global iTerm2 bridge. When set, every new
// TabSession mounts it as "iterm" in its CompositeGraph so the Navigator
// can browse and act on terminal sessions alongside browser elements.
func (h *Handler) SetTermBridge(bridge graph.Graph) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.termBridge = bridge
}

// lookupSession returns the TabSession for the given tab, or nil if none exists.
// Use this for message handlers that should silently discard messages for unknown tabs.
func (h *Handler) lookupSession(tabID int) *TabSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions[tabID]
}

// getSession returns the TabSession for the given tab, creating one if needed.
func (h *Handler) getSession(tabID int) *TabSession {
	h.mu.Lock()
	defer h.mu.Unlock()

	if sess, ok := h.sessions[tabID]; ok {
		return sess
	}

	engine := mache.NewEngine()
	composite := graph.NewCompositeGraph()
	if err := composite.Mount("browser", engine); err != nil {
		log.Printf("Session: mount browser (tab %d): %v", tabID, err)
	}
	if h.termBridge != nil {
		if err := composite.Mount("iterm", h.termBridge); err != nil {
			log.Printf("Session: mount iterm (tab %d): %v", tabID, err)
		}
	}

	// Add the dynamic focus mount that routes to the currently active application.
	appMapping := map[string]string{
		"Google Chrome": "browser",
		"iTerm2":        "iterm",
	}
	focusRouter := focus.NewRouter(composite, appMapping)
	if err := composite.Mount("focus", focusRouter); err != nil {
		log.Printf("Session: mount focus (tab %d): %v", tabID, err)
	}

	nav := navigator.NewAgent(h.NavGen, h.NavModel, composite)
	sess := &TabSession{
		TabID:             tabID,
		Engine:            engine,
		Composite:         composite,
		Navigator:         nav,
		SchemaReady:       make(chan struct{}),
		DOMUpdateCh:       make(chan DOMUpdate, 1),
		DOMMutatedCh:      make(chan struct{}, 1),
		SelectorsResolved: make(chan map[string][]string, 1),
		TabsListedCh:      make(chan []TabInfo, 1),
	}
	h.sessions[tabID] = sess
	log.Printf("Session: created new session for tab %d", tabID)
	return sess
}

// getVoiceSession returns the TabSession for the active voice tab.
// Used by StartVoiceLoop to resolve tool calls against the right tab.
// Returns nil if no voice tab is set (tab 0 means no active tab).
func (h *Handler) getVoiceSession() *TabSession {
	h.mu.Lock()
	tabID := h.activeVoiceTab
	h.mu.Unlock()
	if tabID == 0 {
		return nil
	}
	return h.getSession(tabID)
}

// getVoiceTabID returns the current active voice tab ID.
func (h *Handler) getVoiceTabID() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.activeVoiceTab
}

// getOrCreateDoer lazily creates a Doer for the given tab session.
func (h *Handler) getOrCreateDoer(tabID int, sess *TabSession) *Doer {
	h.mu.Lock()
	defer h.mu.Unlock()
	if sess.Doer != nil {
		return sess.Doer
	}
	doer := NewDoer(h, tabID, sess)
	sess.Doer = doer
	runCtx, runCancel := context.WithCancel(context.Background())
	sess.doerCancel = runCancel
	go doer.Run(runCtx)
	return doer
}

// HandleWebSocket upgrades the HTTP connection and processes messages.
func (h *Handler) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade failed: %v", err)
		return
	}
	defer func() { _ = conn.Close() }()

	h.mu.Lock()
	h.conn = conn
	queued := h.pending
	h.pending = nil
	// Snapshot existing sessions — content.js was re-injected on reconnect and
	// restarted mache-ID numbering from mache-0. Any cached ID→element mapping
	// in the Engine is now stale. Sessions are preserved (not deleted) to avoid
	// killing in-flight voice Doers, but their schema channels are reset so the
	// next DOM_SNAPSHOT properly re-signals schema-ready.
	sessions := make([]*TabSession, 0, len(h.sessions))
	for _, sess := range h.sessions {
		sessions = append(sessions, sess)
	}
	h.mu.Unlock()

	for _, sess := range sessions {
		sess.ResetSchema()
	}

	log.Printf("WebSocket: Client connected (reset schema state for %d sessions)", len(sessions))

	// Flush any actions that were queued while the extension was disconnected.
	for _, a := range queued {
		log.Printf("WebSocket: flushing queued action: %s on %s (tab %d)", a.Action, a.MacheID, a.TabID)
		h.sendMessage(conn, OutboundMessage{
			Type:    MsgExecuteAction,
			TabID:   a.TabID,
			MacheID: a.MacheID,
			Action:  a.Action,
		})
	}

	// Keep-alive: ping every 20s to prevent Chrome MV3 service worker from going idle.
	pingDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				h.mu.Lock()
				err := conn.WriteMessage(websocket.PingMessage, nil)
				h.mu.Unlock()
				if err != nil {
					return
				}
			case <-pingDone:
				return
			}
		}
	}()
	defer close(pingDone)

	for {
		_, msgBytes, err := conn.ReadMessage()
		if err != nil {
			log.Printf("WebSocket read error: %v", err)
			break
		}

		var msg InboundMessage
		if err := json.Unmarshal(msgBytes, &msg); err != nil {
			log.Printf("WebSocket: invalid JSON: %v", err)
			continue
		}

		switch msg.Type {
		case MsgDOMSnapshot:
			go h.handleDOMSnapshot(conn, msg)
		case MsgNavigate:
			go h.handleNavigate(conn, msg)
		case MsgDOMUpdate:
			h.handleDOMUpdate(msg)
		case MsgSelectorsResolved:
			h.handleSelectorsResolved(msg)
		case MsgTabActivated:
			h.mu.Lock()
			h.activeVoiceTab = msg.TabID
			h.mu.Unlock()
			log.Printf("WebSocket: active voice tab set to %d", msg.TabID)
		case MsgTabClosed:
			h.handleTabClosed(msg)
		case MsgDOMMutated:
			h.handleDOMMutated(msg)
		case MsgTabsListed:
			h.handleTabsListed(msg)
		case MsgVoiceLog:
			log.Printf("Voice [ext tab %d]: %s", msg.TabID, msg.Message)
		case MsgPing:
			// Client-side keep-alive heartbeat — no action needed.
		default:
			log.Printf("WebSocket: unknown message type: %s", msg.Type)
		}
	}
}

func (h *Handler) handleNavigate(conn *websocket.Conn, msg InboundMessage) {
	ctx := context.Background()
	sess := h.getSession(msg.TabID)

	// Wait for schema if it hasn't arrived yet (intent queued before snapshot finished).
	if !sess.GetEngine().HasSchema() {
		log.Printf("Navigator: waiting for schema (tab %d) before handling: %s", msg.TabID, msg.Intent)
		h.sendMessage(conn, OutboundMessage{
			Type: MsgStatus, TabID: msg.TabID, Message: "Waiting for schema...", Stage: "navigator",
		})
		select {
		case <-sess.GetSchemaReady():
			log.Printf("Navigator: schema ready, proceeding (tab %d)", msg.TabID)
		case <-time.After(schemaWaitTimeout):
			log.Printf("Navigator: timed out waiting for schema (tab %d)", msg.TabID)
			h.sendMessage(conn, OutboundMessage{
				Type: MsgStatus, TabID: msg.TabID, Message: "Timed out waiting for schema", Stage: "error",
			})
			return
		}
	}

	h.sendMessage(conn, OutboundMessage{
		Type: MsgStatus, TabID: msg.TabID, Message: "Navigating: " + msg.Intent, Stage: "navigator",
	})

	// Inject scroll capability so the Navigator can request page scrolling mid-loop.
	sess.Navigator.SetScrollFunc(func(scrollCtx context.Context, direction string) error {
		return h.scrollPage(scrollCtx, conn, sess, msg.TabID, direction)
	})
	defer sess.Navigator.SetScrollFunc(nil)

	navStart := time.Now()
	action, textResponse, err := sess.Navigator.HandleIntent(ctx, msg.Intent, false)
	if err != nil {
		log.Printf("Navigator failed after %s: %v", time.Since(navStart), err)
		h.sendMessage(conn, OutboundMessage{
			Type: MsgStatus, TabID: msg.TabID, Message: "Navigation failed: " + err.Error(), Stage: "error",
		})
		return
	}

	log.Printf("Navigator handled intent in %s: %q", time.Since(navStart), msg.Intent)

	if action != nil {
		result, _ := json.MarshalIndent(action, "", "  ")
		saveLog("navigate", msg.Intent, string(result))
		if action.Action == "goto" {
			h.sendMessage(conn, OutboundMessage{
				Type:  MsgGotoURL,
				TabID: msg.TabID,
				URL:   action.Path,
			})
		} else {
			h.sendMessage(conn, OutboundMessage{
				Type:    MsgExecuteAction,
				TabID:   msg.TabID,
				MacheID: action.MacheID,
				Action:  action.Action,
				Payload: action.Payload,
			})
		}
	} else if textResponse != "" {
		saveLog("navigate", msg.Intent, textResponse)
		h.sendMessage(conn, OutboundMessage{
			Type: MsgStatus, TabID: msg.TabID, Message: textResponse, Stage: "navigator",
		})
	}
}

// handleTabClosed prunes the session for a closed tab, cancelling any running Doer.
func (h *Handler) handleTabClosed(msg InboundMessage) {
	h.mu.Lock()
	if sess, ok := h.sessions[msg.TabID]; ok {
		if sess.Doer != nil {
			sess.Doer.Cancel()
		}
		if sess.doerCancel != nil {
			sess.doerCancel() // kills the Doer's Run goroutine (Bug #6 fix)
		}
		delete(h.sessions, msg.TabID)
	}
	if h.activeVoiceTab == msg.TabID {
		// Find a fallback tab instead of defaulting to 0.
		h.activeVoiceTab = 0
		for tabID := range h.sessions {
			if tabID != 0 {
				h.activeVoiceTab = tabID
				break
			}
		}
	}
	h.mu.Unlock()
	log.Printf("WebSocket: tab %d closed, session pruned (voiceTab now %d)", msg.TabID, h.activeVoiceTab)
}

// handleDOMMutated signals the Doer that an in-page DOM mutation was detected.
func (h *Handler) handleDOMMutated(msg InboundMessage) {
	sess := h.lookupSession(msg.TabID)
	if sess == nil {
		return
	}
	select {
	case sess.DOMMutatedCh <- struct{}{}:
	default: // non-blocking, don't pile up
	}
}

// handleDOMUpdate receives an updated summary from the browser after a scroll.
// It signals the waiting scrollPage goroutine via the session's DOMUpdateCh.
func (h *Handler) handleDOMUpdate(msg InboundMessage) {
	saveLog("summary-scroll", fmt.Sprintf("tab-%d", msg.TabID), msg.Summary)
	sess := h.lookupSession(msg.TabID)
	if sess == nil {
		return
	}
	update := DOMUpdate{
		Summary:       msg.Summary,
		ResolvedItems: msg.ResolvedItems,
	}
	select {
	case sess.DOMUpdateCh <- update:
		log.Printf("Scroll: received DOM_UPDATE for tab %d (%d bytes, %d zones resolved)",
			msg.TabID, len(msg.Summary), len(msg.ResolvedItems))
	default:
		log.Printf("Scroll: DOM_UPDATE for tab %d but no listener, discarding", msg.TabID)
	}
}

// handleSelectorsResolved receives CSS selector results from the browser.
// Signals the waiting handleDOMSnapshot goroutine via SelectorsResolved channel.
func (h *Handler) handleSelectorsResolved(msg InboundMessage) {
	sess := h.lookupSession(msg.TabID)
	if sess == nil {
		return
	}
	resolved := msg.ResolvedItems
	if resolved == nil {
		resolved = make(map[string][]string)
	}
	select {
	case sess.SelectorsResolved <- resolved:
		log.Printf("Selectors resolved for tab %d (%d zones)", msg.TabID, len(resolved))
	default:
		log.Printf("Selectors resolved for tab %d but no listener, discarding", msg.TabID)
	}
}

// scrollPage sends a SCROLL command to the browser and blocks until the
// updated DOM summary arrives. It then re-runs LoadChildren on the Engine.
func (h *Handler) scrollPage(ctx context.Context, conn *websocket.Conn, sess *TabSession, tabID int, direction string) error {
	log.Printf("Scroll: requesting %s for tab %d", direction, tabID)

	// Drain any stale DOM update from a previous scroll or duplicate message.
	select {
	case <-sess.DOMUpdateCh:
	default:
	}

	selectors := sess.Engine.ZoneSelectors()

	h.sendMessage(conn, OutboundMessage{
		Type: MsgScroll, TabID: tabID, Direction: direction, Selectors: selectors,
	})

	select {
	case update := <-sess.DOMUpdateCh:
		sess.Engine.LoadChildren(update.Summary, update.ResolvedItems)
		log.Printf("Scroll: updated children for tab %d after scroll %s (%d zones resolved)",
			tabID, direction, len(update.ResolvedItems))
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(scrollWaitTimeout):
		return fmt.Errorf("scroll timed out waiting for DOM update")
	}
}

// sendGoto navigates the browser to a new URL via the extension WebSocket.
// Used by voice mode's goto tool. The extension handles navigation + auto-snapshot.
func (h *Handler) sendGoto(tabID int, url string) {
	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()
	if conn == nil {
		log.Printf("Voice: no extension connected, opening Chrome: %s", url)
		if h.openBrowserFn != nil {
			h.openBrowserFn(url)
		}
		return
	}
	h.sendMessage(conn, OutboundMessage{
		Type:  MsgGotoURL,
		TabID: tabID,
		URL:   url,
	})
}

// sendRescan triggers a fresh DOM snapshot + screenshot from the extension.
// The extension calls captureAndSend() which flows through the normal
// DOM_SNAPSHOT → Cartographer → schema cache pipeline.
func (h *Handler) sendRescan(tabID int, macheID string) {
	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()
	if conn == nil {
		log.Printf("Voice: extension not connected, cannot rescan")
		return
	}
	h.sendMessage(conn, OutboundMessage{
		Type:    MsgRescan,
		TabID:   tabID,
		MacheID: macheID,
	})
}

// handleTabsListed delivers the tab list to whichever session is waiting.
func (h *Handler) handleTabsListed(msg InboundMessage) {
	// Deliver to the voice session (or tab 0 if no voice tab yet).
	h.mu.Lock()
	tabID := h.activeVoiceTab
	h.mu.Unlock()
	sess := h.getSession(tabID)
	select {
	case sess.TabsListedCh <- msg.Tabs:
	default:
		log.Printf("WebSocket: TABS_LISTED dropped (no listener on tab %d)", tabID)
	}
}

// sendListTabs asks the extension for all open Chrome tabs.
func (h *Handler) sendListTabs() {
	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()
	if conn == nil {
		log.Printf("Voice: extension not connected, cannot list tabs")
		return
	}
	h.sendMessage(conn, OutboundMessage{Type: MsgListTabs})
}

// sendSwitchTab tells the extension to activate a specific tab.
func (h *Handler) sendSwitchTab(tabID int) {
	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()
	if conn == nil {
		log.Printf("Voice: extension not connected, cannot switch tab")
		return
	}
	h.sendMessage(conn, OutboundMessage{Type: MsgSwitchTab, TabID: tabID})
}

// sendCreateTab tells the extension to open a new Chrome tab with the given URL.
// Used by voice mode's open_url tool when no browser tab is active.
func (h *Handler) sendCreateTab(url string) {
	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()
	if conn == nil {
		log.Printf("Voice: no extension connected, cannot create tab for: %s", url)
		if h.openBrowserFn != nil {
			h.openBrowserFn(url)
		}
		return
	}
	h.sendMessage(conn, OutboundMessage{Type: MsgCreateTab, URL: url})
}

// scrollVoice scrolls the page via the extension WebSocket. Used by voice mode
// which has its own WS connection for audio but needs the extension conn for scroll.
func (h *Handler) scrollVoice(ctx context.Context, sess *TabSession, tabID int, direction string) error {
	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("extension not connected, cannot scroll")
	}
	return h.scrollPage(ctx, conn, sess, tabID, direction)
}

var enableSaveLog = os.Getenv("XRAY_SAVE_LOGS") == "1"

// saveLog writes a timestamped log entry to logs/<kind>/.
// Opt-in via XRAY_SAVE_LOGS=1 to avoid unbounded disk usage.
func saveLog(kind, label, content string) {
	if !enableSaveLog {
		return
	}
	dir := fmt.Sprintf("logs/%s", kind)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("Failed to create log dir: %v", err)
		return
	}
	ts := time.Now().Format("2006-01-02T15-04-05")
	filename := fmt.Sprintf("%s/%s.json", dir, ts)
	entry := map[string]string{"label": label, "content": content, "timestamp": time.Now().Format(time.RFC3339)}
	data, _ := json.MarshalIndent(entry, "", "  ")
	if err := os.WriteFile(filename, data, 0o644); err != nil {
		log.Printf("Failed to write log: %v", err)
	} else {
		log.Printf("Saved %s log to %s", kind, filename)
	}
}

func (h *Handler) sendMessage(conn *websocket.Conn, msg OutboundMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Failed to marshal message: %v", err)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		log.Printf("Failed to send message: %v", err)
	}
}

// SendActionToExtension sends an EXECUTE_ACTION message over the extension WebSocket.
// Used by the voice handler to dispatch act() results to the browser.
// If the extension is disconnected, the action is queued and flushed on reconnect.
func (h *Handler) SendActionToExtension(tabID int, macheID, action, payload string) {
	h.mu.Lock()
	conn := h.conn
	if conn == nil {
		h.pending = append(h.pending, pendingAction{TabID: tabID, MacheID: macheID, Action: action})
		h.mu.Unlock()
		log.Printf("Voice: extension disconnected, queued action: %s on %s (tab %d)", action, macheID, tabID)
		return
	}
	h.mu.Unlock()

	msg := OutboundMessage{
		Type:    MsgExecuteAction,
		TabID:   tabID,
		MacheID: macheID,
		Action:  action,
		Payload: payload,
	}

	// For cv-N IDs, look up pixel center from edge-detected regions for CDP click.
	if strings.HasPrefix(macheID, "cv-") {
		sess := h.getSession(tabID)
		for _, r := range sess.CVRegions {
			if r.ID == macheID {
				msg.PixelX = r.PixelX + r.PixelW/2
				msg.PixelY = r.PixelY + r.PixelH/2
				log.Printf("CV click: %s → pixel (%d, %d) (tab %d)", macheID, msg.PixelX, msg.PixelY, tabID)
				break
			}
		}
	}

	h.sendMessage(conn, msg)
}

// HandleStatus provides a GET /status?tab_id=N endpoint for polling Doer state.
// Returns JSON: {status, goal, step, summary, url}
func (h *Handler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}

	tabIDStr := r.URL.Query().Get("tab_id")
	tabID := 0
	if tabIDStr != "" {
		var err error
		tabID, err = strconv.Atoi(tabIDStr)
		if err != nil {
			http.Error(w, "invalid tab_id", http.StatusBadRequest)
			return
		}
	}

	sess := h.lookupSession(tabID)

	resp := map[string]any{
		"status": "no_session",
		"goal":   "",
		"step":   "",
		"url":    "",
	}

	if sess != nil {
		resp["url"] = sess.GetCurrentURL()

		if sess.Doer != nil {
			status, goal, step, result := sess.Doer.State().Snapshot()
			switch status {
			case DoerIdle:
				resp["status"] = "idle"
			case DoerExecuting:
				resp["status"] = "executing"
			case DoerDone:
				resp["status"] = "done"
			case DoerFailed:
				resp["status"] = "failed"
			}
			resp["goal"] = goal
			resp["step"] = step
			if result != nil {
				resp["summary"] = result.Summary
				resp["success"] = result.Success
				if result.Error != "" {
					resp["error"] = result.Error
				}
			}
		} else {
			// Session exists but no Doer — check if schema is ready.
			select {
			case <-sess.GetSchemaReady():
				resp["status"] = "ready"
			default:
				resp["status"] = "loading"
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// HandleNavigateHTTP provides a POST /navigate endpoint for curl/UI testing.
func (h *Handler) HandleNavigateHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Intent string `json:"intent"`
		TabID  int    `json:"tab_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	sess := h.getSession(req.TabID)
	ctx := context.Background()

	// Wait for schema if needed.
	if !sess.GetEngine().HasSchema() {
		select {
		case <-sess.GetSchemaReady():
		case <-time.After(schemaWaitTimeout):
			http.Error(w, "timed out waiting for schema", http.StatusServiceUnavailable)
			return
		}
	}

	action, textResponse, err := sess.Navigator.HandleIntent(ctx, req.Intent, false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// If there's an action, also send it to the browser via WebSocket
	if action != nil {
		h.SendActionToExtension(req.TabID, action.MacheID, action.Action, action.Payload)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"action":   action,
		"response": textResponse,
	}); err != nil {
		log.Printf("Failed to encode navigate response: %v", err)
	}
}

// HandleDoerHTTP provides a POST /doer endpoint for multi-step goal execution.
// Unlike /navigate (single-turn), this submits a goal to the Doer which runs
// a multi-step loop (up to 5 steps with page-change detection).
// Poll GET /status?tab_id=N for progress and completion.
func (h *Handler) HandleDoerHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Intent   string `json:"intent"`
		TabID    int    `json:"tab_id"`
		GoalID   string `json:"goal_id"`
		ReadOnly bool   `json:"read_only"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if req.GoalID == "" {
		req.GoalID = fmt.Sprintf("wa-%d", time.Now().UnixMilli())
	}

	sess := h.getSession(req.TabID)
	doer := h.getOrCreateDoer(req.TabID, sess)
	doer.Submit(DoerGoal{
		ID:       req.GoalID,
		Text:     req.Intent,
		ReadOnly: req.ReadOnly,
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"accepted": true,
		"goal_id":  req.GoalID,
		"tab_id":   req.TabID,
	})
}

// parseBounds extracts normalized [x, y, w, h] bounds from the DOM summary text.
func parseBounds(summary string) [][4]float64 {
	matches := boundsRe.FindAllStringSubmatch(summary, -1)
	bounds := make([][4]float64, 0, len(matches))
	for _, m := range matches {
		x, _ := strconv.ParseFloat(m[1], 64)
		y, _ := strconv.ParseFloat(m[2], 64)
		w, _ := strconv.ParseFloat(m[3], 64)
		h, _ := strconv.ParseFloat(m[4], 64)
		bounds = append(bounds, [4]float64{x, y, w, h})
	}
	return bounds
}
