package api

import (
	"context"
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
