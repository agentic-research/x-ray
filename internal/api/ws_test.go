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
	h := NewHandler(cart, nil, nil, "test", "test-live")

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
	h := NewHandler(cart, nil, nil, "test", "test-live")

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
	h := NewHandler(&stubCartographer{}, nil, nil, "test", "test-live")

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", h.HandleWebSocket)
	s := httptest.NewServer(mux)
	defer s.Close()

	conn := dialWS(t, s)
	defer func() { _ = conn.Close() }()

	// Give the handler a moment to register the connection
	time.Sleep(50 * time.Millisecond)

	// Queue an action via the public API (simulating voice handler dispatching)
	h.SendActionToExtension(10, "mache-7", "click")

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
	h := NewHandler(&stubCartographer{}, nil, nil, "test", "test-live")

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", h.HandleWebSocket)
	s := httptest.NewServer(mux)
	defer s.Close()

	// Queue actions while no WS is connected
	h.SendActionToExtension(1, "mache-1", "click")
	h.SendActionToExtension(2, "mache-2", "focus")

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

	h := NewHandler(cart, nil, nil, "test", "test-live")

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
	h := NewHandler(cart, nil, nil, "test", "test-live")

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
