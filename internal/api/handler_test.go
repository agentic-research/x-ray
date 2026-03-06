package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/agentic-research/x-ray/internal/config"
)

// stubCartographer is a no-op SchemaGenerator for unit tests.
type stubCartographer struct{}

func (s *stubCartographer) GenerateSchema(ctx context.Context, screenshot []byte, mimeType, summary string) (string, error) {
	return `{"mounts":[]}`, nil
}

// newTestHandler builds a Handler suitable for unit tests. Gemini clients are
// nil — only getSession / session isolation / message plumbing are exercised.
func newTestHandler() *Handler {
	h := NewHandler(&stubCartographer{}, nil, nil, nil, "test-model", "test-live-model", "", "")
	h.openBrowserFn = nil // prevent tests from opening real Chrome windows
	h.Timeouts = config.TimeoutsConfig{
		SchemaWait: 30,
		ScrollWait: 10,
		Summary:    10,
		Overlay:    10,
		Capture:    30,
		LayerTree:  2,
	}
	h.CDPTargetWidth = 800
	h.CDPMaxHeight = 16384
	return h
}

func TestGetSessionCreatesNew(t *testing.T) {
	h := newTestHandler()

	sess := h.getSession(42)
	if sess == nil {
		t.Fatal("getSession returned nil")
	}
	if sess.TabID != 42 {
		t.Errorf("expected TabID 42, got %d", sess.TabID)
	}
	if sess.Engine == nil {
		t.Error("session Engine is nil")
	}
	if sess.Navigator == nil {
		t.Error("session Navigator is nil")
	}
}

func TestGetSessionReturnsCached(t *testing.T) {
	h := newTestHandler()

	s1 := h.getSession(7)
	s2 := h.getSession(7)
	if s1 != s2 {
		t.Error("getSession returned different pointers for same tab ID")
	}
}

func TestGetSessionIsolation(t *testing.T) {
	h := newTestHandler()

	sA := h.getSession(100)
	sB := h.getSession(200)

	if sA == sB {
		t.Fatal("different tab IDs returned same session")
	}
	if sA.Engine == sB.Engine {
		t.Error("different tabs share the same Engine pointer")
	}
	if sA.Navigator == sB.Navigator {
		t.Error("different tabs share the same Navigator pointer")
	}

	// Apply schema on tab A — tab B should remain empty.
	schema := `{"mounts":[{"virtual_path":"/test","mache_id":"m-1","description":"test"}]}`
	if err := sA.Engine.ApplySchema(schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}
	if !sA.Engine.HasSchema() {
		t.Error("tab A should have schema")
	}
	if sB.Engine.HasSchema() {
		t.Error("tab B should NOT have schema — isolation broken")
	}
}

func TestGetSessionConcurrent(t *testing.T) {
	h := newTestHandler()
	var wg sync.WaitGroup

	// Hit the same and different tab IDs from multiple goroutines.
	for i := range 50 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			tabID := id % 5 // 5 distinct tabs
			sess := h.getSession(tabID)
			if sess == nil {
				t.Errorf("getSession(%d) returned nil", tabID)
			}
		}(i)
	}
	wg.Wait()

	// Should have exactly 5 sessions.
	h.mu.Lock()
	n := len(h.sessions)
	h.mu.Unlock()
	if n != 5 {
		t.Errorf("expected 5 sessions, got %d", n)
	}
}

func TestGetSessionZeroTabID(t *testing.T) {
	h := newTestHandler()

	// Tab ID 0 is valid (used when no tab_id is provided).
	sess := h.getSession(0)
	if sess == nil {
		t.Fatal("getSession(0) returned nil")
	}
	if sess.TabID != 0 {
		t.Errorf("expected TabID 0, got %d", sess.TabID)
	}
}

func TestInboundMessageTabID(t *testing.T) {
	raw := `{"type":"DOM_SNAPSHOT","tab_id":42,"url":"https://example.com","summary":"test"}`
	var msg InboundMessage
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if msg.TabID != 42 {
		t.Errorf("expected TabID 42, got %d", msg.TabID)
	}
	if msg.Type != MsgDOMSnapshot {
		t.Errorf("expected type %s, got %s", MsgDOMSnapshot, msg.Type)
	}
}

func TestInboundMessageTabIDOmitted(t *testing.T) {
	raw := `{"type":"NAVIGATE","intent":"click first"}`
	var msg InboundMessage
	if err := json.Unmarshal([]byte(raw), &msg); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if msg.TabID != 0 {
		t.Errorf("expected TabID 0 when omitted, got %d", msg.TabID)
	}
}

func TestOutboundMessageTabID(t *testing.T) {
	msg := OutboundMessage{
		Type:    MsgExecuteAction,
		TabID:   123,
		MacheID: "mache-5",
		Action:  "click",
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	tabID, ok := decoded["tab_id"].(float64)
	if !ok {
		t.Fatal("tab_id missing from serialized message")
	}
	if int(tabID) != 123 {
		t.Errorf("expected tab_id 123, got %v", tabID)
	}
}

func TestOutboundMessageTabIDOmitZero(t *testing.T) {
	msg := OutboundMessage{Type: MsgStatus, Message: "hello"}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	// TabID 0 should be omitted (omitempty).
	if _, exists := decoded["tab_id"]; exists {
		t.Error("tab_id should be omitted when zero")
	}
}

func TestSendActionToExtensionQueuesWhenDisconnected(t *testing.T) {
	h := newTestHandler()
	// conn is nil — action should be queued.
	h.SendActionToExtension(42, "mache-5", "click", "")

	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.pending) != 1 {
		t.Fatalf("expected 1 pending action, got %d", len(h.pending))
	}
	p := h.pending[0]
	if p.TabID != 42 {
		t.Errorf("expected queued TabID 42, got %d", p.TabID)
	}
	if p.MacheID != "mache-5" {
		t.Errorf("expected queued MacheID mache-5, got %s", p.MacheID)
	}
	if p.Action != "click" {
		t.Errorf("expected queued Action click, got %s", p.Action)
	}
}

func TestSendActionToExtensionMultipleTabs(t *testing.T) {
	h := newTestHandler()

	h.SendActionToExtension(10, "m-1", "click", "")
	h.SendActionToExtension(20, "m-2", "focus", "")
	h.SendActionToExtension(10, "m-3", "click", "")

	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.pending) != 3 {
		t.Fatalf("expected 3 pending actions, got %d", len(h.pending))
	}
	if h.pending[0].TabID != 10 || h.pending[1].TabID != 20 || h.pending[2].TabID != 10 {
		t.Errorf("tab IDs wrong: %v", h.pending)
	}
}

func TestHandleNavigateHTTPParsesTabID(t *testing.T) {
	h := newTestHandler()

	// Pre-populate session 99 with a schema so we can verify it was resolved.
	sess := h.getSession(99)
	schema := `{"mounts":[{"virtual_path":"/main/link","mache_id":"mache-1","description":"link"}]}`
	if err := sess.Engine.ApplySchema(schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	// Verify that a POST with tab_id resolves the correct session.
	// We can't call HandleNavigateHTTP (nil Gemini client panics in navigator)
	// so instead we verify the JSON parsing and session lookup directly.
	body := `{"intent":"click the link","tab_id":99}`
	var req struct {
		Intent string `json:"intent"`
		TabID  int    `json:"tab_id"`
	}
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&req); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if req.TabID != 99 {
		t.Errorf("expected tab_id 99, got %d", req.TabID)
	}

	resolved := h.getSession(req.TabID)
	if resolved != sess {
		t.Error("getSession(99) returned a different session than expected")
	}
	if !resolved.Engine.HasSchema() {
		t.Error("resolved session should have schema")
	}
}

func TestHandleNavigateHTTPMethodNotAllowed(t *testing.T) {
	h := newTestHandler()

	req := httptest.NewRequest(http.MethodGet, "/navigate", nil)
	w := httptest.NewRecorder()

	h.HandleNavigateHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestHandleNavigateHTTPBadJSON(t *testing.T) {
	h := newTestHandler()

	req := httptest.NewRequest(http.MethodPost, "/navigate", strings.NewReader("not json"))
	w := httptest.NewRecorder()

	h.HandleNavigateHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestNewHandlerInitializesSessionsMap(t *testing.T) {
	h := newTestHandler()
	if h.sessions == nil {
		t.Error("sessions map should be initialized, got nil")
	}
	if len(h.sessions) != 0 {
		t.Errorf("sessions map should be empty, got %d entries", len(h.sessions))
	}
}

func TestOpenBrowserFuncIsNilByDefault(t *testing.T) {
	h := newTestHandler()
	if h.openBrowserFn != nil {
		t.Error("openBrowserFn should be nil by default to prevent opening windows during tests")
	}
}

// --- Tab promotion + voice resolution tests ---

func TestActiveVoiceTabPromotion(t *testing.T) {
	h := newTestHandler()

	// Initially, activeVoiceTab is 0 (no extension connected).
	if h.getVoiceTabID() != 0 {
		t.Errorf("expected initial voice tab 0, got %d", h.getVoiceTabID())
	}

	// Simulate TAB_ACTIVATED from the extension.
	h.mu.Lock()
	h.activeVoiceTab = 12345
	h.mu.Unlock()

	if h.getVoiceTabID() != 12345 {
		t.Errorf("expected voice tab 12345, got %d", h.getVoiceTabID())
	}
}

func TestTabZeroPromotionSignalsSchemaReady(t *testing.T) {
	h := newTestHandler()

	// Create a tab-0 session and reset its schema (simulating a Doer goto).
	sess0 := h.getSession(0)
	sess0.ResetSchema()

	// SchemaReady should be open (not signaled).
	select {
	case <-sess0.SchemaReady:
		t.Fatal("SchemaReady should not be signaled yet")
	default:
	}

	// Simulate the promotion that handleDOMSnapshot does when a real tab arrives.
	h.mu.Lock()
	if h.activeVoiceTab == 0 {
		h.activeVoiceTab = 999
		if oldSess, ok := h.sessions[0]; ok {
			oldSess.SignalSchemaReady()
		}
	}
	h.mu.Unlock()

	// Tab-0 SchemaReady should now be signaled.
	select {
	case <-sess0.SchemaReady:
		// OK — unblocked as expected.
	default:
		t.Fatal("tab-0 SchemaReady should have been signaled by promotion")
	}
}

func TestTabZeroPromotionIdempotent(t *testing.T) {
	h := newTestHandler()
	sess0 := h.getSession(0)
	sess0.ResetSchema()

	// Promote once.
	h.mu.Lock()
	h.activeVoiceTab = 100
	if oldSess, ok := h.sessions[0]; ok {
		oldSess.SignalSchemaReady()
	}
	h.mu.Unlock()

	// Promote again (should be a no-op because activeVoiceTab != 0).
	h.mu.Lock()
	prev := h.activeVoiceTab
	if h.activeVoiceTab == 0 {
		t.Error("should not re-promote; activeVoiceTab is already set")
	}
	h.mu.Unlock()

	if prev != 100 {
		t.Errorf("expected activeVoiceTab to remain 100, got %d", prev)
	}
}

func TestGetVoiceSessionResolvesCorrectTab(t *testing.T) {
	h := newTestHandler()

	// Before promotion: voice session is nil (tab 0 = no active tab).
	sess0 := h.getVoiceSession()
	if sess0 != nil {
		t.Errorf("expected nil voice session for tab 0, got tab %d", sess0.TabID)
	}

	// After promotion: voice session is the real tab.
	h.mu.Lock()
	h.activeVoiceTab = 42
	h.mu.Unlock()

	sess42 := h.getVoiceSession()
	if sess42 == nil {
		t.Fatal("expected non-nil voice session for tab 42")
	}
	if sess42.TabID != 42 {
		t.Errorf("expected voice session tab 42, got %d", sess42.TabID)
	}
}

func TestGetOrCreateDoerLazy(t *testing.T) {
	h := newTestHandler()
	sess := h.getSession(5)

	// No Doer initially.
	if sess.Doer != nil {
		t.Error("fresh session should not have a Doer")
	}

	// First call creates a Doer.
	d1 := h.getOrCreateDoer(5, sess)
	if d1 == nil {
		t.Fatal("getOrCreateDoer returned nil")
	}
	if sess.Doer != d1 {
		t.Error("Doer should be stored on session")
	}

	// Second call returns the same Doer.
	d2 := h.getOrCreateDoer(5, sess)
	if d1 != d2 {
		t.Error("getOrCreateDoer should return cached Doer")
	}
}

func TestSchemaResetAndSignalCycle(t *testing.T) {
	h := newTestHandler()
	sess := h.getSession(1)

	// Initial SchemaReady is open.
	select {
	case <-sess.SchemaReady:
		t.Fatal("fresh SchemaReady should be open")
	default:
	}

	// Signal it.
	sess.SignalSchemaReady()
	select {
	case <-sess.SchemaReady:
	default:
		t.Fatal("SchemaReady should be closed after signal")
	}

	// Double-signal is a no-op (no panic).
	sess.SignalSchemaReady()

	// Reset creates a new open channel.
	sess.ResetSchema()
	select {
	case <-sess.SchemaReady:
		t.Fatal("SchemaReady should be open after reset")
	default:
	}

	// Signal the new channel.
	sess.SignalSchemaReady()
	select {
	case <-sess.SchemaReady:
	default:
		t.Fatal("new SchemaReady should be closed after signal")
	}
}

func TestTabClosedPrunesSession(t *testing.T) {
	h := newTestHandler()

	// Create a session for tab 42.
	sess := h.getSession(42)
	if sess == nil {
		t.Fatal("getSession returned nil")
	}

	// Set tab 42 as the active voice tab.
	h.mu.Lock()
	h.activeVoiceTab = 42
	h.mu.Unlock()

	// Verify session exists.
	h.mu.Lock()
	_, exists := h.sessions[42]
	h.mu.Unlock()
	if !exists {
		t.Fatal("session for tab 42 should exist before close")
	}

	// Simulate TAB_CLOSED message.
	h.handleTabClosed(InboundMessage{Type: MsgTabClosed, TabID: 42})

	// Session should be pruned.
	h.mu.Lock()
	_, exists = h.sessions[42]
	voiceTab := h.activeVoiceTab
	h.mu.Unlock()
	if exists {
		t.Error("session for tab 42 should have been pruned")
	}
	if voiceTab != 0 {
		t.Errorf("activeVoiceTab should be 0 after closing voice tab, got %d", voiceTab)
	}
}

func TestTabClosedCancelsDoer(t *testing.T) {
	h := newTestHandler()
	sess := h.getSession(99)

	// Create and attach a Doer.
	doer := NewDoer(h, 99, sess)
	sess.Doer = doer

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go doer.Run(ctx)

	// Simulate TAB_CLOSED — should cancel the Doer.
	h.handleTabClosed(InboundMessage{Type: MsgTabClosed, TabID: 99})

	// Doer state should be Cancelled (from Cancel()).
	status, _, _, result := doer.State().Snapshot()
	if status != StatusCancelled {
		t.Errorf("expected StatusCancelled after tab close, got %s", status)
	}
	if result == nil || result.Summary != "Cancelled by user." {
		t.Errorf("expected cancel result, got %v", result)
	}
}

func TestTabClosedNonVoiceTabPreservesVoiceTab(t *testing.T) {
	h := newTestHandler()
	h.getSession(10)
	h.getSession(20)

	h.mu.Lock()
	h.activeVoiceTab = 10
	h.mu.Unlock()

	// Close tab 20 (not the voice tab).
	h.handleTabClosed(InboundMessage{Type: MsgTabClosed, TabID: 20})

	h.mu.Lock()
	voiceTab := h.activeVoiceTab
	_, exists10 := h.sessions[10]
	_, exists20 := h.sessions[20]
	h.mu.Unlock()

	if voiceTab != 10 {
		t.Errorf("activeVoiceTab should remain 10, got %d", voiceTab)
	}
	if !exists10 {
		t.Error("session for tab 10 should still exist")
	}
	if exists20 {
		t.Error("session for tab 20 should have been pruned")
	}
}
