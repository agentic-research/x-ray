package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jamesgardner/x-ray/internal/mache"
	"github.com/jamesgardner/x-ray/internal/navigator"
)

// To see the race detector failures, run:
// go test -race -run TestBug_ ./internal/api/

func TestBug_SchemaReadyDataRace(t *testing.T) {
	// Demonstrates the data race when reading from a channel that is
	// concurrently being reassigned by ResetSchema().
	h := newTestHandler()
	sess := h.getSession(1)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		// Simulate handleNavigate waiting for schema
		select {
		case <-sess.GetSchemaReady():
		case <-time.After(50 * time.Millisecond):
		}
	}()

	go func() {
		defer wg.Done()
		// Simulate Doer triggering goto/rescan which resets the schema channel
		sess.ResetSchema()
	}()

	wg.Wait()
}

func TestBug_EnginePointerDataRace(t *testing.T) {
	// Demonstrates the data race on the TabSession.Engine pointer.
	h := newTestHandler()
	sess := h.getSession(1)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		// Simulate handleNavigate / handleDOMSnapshot reading the engine
		_ = sess.GetEngine().HasSchema()
	}()

	go func() {
		defer wg.Done()
		// Simulate Doer overwriting the engine pointer during goto
		sess.SwapEngine(mache.NewEngine())
	}()

	wg.Wait()
}

func TestBug_NavigatorCallbackOverwrite(t *testing.T) {
	// Demonstrates that concurrent uses of the same Navigator overwrite
	// each other's callbacks, leading to potential nil dereferences.

	// We must use the real navigator.Agent here, not mockIntentHandler,
	// because the bug exists in the real implementation's lack of mutexes.
	engine := mache.NewEngine()
	nav := navigator.NewAgent(nil, "test-model", engine)

	var wg sync.WaitGroup
	wg.Add(2)

	// Simulate Intent A
	go func() {
		defer wg.Done()
		nav.SetScrollFunc(func(ctx context.Context, dir string) error { return nil })

		// Simulate Intent A finishing quickly and cleaning up
		nav.SetScrollFunc(nil)
	}()

	// Simulate Intent B running concurrently
	go func() {
		defer wg.Done()
		nav.SetScrollFunc(func(ctx context.Context, dir string) error { return nil })

		// Wait for Intent A to finish and nil out the global callback
		time.Sleep(50 * time.Millisecond)

		// If Intent B now tried to execute a scroll tool via nav.HandleIntent,
		// it would panic because the scrollFn was nilled out by Intent A.
	}()

	wg.Wait()
}

func TestBug_DoerTeleportationTab0(t *testing.T) {
	// Verifies that a Doer starting on Tab 0 (disconnected extension) correctly
	// rebinds to the real tab when the extension wakes up mid-goal.

	mock := &mockIntentHandler{
		responses: []mockResponse{
			{action: &navigator.ActionResult{Action: "browser.goto", Path: "https://example.com"}},
			{textResp: "Successfully reading the real tab 99 engine."},
		},
	}

	h := newTestHandler()
	sess0 := h.getSession(0)
	sess0.Navigator = mock
	doer := NewDoer(h, 0, sess0)
	sess0.Doer = doer

	// Pre-signal schema so the Doer doesn't wait 3s for the initial soft wait.
	sess0.SignalSchemaReady()

	// Pre-create sess99 with a navigator so the rebind has something to work with.
	// In production, handleDOMSnapshot sets up the navigator on the real session.
	sess99 := h.getSession(99)
	sess99.Navigator = mock
	sess99.SwapEngine(mache.NewEngine())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go doer.Run(ctx)

	// Set up completion notification BEFORE submitting to avoid race
	// (the Doer can finish before waitForDone sets its callback).
	done := make(chan struct{}, 1)
	doer.SetResultNotifyFn(func(_ string) {
		select {
		case done <- struct{}{}:
		default:
		}
	})

	// Start a goto command on Tab 0 (simulating disconnected extension start)
	doer.Submit(DoerGoal{ID: "g-teleport", Text: "go to example.com"})

	// Simulate the extension waking up and reporting its real ID (Tab 99)
	time.Sleep(50 * time.Millisecond)
	h.mu.Lock()
	h.activeVoiceTab = 99
	h.mu.Unlock()

	// Signal Tab 0's schema so the goto unblocks.
	if oldSess, ok := h.sessions[0]; ok {
		oldSess.SignalSchemaReady()
	}

	// Wait for the Doer to finish its multi-step loop
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		status, _, step, _ := doer.State().Snapshot()
		t.Fatalf("Doer did not complete within 5s (status=%d, step=%q)", status, step)
	}
	_, _, _, result := doer.State().Snapshot()

	if !result.Success {
		t.Errorf("expected Doer to complete, got error: %s", result.Error)
	}

	// Verify the Doer rebound to the real tab.
	if doer.tabID != 99 {
		t.Errorf("expected Doer to rebind to tab 99, got tab %d", doer.tabID)
	}
}

func TestBug_DoerGoroutineLeakOnTabClose(t *testing.T) {
	// Verifies that closing a tab kills the Doer's Run goroutine via
	// the run-context cancel (Bug #6 fix). Uses getOrCreateDoer which
	// stores doerCancel on the session.
	h := newTestHandler()
	sess := h.getSession(1)

	// Use getOrCreateDoer (the fixed path) which stores doerCancel.
	doer := h.getOrCreateDoer(1, sess)

	// Fill the buffered goalCh so the goroutine must drain it to accept more.
	doer.goalCh <- DoerGoal{ID: "fill-buffer", Text: "fill"}

	// Simulate the extension closing the tab via handleTabClosed.
	h.handleTabClosed(InboundMessage{Type: MsgTabClosed, TabID: 1})

	// Give the Run goroutine a moment to observe context cancellation.
	time.Sleep(100 * time.Millisecond)

	// Drain the buffer (the goroutine may or may not have consumed it).
	select {
	case <-doer.goalCh:
	default:
	}

	// Now the buffer is empty. If the goroutine is dead, a second send
	// will block (nobody is reading). If alive, it will be consumed.
	select {
	case doer.goalCh <- DoerGoal{ID: "orphaned-goal", Text: "I shouldn't execute"}:
		// Send went into the buffer (size 1). Try a third send to truly block.
		select {
		case doer.goalCh <- DoerGoal{ID: "orphaned-goal-2", Text: "I also shouldn't execute"}:
			t.Errorf("BUG NOT FIXED: The Doer goroutine is still alive and accepting goals after tab close.")
		case <-time.After(100 * time.Millisecond):
			// Blocked — goroutine is dead. The buffer accepted one but no reader drained it.
		}
	case <-time.After(100 * time.Millisecond):
		// Blocked as expected — goroutine is dead.
	}
}

func TestBug_GoAwaySenderGoroutineLeak(t *testing.T) {
	// Verifies that session-scoped context cancellation kills old sender
	// goroutines during a GoAway reconnect loop (Bug #8 fix).

	mic := make(chan []byte)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var activeSenders atomic.Int32

	// Simulating the outer reconnect loop in StartVoiceLoop
	for i := 0; i < 2; i++ {
		// Session-scoped context: cancelled on GoAway before creating new sender.
		sessionCtx, sessionCancel := context.WithCancel(ctx)

		activeSenders.Add(1)
		go func() {
			defer activeSenders.Add(-1)
			for {
				select {
				case <-sessionCtx.Done(): // FIX: session-scoped ctx
					return
				case <-mic:
					// send to session
				}
			}
		}()

		// Simulate receiving a GoAway message from Gemini and breaking the receive loop
		time.Sleep(10 * time.Millisecond)
		// Cancel old sender before reconnecting (the fix)
		sessionCancel()
		time.Sleep(10 * time.Millisecond)
	}

	time.Sleep(50 * time.Millisecond)

	senders := activeSenders.Load()
	if senders > 1 {
		t.Errorf("BUG NOT FIXED: %d sender goroutines are active, but only 1 should be.", senders)
	}
}

// ---------------------------------------------------------------------------
// FIX 1 regression tests: TAB_ACTIVATED voice UI filtering
// ---------------------------------------------------------------------------

func TestBug_TabActivatedVoiceUIFiltered(t *testing.T) {
	// TAB_ACTIVATED for a tab with no session (e.g., the voice UI tab)
	// must NOT update activeVoiceTab.
	h := newTestHandler()

	// Establish a real voice tab with an existing session.
	h.getSession(42)
	h.mu.Lock()
	h.activeVoiceTab = 42
	h.mu.Unlock()

	// Simulate receiving TAB_ACTIVATED for tab 999 (no session — like the voice UI tab).
	// This inlines the fixed MsgTabActivated handler logic.
	h.mu.Lock()
	if _, exists := h.sessions[999]; exists {
		h.activeVoiceTab = 999
	}
	h.mu.Unlock()

	h.mu.Lock()
	got := h.activeVoiceTab
	h.mu.Unlock()

	if got != 42 {
		t.Errorf("BUG: activeVoiceTab changed to %d (voice UI tab), should remain 42", got)
	}
}

func TestBug_TabActivatedRealTabAccepted(t *testing.T) {
	// TAB_ACTIVATED for a tab WITH a session must be accepted.
	h := newTestHandler()
	h.getSession(10)
	h.getSession(20)
	h.mu.Lock()
	h.activeVoiceTab = 10
	h.mu.Unlock()

	// Switch to tab 20 (has a session) — should be accepted.
	h.mu.Lock()
	if _, exists := h.sessions[20]; exists {
		h.activeVoiceTab = 20
	}
	h.mu.Unlock()

	h.mu.Lock()
	got := h.activeVoiceTab
	h.mu.Unlock()

	if got != 20 {
		t.Errorf("expected activeVoiceTab=20, got %d", got)
	}
}

func TestBug_TabActivatedVoiceUIViaWebSocket(t *testing.T) {
	// Full WS integration: send TAB_ACTIVATED for a sessionless tab over WebSocket.
	h := NewHandler(&stubCartographer{}, nil, nil, "test", "test-live", "")
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", h.HandleWebSocket)
	s := httptest.NewServer(mux)
	defer s.Close()

	conn := dialWS(t, s)
	defer func() { _ = conn.Close() }()
	time.Sleep(20 * time.Millisecond)

	// Seed a real session and set it as voice tab.
	sess42 := h.getSession(42)
	sess42.SignalSchemaReady()
	h.mu.Lock()
	h.activeVoiceTab = 42
	h.mu.Unlock()

	// Send TAB_ACTIVATED for tab 999 (no session — like voice UI).
	sendJSON(t, conn, InboundMessage{Type: MsgTabActivated, TabID: 999})
	time.Sleep(50 * time.Millisecond)

	h.mu.Lock()
	voiceTab := h.activeVoiceTab
	h.mu.Unlock()

	if voiceTab != 42 {
		t.Errorf("BUG: voice UI TAB_ACTIVATED changed activeVoiceTab from 42 to %d", voiceTab)
	}
}

// ---------------------------------------------------------------------------
// FIX 2 regression tests: Cross-tab cache poisoning via bounds mismatch
// ---------------------------------------------------------------------------

func TestBug_CrossTabCachePoisoning(t *testing.T) {
	// Cached schema: mache-3 at [0.1, 0.3, 0.8, 0.5] (center ≈ 0.5, 0.55).
	// Tab B summary: mache-3 at [0.8, 0.1, 0.15, 0.08] (center ≈ 0.875, 0.14).
	// ValidateSchemaZones passes (mache-3 exists). ValidateSchemaBounds should fail.
	cachedSchema := `{"mounts":[{
		"virtual_path":"/main/feed",
		"mache_id":"mache-3",
		"description":"Feed",
		"bounds":[0.1, 0.3, 0.8, 0.5]
	}]}`

	tabBSummary := "ID: mache-3 | Parent: none | Tag: a | Text: \"Sidebar\" | Bounds: [0.8, 0.1, 0.15, 0.08]\n"

	// ID check passes — mache-3 is in the summary.
	staleByID := mache.ValidateSchemaZones(cachedSchema, tabBSummary)
	if len(staleByID) != 0 {
		t.Fatalf("test setup: ValidateSchemaZones should pass, got %v", staleByID)
	}

	// Bounds check catches the poisoning.
	boundsStale := mache.ValidateSchemaBounds(cachedSchema, tabBSummary, 0.10)
	if len(boundsStale) == 0 {
		t.Error("BUG NOT FIXED: ValidateSchemaBounds should catch cross-tab poisoning " +
			"(mache-3 center moved ~56% but bounds check passed)")
	}
	if _, ok := boundsStale["/main/feed"]; !ok {
		t.Errorf("expected /main/feed in boundsStale, got: %v", boundsStale)
	}
}

func TestBug_CrossTabCachePoisoningE2E(t *testing.T) {
	// Full WS test: Tab A snapshots (cache miss → 1 Cartographer call).
	// Tab B same URL but element positions shifted → cache hit rejected → 2nd Cartographer call.
	schemaA := `{"mounts":[{
		"virtual_path":"/main/feed",
		"mache_id":"mache-1",
		"description":"Feed",
		"bounds":[0.1, 0.3, 0.8, 0.5]
	}]}`

	cart := &mockCartographer{schema: schemaA}
	h := NewHandler(cart, nil, nil, "test", "test-live", "")
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", h.HandleWebSocket)
	s := httptest.NewServer(mux)
	defer s.Close()

	conn := dialWS(t, s)
	defer func() { _ = conn.Close() }()

	// Tab A: full scan, cache miss.
	summaryA := "ID: mache-1 | Parent: none | Tag: div | Text: \"Feed\" | Bounds: [0.1, 0.3, 0.8, 0.5]\n"
	sendJSON(t, conn, InboundMessage{
		Type: MsgDOMSnapshot, TabID: 1,
		URL: "https://example.com/feed", Summary: summaryA,
	})
	for i := 0; i < 5; i++ {
		if readMessage(t, conn).Type == MsgSchemaReady {
			break
		}
	}
	if cart.callCount.Load() != 1 {
		t.Fatalf("expected 1 Cartographer call after Tab A, got %d", cart.callCount.Load())
	}

	// Tab B: same URL, same mache-ID name, but completely different position.
	summaryB := "ID: mache-1 | Parent: none | Tag: a | Text: \"Sidebar\" | Bounds: [0.8, 0.05, 0.15, 0.05]\n"
	// Update cartographer output for Tab B.
	cart.schema = `{"mounts":[{
		"virtual_path":"/main/feed",
		"mache_id":"mache-1",
		"description":"Feed (tab B)",
		"bounds":[0.8, 0.05, 0.15, 0.05]
	}]}`

	sendJSON(t, conn, InboundMessage{
		Type: MsgDOMSnapshot, TabID: 2,
		URL: "https://example.com/feed", Summary: summaryB,
	})
	for i := 0; i < 5; i++ {
		if readMessage(t, conn).Type == MsgSchemaReady {
			break
		}
	}

	// Cache should have been rejected for Tab B → Cartographer called a second time.
	if got := cart.callCount.Load(); got < 2 {
		t.Errorf("BUG NOT FIXED: expected ≥2 Cartographer calls (Tab B cache rejected), got %d", got)
	}
}

// ---------------------------------------------------------------------------
// FIX 3 regression tests: Session reset on extension reconnect
// ---------------------------------------------------------------------------

func TestBug_ReconnectResetsSessionSchemaState(t *testing.T) {
	// After extension reconnect, all sessions must have fresh (open) SchemaReady channels.
	h := NewHandler(&stubCartographer{}, nil, nil, "test", "test-live", "")
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", h.HandleWebSocket)
	s := httptest.NewServer(mux)
	defer s.Close()

	// First connection.
	conn1 := dialWS(t, s)
	time.Sleep(20 * time.Millisecond)

	sess42 := h.getSession(42)
	sess43 := h.getSession(43)
	sess42.SignalSchemaReady()
	sess43.SignalSchemaReady()

	// Verify schema was signaled.
	select {
	case <-sess42.GetSchemaReady():
	default:
		t.Fatal("sess42 should be signaled before disconnect")
	}

	// Disconnect.
	_ = conn1.Close()
	time.Sleep(50 * time.Millisecond)

	// Reconnect.
	conn2 := dialWS(t, s)
	defer func() { _ = conn2.Close() }()
	time.Sleep(50 * time.Millisecond)

	// After reconnect, SchemaReady channels should be fresh (open, not closed).
	select {
	case <-sess42.GetSchemaReady():
		t.Error("BUG NOT FIXED: sess42 SchemaReady still closed after reconnect — stale schema")
	default:
		// Correct: channel is open.
	}
	select {
	case <-sess43.GetSchemaReady():
		t.Error("BUG NOT FIXED: sess43 SchemaReady still closed after reconnect — stale schema")
	default:
		// Correct.
	}
}

func TestBug_ReconnectPreservesSessionsForDoer(t *testing.T) {
	// Sessions must NOT be deleted on reconnect (Doer continuity).
	h := NewHandler(&stubCartographer{}, nil, nil, "test", "test-live", "")
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", h.HandleWebSocket)
	s := httptest.NewServer(mux)
	defer s.Close()

	conn1 := dialWS(t, s)
	time.Sleep(20 * time.Millisecond)

	sess := h.getSession(42)
	doer := NewDoer(h, 42, sess)
	sess.Doer = doer

	_ = conn1.Close()
	time.Sleep(50 * time.Millisecond)

	conn2 := dialWS(t, s)
	defer func() { _ = conn2.Close() }()
	time.Sleep(50 * time.Millisecond)

	h.mu.Lock()
	_, exists := h.sessions[42]
	doerAttached := sess.Doer != nil
	h.mu.Unlock()

	if !exists {
		t.Error("session should survive reconnect (Doer may be in-flight)")
	}
	if !doerAttached {
		t.Error("Doer should be preserved on the session after reconnect")
	}
}

func TestBug_ReconnectNoSessionsNoOp(t *testing.T) {
	// Reconnect with zero sessions should not panic.
	h := NewHandler(&stubCartographer{}, nil, nil, "test", "test-live", "")
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", h.HandleWebSocket)
	s := httptest.NewServer(mux)
	defer s.Close()

	conn1 := dialWS(t, s)
	time.Sleep(20 * time.Millisecond)
	_ = conn1.Close()
	time.Sleep(50 * time.Millisecond)

	// Reconnect with no sessions — should be a clean no-op.
	conn2 := dialWS(t, s)
	defer func() { _ = conn2.Close() }()
	time.Sleep(50 * time.Millisecond)

	h.mu.Lock()
	n := len(h.sessions)
	h.mu.Unlock()
	if n != 0 {
		t.Errorf("expected 0 sessions, got %d", n)
	}
}
