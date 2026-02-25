package api

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/jamesgardner/x-ray/internal/mache"
	"github.com/jamesgardner/x-ray/internal/navigator"
)

// DoerStatus represents the current state of background work.
type DoerStatus int

const (
	DoerIdle      DoerStatus = iota
	DoerExecuting            // tool-use loop is running
	DoerDone                 // result available
	DoerFailed               // error occurred
)

// DoerGoal is sent from the Talker to the Doer.
type DoerGoal struct {
	ID   string // unique goal ID for correlation
	Text string // natural language intent
}

// DoerResult is produced when a goal completes or fails.
type DoerResult struct {
	GoalID  string
	Success bool
	Summary string // human-readable for Talker to speak
	Error   string
}

// DoerState is the shared state the Talker can poll at any time.
type DoerState struct {
	mu       sync.RWMutex
	Status   DoerStatus
	GoalText string
	Step     string // e.g., "reading /main/feed/children"
	Result   *DoerResult
}

// Snapshot returns a thread-safe copy of the current state.
func (ds *DoerState) Snapshot() (DoerStatus, string, string, *DoerResult) {
	ds.mu.RLock()
	defer ds.mu.RUnlock()
	return ds.Status, ds.GoalText, ds.Step, ds.Result
}

// Doer runs background navigation work for a tab session.
type Doer struct {
	handler  *Handler
	tabID    int
	sess     *TabSession
	state    *DoerState
	goalCh   chan DoerGoal
	cancelMu sync.Mutex
	cancelFn context.CancelFunc

	resultNotifyFn func(summary string)                  // called when goal completes
	actionNotifyFn func(macheID, action, payload string) // called when action dispatched
}

// NewDoer creates a Doer for the given tab session.
func NewDoer(handler *Handler, tabID int, sess *TabSession) *Doer {
	return &Doer{
		handler: handler,
		tabID:   tabID,
		sess:    sess,
		state:   &DoerState{Status: DoerIdle},
		goalCh:  make(chan DoerGoal, 1),
	}
}

// SetResultNotifyFn sets the callback fired when a goal completes.
// The Talker uses this to inject a synthetic message into the Live session.
func (d *Doer) SetResultNotifyFn(fn func(summary string)) {
	d.resultNotifyFn = fn
}

// SetActionNotifyFn sets the callback fired when an action is dispatched.
// The Talker uses this to forward EXECUTE_ACTION to the voice WebSocket.
func (d *Doer) SetActionNotifyFn(fn func(macheID, action, payload string)) {
	d.actionNotifyFn = fn
}

// State returns the DoerState for the Talker to poll.
func (d *Doer) State() *DoerState {
	return d.state
}

// Run is the main loop. Blocks until ctx is cancelled.
func (d *Doer) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case goal := <-d.goalCh:
			d.executeGoal(ctx, goal)
		}
	}
}

// Submit sends a goal to the Doer. Cancels any in-flight work first.
func (d *Doer) Submit(goal DoerGoal) {
	// Cancel current work.
	d.cancelMu.Lock()
	if d.cancelFn != nil {
		d.cancelFn()
	}
	d.cancelMu.Unlock()

	// Drain stale goal if any.
	select {
	case <-d.goalCh:
	default:
	}

	d.goalCh <- goal
}

// Cancel aborts the current goal.
func (d *Doer) Cancel() {
	d.cancelMu.Lock()
	if d.cancelFn != nil {
		d.cancelFn()
		d.cancelFn = nil
	}
	d.cancelMu.Unlock()

	d.state.mu.Lock()
	d.state.Status = DoerIdle
	d.state.GoalText = ""
	d.state.Step = ""
	d.state.Result = &DoerResult{Summary: "Cancelled by user."}
	d.state.mu.Unlock()
}

func (d *Doer) executeGoal(parentCtx context.Context, goal DoerGoal) {
	goalCtx, cancelFn := context.WithCancel(parentCtx)
	d.cancelMu.Lock()
	d.cancelFn = cancelFn
	d.cancelMu.Unlock()
	defer cancelFn()

	// Update state: executing.
	d.state.mu.Lock()
	d.state.Status = DoerExecuting
	d.state.GoalText = goal.Text
	d.state.Step = "starting"
	d.state.Result = nil
	d.state.mu.Unlock()

	log.Printf("Doer [tab %d]: executing goal %q", d.tabID, goal.Text)

	// Wire scroll for this goal.
	d.sess.Navigator.SetScrollFunc(func(scrollCtx context.Context, direction string) error {
		d.updateStep(fmt.Sprintf("scrolling %s", direction))
		return d.handler.scrollVoice(scrollCtx, d.sess, d.tabID, direction)
	})
	defer d.sess.Navigator.SetScrollFunc(nil)

	// Wire progress reporting.
	d.sess.Navigator.SetProgressFunc(func(toolName string, args map[string]any) {
		p, _ := args["path"].(string)
		if p != "" {
			d.updateStep(fmt.Sprintf("%s %s", toolName, p))
		} else {
			d.updateStep(toolName)
		}
	})
	defer d.sess.Navigator.SetProgressFunc(nil)

	// Wait briefly for schema, but proceed without one.
	// HandleIntent works with empty state for goto intents (e.g., "go to reddit.com"
	// produces a goto action regardless of whether a page schema exists).
	d.updateStep("waiting for page schema")
	select {
	case <-d.sess.SchemaReady:
		// Schema is ready — full tree available.
	case <-time.After(3 * time.Second):
		log.Printf("Doer [tab %d]: schema not ready after 3s, proceeding without", d.tabID)
	case <-goalCtx.Done():
		d.finishGoal(goal.ID, false, "Cancelled.", "cancelled")
		return
	}

	// Run the navigator's agentic tool loop.
	d.updateStep("exploring page structure")
	action, textResponse, err := d.sess.Navigator.HandleIntent(goalCtx, goal.Text)
	if err != nil {
		if goalCtx.Err() != nil {
			d.finishGoal(goal.ID, false, "Cancelled.", "cancelled")
		} else {
			d.finishGoal(goal.ID, false, fmt.Sprintf("Failed: %v", err), err.Error())
		}
		return
	}

	// Handle action dispatch (goto, rescan, act).
	if action != nil {
		summary := d.dispatchAction(goalCtx, action)
		d.finishGoal(goal.ID, true, summary, "")
		return
	}

	// Text response (the navigator decided to answer rather than act).
	if textResponse != "" {
		d.finishGoal(goal.ID, true, textResponse, "")
		return
	}

	d.finishGoal(goal.ID, false, "Could not determine what to do.", "no action or response")
}

func (d *Doer) dispatchAction(ctx context.Context, action *navigator.ActionResult) string {
	switch action.Action {
	case "goto":
		// Idempotent: skip reset if already on this URL.
		if d.sess.CurrentURL == action.Path {
			d.updateStep(fmt.Sprintf("already on %s, waiting for schema", action.Path))
			select {
			case <-d.sess.SchemaReady:
				return fmt.Sprintf("Already on %s, page is loaded.", action.Path)
			case <-time.After(schemaWaitTimeout):
				return fmt.Sprintf("Already on %s but schema is still loading.", action.Path)
			case <-ctx.Done():
				return "Navigation cancelled."
			}
		}
		d.sess.CurrentURL = action.Path
		d.sess.ResetSchema()
		d.sess.Engine = mache.NewEngine()
		d.sess.Navigator.SetEngine(d.sess.Engine)
		d.handler.sendGoto(d.tabID, action.Path)
		d.updateStep(fmt.Sprintf("navigating to %s", action.Path))
		log.Printf("Doer [tab %d]: goto %s", d.tabID, action.Path)

		select {
		case <-d.sess.SchemaReady:
			return fmt.Sprintf("Navigated to %s. Page is loaded.", action.Path)
		case <-time.After(schemaWaitTimeout):
			return fmt.Sprintf("Navigated to %s but page load timed out.", action.Path)
		case <-ctx.Done():
			return "Navigation cancelled."
		}

	case "rescan":
		if action.Path != "" {
			// Targeted rescan: keep existing engine, zoom into zone.
			d.sess.ResetSchema()
			d.sess.RescanPath = action.Path
			d.handler.sendRescan(d.tabID, action.MacheID)
			d.updateStep(fmt.Sprintf("zooming into %s", action.Path))
			log.Printf("Doer [tab %d]: targeted rescan %q (mache_id %s)", d.tabID, action.Path, action.MacheID)
		} else {
			// Full-page rescan: reset everything.
			d.sess.ResetSchema()
			d.sess.Engine = mache.NewEngine()
			d.sess.Navigator.SetEngine(d.sess.Engine)
			d.handler.sendRescan(d.tabID, "")
			d.updateStep("rescanning full page")
			log.Printf("Doer [tab %d]: full rescan", d.tabID)
		}
		select {
		case <-d.sess.SchemaReady:
			if action.Path != "" {
				return fmt.Sprintf("Zoomed into %s for more detail.", action.Path)
			}
			return "Page rescanned."
		case <-time.After(schemaWaitTimeout):
			return "Rescan timed out."
		case <-ctx.Done():
			return "Rescan cancelled."
		}

	default:
		// Click/focus/type/enter — dispatch to extension.
		d.updateStep(fmt.Sprintf("performing %s on %s", action.Action, action.Path))
		d.handler.SendActionToExtension(d.tabID, action.MacheID, action.Action, action.Payload)
		if d.actionNotifyFn != nil {
			d.actionNotifyFn(action.MacheID, action.Action, action.Payload)
		}
		if action.Payload != "" {
			return fmt.Sprintf("Typed %q into %s.", action.Payload, action.Path)
		}
		return fmt.Sprintf("Done. Performed %s on %s.", action.Action, action.Path)
	}
}

func (d *Doer) updateStep(step string) {
	d.state.mu.Lock()
	d.state.Step = step
	d.state.mu.Unlock()
}

func (d *Doer) finishGoal(goalID string, success bool, summary, errStr string) {
	d.state.mu.Lock()
	if success {
		d.state.Status = DoerDone
	} else {
		d.state.Status = DoerFailed
	}
	d.state.Step = ""
	d.state.Result = &DoerResult{
		GoalID:  goalID,
		Success: success,
		Summary: summary,
		Error:   errStr,
	}
	d.state.mu.Unlock()

	log.Printf("Doer [tab %d]: goal %s finished (success=%v): %s", d.tabID, goalID, success, summary)

	// Notify the Talker so it can announce the result.
	if d.resultNotifyFn != nil {
		d.resultNotifyFn(summary)
	}
}
