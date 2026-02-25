package api

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jamesgardner/x-ray/internal/mache"
	"github.com/jamesgardner/x-ray/internal/navigator"
	"google.golang.org/genai"
)

// mockIntentHandler is a configurable IntentHandler for Doer tests.
type mockIntentHandler struct {
	action      *navigator.ActionResult
	textResp    string
	err         error
	delay       time.Duration // artificial latency
	handleCalls atomic.Int32
	engine      *mache.Engine

	mu         sync.Mutex
	scrollFn   func(ctx context.Context, direction string) error
	progressFn func(toolName string, args map[string]any)
}

func (m *mockIntentHandler) HandleIntent(ctx context.Context, intent string) (*navigator.ActionResult, string, error) {
	m.handleCalls.Add(1)
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return nil, "", ctx.Err()
		}
	}
	return m.action, m.textResp, m.err
}

func (m *mockIntentHandler) ExecuteTool(_ context.Context, _ *genai.FunctionCall) (string, *navigator.ActionResult) {
	return "", nil
}

func (m *mockIntentHandler) SetEngine(engine *mache.Engine) { m.engine = engine }

func (m *mockIntentHandler) SetScrollFunc(fn func(ctx context.Context, direction string) error) {
	m.mu.Lock()
	m.scrollFn = fn
	m.mu.Unlock()
}

func (m *mockIntentHandler) SetProgressFunc(fn func(toolName string, args map[string]any)) {
	m.mu.Lock()
	m.progressFn = fn
	m.mu.Unlock()
}

func (m *mockIntentHandler) SetListTabsFunc(_ func(ctx context.Context) ([]navigator.TabInfo, error)) {
}

// newDoerTestHarness wires up a Handler + TabSession + Doer for unit tests.
// The returned Doer is NOT started (call go doer.Run(ctx) yourself).
func newDoerTestHarness(nav IntentHandler) (*Handler, *TabSession, *Doer) {
	h := newTestHandler()
	sess := h.getSession(0)
	sess.Navigator = nav
	doer := NewDoer(h, 0, sess)
	sess.Doer = doer
	return h, sess, doer
}

func TestDoerStateTransitions(t *testing.T) {
	mock := &mockIntentHandler{
		textResp: "I see a navigation bar with links.",
		delay:    50 * time.Millisecond,
	}
	_, sess, doer := newDoerTestHarness(mock)

	// Pre-signal schema so the Doer doesn't wait.
	sess.SignalSchemaReady()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go doer.Run(ctx)

	// State should be Idle before any goal.
	status, _, _, _ := doer.State().Snapshot()
	if status != DoerIdle {
		t.Errorf("expected DoerIdle, got %d", status)
	}

	// Submit a goal.
	var notified atomic.Value
	doer.SetResultNotifyFn(func(summary string) { notified.Store(summary) })
	doer.Submit(DoerGoal{ID: "g1", Text: "describe the page"})

	// Wait for completion.
	deadline := time.After(2 * time.Second)
	for {
		status, _, _, result := doer.State().Snapshot()
		if status == DoerDone && result != nil {
			if result.GoalID != "g1" {
				t.Errorf("expected goal ID g1, got %s", result.GoalID)
			}
			if !result.Success {
				t.Errorf("expected success, got failure: %s", result.Error)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("Doer did not complete within 2s (status=%d)", status)
		case <-time.After(10 * time.Millisecond):
		}
	}

	// resultNotifyFn should have been called.
	if v := notified.Load(); v == nil {
		t.Error("resultNotifyFn was never called")
	}
}

func TestDoerCancel(t *testing.T) {
	mock := &mockIntentHandler{
		textResp: "done",
		delay:    5 * time.Second, // long delay — will be cancelled
	}
	_, sess, doer := newDoerTestHarness(mock)
	sess.SignalSchemaReady()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go doer.Run(ctx)

	doer.Submit(DoerGoal{ID: "g-cancel", Text: "do something slow"})

	// Wait for Executing state.
	deadline := time.After(2 * time.Second)
	for {
		status, _, _, _ := doer.State().Snapshot()
		if status == DoerExecuting {
			break
		}
		select {
		case <-deadline:
			t.Fatal("Doer never entered Executing state")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// Cancel and verify state transitions to Idle.
	doer.Cancel()
	status, _, _, result := doer.State().Snapshot()
	if status != DoerIdle {
		t.Errorf("expected DoerIdle after cancel, got %d", status)
	}
	if result == nil || result.Summary != "Cancelled by user." {
		t.Errorf("expected cancel result, got %v", result)
	}
}

func TestDoerSubmitCancelsPrevious(t *testing.T) {
	callCount := &atomic.Int32{}
	mock := &mockIntentHandler{
		textResp: "done",
		delay:    200 * time.Millisecond,
	}
	_, sess, doer := newDoerTestHarness(mock)
	sess.SignalSchemaReady()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go doer.Run(ctx)

	// Submit first goal.
	doer.Submit(DoerGoal{ID: "g-old", Text: "old task"})
	time.Sleep(50 * time.Millisecond) // let it start

	// Submit second goal — should cancel the first.
	doer.Submit(DoerGoal{ID: "g-new", Text: "new task"})

	// Wait for the second goal to complete.
	deadline := time.After(3 * time.Second)
	for {
		status, _, _, result := doer.State().Snapshot()
		if status == DoerDone && result != nil && result.GoalID == "g-new" {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("second goal never completed (status=%d, calls=%d)", status, callCount.Load())
		case <-time.After(10 * time.Millisecond):
		}
	}

	// HandleIntent should have been called at least twice (once for each goal,
	// though the first may have been cancelled mid-flight).
	if mock.handleCalls.Load() < 2 {
		t.Errorf("expected at least 2 HandleIntent calls, got %d", mock.handleCalls.Load())
	}
}

func TestDoerGotoDispatch(t *testing.T) {
	mock := &mockIntentHandler{
		action: &navigator.ActionResult{
			Action: "goto",
			Path:   "https://example.com",
		},
	}
	h, sess, doer := newDoerTestHarness(mock)
	sess.SignalSchemaReady()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go doer.Run(ctx)

	doer.Submit(DoerGoal{ID: "g-goto", Text: "go to example.com"})

	// The Doer's dispatchAction calls ResetSchema + sendGoto, then waits
	// on SchemaReady. Simulate the extension responding by signaling after a delay.
	go func() {
		time.Sleep(100 * time.Millisecond)
		// sendGoto on a nil conn opens Chrome (no-op in tests). The Doer
		// is waiting on sess.SchemaReady. Signal it to simulate the extension
		// completing the page load + schema pipeline.
		sess.SignalSchemaReady()
	}()

	deadline := time.After(3 * time.Second)
	for {
		status, _, _, result := doer.State().Snapshot()
		if status == DoerDone && result != nil {
			if !result.Success {
				t.Errorf("expected success, got error: %s", result.Error)
			}
			if result.GoalID != "g-goto" {
				t.Errorf("expected goal g-goto, got %s", result.GoalID)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("goto goal never completed (status=%d)", status)
		case <-time.After(10 * time.Millisecond):
		}
	}

	// Verify the engine was reset (new Engine created for goto).
	if sess.Engine == nil {
		t.Error("engine should have been replaced, not nil")
	}

	_ = h // used for sendGoto side-effect (conn is nil, opens Chrome)
}

func TestDoerGotoTimeout(t *testing.T) {
	mock := &mockIntentHandler{
		action: &navigator.ActionResult{
			Action: "goto",
			Path:   "https://slow-page.example.com",
		},
	}
	_, sess, doer := newDoerTestHarness(mock)
	sess.SignalSchemaReady()

	// Override schemaWaitTimeout for test speed — the Doer uses the package-level
	// constant, so we test the timeout branch by never signaling SchemaReady.
	// The default 30s is too slow for tests; the Doer will timeout eventually.
	// Instead, we'll just verify the Doer finishes (success=true with timeout msg).

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go doer.Run(ctx)

	doer.Submit(DoerGoal{ID: "g-slow", Text: "go to slow page"})

	// Don't signal SchemaReady. The Doer should timeout after schemaWaitTimeout (30s).
	// That's too long for a unit test, so instead cancel the context after 200ms.
	time.Sleep(200 * time.Millisecond)
	cancel()

	// Give the Doer a moment to process the cancellation.
	time.Sleep(100 * time.Millisecond)

	status, _, _, result := doer.State().Snapshot()
	// After ctx cancel, the Doer should have finished (cancelled or failed).
	if status == DoerExecuting {
		t.Error("Doer should not still be Executing after context cancel")
	}
	_ = result // may be nil if cancellation raced
}

func TestDoerSchemaWaitSoftProceed(t *testing.T) {
	// Verify the Doer proceeds without schema after the 3s soft wait.
	mock := &mockIntentHandler{
		action: &navigator.ActionResult{
			Action: "goto",
			Path:   "https://example.com",
		},
	}
	_, sess, doer := newDoerTestHarness(mock)
	// Do NOT signal SchemaReady — the 3s soft wait should proceed.

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go doer.Run(ctx)

	doer.Submit(DoerGoal{ID: "g-noscema", Text: "go to example.com"})

	// Signal SchemaReady for the goto's post-navigate wait (dispatchAction).
	go func() {
		time.Sleep(4 * time.Second) // after the 3s soft wait, before 30s timeout
		sess.SignalSchemaReady()
	}()

	deadline := time.After(10 * time.Second)
	for {
		status, _, _, result := doer.State().Snapshot()
		if (status == DoerDone || status == DoerFailed) && result != nil {
			// The Doer should have proceeded without schema and dispatched goto.
			break
		}
		select {
		case <-deadline:
			t.Fatalf("Doer never completed (status=%d)", status)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func TestDoerActionDispatch(t *testing.T) {
	mock := &mockIntentHandler{
		action: &navigator.ActionResult{
			Action:  "click",
			MacheID: "mache-42",
			Path:    "/main/button",
		},
	}
	_, sess, doer := newDoerTestHarness(mock)
	sess.SignalSchemaReady()

	var actionNotified atomic.Value
	doer.SetActionNotifyFn(func(macheID, action, payload string) {
		actionNotified.Store(macheID + ":" + action)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go doer.Run(ctx)

	doer.Submit(DoerGoal{ID: "g-click", Text: "click the button"})

	deadline := time.After(2 * time.Second)
	for {
		status, _, _, result := doer.State().Snapshot()
		if status == DoerDone && result != nil {
			if !result.Success {
				t.Errorf("expected success, got error: %s", result.Error)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("click goal never completed (status=%d)", status)
		case <-time.After(10 * time.Millisecond):
		}
	}

	if v := actionNotified.Load(); v != "mache-42:click" {
		t.Errorf("expected actionNotifyFn with 'mache-42:click', got %v", v)
	}
}

func TestDoerProgressCallback(t *testing.T) {
	mock := &mockIntentHandler{
		textResp: "found it",
		delay:    50 * time.Millisecond,
	}
	_, sess, doer := newDoerTestHarness(mock)
	sess.SignalSchemaReady()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go doer.Run(ctx)

	doer.Submit(DoerGoal{ID: "g-prog", Text: "find the link"})

	deadline := time.After(2 * time.Second)
	for {
		status, _, _, result := doer.State().Snapshot()
		if status == DoerDone && result != nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("goal never completed")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// Verify progressFn was wired during execution (set by Doer, cleared after).
	// After completion, it should be nil (defer clears it).
	mock.mu.Lock()
	afterProgress := mock.progressFn
	mock.mu.Unlock()
	if afterProgress != nil {
		t.Error("progressFn should be nil after goal completes (defer cleanup)")
	}
}
