package api

import (
	"context"
	"encoding/base64"
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
	"github.com/gorilla/websocket"
	"github.com/jamesgardner/x-ray/internal/focus"
	"github.com/jamesgardner/x-ray/internal/mache"
	"github.com/jamesgardner/x-ray/internal/navigator"
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

// pendingAction is an action queued for dispatch when the extension reconnects.
type pendingAction struct {
	TabID   int
	MacheID string
	Action  string
}

// DOMUpdate carries the post-scroll summary and any browser-resolved primary items.
type DOMUpdate struct {
	Summary       string
	ResolvedItems map[string][]string
}

// TabSession holds per-tab state: its own Engine and Navigator.
type TabSession struct {
	TabID             int
	Engine            *mache.Engine
	Composite         *graph.CompositeGraph // multiplexes browser + iterm
	Navigator         IntentHandler
	SchemaReady       chan struct{}            // closed when schema is applied
	DOMUpdateCh       chan DOMUpdate           // receives summary + resolved items after scroll
	DOMMutatedCh      chan struct{}            // signals in-page DOM mutation (from MutationObserver)
	SelectorsResolved chan map[string][]string // receives resolved items from RESOLVE_SELECTORS round-trip
	RescanPath        string                   // set by voice handler for targeted rescan, consumed by handleDOMSnapshot
	CurrentURL        string                   // URL of the page currently loaded or loading (prevents redundant goto)
	Doer              *Doer                    // background execution agent (created lazily on first voice session)
	doerCancel        context.CancelFunc       // cancels the Doer's Run goroutine (not just the current goal)
	TabsListedCh      chan []TabInfo           // receives tab list from LIST_TABS round-trip
	CVRegions         []EdgeRegion             // canvas regions detected via edge analysis, used for CDP pixel-click

	schemaMu     sync.Mutex // protects SchemaReady close + schemaGen
	schemaClosed bool
	schemaGen    uint64       // monotonically increasing; only the latest generation applies
	engineMu     sync.RWMutex // protects Engine pointer swaps
}

// SignalSchemaReady safely closes SchemaReady. No-op if already closed.
func (s *TabSession) SignalSchemaReady() {
	s.schemaMu.Lock()
	defer s.schemaMu.Unlock()
	if !s.schemaClosed {
		close(s.SchemaReady)
		s.schemaClosed = true
	}
}

// ResetSchema creates a fresh SchemaReady channel (used by goto navigation).
func (s *TabSession) ResetSchema() {
	s.schemaMu.Lock()
	defer s.schemaMu.Unlock()
	s.SchemaReady = make(chan struct{})
	s.schemaClosed = false
}

// GetSchemaReady returns the SchemaReady channel under the lock, preventing
// a data race between select-reading the channel and ResetSchema replacing it.
func (s *TabSession) GetSchemaReady() <-chan struct{} {
	s.schemaMu.Lock()
	defer s.schemaMu.Unlock()
	return s.SchemaReady
}

// GetEngine returns the Engine pointer under a read lock.
func (s *TabSession) GetEngine() *mache.Engine {
	s.engineMu.RLock()
	defer s.engineMu.RUnlock()
	return s.Engine
}

// SwapEngine atomically replaces the Engine pointer.
func (s *TabSession) SwapEngine(engine *mache.Engine) {
	s.engineMu.Lock()
	defer s.engineMu.Unlock()
	s.Engine = engine
}

// GetCurrentURL returns the URL currently associated with this session.
func (s *TabSession) GetCurrentURL() string {
	s.schemaMu.Lock()
	defer s.schemaMu.Unlock()
	return s.CurrentURL
}

// SetCurrentURL updates the URL for this session.
func (s *TabSession) SetCurrentURL(url string) {
	s.schemaMu.Lock()
	defer s.schemaMu.Unlock()
	s.CurrentURL = url
}

// Handler holds the dependencies for the WebSocket handler.
type Handler struct {
	Cartographer SchemaGenerator
	NavGen       navigator.ContentGenerator // for creating per-tab Navigators
	LiveClient   *genai.Client              // Live API client (voice mode)
	NavModel     string                     // model name for creating per-tab Navigators
	LiveModel    string                     // model name for voice sessions

	mu             sync.Mutex
	conn           *websocket.Conn
	pending        []pendingAction
	sessions       map[int]*TabSession
	schemas        *SchemaCache     // domain+path → schema JSON
	activeVoiceTab int              // tab ID for native voice mode (set by TAB_ACTIVATED)
	openBrowserFn  func(url string) // fallback when no WS connection; nil = no-op (tests)
	termBridge     graph.Graph      // global iTerm2 bridge (nil if iTerm not available)
}

func NewHandler(cart SchemaGenerator, navGen navigator.ContentGenerator, liveClient *genai.Client, navModel, liveModel, dbPath string) *Handler {
	return &Handler{
		Cartographer: cart,
		NavGen:       navGen,
		LiveClient:   liveClient,
		NavModel:     navModel,
		LiveModel:    liveModel,
		sessions:     make(map[int]*TabSession),
		schemas:      NewSchemaCache(dbPath),
	}
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
func (h *Handler) getVoiceSession() *TabSession {
	h.mu.Lock()
	tabID := h.activeVoiceTab
	h.mu.Unlock()
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
	h.mu.Unlock()

	log.Println("WebSocket: Client connected")

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

func (h *Handler) handleDOMSnapshot(conn *websocket.Conn, msg InboundMessage) {
	ctx := context.Background()
	sess := h.getSession(msg.TabID)

	// Early tab promotion: if the voice Doer is stranded on tab 0 (no extension
	// was connected at voice-start time), unblock it now — before the slow
	// Cartographer call. The Doer just needs to know "goto worked," and the
	// real tab's schema will be ready independently for the next command.
	h.mu.Lock()
	if h.activeVoiceTab == 0 && msg.TabID != 0 {
		h.activeVoiceTab = msg.TabID
		log.Printf("WebSocket: active voice tab promoted to %d", msg.TabID)
		if oldSess, ok := h.sessions[0]; ok {
			oldSess.SignalSchemaReady()
		}
	}
	h.mu.Unlock()

	// Claim a generation number. If another snapshot starts processing while
	// this one is in-flight (e.g., goto fires twice), the stale generation
	// is discarded at apply time to prevent overwriting a newer schema.
	sess.schemaMu.Lock()
	sess.schemaGen++
	myGen := sess.schemaGen
	sess.schemaMu.Unlock()

	// --- Schema cache lookup (bypassed on rescan) ---
	key := CacheKey(msg.URL)
	var schemaJSON string
	var fromCache bool

	if msg.IsRescan {
		log.Printf("Schema CACHE BYPASS (rescan) for %q (tab %d)", key, msg.TabID)
	} else if key != "" {
		if cached, ok := h.schemas.Get(key); ok {
			if bad := mache.ValidateSchema(cached, msg.Summary); len(bad) == 0 {
				schemaJSON = cached
				fromCache = true
				log.Printf("Schema CACHE HIT for %q (tab %d) — skipping Cartographer", key, msg.TabID)
				h.sendMessage(conn, OutboundMessage{
					Type: MsgStatus, TabID: msg.TabID, Message: "Using cached schema", Stage: "cartographer",
				})
			} else {
				log.Printf("Schema cache STALE for %q (tab %d) — %d invalid IDs: %v",
					key, msg.TabID, len(bad), bad)
			}
		} else {
			log.Printf("Schema CACHE MISS for %q (tab %d)", key, msg.TabID)
		}
	}

	// Check if this is a targeted rescan (magnifying glass mode).
	rescanPath := ""
	if msg.IsRescan {
		rescanPath = sess.RescanPath
		sess.RescanPath = "" // consume
	}

	if !fromCache {
		h.sendMessage(conn, OutboundMessage{
			Type: MsgStatus, TabID: msg.TabID, Message: "Generating semantic schema...", Stage: "cartographer",
		})

		// Decode screenshot from base64
		var screenshotBytes []byte
		if msg.Screenshot != "" {
			var err error
			screenshotBytes, err = base64.StdEncoding.DecodeString(msg.Screenshot)
			if err != nil {
				log.Printf("Failed to decode screenshot: %v", err)
			}
		}

		mimeType := "image/jpeg"

		// --- Canvas edge detection: find UI regions inside <canvas> / WebGL ---
		if len(screenshotBytes) > 0 {
			existingBounds := parseBounds(msg.Summary)
			cvRegions, annotatedJPEG, edgeErr := DetectCanvasRegions(screenshotBytes, existingBounds)
			if edgeErr != nil {
				log.Printf("Edge detection failed (tab %d): %v", msg.TabID, edgeErr)
			} else if len(cvRegions) > 0 {
				screenshotBytes = annotatedJPEG
				sess.CVRegions = cvRegions
				log.Printf("Edge detection: found %d cv regions (tab %d)", len(cvRegions), msg.TabID)
			} else {
				sess.CVRegions = nil
			}
		}

		// For targeted rescan, hint the Cartographer to output absolute sub-zone paths.
		cartSummary := msg.Summary

		// Append cv-N entries so the Cartographer sees canvas-detected regions.
		if len(sess.CVRegions) > 0 {
			for _, r := range sess.CVRegions {
				cartSummary += fmt.Sprintf(
					"ID: %s | Color: CYAN | Bounds: [%.3f, %.3f, %.3f, %.3f] | Parent: none | Tag: canvas | Text: \"[CV detected]\" | Path: canvas\n",
					r.ID, r.X, r.Y, r.W, r.H,
				)
			}
		}

		if rescanPath != "" {
			cartSummary = fmt.Sprintf(
				"[FOCUSED RESCAN: You are zoomed into the component at %s. "+
					"Map its internal sub-zones. CRITICAL: Output your virtual_paths as "+
					"absolute paths starting from the component path, e.g. %s/controls, "+
					"%s/progress_bar. Do NOT output bare paths like /controls.]\n\n%s",
				rescanPath, rescanPath, rescanPath, msg.Summary,
			)
			log.Printf("Schema: focused rescan hint for %q (tab %d)", rescanPath, msg.TabID)
		}

		cartStart := time.Now()
		var err error
		schemaJSON, err = h.Cartographer.GenerateSchema(ctx, screenshotBytes, mimeType, cartSummary)
		if err != nil {
			log.Printf("Cartographer failed after %s: %v", time.Since(cartStart), err)
			h.sendMessage(conn, OutboundMessage{
				Type: MsgStatus, TabID: msg.TabID, Message: "Schema generation failed: " + err.Error(), Stage: "error",
			})
			return
		}

		log.Printf("Cartographer generated schema (tab %d) in %s: %s", msg.TabID, time.Since(cartStart), schemaJSON)

		// Validate: every mache_id must exist in the DOM summary.
		if bad := mache.ValidateSchema(schemaJSON, msg.Summary); len(bad) > 0 {
			log.Printf("Cartographer hallucinated IDs: %v — regenerating", bad)
			h.sendMessage(conn, OutboundMessage{
				Type: MsgStatus, TabID: msg.TabID, Message: "Schema had invalid IDs, retrying...", Stage: "cartographer",
			})
			schemaJSON, err = h.Cartographer.GenerateSchema(ctx, screenshotBytes, mimeType, cartSummary)
			if err != nil {
				log.Printf("Cartographer retry failed: %v", err)
				h.sendMessage(conn, OutboundMessage{
					Type: MsgStatus, TabID: msg.TabID, Message: "Schema retry failed: " + err.Error(), Stage: "error",
				})
				return
			}
			log.Printf("Cartographer retry schema: %s", schemaJSON)
			if bad2 := mache.ValidateSchema(schemaJSON, msg.Summary); len(bad2) > 0 {
				log.Printf("Cartographer still hallucinating after retry: %v", bad2)
			}
		}

		// Cache the validated schema.
		if key != "" {
			h.schemas.Put(key, schemaJSON)
			log.Printf("Schema cached for %q", key)
		}
	}

	// Generation guard: discard if a newer snapshot started processing.
	sess.schemaMu.Lock()
	stale := sess.schemaGen != myGen
	if !stale {
		sess.CurrentURL = msg.URL
	}
	sess.schemaMu.Unlock()
	if stale {
		log.Printf("Schema: generation %d superseded (tab %d), discarding stale Cartographer result", myGen, msg.TabID)
		return
	}

	// Save schema to disk for reference
	saveLog("schema", msg.URL, schemaJSON)
	saveLog("summary", msg.URL, msg.Summary)

	if rescanPath != "" {
		// Targeted rescan: graft new sub-zones into existing filesystem.
		if err := sess.Engine.MergeSchema(schemaJSON); err != nil {
			log.Printf("Engine merge failed: %v", err)
			h.sendMessage(conn, OutboundMessage{
				Type: MsgStatus, TabID: msg.TabID, Message: "Engine merge failed: " + err.Error(), Stage: "error",
			})
			return
		}
		log.Printf("Schema: merged sub-zones under %q (tab %d)", rescanPath, msg.TabID)
	} else {
		if err := sess.Engine.ApplySchema(schemaJSON); err != nil {
			log.Printf("Engine apply failed: %v", err)
			h.sendMessage(conn, OutboundMessage{
				Type: MsgStatus, TabID: msg.TabID, Message: "Engine failed: " + err.Error(), Stage: "error",
			})
			return
		}
	}

	// Resolve CSS selectors via the browser so LoadChildren has the full item list.
	// This is the same mechanism used after scroll, but applied on initial load too.
	var resolvedItems map[string][]string
	if selectors := sess.Engine.ZoneSelectors(); len(selectors) > 0 {
		// Drain any stale response from a previous snapshot.
		select {
		case <-sess.SelectorsResolved:
		default:
		}
		h.sendMessage(conn, OutboundMessage{
			Type: MsgResolveSelectors, TabID: msg.TabID, Selectors: selectors,
		})
		select {
		case resolvedItems = <-sess.SelectorsResolved:
			log.Printf("Schema: resolved selectors for %d zones (tab %d)", len(resolvedItems), msg.TabID)
		case <-time.After(5 * time.Second):
			log.Printf("Schema: selector resolution timed out for tab %d, using static primary_items", msg.TabID)
		case <-ctx.Done():
			return
		}
	}

	sess.Engine.LoadChildren(msg.Summary, resolvedItems)
	sess.Navigator.SetGraph(sess.Composite)

	// Signal that schema is ready — unblocks any waiting handleNavigate or voice tool call.
	sess.SignalSchemaReady()

	var schema any
	if err := json.Unmarshal([]byte(schemaJSON), &schema); err != nil {
		log.Printf("Failed to parse schema JSON for response: %v", err)
	}
	h.sendMessage(conn, OutboundMessage{Type: MsgSchemaReady, TabID: msg.TabID, Schema: schema})
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
		h.activeVoiceTab = 0
	}
	h.mu.Unlock()
	log.Printf("WebSocket: tab %d closed, session pruned", msg.TabID)
}

// handleDOMMutated signals the Doer that an in-page DOM mutation was detected.
func (h *Handler) handleDOMMutated(msg InboundMessage) {
	sess := h.getSession(msg.TabID)
	select {
	case sess.DOMMutatedCh <- struct{}{}:
	default: // non-blocking, don't pile up
	}
}

// handleDOMUpdate receives an updated summary from the browser after a scroll.
// It signals the waiting scrollPage goroutine via the session's DOMUpdateCh.
func (h *Handler) handleDOMUpdate(msg InboundMessage) {
	saveLog("summary-scroll", fmt.Sprintf("tab-%d", msg.TabID), msg.Summary)
	sess := h.getSession(msg.TabID)
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
	sess := h.getSession(msg.TabID)
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
