package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jamesgardner/x-ray/internal/mache"
	"github.com/jamesgardner/x-ray/internal/navigator"
	"google.golang.org/genai"
)

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
	Navigator         IntentHandler
	SchemaReady       chan struct{}            // closed when schema is applied
	DOMUpdateCh       chan DOMUpdate           // receives summary + resolved items after scroll
	SelectorsResolved chan map[string][]string // receives resolved items from RESOLVE_SELECTORS round-trip

	schemaMu     sync.Mutex // protects SchemaReady close
	schemaClosed bool
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
	schemas        *schemaCache // domain+path → schema JSON
	activeVoiceTab int          // tab ID for native voice mode (set by TAB_ACTIVATED)
}

func NewHandler(cart SchemaGenerator, navGen navigator.ContentGenerator, liveClient *genai.Client, navModel, liveModel, dbPath string) *Handler {
	return &Handler{
		Cartographer: cart,
		NavGen:       navGen,
		LiveClient:   liveClient,
		NavModel:     navModel,
		LiveModel:    liveModel,
		sessions:     make(map[int]*TabSession),
		schemas:      newSchemaCache(dbPath),
	}
}

// getSession returns the TabSession for the given tab, creating one if needed.
func (h *Handler) getSession(tabID int) *TabSession {
	h.mu.Lock()
	defer h.mu.Unlock()

	if sess, ok := h.sessions[tabID]; ok {
		return sess
	}

	engine := mache.NewEngine()
	nav := navigator.NewAgent(h.NavGen, h.NavModel, engine)
	sess := &TabSession{
		TabID:             tabID,
		Engine:            engine,
		Navigator:         nav,
		SchemaReady:       make(chan struct{}),
		DOMUpdateCh:       make(chan DOMUpdate, 1),
		SelectorsResolved: make(chan map[string][]string, 1),
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
		case MsgVoiceLog:
			log.Printf("Voice [ext tab %d]: %s", msg.TabID, msg.Message)
		default:
			log.Printf("WebSocket: unknown message type: %s", msg.Type)
		}
	}
}

func (h *Handler) handleDOMSnapshot(conn *websocket.Conn, msg InboundMessage) {
	ctx := context.Background()
	sess := h.getSession(msg.TabID)

	// --- Schema cache lookup ---
	key := cacheKey(msg.URL)
	var schemaJSON string
	var fromCache bool

	if key != "" {
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

		cartStart := time.Now()
		var err error
		schemaJSON, err = h.Cartographer.GenerateSchema(ctx, screenshotBytes, mimeType, msg.Summary)
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
			schemaJSON, err = h.Cartographer.GenerateSchema(ctx, screenshotBytes, mimeType, msg.Summary)
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

	h.mu.Lock()
	if h.activeVoiceTab == 0 {
		h.activeVoiceTab = msg.TabID
		log.Printf("WebSocket: active voice tab initialized to %d", msg.TabID)
	}
	h.mu.Unlock()

	// Save schema to disk for reference
	saveLog("schema", msg.URL, schemaJSON)
	saveLog("summary", msg.URL, msg.Summary)

	if err := sess.Engine.ApplySchema(schemaJSON); err != nil {
		log.Printf("Engine apply failed: %v", err)
		h.sendMessage(conn, OutboundMessage{
			Type: MsgStatus, TabID: msg.TabID, Message: "Engine failed: " + err.Error(), Stage: "error",
		})
		return
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
	sess.Navigator.SetEngine(sess.Engine)

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
	if !sess.Engine.HasSchema() {
		log.Printf("Navigator: waiting for schema (tab %d) before handling: %s", msg.TabID, msg.Intent)
		h.sendMessage(conn, OutboundMessage{
			Type: MsgStatus, TabID: msg.TabID, Message: "Waiting for schema...", Stage: "navigator",
		})
		select {
		case <-sess.SchemaReady:
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
	action, textResponse, err := sess.Navigator.HandleIntent(ctx, msg.Intent)
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
			})
		}
	} else if textResponse != "" {
		saveLog("navigate", msg.Intent, textResponse)
		h.sendMessage(conn, OutboundMessage{
			Type: MsgStatus, TabID: msg.TabID, Message: textResponse, Stage: "navigator",
		})
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
		_ = exec.Command("open", "-a", "Google Chrome", url).Start()
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
func (h *Handler) sendRescan(tabID int) {
	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()
	if conn == nil {
		log.Printf("Voice: extension not connected, cannot rescan")
		return
	}
	h.sendMessage(conn, OutboundMessage{
		Type:  MsgRescan,
		TabID: tabID,
	})
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

// saveLog writes a timestamped log entry to logs/<kind>/.
func saveLog(kind, label, content string) {
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
func (h *Handler) SendActionToExtension(tabID int, macheID, action string) {
	h.mu.Lock()
	conn := h.conn
	if conn == nil {
		h.pending = append(h.pending, pendingAction{TabID: tabID, MacheID: macheID, Action: action})
		h.mu.Unlock()
		log.Printf("Voice: extension disconnected, queued action: %s on %s (tab %d)", action, macheID, tabID)
		return
	}
	h.mu.Unlock()
	h.sendMessage(conn, OutboundMessage{
		Type:    MsgExecuteAction,
		TabID:   tabID,
		MacheID: macheID,
		Action:  action,
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
	if !sess.Engine.HasSchema() {
		select {
		case <-sess.SchemaReady:
		case <-time.After(schemaWaitTimeout):
			http.Error(w, "timed out waiting for schema", http.StatusServiceUnavailable)
			return
		}
	}

	action, textResponse, err := sess.Navigator.HandleIntent(ctx, req.Intent)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// If there's an action, also send it to the browser via WebSocket
	if action != nil {
		h.SendActionToExtension(req.TabID, action.MacheID, action.Action)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"action":   action,
		"response": textResponse,
	}); err != nil {
		log.Printf("Failed to encode navigate response: %v", err)
	}
}
