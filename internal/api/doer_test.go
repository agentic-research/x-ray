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

// mockResponse holds one response for the sequencing mock.
type mockResponse struct {
	action   *navigator.ActionResult
	textResp string
	err      error
}

// mockIntentHandler is a configurable IntentHandler for Doer tests.
// If responses is non-empty, successive HandleIntent calls pop from it.
// Otherwise the single action/textResp/err fields are used (legacy behavior).
type mockIntentHandler struct {
	action      *navigator.ActionResult
	textResp    string
	err         error
	delay       time.Duration // artificial latency
	handleCalls atomic.Int32
	engine      *mache.Engine

	mu         sync.Mutex
	responses  []mockResponse
	scrollFn   func(ctx context.Context, direction string) error
	progressFn func(toolName string, args map[string]any)
}

func (m *mockIntentHandler) HandleIntent(ctx context.Context, intent string, _ bool) (*navigator.ActionResult, string, error) {
	idx := int(m.handleCalls.Add(1)) - 1
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return nil, "", ctx.Err()
		}
	}
	m.mu.Lock()
	if idx < len(m.responses) {
		r := m.responses[idx]
		m.mu.Unlock()
		return r.action, r.textResp, r.err
	}
	m.mu.Unlock()
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

// waitForDone blocks until the Doer finishes (DoerDone or DoerFailed) or times out.
// It wires a completion channel through SetResultNotifyFn internally.
func waitForDone(t *testing.T, doer *Doer, timeout time.Duration) *DoerResult {
	t.Helper()
	done := make(chan struct{}, 1)
	doer.SetResultNotifyFn(func(_ string) {
		select {
		case done <- struct{}{}:
		default:
		}
	})
	select {
	case <-done:
	case <-time.After(timeout):
		status, _, step, _ := doer.State().Snapshot()
		t.Fatalf("Doer did not complete within %s (status=%d, step=%q)", timeout, status, step)
	}
	_, _, _, result := doer.State().Snapshot()
	return result
}

// waitForDoneWithNotify is like waitForDone but also calls extraFn on completion.
func waitForDoneWithNotify(t *testing.T, doer *Doer, timeout time.Duration, extraFn func(string)) *DoerResult {
	t.Helper()
	done := make(chan struct{}, 1)
	doer.SetResultNotifyFn(func(summary string) {
		if extraFn != nil {
			extraFn(summary)
		}
		select {
		case done <- struct{}{}:
		default:
		}
	})
	select {
	case <-done:
	case <-time.After(timeout):
		status, _, step, _ := doer.State().Snapshot()
		t.Fatalf("Doer did not complete within %s (status=%d, step=%q)", timeout, status, step)
	}
	_, _, _, result := doer.State().Snapshot()
	return result
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
	doer.Submit(DoerGoal{ID: "g1", Text: "describe the page"})

	result := waitForDoneWithNotify(t, doer, 2*time.Second, func(summary string) {
		notified.Store(summary)
	})

	if result.GoalID != "g1" {
		t.Errorf("expected goal ID g1, got %s", result.GoalID)
	}
	if !result.Success {
		t.Errorf("expected success, got failure: %s", result.Error)
	}
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

	// Wait for g-new specifically (g-old's cancellation also fires resultNotifyFn).
	done := make(chan struct{}, 1)
	doer.SetResultNotifyFn(func(_ string) {
		_, _, _, r := doer.State().Snapshot()
		if r != nil && r.GoalID == "g-new" {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	})
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("second goal never completed")
	}

	// HandleIntent should have been called at least twice (once for each goal,
	// though the first may have been cancelled mid-flight).
	if mock.handleCalls.Load() < 2 {
		t.Errorf("expected at least 2 HandleIntent calls, got %d", mock.handleCalls.Load())
	}
}

func TestDoerGotoDispatch(t *testing.T) {
	// Step 1: goto → SchemaReady, Step 2: text answer → done.
	mock := &mockIntentHandler{
		responses: []mockResponse{
			{action: &navigator.ActionResult{Action: "goto", Path: "https://example.com"}},
			{textResp: "Page loaded."},
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
		sess.SignalSchemaReady()
	}()

	result := waitForDone(t, doer, 3*time.Second)
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
	if result.GoalID != "g-goto" {
		t.Errorf("expected goal g-goto, got %s", result.GoalID)
	}

	// Verify the engine was reset (new Engine created for goto).
	if sess.Engine == nil {
		t.Error("engine should have been replaced, not nil")
	}
	if mock.handleCalls.Load() != 2 {
		t.Errorf("expected 2 HandleIntent calls (goto + text), got %d", mock.handleCalls.Load())
	}

	_ = h // used for sendGoto side-effect (conn is nil, opens Chrome)
}

func TestDoerGotoCancellation(t *testing.T) {
	// Verify that cancelling the context while a goto is waiting for SchemaReady
	// causes the Doer to exit cleanly (not block forever).
	mock := &mockIntentHandler{
		responses: []mockResponse{
			{action: &navigator.ActionResult{Action: "goto", Path: "https://slow-page.example.com"}},
		},
	}
	_, sess, doer := newDoerTestHarness(mock)
	sess.SignalSchemaReady()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go doer.Run(ctx)

	doer.Submit(DoerGoal{ID: "g-slow", Text: "go to slow page"})

	// Cancel the context after 200ms rather than waiting for the real 30s timeout.
	time.Sleep(200 * time.Millisecond)
	cancel()

	// Give the Doer a moment to process the cancellation.
	time.Sleep(100 * time.Millisecond)

	status, _, _, result := doer.State().Snapshot()
	if status == DoerExecuting {
		t.Error("Doer should not still be Executing after context cancel")
	}
	_ = result // may be nil if cancellation raced
}

func TestDoerSchemaWaitSoftProceed(t *testing.T) {
	// Verify the Doer proceeds without schema after the 3s soft wait.
	mock := &mockIntentHandler{
		responses: []mockResponse{
			{action: &navigator.ActionResult{Action: "goto", Path: "https://example.com"}},
			{textResp: "Page loaded."},
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

	_ = waitForDone(t, doer, 10*time.Second)
}

func TestDoerActionDispatch(t *testing.T) {
	// Click dispatches the action, then the multi-step loop waits for settle.
	// After settle timeout + rescan, it loops back — second call returns text to finish.
	mock := &mockIntentHandler{
		responses: []mockResponse{
			{action: &navigator.ActionResult{Action: "click", MacheID: "mache-42", Path: "/main/button"}},
			{textResp: "Button clicked successfully."},
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

	// After the click, the Doer resets schema and waits for settle.
	// Simulate: no auto-snapshot (timeout), then rescan completes.
	go func() {
		// Wait for the settle timeout + rescan reset, then signal.
		time.Sleep(actionSettleTimeout + 200*time.Millisecond)
		sess.SignalSchemaReady()
	}()

	result := waitForDone(t, doer, 10*time.Second)
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}

	if v := actionNotified.Load(); v != "mache-42:click" {
		t.Errorf("expected actionNotifyFn with 'mache-42:click', got %v", v)
	}
	if mock.handleCalls.Load() != 2 {
		t.Errorf("expected 2 HandleIntent calls, got %d", mock.handleCalls.Load())
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

	_ = waitForDone(t, doer, 2*time.Second)

	// Give the defer cleanup in executeGoal a moment to run — the resultNotifyFn
	// fires before the deferred SetProgressFunc(nil) executes.
	time.Sleep(50 * time.Millisecond)

	// Verify progressFn was wired during execution (set by Doer, cleared after).
	// After completion, it should be nil (defer clears it).
	mock.mu.Lock()
	afterProgress := mock.progressFn
	mock.mu.Unlock()
	if afterProgress != nil {
		t.Error("progressFn should be nil after goal completes (defer cleanup)")
	}
}

func TestDoerMultiStepGotoThenRead(t *testing.T) {
	// Step 1: goto → navigate, Step 2: text answer from reading page.
	mock := &mockIntentHandler{
		responses: []mockResponse{
			{action: &navigator.ActionResult{Action: "goto", Path: "https://news.ycombinator.com"}},
			{textResp: "The top story is about AI safety regulations."},
		},
	}
	_, sess, doer := newDoerTestHarness(mock)
	sess.SignalSchemaReady()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go doer.Run(ctx)

	doer.Submit(DoerGoal{ID: "g-multi-goto", Text: "go to HN and tell me the top story"})

	// Simulate extension: goto resets schema, signal after a delay.
	go func() {
		time.Sleep(100 * time.Millisecond)
		sess.SignalSchemaReady()
	}()

	result := waitForDone(t, doer, 3*time.Second)
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
	if result.Summary != "The top story is about AI safety regulations." {
		t.Errorf("unexpected summary: %s", result.Summary)
	}

	if calls := mock.handleCalls.Load(); calls != 2 {
		t.Errorf("expected 2 HandleIntent calls, got %d", calls)
	}
}

func TestDoerMultiStepClickNavigates(t *testing.T) {
	// Step 1: click a link → page navigates (SchemaReady fires quickly).
	// Step 2: text answer from reading the new page.
	mock := &mockIntentHandler{
		responses: []mockResponse{
			{action: &navigator.ActionResult{Action: "click", MacheID: "mache-7", Path: "/main/stories/_c/3"}},
			{textResp: "The article discusses quantum computing breakthroughs."},
		},
	}
	_, sess, doer := newDoerTestHarness(mock)
	sess.SignalSchemaReady()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go doer.Run(ctx)

	doer.Submit(DoerGoal{ID: "g-click-nav", Text: "click the third story and tell me about it"})

	// Simulate: click causes URL change → auto-snapshot → SchemaReady fires quickly.
	go func() {
		time.Sleep(100 * time.Millisecond)
		sess.SignalSchemaReady()
	}()

	result := waitForDone(t, doer, 3*time.Second)
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
	if result.Summary != "The article discusses quantum computing breakthroughs." {
		t.Errorf("unexpected summary: %s", result.Summary)
	}

	if calls := mock.handleCalls.Load(); calls != 2 {
		t.Errorf("expected 2 HandleIntent calls, got %d", calls)
	}
}

func TestDoerMultiStepClickNoNav(t *testing.T) {
	// Click doesn't cause navigation → DOMMutatedCh fires → rescan → loop continues.
	// Step 1: click (in-page toggle), Step 2: text answer.
	mock := &mockIntentHandler{
		responses: []mockResponse{
			{action: &navigator.ActionResult{Action: "click", MacheID: "mache-10", Path: "/main/dropdown"}},
			{textResp: "Dropdown is now open."},
		},
	}
	_, sess, doer := newDoerTestHarness(mock)
	sess.SignalSchemaReady()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go doer.Run(ctx)

	doer.Submit(DoerGoal{ID: "g-click-nonav", Text: "open the dropdown menu"})

	// Simulate: MutationObserver fires after 200ms (much faster than 2s settle),
	// then rescan completes and signals SchemaReady.
	go func() {
		time.Sleep(200 * time.Millisecond)
		sess.DOMMutatedCh <- struct{}{}
		// Doer sends rescan after DOM mutation, then waits for SchemaReady.
		time.Sleep(100 * time.Millisecond)
		sess.SignalSchemaReady()
	}()

	result := waitForDone(t, doer, 5*time.Second)
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}

	if calls := mock.handleCalls.Load(); calls != 2 {
		t.Errorf("expected 2 HandleIntent calls (click + text), got %d", calls)
	}
}

func TestDoerMultiStepClickNoNavFallbackTimeout(t *testing.T) {
	// Verify the 2s fallback still works when no DOMMutatedCh signal arrives.
	mock := &mockIntentHandler{
		responses: []mockResponse{
			{action: &navigator.ActionResult{Action: "click", MacheID: "mache-10", Path: "/main/dropdown"}},
			{textResp: "Dropdown is now open."},
		},
	}
	_, sess, doer := newDoerTestHarness(mock)
	sess.SignalSchemaReady()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go doer.Run(ctx)

	doer.Submit(DoerGoal{ID: "g-click-nonav-fallback", Text: "open the dropdown menu"})

	// No DOMMutatedCh signal — the 2s settle timeout fires, then rescan.
	go func() {
		time.Sleep(actionSettleTimeout + 200*time.Millisecond)
		sess.SignalSchemaReady()
	}()

	result := waitForDone(t, doer, 10*time.Second)
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
	if calls := mock.handleCalls.Load(); calls != 2 {
		t.Errorf("expected 2 HandleIntent calls (click + text), got %d", calls)
	}
}

func TestDoerMultiStepMaxSteps(t *testing.T) {
	// Navigator always returns a click action — verify we stop at maxGoalSteps.
	mock := &mockIntentHandler{
		action: &navigator.ActionResult{Action: "click", MacheID: "mache-1", Path: "/main/button"},
	}
	_, sess, doer := newDoerTestHarness(mock)
	sess.SignalSchemaReady()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go doer.Run(ctx)

	doer.Submit(DoerGoal{ID: "g-loop", Text: "keep clicking forever"})

	// Each click step: settle timeout (2s) + rescan signal needed.
	// Signal SchemaReady repeatedly to keep the loop going.
	go func() {
		for i := 0; i < maxGoalSteps+1; i++ {
			time.Sleep(actionSettleTimeout + 100*time.Millisecond)
			sess.SignalSchemaReady()
		}
	}()

	timeout := time.Duration(maxGoalSteps)*(actionSettleTimeout+500*time.Millisecond) + 5*time.Second
	result := waitForDone(t, doer, timeout)
	if !result.Success {
		t.Errorf("expected success (exhausted steps), got error: %s", result.Error)
	}

	if calls := mock.handleCalls.Load(); int(calls) != maxGoalSteps {
		t.Errorf("expected %d HandleIntent calls (maxGoalSteps), got %d", maxGoalSteps, calls)
	}
}

func TestDoerMultiStepClickOpensNewTab(t *testing.T) {
	// Click opens a new tab (target="_blank" on site). The Doer detects
	// activeVoiceTab changed and rebinds to the new tab's session.

	// Mock for tab 0: returns click action.
	mock0 := &mockIntentHandler{
		responses: []mockResponse{
			{action: &navigator.ActionResult{Action: "click", MacheID: "mache-5", Path: "/main/link"}},
		},
	}
	// Mock for tab 99 (new tab): returns text answer.
	mock99 := &mockIntentHandler{
		responses: []mockResponse{
			{textResp: "New tab page content."},
		},
	}

	h, sess0, doer := newDoerTestHarness(mock0)
	sess0.SignalSchemaReady()

	// Pre-create session for tab 99 with its own mock navigator.
	sess99 := h.getSession(99)
	sess99.Navigator = mock99

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go doer.Run(ctx)

	doer.Submit(DoerGoal{ID: "g-newtab", Text: "click the link and read the new page"})

	// Simulate: click opens new tab. The extension sends TAB_ACTIVATED immediately
	// (before the settle timeout), then the new tab's page loads and SchemaReady fires.
	go func() {
		// TAB_ACTIVATED arrives quickly after the click.
		time.Sleep(200 * time.Millisecond)
		h.mu.Lock()
		h.activeVoiceTab = 99
		h.mu.Unlock()
		// New tab's page loads after settle timeout.
		time.Sleep(actionSettleTimeout)
		sess99.SignalSchemaReady()
	}()

	result := waitForDone(t, doer, 10*time.Second)
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
	if result.Summary != "New tab page content." {
		t.Errorf("unexpected summary: %s", result.Summary)
	}

	// Tab 0 mock: 1 call (click). Tab 99 mock: 1 call (text answer).
	if calls := mock0.handleCalls.Load(); calls != 1 {
		t.Errorf("expected 1 HandleIntent call on tab 0, got %d", calls)
	}
	if calls := mock99.handleCalls.Load(); calls != 1 {
		t.Errorf("expected 1 HandleIntent call on tab 99, got %d", calls)
	}
}
