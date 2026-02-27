package iterm

import (
	"context"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/agentic-research/mache/graph"
	pb "github.com/tmc/it2/proto"
)

// DefaultBufferLines is how many trailing lines to fetch from each session.
const DefaultBufferLines = 20

// DefaultPromptPattern matches common shell prompts to detect idle state.
var DefaultPromptPattern = regexp.MustCompile(`(?m)^.*[$#%>❯]\s*$`)

// Bridge connects to iTerm2 and maintains a live mache graph projection
// of all terminal sessions. It implements graph.Graph so it can be mounted
// directly into a CompositeGraph.
type Bridge struct {
	client *Client
	store  *graph.MemoryStore

	mu          sync.RWMutex
	sessions    map[string]*trackedSession // sessionID → state
	active      string                     // focused session ID
	selfSession string                     // the agent's own terminal session ID (blind spot)
	bufLines    int32
	prompt      *regexp.Regexp

	debug bool
}

type trackedSession struct {
	info   SessionInfo
	buffer string // ANSI-stripped
	status string // "idle" or "running"
}

// BridgeOption configures Bridge behavior.
type BridgeOption func(*Bridge)

// WithBufferLines sets how many trailing lines to fetch per session.
func WithBufferLines(n int32) BridgeOption {
	return func(b *Bridge) { b.bufLines = n }
}

// WithPromptPattern sets the regex used to detect idle prompts.
func WithPromptPattern(re *regexp.Regexp) BridgeOption {
	return func(b *Bridge) { b.prompt = re }
}

// WithDebug enables debug logging.
func WithDebug(v bool) BridgeOption {
	return func(b *Bridge) { b.debug = v }
}

// NewBridge creates a Bridge. Call Start() to connect and begin syncing.
func NewBridge(opts ...BridgeOption) *Bridge {
	b := &Bridge{
		store:       graph.NewMemoryStore(),
		sessions:    make(map[string]*trackedSession),
		bufLines:    DefaultBufferLines,
		prompt:      DefaultPromptPattern,
		selfSession: normalizeSessionID(os.Getenv("ITERM_SESSION_ID")),
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// Start connects to iTerm2, performs an initial sync, subscribes to
// layout and screen events, and starts a background event loop.
// The returned context controls the lifecycle — cancel it to stop.
func (b *Bridge) Start(ctx context.Context) error {
	c, err := Dial(ctx)
	if err != nil {
		return err
	}
	b.client = c

	// Initial sync.
	if err := b.reconcileSessions(ctx); err != nil {
		_ = c.Close()
		return fmt.Errorf("iterm bridge: initial sync: %w", err)
	}

	// Subscribe to layout changes (new/closed sessions).
	if err := c.SubscribeLayoutChanges(ctx); err != nil {
		if b.debug {
			log.Printf("iterm bridge: layout subscribe failed (non-fatal): %v", err)
		}
	}

	// Subscribe to screen updates for each session.
	b.mu.RLock()
	for sid := range b.sessions {
		if err := c.SubscribeScreenUpdates(ctx, sid); err != nil && b.debug {
			log.Printf("iterm bridge: screen subscribe %s failed: %v", sid, err)
		}
	}
	b.mu.RUnlock()

	// Event loop.
	go b.eventLoop(ctx)

	return nil
}

// eventLoop processes notifications from iTerm2.
func (b *Bridge) eventLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			_ = b.client.Close()
			return
		case msg, ok := <-b.client.Notifications():
			if !ok {
				return
			}
			b.handleNotification(ctx, msg)
		}
	}
}

func (b *Bridge) handleNotification(ctx context.Context, msg *pb.ServerOriginatedMessage) {
	notif := msg.GetNotification()
	if notif == nil {
		return
	}

	// Layout changed → reconcile sessions.
	if notif.GetLayoutChangedNotification() != nil {
		if err := b.reconcileSessions(ctx); err != nil && b.debug {
			log.Printf("iterm bridge: reconcile: %v", err)
		}
		return
	}

	// Screen update → refresh that session's buffer.
	if su := notif.GetScreenUpdateNotification(); su != nil {
		sid := su.GetSession()
		if sid != "" {
			b.refreshBuffer(ctx, sid)
		}
		return
	}
}

// reconcileSessions re-lists all iTerm2 sessions and rebuilds the graph.
func (b *Bridge) reconcileSessions(ctx context.Context) error {
	sessions, err := b.client.ListSessions(ctx)
	if err != nil {
		return err
	}

	// Get focused session.
	active, _ := b.client.GetFocusedSession(ctx)

	b.mu.Lock()

	// Detect new sessions.
	currentIDs := make(map[string]bool)
	for _, s := range sessions {
		// Blind spot: completely ignore the agent's own terminal session.
		if b.selfSession != "" && s.SessionID == b.selfSession {
			continue
		}

		currentIDs[s.SessionID] = true
		if _, exists := b.sessions[s.SessionID]; !exists {
			b.sessions[s.SessionID] = &trackedSession{
				info:   s,
				status: "idle",
			}
			// Subscribe to screen updates for new session.
			go func(sid string) {
				if err := b.client.SubscribeScreenUpdates(ctx, sid); err != nil && b.debug {
					log.Printf("iterm bridge: screen subscribe %s: %v", sid, err)
				}
			}(s.SessionID)
		} else {
			// Update metadata (title may have changed).
			b.sessions[s.SessionID].info = s
		}
	}

	// Remove closed sessions.
	for sid := range b.sessions {
		if !currentIDs[sid] {
			delete(b.sessions, sid)
		}
	}

	// Never set the active session to the blind spot.
	if active == b.selfSession {
		b.active = ""
	} else {
		b.active = active
	}
	b.mu.Unlock()

	// Fetch buffers for all sessions.
	for _, s := range sessions {
		b.refreshBuffer(ctx, s.SessionID)
	}

	b.rebuildGraph()
	return nil
}

// refreshBuffer fetches and caches a session's buffer content.
func (b *Bridge) refreshBuffer(ctx context.Context, sessionID string) {
	raw, err := b.client.GetBuffer(ctx, sessionID, b.bufLines)
	if err != nil {
		if b.debug {
			log.Printf("iterm bridge: buffer %s: %v", sessionID, err)
		}
		return
	}

	clean := StripANSI(raw)

	// Detect idle vs running from prompt pattern.
	lines := strings.Split(strings.TrimRight(clean, "\n"), "\n")
	status := "running"
	if len(lines) > 0 {
		lastLine := lines[len(lines)-1]
		if b.prompt.MatchString(lastLine) {
			status = "idle"
		}
	}

	b.mu.Lock()
	if ts, ok := b.sessions[sessionID]; ok {
		ts.buffer = clean
		ts.status = status
	}
	b.mu.Unlock()

	b.rebuildGraph()
}

// rebuildGraph rebuilds the MemoryStore from current tracked state.
func (b *Bridge) rebuildGraph() {
	b.mu.RLock()
	sessions := make([]SessionInfo, 0, len(b.sessions))
	buffers := make(map[string]string, len(b.sessions))
	statuses := make(map[string]string, len(b.sessions))
	for _, ts := range b.sessions {
		sessions = append(sessions, ts.info)
		buffers[ts.info.SessionID] = ts.buffer
		statuses[ts.info.SessionID] = ts.status
	}
	active := b.active
	b.mu.RUnlock()

	newStore := ProjectToGraph(sessions, buffers, statuses, active)

	b.mu.Lock()
	b.store = newStore
	b.mu.Unlock()
}

// --- graph.Graph interface ---

func (b *Bridge) GetNode(id string) (*graph.Node, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.store.GetNode(id)
}

func (b *Bridge) ListChildren(id string) ([]string, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.store.ListChildren(id)
}

func (b *Bridge) ReadContent(id string, buf []byte, offset int64) (int, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.store.ReadContent(id, buf, offset)
}

func (b *Bridge) GetCallers(token string) ([]*graph.Node, error) {
	return nil, nil // not applicable for terminal sessions
}

func (b *Bridge) GetCallees(id string) ([]*graph.Node, error) {
	return nil, nil // not applicable for terminal sessions
}

func (b *Bridge) Invalidate(id string) {
	// No-op; screen update notifications handle refresh.
}

// Act performs an action on a terminal session node.
// Supported actions: "type" (send text), "enter" (send special key), "focus", "new_window", "new_tab".
func (b *Bridge) Act(id, action, payload string) (*graph.ActionResult, error) {
	ctx := context.Background()

	// Handle window/tab creation which don't require an existing session.
	if action == "new_window" {
		newSession, err := b.client.CreateTab(ctx, "")
		if err != nil {
			return nil, fmt.Errorf("new_window failed: %w", err)
		}
		// Force sync reconcile so the next `ls` from the agent sees the new window
		// and doesn't get caught in a retry loop.
		_ = b.reconcileSessions(ctx)
		return &graph.ActionResult{
			NodeID:  "iterm:" + newSession,
			Action:  action,
			Path:    id,
			Payload: payload,
		}, nil
	}

	if action == "new_tab" {
		windowID := b.resolveWindowID(id)
		newSession, err := b.client.CreateTab(ctx, windowID)
		if err != nil {
			return nil, fmt.Errorf("new_tab failed: %w", err)
		}
		// Force sync reconcile so the next `ls` from the agent sees the new tab.
		_ = b.reconcileSessions(ctx)
		return &graph.ActionResult{
			NodeID:  "iterm:" + newSession,
			Action:  action,
			Path:    id,
			Payload: payload,
		}, nil
	}

	// Resolve the session ID from the node path for session-specific actions.
	// Path format: windows/{wid}/tabs/{tid}/sessions/{sid}/...
	sessionID := b.resolveSessionID(id)
	if sessionID == "" {
		return nil, fmt.Errorf("cannot resolve session from path: %s", id)
	}

	switch action {
	case "type":
		if err := b.client.SendText(ctx, sessionID, payload); err != nil {
			return nil, fmt.Errorf("type failed: %w", err)
		}
	case "enter":
		text := specialKeyText(payload)
		if err := b.client.SendText(ctx, sessionID, text); err != nil {
			return nil, fmt.Errorf("enter failed: %w", err)
		}
	case "focus":
		if err := b.client.ActivateSession(ctx, sessionID); err != nil {
			return nil, fmt.Errorf("focus failed: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported action %q for terminal session", action)
	}

	return &graph.ActionResult{
		NodeID:  "iterm:" + sessionID,
		Action:  action,
		Path:    id,
		Payload: payload,
	}, nil
}

// resolveSessionID extracts the session UUID from a graph node path.
// Expected: windows/{wid}/tabs/{tid}/sessions/{sid} or deeper.
func (b *Bridge) resolveSessionID(path string) string {
	if strings.HasPrefix(path, "active_session") || strings.HasPrefix(path, "/active_session") {
		b.mu.RLock()
		defer b.mu.RUnlock()
		return b.active
	}

	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	// Find "sessions" segment and take the next part.
	for i, p := range parts {
		if p == "sessions" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	// Also accept a direct mache_id like "iterm:{uuid}".
	if strings.HasPrefix(path, "iterm:") {
		return strings.TrimPrefix(path, "iterm:")
	}
	return ""
}

// resolveWindowID extracts the window ID from a graph node path.
// Expected: windows/{wid} or deeper.
func (b *Bridge) resolveWindowID(path string) string {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, p := range parts {
		if p == "windows" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

// specialKeyText maps named keys to their escape sequences.
func specialKeyText(key string) string {
	switch strings.ToLower(key) {
	case "", "return", "enter":
		return "\r"
	case "ctrl-c":
		return "\x03"
	case "ctrl-d":
		return "\x04"
	case "ctrl-z":
		return "\x1a"
	case "ctrl-l":
		return "\x0c"
	case "tab":
		return "\t"
	case "escape", "esc":
		return "\x1b"
	case "up":
		return "\x1b[A"
	case "down":
		return "\x1b[B"
	case "right":
		return "\x1b[C"
	case "left":
		return "\x1b[D"
	default:
		return key
	}
}
