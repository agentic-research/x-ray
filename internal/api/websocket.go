package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/agentic-research/mache/graph"
	"github.com/agentic-research/mache/mount"
	"github.com/agentic-research/x-ray/internal/cdp"
	"github.com/agentic-research/x-ray/internal/config"
	"github.com/agentic-research/x-ray/internal/focus"
	"github.com/agentic-research/x-ray/internal/interactions"
	"github.com/agentic-research/x-ray/internal/mache"
	"github.com/agentic-research/x-ray/internal/navigator"
	"github.com/gorilla/websocket"
	"google.golang.org/genai"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Handler holds the dependencies for the WebSocket handler.
type Handler struct {
	Cartographer   SchemaGenerator
	NavGen         navigator.ContentGenerator // for creating per-tab Navigators
	LiveClient     *genai.Client              // Live API client (voice mode)
	PlannerClient  *genai.Client              // regular (non-Live) Gemini client for Planner
	NavModel       string                     // model name for creating per-tab Navigators
	LiveModel      string                     // model name for voice sessions
	PlannerModel   string                     // model name for Planner (e.g., "gemini-2.5-flash")
	Timeouts       config.TimeoutsConfig      // configurable orchestration timeouts
	CDPTargetWidth float64                    // screenshot width for scale computation
	CDPMaxHeight   float64                    // max page height cap
	EnableNFSMount bool                       // mount CompositeGraph as NFS per tab
	NavSpeed       string                     // "fast" or "safe" (default)
	VoiceLanguage  string                     // BCP-47 language code (e.g., "en-US")
	VoiceName      string                     // Prebuilt voice name (e.g., "Kore")

	mu             sync.Mutex
	conn           *websocket.Conn
	pending        []pendingAction
	sessions       map[int]*TabSession
	schemas        *SchemaCache        // domain+path → schema JSON
	activeVoiceTab int                 // tab ID for native voice mode (set by TAB_ACTIVATED)
	openBrowserFn  func(url string)    // fallback when no WS connection; nil = no-op (tests)
	termBridge     graph.Graph         // global iTerm2 bridge (nil if iTerm not available)
	planner        *Planner            // Planner for /agent/task (non-voice Talker)
	cdpProxy       *cdp.Proxy          // CDP proxy for Dumb Pipe architecture
	nfsRoot        *graph.HotSwapGraph // NFS root → active tab's CompositeGraph (flat: browser/, iterm/, ...)
	nfsServer      *mount.Server       // single NFS server (nil if disabled)
	nfsMountPath   string              // e.g. "/tmp/xray-mache/"
	nfsActiveTab   int                 // tab ID currently exposed via NFS
	cookiesSetCh   chan struct{}       // signaled when COOKIES_SET ack received
	voiceTabCh     chan int            // signaled when a new tab is activated (cold start or cross-origin goto)
	backend        BrowserBackend      // nil = extension mode (default); set for CF Browser Rendering
	connCtx        context.Context     // cancelled when the current WS connection closes
	connCancel     context.CancelFunc  // cancels connCtx
	videoFrameCh   chan videoFrame     // screenshot → voice session (1-buffered, non-blocking send)
	videoEnabled   atomic.Bool         // toggled by screen_share tool; gates videoFrameCh sends
}

// videoFrame carries a page screenshot to the Talker's Live session.
type videoFrame struct {
	Data []byte
	MIME string
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
		cdpProxy:      cdp.New(nil),
		cookiesSetCh:  make(chan struct{}, 1),
		voiceTabCh:    make(chan int, 1),
		videoFrameCh:  make(chan videoFrame, 1),
	}
	if client != nil && plannerModel != "" {
		h.planner = &Planner{handler: h, client: client, model: plannerModel}
	}
	return h
}

// StartNFS initializes the shared NFS server and mounts it once.
// Call this after NewHandler if EnableNFSMount is true.
// Tabs are registered/unregistered as subdirectories dynamically.
func (h *Handler) StartNFS() error {
	// Start with an empty graph; swapped to active tab's CompositeGraph on first connect.
	h.nfsRoot = graph.NewHotSwapGraph(graph.NewMemoryStore())
	h.nfsMountPath = "/tmp/xray-mache"
	if err := os.MkdirAll(h.nfsMountPath, 0o755); err != nil {
		return fmt.Errorf("NFS mkdir: %w", err)
	}
	srv, err := mount.NFS(h.nfsRoot, h.nfsMountPath, nil)
	if err != nil {
		return fmt.Errorf("NFS mount: %w", err)
	}
	h.nfsServer = srv
	log.Printf("NFS: mounted at %s (port %d)", h.nfsMountPath, srv.Port())
	return nil
}

// StopNFS unmounts and stops the shared NFS server.
func (h *Handler) StopNFS() {
	if h.nfsServer == nil {
		return
	}
	if err := mount.Unmount(h.nfsMountPath); err != nil {
		log.Printf("NFS: unmount failed: %v", err)
	}
	_ = h.nfsServer.Close()
	_ = os.RemoveAll(h.nfsMountPath)
	log.Printf("NFS: stopped")
}

// SetBrowserBackend sets the BrowserBackend for headless browser mode (e.g., CF Browser Rendering).
// When set, captureGo and dispatchAction route through the backend instead of the extension WebSocket.
func (h *Handler) SetBrowserBackend(b BrowserBackend) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.backend = b
}

// GetBrowserBackend returns the current BrowserBackend (nil if extension mode).
func (h *Handler) GetBrowserBackend() BrowserBackend {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.backend
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
	if h.termBridge != nil && os.Getenv("WEBARENA_MODE") == "" {
		if err := composite.Mount("iterm", h.termBridge); err != nil {
			log.Printf("Session: mount iterm (tab %d): %v", tabID, err)
		}
	}

	// Mount interaction tracking graph for Navigator scratchpad + status signaling.
	ixGraph := interactions.New()
	if err := composite.Mount("interactions", ixGraph); err != nil {
		log.Printf("Session: mount interactions (tab %d): %v", tabID, err)
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
	if h.NavSpeed == "fast" {
		nav.FastMode = true
	}
	sess := &TabSession{
		TabID:             tabID,
		Engine:            engine,
		Composite:         composite,
		Navigator:         nav,
		Tasks:             ixGraph,
		SchemaReady:       make(chan struct{}),
		DOMUpdateCh:       make(chan DOMUpdate, 1),
		DOMMutatedCh:      make(chan struct{}, 1),
		SelectorsResolved: make(chan map[string][]string, 1),
		TabsListedCh:      make(chan []TabInfo, 1),
		SummaryCh:         make(chan SummaryResponse, 1),
		OverlayDrawnCh:    make(chan struct{}, 1),
		OverlayRemovedCh:  make(chan struct{}, 1),
		captureSem:        make(chan struct{}, 1),
	}
	// If NFS is enabled and no tab is active yet, swap to this tab's graph.
	if h.nfsRoot != nil && h.nfsActiveTab == 0 {
		h.nfsRoot.Swap(composite)
		h.nfsActiveTab = tabID
		log.Printf("Session: NFS showing tab %d at %s", tabID, h.nfsMountPath)
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

	// Connection-scoped context: cancelled when this WS handler returns
	// (i.e., when the browser disconnects). All goroutines spawned from this
	// connection inherit this context so they abort instead of running to
	// completion against a dead socket.
	connCtx, connCancel := context.WithCancel(context.Background())
	defer connCancel()

	h.mu.Lock()
	// Cancel any previous connection's context before replacing.
	if h.connCancel != nil {
		h.connCancel()
	}
	h.connCtx = connCtx
	h.connCancel = connCancel
	h.conn = conn
	h.cdpProxy.SetSender(h)
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
			go h.handleDOMSnapshot(connCtx, conn, msg)
		case MsgNavigate:
			go h.handleNavigate(connCtx, conn, msg)
		case MsgDOMUpdate:
			h.handleDOMUpdate(msg)
		case MsgSelectorsResolved:
			h.handleSelectorsResolved(msg)
		case MsgTabActivated:
			h.mu.Lock()
			prevTab := h.activeVoiceTab
			h.activeVoiceTab = msg.TabID
			// Notify Doer when a new tab appears:
			//   - Cold start: tab 0 → real tab
			//   - Cross-origin goto: old tab → new tab opened by sendCreateTab
			if msg.TabID != 0 && msg.TabID != prevTab {
				select {
				case h.voiceTabCh <- msg.TabID:
				default:
				}
			}
			// Swap NFS root to the newly activated tab's CompositeGraph.
			if h.nfsRoot != nil {
				if sess, ok := h.sessions[msg.TabID]; ok {
					h.nfsRoot.Swap(sess.Composite)
					h.nfsActiveTab = msg.TabID
				}
			}
			h.mu.Unlock()
			log.Printf("WebSocket: active tab set to %d", msg.TabID)
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
		case MsgShellCommand:
			go h.handleShellCommand(conn, msgBytes)

		// Go-driven capture orchestration messages.
		case MsgPageReady:
			go h.captureGo(connCtx, msg.TabID, msg.IsRescan, msg.TargetMacheID)
		case MsgSummaryResponse:
			h.handleSummaryResponse(msg)
		case MsgOverlayDrawn:
			h.handleOverlayDrawn(msg)
		case MsgOverlayRemoved:
			h.handleOverlayRemoved(msg)
		case MsgHumanOverlayDrawn:
			// Ack only — no action needed.
		case MsgCookiesSet:
			h.handleCookiesSet(msg)

		// CDP proxy messages (Dumb Pipe architecture).
		case MsgCDPAttached:
			h.cdpProxy.HandleAttached(msg.TabID)
		case MsgCDPAttachFailed:
			h.cdpProxy.HandleAttachFailed(msg.TabID, msg.CDPError)
		case MsgCDPResult:
			h.cdpProxy.HandleResult(msg.CDPRequestID, msg.CDPResult)
		case MsgCDPError:
			h.cdpProxy.HandleError(msg.CDPRequestID, msg.CDPError)
		case MsgCDPEvent:
			h.cdpProxy.HandleEvent(msg.TabID, msg.CDPMethod, msg.CDPParams)
		case MsgCDPDetached:
			h.cdpProxy.HandleDetached(msg.TabID)

		default:
			log.Printf("WebSocket: unknown message type: %s", msg.Type)
		}
	}
}

func (h *Handler) handleNavigate(ctx context.Context, conn *websocket.Conn, msg InboundMessage) {
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
		case <-time.After(config.Dur(h.Timeouts.SchemaWait)):
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
	// If closed tab was NFS-active, swap to another tab or empty.
	if h.nfsRoot != nil && h.nfsActiveTab == msg.TabID {
		h.nfsActiveTab = 0
		for tabID, sess := range h.sessions {
			if tabID != 0 {
				h.nfsRoot.Swap(sess.Composite)
				h.nfsActiveTab = tabID
				break
			}
		}
		if h.nfsActiveTab == 0 {
			h.nfsRoot.Swap(graph.NewMemoryStore())
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
		Summary:        msg.Summary,
		ResolvedItems:  msg.ResolvedItems,
		AtBottom:       msg.AtBottom,
		AtTop:          msg.AtTop,
		ScrollMoved:    msg.ScrollMoved,
		ScrollY:        msg.ScrollY,
		ScrollHeight:   msg.ScrollHeight,
		ViewportHeight: msg.ViewportHeight,
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
		// Message delivered to snapshot handler (which logs it).
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
		// Update the Navigator's viewport state so tree dumps show position.
		sess.Navigator.SetViewport(update.ScrollY, update.ScrollHeight, update.ViewportHeight)
		log.Printf("Scroll: updated children for tab %d after scroll %s (%d zones resolved, viewport %.0f/%.0f)",
			tabID, direction, len(update.ResolvedItems), update.ScrollY, update.ScrollHeight)
		// Report when the page didn't actually move (already at boundary).
		if !update.ScrollMoved || (direction == "down" && update.AtBottom) {
			if update.AtBottom {
				return navigator.ErrAtBottom
			}
			if update.AtTop {
				return navigator.ErrAtTop
			}
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(config.Dur(h.Timeouts.ScrollWait)):
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

// handleCookiesSet processes the COOKIES_SET ack from the extension.
func (h *Handler) handleCookiesSet(msg InboundMessage) {
	select {
	case h.cookiesSetCh <- struct{}{}:
	default:
	}
}

// injectCookies sends session cookies to the extension for injection via chrome.cookies API,
// then reloads the tab so the page loads with the authenticated session.
func (h *Handler) injectCookies(ctx context.Context, tabID int, cookies []struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Domain string `json:"domain"`
	Path   string `json:"path"`
}, startURL string,
) {
	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()
	if conn == nil {
		log.Printf("Planner: no extension connected, cannot inject cookies")
		return
	}

	// Drain stale ack.
	select {
	case <-h.cookiesSetCh:
	default:
	}

	var msgs []CookieMsg
	for _, c := range cookies {
		msgs = append(msgs, CookieMsg{
			Name:   c.Name,
			Value:  c.Value,
			Domain: c.Domain,
			Path:   c.Path,
			URL:    fmt.Sprintf("http://%s%s", c.Domain, c.Path),
		})
	}
	h.sendMessage(conn, OutboundMessage{Type: MsgSetCookies, TabID: tabID, Cookies: msgs})

	// Wait for ack.
	select {
	case <-h.cookiesSetCh:
		log.Printf("Planner: injected %d cookies for tab %d", len(msgs), tabID)
	case <-time.After(5 * time.Second):
		log.Printf("Planner: cookie injection timed out for tab %d", tabID)
	case <-ctx.Done():
		return
	}

	// Reload the tab so it picks up the cookies.
	// Reset schema so we wait for the post-reload capture.
	sess := h.getSession(tabID)
	sess.ResetSchema()
	h.sendMessage(conn, OutboundMessage{Type: MsgGotoURL, TabID: tabID, URL: startURL})
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

// SendJSON implements cdp.Sender, writing a raw JSON map over the extension WebSocket.
func (h *Handler) SendJSON(msg map[string]any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conn == nil {
		return fmt.Errorf("extension not connected")
	}
	return h.conn.WriteMessage(websocket.TextMessage, data)
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
		for _, r := range sess.GetCVRegions() {
			if r.ID == macheID {
				msg.PixelX = r.PixelX + r.PixelW/2
				msg.PixelY = r.PixelY + r.PixelH/2
				log.Printf("CV click: %s → pixel (%d, %d) (tab %d)", macheID, msg.PixelX, msg.PixelY, tabID)

				// Dispatch click directly via CDP proxy.
				// Use connection-scoped context so the goroutine aborts on WS disconnect.
				h.mu.Lock()
				parentCtx := h.connCtx
				h.mu.Unlock()
				if parentCtx == nil {
					parentCtx = context.Background()
				}
				go func() {
					ctx, cancel := context.WithTimeout(parentCtx, 10*time.Second)
					defer cancel()
					if err := h.cdpProxy.Attach(ctx, tabID); err != nil {
						log.Printf("CV click: attach failed: %v", err)
						return
					}
					defer func() {
						// Detach uses Background — must complete even if connection closed.
						dCtx, dCancel := context.WithTimeout(context.Background(), 2*time.Second)
						defer dCancel()
						_ = h.cdpProxy.Detach(dCtx, tabID)
					}()
					if err := cdp.PixelClick(ctx, h.cdpProxy, tabID, float64(msg.PixelX), float64(msg.PixelY), h.CDPTargetWidth); err != nil {
						log.Printf("CV click: PixelClick failed: %v", err)
					} else {
						log.Printf("CV click: dispatched click at (%d, %d) for %s (tab %d)", msg.PixelX, msg.PixelY, macheID, tabID)
					}
				}()
				return
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

	// tab_id=0 → resolve to active browser tab (same as /doer).
	if tabID == 0 {
		h.mu.Lock()
		tabID = h.activeVoiceTab
		h.mu.Unlock()
	}

	sess := h.lookupSession(tabID)

	resp := map[string]any{
		"status": "no_session",
		"intent": "",
		"step":   "",
		"url":    "",
	}

	if sess != nil {
		resp["url"] = sess.GetCurrentURL()

		if sess.Doer != nil {
			status, intent, step, result := sess.Doer.State().Snapshot()
			resp["status"] = string(status)
			resp["intent"] = intent
			resp["step"] = step
			if result != nil {
				resp["interaction_id"] = result.InteractionID
				resp["summary"] = result.Summary
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

// HandleCaptureTestdata saves the current tab's DOM summary + screenshot
// to testdata/<name>/ for offline replay testing.
// GET /capture-testdata?name=youtube
func (h *Handler) HandleCaptureTestdata(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "?name= required (e.g. youtube)", http.StatusBadRequest)
		return
	}

	// Resolve active tab.
	h.mu.Lock()
	tabID := h.activeVoiceTab
	h.mu.Unlock()
	if tabID == 0 {
		http.Error(w, "no active browser tab", http.StatusPreconditionFailed)
		return
	}

	sess := h.lookupSession(tabID)
	if sess == nil {
		http.Error(w, "no session for active tab", http.StatusNotFound)
		return
	}

	dir := "testdata/" + name
	os.MkdirAll(dir, 0o755)

	saved := map[string]string{}

	// Grab screenshot from session.
	screenshot, mime := sess.GetScreenshot()
	if len(screenshot) > 0 {
		path := dir + "/page.png"
		if err := os.WriteFile(path, screenshot, 0o644); err != nil {
			log.Printf("capture-testdata: write screenshot: %v", err)
		} else {
			saved["screenshot"] = fmt.Sprintf("%s (%d bytes, %s)", path, len(screenshot), mime)
		}
	}

	// Request fresh DOM summary from extension (raw format with Bounds/Tag/etc).
	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()
	if conn != nil {
		// Drain any stale summary.
		select {
		case <-sess.SummaryCh:
		default:
		}
		h.sendMessage(conn, OutboundMessage{Type: MsgRequestSummary, TabID: tabID})
		select {
		case summaryResp := <-sess.SummaryCh:
			if summaryResp.Summary != "" {
				path := dir + "/page_summary.txt"
				if err := os.WriteFile(path, []byte(summaryResp.Summary), 0o644); err != nil {
					log.Printf("capture-testdata: write summary: %v", err)
				} else {
					lines := strings.Count(summaryResp.Summary, "\n")
					saved["summary"] = fmt.Sprintf("%s (%d lines)", path, lines)
				}
			}
		case <-time.After(10 * time.Second):
			log.Printf("capture-testdata: summary request timed out")
		}
	}

	// Save URL.
	url := sess.GetCurrentURL()
	if url != "" {
		os.WriteFile(dir+"/url.txt", []byte(url+"\n"), 0o644)
		saved["url"] = url
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"dir":   dir,
		"saved": saved,
		"tab_id": tabID,
	})
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
		case <-time.After(config.Dur(h.Timeouts.SchemaWait)):
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
		Intent        string `json:"intent"`
		TabID         int    `json:"tab_id"`
		InteractionID string `json:"interaction_id"`
		ReadOnly      bool   `json:"read_only"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if req.InteractionID == "" {
		req.InteractionID = fmt.Sprintf("ix-%d", time.Now().UnixMilli())
	}

	// tab_id=0 → resolve to active browser tab (same behavior as voice flow).
	// Wait up to 10s for a tab to connect if none yet (agentd just started).
	tabID := req.TabID
	if tabID == 0 {
		h.mu.Lock()
		tabID = h.activeVoiceTab
		h.mu.Unlock()
		if tabID == 0 {
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				time.Sleep(500 * time.Millisecond)
				h.mu.Lock()
				tabID = h.activeVoiceTab
				h.mu.Unlock()
				if tabID != 0 {
					break
				}
			}
		}
		if tabID == 0 {
			http.Error(w, "no active browser tab — open a page with X-Ray extension first", http.StatusPreconditionFailed)
			return
		}
	}

	sess := h.getSession(tabID)
	doer := h.getOrCreateDoer(tabID, sess)
	doer.Submit(Interaction{
		ID:       req.InteractionID,
		Intent:   req.Intent,
		ReadOnly: req.ReadOnly,
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"accepted":       true,
		"interaction_id": req.InteractionID,
		"tab_id":         req.TabID,
	})
}

// handleSummaryResponse delivers the content-script summary to the waiting captureGo.
func (h *Handler) handleSummaryResponse(msg InboundMessage) {
	sess := h.lookupSession(msg.TabID)
	if sess == nil {
		return
	}
	select {
	case sess.SummaryCh <- SummaryResponse{Summary: msg.Summary, URL: msg.URL}:
	default:
		log.Printf("WebSocket: SUMMARY_RESPONSE for tab %d but no listener, discarding", msg.TabID)
	}
}

// handleOverlayDrawn signals that the machine overlay is visible.
func (h *Handler) handleOverlayDrawn(msg InboundMessage) {
	sess := h.lookupSession(msg.TabID)
	if sess == nil {
		return
	}
	select {
	case sess.OverlayDrawnCh <- struct{}{}:
	default:
	}
}

// handleOverlayRemoved signals that the machine overlay has been removed.
func (h *Handler) handleOverlayRemoved(msg InboundMessage) {
	sess := h.lookupSession(msg.TabID)
	if sess == nil {
		return
	}
	select {
	case sess.OverlayRemovedCh <- struct{}{}:
	default:
	}
}

// parseBounds extracts normalized [x, y, w, h] bounds from the DOM summary text.
func parseBounds(summary string) [][4]float64 {
	return mache.ParseAllBounds(summary)
}
