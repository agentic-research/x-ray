package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// stubCartographer is a no-op SchemaGenerator for unit tests.
type stubCartographer struct{}

func (s *stubCartographer) GenerateSchema(ctx context.Context, screenshot []byte, mimeType, summary string) (string, error) {
	return `{"mounts":[]}`, nil
}

// newTestHandler builds a Handler suitable for unit tests. Gemini clients are
// nil — only getSession / session isolation / message plumbing are exercised.
func newTestHandler() *Handler {
	return NewHandler(&stubCartographer{}, nil, nil, "test-model", "test-live-model")
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
	h.SendActionToExtension(42, "mache-5", "click")

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

	h.SendActionToExtension(10, "m-1", "click")
	h.SendActionToExtension(20, "m-2", "focus")
	h.SendActionToExtension(10, "m-3", "click")

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
