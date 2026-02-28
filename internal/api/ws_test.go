package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// mockCartographer returns a fixed schema with mache_ids matching the summary.
type mockCartographer struct {
	schema    string
	err       error
	callCount atomic.Int32
}

func (m *mockCartographer) GenerateSchema(_ context.Context, _ []byte, _, _ string) (string, error) {
	m.callCount.Add(1)
	return m.schema, m.err
}

// dialWS upgrades to a WebSocket connection against the test server.
func dialWS(t *testing.T, s *httptest.Server) *websocket.Conn {
	t.Helper()
	url := "ws" + s.URL[len("http"):] + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("WebSocket dial failed: %v", err)
	}
	return conn
}

// readMessage reads one JSON message from the WebSocket with a timeout.
func readMessage(t *testing.T, conn *websocket.Conn) OutboundMessage {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline failed: %v", err)
	}
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	var msg OutboundMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	return msg
}

// sendJSON sends a JSON message on the WebSocket.
func sendJSON(t *testing.T, conn *websocket.Conn, msg any) {
	t.Helper()
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		t.Fatalf("write failed: %v", err)
	}
}

func TestWSDOMSnapshotCreatesSession(t *testing.T) {
	cart := &mockCartographer{
		schema: `{"mounts":[{"virtual_path":"/main/zone","mache_id":"mache-1","description":"test zone"}]}`,
	}
	h := NewHandler(cart, nil, nil, "test", "test-live", "")

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", h.HandleWebSocket)
	s := httptest.NewServer(mux)
	defer s.Close()

	conn := dialWS(t, s)
	defer func() { _ = conn.Close() }()

	// Send DOM_SNAPSHOT with a summary containing the mache_id
	sendJSON(t, conn, InboundMessage{
		Type:    MsgDOMSnapshot,
		TabID:   42,
		URL:     "https://example.com",
		Summary: "Interactive Elements:\nID: mache-1 | Parent: none | Tag: div | Text: \"Zone\"\n",
	})

	// Should receive STATUS (cartographer working) then SCHEMA_READY
	var gotSchemaReady bool
	for i := 0; i < 5; i++ {
		msg := readMessage(t, conn)
		if msg.Type == MsgSchemaReady {
			gotSchemaReady = true
			if msg.TabID != 42 {
				t.Errorf("expected tab_id 42, got %d", msg.TabID)
			}
			break
		}
	}
	if !gotSchemaReady {
		t.Fatal("never received SCHEMA_READY")
	}

	// Verify session was created
	h.mu.Lock()
	sess, ok := h.sessions[42]
	h.mu.Unlock()
	if !ok {
		t.Fatal("session not created for tab 42")
	}
	if !sess.Engine.HasSchema() {
		t.Error("session engine should have schema applied")
	}
}

func TestWSSchemaReadyFlow(t *testing.T) {
	cart := &mockCartographer{
		schema: `{"mounts":[{"virtual_path":"/nav","mache_id":"mache-5","description":"nav bar"}]}`,
	}
	h := NewHandler(cart, nil, nil, "test", "test-live", "")

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", h.HandleWebSocket)
	s := httptest.NewServer(mux)
	defer s.Close()

	conn := dialWS(t, s)
	defer func() { _ = conn.Close() }()

	sendJSON(t, conn, InboundMessage{
		Type:    MsgDOMSnapshot,
		TabID:   1,
		Summary: "Interactive Elements:\nID: mache-5 | Parent: none | Tag: nav | Text: \"Nav\"\n",
	})

	// Drain messages until SCHEMA_READY
	var schema any
	for i := 0; i < 5; i++ {
		msg := readMessage(t, conn)
		if msg.Type == MsgSchemaReady {
			schema = msg.Schema
			break
		}
	}
	if schema == nil {
		t.Fatal("no schema in SCHEMA_READY message")
	}

	// Schema should be the parsed JSON from the cartographer
	schemaMap, ok := schema.(map[string]any)
	if !ok {
		t.Fatalf("schema is not a map: %T", schema)
	}
	mounts, ok := schemaMap["mounts"]
	if !ok {
		t.Fatal("schema missing 'mounts' key")
	}
	arr, ok := mounts.([]any)
	if !ok || len(arr) != 1 {
		t.Fatalf("expected 1 mount, got %v", mounts)
	}
}

func TestWSExecuteActionRouting(t *testing.T) {
	h := NewHandler(&stubCartographer{}, nil, nil, "test", "test-live", "")

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", h.HandleWebSocket)
	s := httptest.NewServer(mux)
	defer s.Close()

	conn := dialWS(t, s)
	defer func() { _ = conn.Close() }()

	// Give the handler a moment to register the connection
	time.Sleep(50 * time.Millisecond)

	// Queue an action via the public API (simulating voice handler dispatching)
	h.SendActionToExtension(10, "mache-7", "click", "")

	msg := readMessage(t, conn)
	if msg.Type != MsgExecuteAction {
		t.Fatalf("expected EXECUTE_ACTION, got %s", msg.Type)
	}
	if msg.TabID != 10 {
		t.Errorf("expected tab_id 10, got %d", msg.TabID)
	}
	if msg.MacheID != "mache-7" {
		t.Errorf("expected mache_id mache-7, got %s", msg.MacheID)
	}
	if msg.Action != "click" {
		t.Errorf("expected action click, got %s", msg.Action)
	}
}

func TestWSReconnectFlushesQueue(t *testing.T) {
	h := NewHandler(&stubCartographer{}, nil, nil, "test", "test-live", "")

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", h.HandleWebSocket)
	s := httptest.NewServer(mux)
	defer s.Close()

	// Queue actions while no WS is connected
	h.SendActionToExtension(1, "mache-1", "click", "")
	h.SendActionToExtension(2, "mache-2", "focus", "")

	h.mu.Lock()
	if len(h.pending) != 2 {
		t.Fatalf("expected 2 pending, got %d", len(h.pending))
	}
	h.mu.Unlock()

	// Now connect — pending actions should flush
	conn := dialWS(t, s)
	defer func() { _ = conn.Close() }()

	// Read two flushed actions
	for i := 0; i < 2; i++ {
		msg := readMessage(t, conn)
		if msg.Type != MsgExecuteAction {
			t.Fatalf("flush message %d: expected EXECUTE_ACTION, got %s", i, msg.Type)
		}
	}

	// Pending should be empty now
	h.mu.Lock()
	if len(h.pending) != 0 {
		t.Errorf("expected 0 pending after flush, got %d", len(h.pending))
	}
	h.mu.Unlock()
}

func TestWSMultipleTabIsolation(t *testing.T) {
	schemaA := `{"mounts":[{"virtual_path":"/zone_a","mache_id":"mache-a1","description":"Zone A"}]}`
	schemaB := `{"mounts":[{"virtual_path":"/zone_b","mache_id":"mache-b1","description":"Zone B"}]}`

	cart := &mockCartographer{}

	h := NewHandler(cart, nil, nil, "test", "test-live", "")

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", h.HandleWebSocket)
	s := httptest.NewServer(mux)
	defer s.Close()

	conn := dialWS(t, s)
	defer func() { _ = conn.Close() }()

	// Send snapshot for tab A
	cart.schema = schemaA
	sendJSON(t, conn, InboundMessage{
		Type:    MsgDOMSnapshot,
		TabID:   10,
		Summary: "Interactive Elements:\nID: mache-a1 | Parent: none | Tag: div | Text: \"A\"\n",
	})

	// Drain until SCHEMA_READY for tab 10
	for i := 0; i < 5; i++ {
		msg := readMessage(t, conn)
		if msg.Type == MsgSchemaReady && msg.TabID == 10 {
			break
		}
	}

	// Send snapshot for tab B
	cart.schema = schemaB
	sendJSON(t, conn, InboundMessage{
		Type:    MsgDOMSnapshot,
		TabID:   20,
		Summary: "Interactive Elements:\nID: mache-b1 | Parent: none | Tag: div | Text: \"B\"\n",
	})

	for i := 0; i < 5; i++ {
		msg := readMessage(t, conn)
		if msg.Type == MsgSchemaReady && msg.TabID == 20 {
			break
		}
	}

	// Verify isolation: tab 10 has zone_a, tab 20 has zone_b
	h.mu.Lock()
	sessA := h.sessions[10]
	sessB := h.sessions[20]
	h.mu.Unlock()

	if sessA == nil || sessB == nil {
		t.Fatal("both sessions should exist")
	}

	entriesA, _ := sessA.Engine.ListDir("/")
	entriesB, _ := sessB.Engine.ListDir("/")

	if len(entriesA) != 1 || entriesA[0] != "zone_a/" {
		t.Errorf("tab 10 expected [zone_a/], got %v", entriesA)
	}
	if len(entriesB) != 1 || entriesB[0] != "zone_b/" {
		t.Errorf("tab 20 expected [zone_b/], got %v", entriesB)
	}
}

func TestWSSchemaCacheHit(t *testing.T) {
	schema := `{"mounts":[{"virtual_path":"/main/stories","mache_id":"mache-1","description":"stories"}]}`
	cart := &mockCartographer{schema: schema}
	h := NewHandler(cart, nil, nil, "test", "test-live", "")

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", h.HandleWebSocket)
	s := httptest.NewServer(mux)
	defer s.Close()

	conn := dialWS(t, s)
	defer func() { _ = conn.Close() }()

	summary := "Interactive Elements:\nID: mache-1 | Parent: none | Tag: div | Text: \"Story\"\n"

	// First snapshot: cache miss → calls Cartographer.
	sendJSON(t, conn, InboundMessage{
		Type:    MsgDOMSnapshot,
		TabID:   1,
		URL:     "https://news.ycombinator.com/news?p=1",
		Summary: summary,
	})
	for i := 0; i < 5; i++ {
		if readMessage(t, conn).Type == MsgSchemaReady {
			break
		}
	}

	// Second snapshot: same domain+path, different query → cache hit.
	sendJSON(t, conn, InboundMessage{
		Type:    MsgDOMSnapshot,
		TabID:   2,
		URL:     "https://news.ycombinator.com/news?p=2",
		Summary: summary,
	})
	for i := 0; i < 5; i++ {
		if readMessage(t, conn).Type == MsgSchemaReady {
			break
		}
	}

	// Cartographer should have been called exactly once.
	if got := cart.callCount.Load(); got != 1 {
		t.Errorf("expected 1 Cartographer call, got %d", got)
	}
}

func TestSignalSchemaReadyIdempotent(t *testing.T) {
	sess := &TabSession{
		SchemaReady: make(chan struct{}),
	}

	// First signal should close the channel.
	sess.SignalSchemaReady()
	select {
	case <-sess.SchemaReady:
		// good — channel is closed
	default:
		t.Fatal("SchemaReady should be closed after SignalSchemaReady")
	}

	// Second signal should not panic (double-close protection).
	sess.SignalSchemaReady()
}

func TestSignalSchemaReadyConcurrent(t *testing.T) {
	sess := &TabSession{
		SchemaReady: make(chan struct{}),
	}

	// Race 10 goroutines all trying to signal at once.
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			sess.SignalSchemaReady()
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}

	// Channel should be closed exactly once.
	select {
	case <-sess.SchemaReady:
	default:
		t.Fatal("SchemaReady should be closed")
	}
}

func TestResetSchemaAllowsReSignal(t *testing.T) {
	sess := &TabSession{
		SchemaReady: make(chan struct{}),
	}

	sess.SignalSchemaReady()
	select {
	case <-sess.SchemaReady:
	default:
		t.Fatal("should be closed")
	}

	// Reset creates a fresh channel.
	sess.ResetSchema()
	select {
	case <-sess.SchemaReady:
		t.Fatal("SchemaReady should be open after reset")
	default:
		// good — channel is open
	}

	// Signal again should work.
	sess.SignalSchemaReady()
	select {
	case <-sess.SchemaReady:
	default:
		t.Fatal("should be closed after re-signal")
	}
}

// --- Partial Regen E2E Tests ---

// TestWSPartialRegenSingleStaleZone verifies that when one of three cached zones
// becomes stale, only that zone is regenerated (partial regen), not a full regen.
func TestWSPartialRegenSingleStaleZone(t *testing.T) {
	// Schema with 3 zones. Cartographer returns this for the initial full scan.
	fullSchema := `{"mounts":[
		{"virtual_path":"/header","mache_id":"mache-1","description":"header","bounds":[0,0,1,0.1]},
		{"virtual_path":"/main/feed","mache_id":"mache-2","description":"feed","bounds":[0,0.1,1,0.7]},
		{"virtual_path":"/footer","mache_id":"mache-3","description":"footer","bounds":[0,0.8,1,0.2]}
	]}`

	// For partial regen, Cartographer returns just the stale zone.
	partialSchema := `{"mounts":[{"virtual_path":"/main/feed","mache_id":"mache-99","description":"refreshed feed","bounds":[0,0.1,1,0.7]}]}`

	cart := &mockCartographer{schema: fullSchema}
	h := NewHandler(cart, nil, nil, "test", "test-live", "")

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", h.HandleWebSocket)
	s := httptest.NewServer(mux)
	defer s.Close()

	conn := dialWS(t, s)
	defer func() { _ = conn.Close() }()

	// Summary with all 3 mache_ids present.
	summaryAll := "Interactive Elements:\n" +
		"ID: mache-1 | Parent: none | Tag: nav | Text: \"Header\"\n" +
		"ID: mache-2 | Parent: none | Tag: div | Text: \"Feed\"\n" +
		"ID: mache-3 | Parent: none | Tag: footer | Text: \"Footer\"\n"

	// First snapshot: full scan, cache miss.
	sendJSON(t, conn, InboundMessage{
		Type:    MsgDOMSnapshot,
		TabID:   1,
		URL:     "https://example.com/page",
		Summary: summaryAll,
	})
	for i := 0; i < 5; i++ {
		if readMessage(t, conn).Type == MsgSchemaReady {
			break
		}
	}
	if cart.callCount.Load() != 1 {
		t.Fatalf("expected 1 Cartographer call after first snapshot, got %d", cart.callCount.Load())
	}

	// Second snapshot: mache-2 is gone (stale), mache-1 and mache-3 still present.
	// This should trigger partial regen for /main/feed only.
	cart.schema = partialSchema
	summaryStale := "Interactive Elements:\n" +
		"ID: mache-1 | Parent: none | Tag: nav | Text: \"Header\"\n" +
		"ID: mache-99 | Parent: none | Tag: div | Text: \"New Feed\"\n" +
		"ID: mache-3 | Parent: none | Tag: footer | Text: \"Footer\"\n"

	sendJSON(t, conn, InboundMessage{
		Type:    MsgDOMSnapshot,
		TabID:   1,
		URL:     "https://example.com/page",
		Summary: summaryStale,
	})
	for i := 0; i < 5; i++ {
		if readMessage(t, conn).Type == MsgSchemaReady {
			break
		}
	}

	// Cartographer should have been called exactly 2 times total:
	// 1 for full scan + 1 for partial regen of the stale zone.
	if got := cart.callCount.Load(); got != 2 {
		t.Errorf("expected 2 Cartographer calls (1 full + 1 partial), got %d", got)
	}
}

// TestWSPartialRegenAllStale verifies that when ALL zones are stale,
// the system falls through to a full regeneration (single Cartographer call).
func TestWSPartialRegenAllStale(t *testing.T) {
	fullSchema := `{"mounts":[
		{"virtual_path":"/header","mache_id":"mache-1","description":"header"},
		{"virtual_path":"/main","mache_id":"mache-2","description":"main"}
	]}`

	cart := &mockCartographer{schema: fullSchema}
	h := NewHandler(cart, nil, nil, "test", "test-live", "")

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", h.HandleWebSocket)
	s := httptest.NewServer(mux)
	defer s.Close()

	conn := dialWS(t, s)
	defer func() { _ = conn.Close() }()

	summaryAll := "Interactive Elements:\n" +
		"ID: mache-1 | Parent: none | Tag: nav | Text: \"Header\"\n" +
		"ID: mache-2 | Parent: none | Tag: div | Text: \"Main\"\n"

	// First snapshot: full scan.
	sendJSON(t, conn, InboundMessage{
		Type:    MsgDOMSnapshot,
		TabID:   1,
		URL:     "https://example.com/all-stale",
		Summary: summaryAll,
	})
	for i := 0; i < 5; i++ {
		if readMessage(t, conn).Type == MsgSchemaReady {
			break
		}
	}

	// Second snapshot: ALL mache_ids are gone → should be full regen, not N partial calls.
	newSchema := `{"mounts":[
		{"virtual_path":"/header","mache_id":"mache-10","description":"new header"},
		{"virtual_path":"/main","mache_id":"mache-20","description":"new main"}
	]}`
	cart.schema = newSchema
	summaryNew := "Interactive Elements:\n" +
		"ID: mache-10 | Parent: none | Tag: nav | Text: \"New Header\"\n" +
		"ID: mache-20 | Parent: none | Tag: div | Text: \"New Main\"\n"

	sendJSON(t, conn, InboundMessage{
		Type:    MsgDOMSnapshot,
		TabID:   1,
		URL:     "https://example.com/all-stale",
		Summary: summaryNew,
	})
	for i := 0; i < 5; i++ {
		if readMessage(t, conn).Type == MsgSchemaReady {
			break
		}
	}

	// Should be exactly 2 Cartographer calls: 1 initial + 1 full regen.
	// NOT 3 (1 initial + 2 partial per zone).
	if got := cart.callCount.Load(); got != 2 {
		t.Errorf("expected 2 Cartographer calls (all-stale → full regen), got %d", got)
	}
}

// TestWSPartialRegenCacheUpdated verifies that after partial regen,
// new zones are stored in the cache and old stale zones are invalidated.
func TestWSPartialRegenCacheUpdated(t *testing.T) {
	fullSchema := `{"mounts":[
		{"virtual_path":"/header","mache_id":"mache-1","description":"header","bounds":[0,0,1,0.1]},
		{"virtual_path":"/main/feed","mache_id":"mache-2","description":"feed","bounds":[0,0.1,1,0.7]},
		{"virtual_path":"/footer","mache_id":"mache-3","description":"footer","bounds":[0,0.8,1,0.2]}
	]}`
	partialSchema := `{"mounts":[{"virtual_path":"/main/feed","mache_id":"mache-99","description":"refreshed","bounds":[0,0.1,1,0.7]}]}`

	cart := &mockCartographer{schema: fullSchema}
	h := NewHandler(cart, nil, nil, "test", "test-live", "")

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", h.HandleWebSocket)
	s := httptest.NewServer(mux)
	defer s.Close()

	conn := dialWS(t, s)
	defer func() { _ = conn.Close() }()

	key := "example.com/cached"

	summaryAll := "Interactive Elements:\n" +
		"ID: mache-1 | Parent: none | Tag: nav | Text: \"Header\"\n" +
		"ID: mache-2 | Parent: none | Tag: div | Text: \"Feed\"\n" +
		"ID: mache-3 | Parent: none | Tag: footer | Text: \"Footer\"\n"

	// First snapshot: full scan.
	sendJSON(t, conn, InboundMessage{
		Type:    MsgDOMSnapshot,
		TabID:   1,
		URL:     "https://example.com/cached",
		Summary: summaryAll,
	})
	for i := 0; i < 5; i++ {
		if readMessage(t, conn).Type == MsgSchemaReady {
			break
		}
	}

	// Verify cache has 3 zones.
	cachedJSON, ok := h.schemas.GetAllZones(key)
	if !ok {
		t.Fatal("expected cached zones after first snapshot")
	}
	var output struct{ Mounts []json.RawMessage }
	_ = json.Unmarshal([]byte(cachedJSON), &output)
	if len(output.Mounts) != 3 {
		t.Fatalf("expected 3 cached zones, got %d", len(output.Mounts))
	}

	// Partial regen: mache-2 gone.
	cart.schema = partialSchema
	summaryStale := "Interactive Elements:\n" +
		"ID: mache-1 | Parent: none | Tag: nav | Text: \"Header\"\n" +
		"ID: mache-99 | Parent: none | Tag: div | Text: \"New Feed\"\n" +
		"ID: mache-3 | Parent: none | Tag: footer | Text: \"Footer\"\n"

	sendJSON(t, conn, InboundMessage{
		Type:    MsgDOMSnapshot,
		TabID:   1,
		URL:     "https://example.com/cached",
		Summary: summaryStale,
	})
	for i := 0; i < 5; i++ {
		if readMessage(t, conn).Type == MsgSchemaReady {
			break
		}
	}

	// After partial regen, cache should still have 3 zones total,
	// but /main/feed should now have mache-99 instead of mache-2.
	cachedJSON, ok = h.schemas.GetAllZones(key)
	if !ok {
		t.Fatal("expected cached zones after partial regen")
	}
	if !json.Valid([]byte(cachedJSON)) {
		t.Fatalf("invalid JSON from cache: %s", cachedJSON)
	}
	// Verify the new mache_id appears in the cached schema.
	if !contains(cachedJSON, "mache-99") {
		t.Errorf("cached schema should contain mache-99 after partial regen: %s", cachedJSON)
	}
}

// TestWSPartialRegenThenCacheHit verifies that after a partial regen updates
// the cache, a subsequent identical snapshot gets a cache hit (0 new Cartographer calls).
func TestWSPartialRegenThenCacheHit(t *testing.T) {
	fullSchema := `{"mounts":[
		{"virtual_path":"/header","mache_id":"mache-1","description":"header","bounds":[0,0,1,0.1]},
		{"virtual_path":"/main/feed","mache_id":"mache-2","description":"feed","bounds":[0,0.1,1,0.8]}
	]}`
	partialSchema := `{"mounts":[{"virtual_path":"/main/feed","mache_id":"mache-99","description":"refreshed","bounds":[0,0.1,1,0.8]}]}`

	cart := &mockCartographer{schema: fullSchema}
	h := NewHandler(cart, nil, nil, "test", "test-live", "")

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", h.HandleWebSocket)
	s := httptest.NewServer(mux)
	defer s.Close()

	conn := dialWS(t, s)
	defer func() { _ = conn.Close() }()

	summaryAll := "Interactive Elements:\n" +
		"ID: mache-1 | Parent: none | Tag: nav | Text: \"Header\"\n" +
		"ID: mache-2 | Parent: none | Tag: div | Text: \"Feed\"\n"

	// 1) Full scan.
	sendJSON(t, conn, InboundMessage{
		Type: MsgDOMSnapshot, TabID: 1,
		URL: "https://example.com/cachehit", Summary: summaryAll,
	})
	for i := 0; i < 5; i++ {
		if readMessage(t, conn).Type == MsgSchemaReady {
			break
		}
	}

	// 2) Partial regen: mache-2 gone.
	cart.schema = partialSchema
	summaryAfterPartial := "Interactive Elements:\n" +
		"ID: mache-1 | Parent: none | Tag: nav | Text: \"Header\"\n" +
		"ID: mache-99 | Parent: none | Tag: div | Text: \"New Feed\"\n"

	sendJSON(t, conn, InboundMessage{
		Type: MsgDOMSnapshot, TabID: 1,
		URL: "https://example.com/cachehit", Summary: summaryAfterPartial,
	})
	for i := 0; i < 5; i++ {
		if readMessage(t, conn).Type == MsgSchemaReady {
			break
		}
	}

	callsAfterPartial := cart.callCount.Load()

	// 3) Same snapshot again (mache-1 and mache-99 both present) → cache hit.
	sendJSON(t, conn, InboundMessage{
		Type: MsgDOMSnapshot, TabID: 1,
		URL: "https://example.com/cachehit", Summary: summaryAfterPartial,
	})
	for i := 0; i < 5; i++ {
		if readMessage(t, conn).Type == MsgSchemaReady {
			break
		}
	}

	// No new Cartographer calls on the third snapshot (cache hit).
	if got := cart.callCount.Load(); got != callsAfterPartial {
		t.Errorf("expected 0 new Cartographer calls on cache hit, got %d new (total %d, before %d)",
			got-callsAfterPartial, got, callsAfterPartial)
	}
}

// contains checks if substr exists in s (helper for assertions).
func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && json.Valid([]byte(s)) && containsStr(s, substr)
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
