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

const (
	maxGoalSteps        = 5
	actionSettleTimeout = 2 * time.Second
)

// DoerGoal is sent from the Talker to the Doer.
type DoerGoal struct {
	ID       string // unique goal ID for correlation
	Text     string // natural language intent
	ReadOnly bool   // true → Navigator cannot use act(); must answer with text
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
	mu       sync.Mutex
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
	d.mu.Lock()
	d.resultNotifyFn = fn
	d.mu.Unlock()
}

// SetActionNotifyFn sets the callback fired when an action is dispatched.
// The Talker uses this to forward EXECUTE_ACTION to the voice WebSocket.
func (d *Doer) SetActionNotifyFn(fn func(macheID, action, payload string)) {
	d.mu.Lock()
	d.actionNotifyFn = fn
	d.mu.Unlock()
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
	d.mu.Lock()
	if d.cancelFn != nil {
		d.cancelFn()
	}
	d.mu.Unlock()

	// Drain stale goal if any.
	select {
	case <-d.goalCh:
	default:
	}

	d.goalCh <- goal
}

// Cancel aborts the current goal.
func (d *Doer) Cancel() {
	d.mu.Lock()
	if d.cancelFn != nil {
		d.cancelFn()
		d.cancelFn = nil
	}
	d.mu.Unlock()

	d.state.mu.Lock()
	d.state.Status = DoerIdle
	d.state.GoalText = ""
	d.state.Step = ""
	d.state.Result = &DoerResult{Summary: "Cancelled by user."}
	d.state.mu.Unlock()
}

func (d *Doer) executeGoal(parentCtx context.Context, goal DoerGoal) {
	goalCtx, cancelFn := context.WithCancel(parentCtx)
	d.mu.Lock()
	d.cancelFn = cancelFn
	d.mu.Unlock()
	defer cancelFn()

	// Update state: executing.
	d.state.mu.Lock()
	d.state.Status = DoerExecuting
	d.state.GoalText = goal.Text
	d.state.Step = "starting"
	d.state.Result = nil
	d.state.mu.Unlock()

	log.Printf("Doer [tab %d]: executing goal %q", d.tabID, goal.Text)

	// Wire navigator callbacks (scroll, progress, list_tabs).
	d.wireNavigatorCallbacks()
	defer func() {
		d.sess.Navigator.SetScrollFunc(nil)
		d.sess.Navigator.SetProgressFunc(nil)
		d.sess.Navigator.SetListTabsFunc(nil)
	}()

	// Wait for schema, but proceed without one if it takes too long.
	// HandleIntent works with empty state for goto intents (e.g., "go to reddit.com"
	// produces a goto action regardless of whether a page schema exists).
	d.updateStep("waiting for page schema")
	select {
	case <-d.sess.GetSchemaReady():
		// Schema is ready — full tree available.
	case <-time.After(15 * time.Second):
		log.Printf("Doer [tab %d]: schema not ready after 15s, proceeding without", d.tabID)
	case <-goalCtx.Done():
		d.finishGoal(goal.ID, false, "Cancelled.", "cancelled")
		return
	}

	// Multi-step loop: dispatch action, wait for page settle, feed result back.
	enrichedIntent := goal.Text
	var lastSummary string

	for step := 0; step < maxGoalSteps; step++ {
		if step > 0 {
			d.updateStep(fmt.Sprintf("step %d", step+1))
		} else {
			d.updateStep("exploring page structure")
		}

		action, textResponse, err := d.sess.Navigator.HandleIntent(goalCtx, enrichedIntent, goal.ReadOnly)
		if err != nil {
			if goalCtx.Err() != nil {
				d.finishGoal(goal.ID, false, "Cancelled.", "cancelled")
			} else {
				d.finishGoal(goal.ID, false, fmt.Sprintf("Failed: %v", err), err.Error())
			}
			return
		}

		// Text response — the navigator answered rather than acted. Done.
		if textResponse != "" {
			d.finishGoal(goal.ID, true, textResponse, "")
			return
		}

		if action == nil {
			d.finishGoal(goal.ID, false, "Could not determine what to do.", "no action or response")
			return
		}

		lastSummary = d.dispatchAction(goalCtx, action)

		// If we started on Tab 0 (disconnected), the goto woke up the extension
		// which reported its real tab ID. Rebind the Doer to the new session.
		d.handler.mu.Lock()
		activeTab := d.handler.activeVoiceTab
		d.handler.mu.Unlock()

		if activeTab != 0 && activeTab != d.tabID {
			log.Printf("Doer [tab %d]: tab promoted to %d, rebinding", d.tabID, activeTab)
			d.tabID = activeTab
			d.sess = d.handler.getSession(activeTab)
			d.wireNavigatorCallbacks()
		}

		// For interactive actions (click/type/enter/focus), detect if the page navigated.
		if isInteractiveAction(action.Action) {
			d.sess.ResetSchema()
			// Drain any stale DOM mutation signal from a previous action.
			select {
			case <-d.sess.DOMMutatedCh:
			default:
			}
			select {
			case <-d.sess.GetSchemaReady():
				// Same-tab navigation (URL change → auto-snapshot) — continue loop.
			case <-d.sess.DOMMutatedCh:
				// In-page DOM mutation detected via MutationObserver.
				// Trigger rescan for fresh VFS.
				d.sess.ResetSchema()
				d.handler.sendRescan(d.tabID, "")
				select {
				case <-d.sess.GetSchemaReady():
				case <-time.After(schemaWaitTimeout):
				case <-goalCtx.Done():
					d.finishGoal(goal.ID, false, "Cancelled.", "cancelled")
					return
				}
			case <-time.After(actionSettleTimeout):
				// No same-tab navigation or DOM mutation. Check if a new tab was activated.
				d.handler.mu.Lock()
				newTabID := d.handler.activeVoiceTab
				d.handler.mu.Unlock()

				if newTabID != 0 && newTabID != d.tabID {
					// Click opened a new tab — rebind Doer to it.
					log.Printf("Doer [tab %d]: tab changed to %d, rebinding", d.tabID, newTabID)
					d.tabID = newTabID
					d.sess = d.handler.getSession(newTabID)
					d.wireNavigatorCallbacks()
					select {
					case <-d.sess.GetSchemaReady():
						// New tab loaded — continue loop.
					case <-time.After(schemaWaitTimeout):
						// Timed out waiting for new tab.
					case <-goalCtx.Done():
						d.finishGoal(goal.ID, false, "Cancelled.", "cancelled")
						return
					}
				} else {
					// No tab change — rescan for in-page DOM mutations.
					d.sess.ResetSchema()
					d.handler.sendRescan(d.tabID, "")
					select {
					case <-d.sess.GetSchemaReady():
						// Rescan complete — continue loop with fresh VFS.
					case <-time.After(schemaWaitTimeout):
						// Give up waiting for rescan.
					case <-goalCtx.Done():
						d.finishGoal(goal.ID, false, "Cancelled.", "cancelled")
						return
					}
				}
			case <-goalCtx.Done():
				d.finishGoal(goal.ID, false, "Cancelled.", "cancelled")
				return
			}
		}
		// goto/rescan/switch_tab already waited for SchemaReady in dispatchAction.

		enrichedIntent = buildContinuation(goal.Text, step, action, lastSummary)
	}

	// Exhausted steps — return whatever we have.
	d.finishGoal(goal.ID, true, lastSummary, "")
}

func (d *Doer) dispatchAction(ctx context.Context, action *navigator.ActionResult) string {
	switch action.Action {
	case "browser.goto":
		// Idempotent: skip reset if already on this URL.
		if d.sess.CurrentURL == action.Path {
			d.updateStep(fmt.Sprintf("already on %s, waiting for schema", action.Path))
			select {
			case <-d.sess.GetSchemaReady():
				return fmt.Sprintf("Already on %s, page is loaded.", action.Path)
			case <-time.After(schemaWaitTimeout):
				return fmt.Sprintf("Already on %s but schema is still loading.", action.Path)
			case <-ctx.Done():
				return "Navigation cancelled."
			}
		}
		d.sess.CurrentURL = action.Path
		d.sess.ResetSchema()
		newEngine := mache.NewEngine()
		d.sess.SwapEngine(newEngine)
		if err := d.sess.Composite.Unmount("browser"); err != nil {
			log.Printf("Doer [tab %d]: unmount browser: %v", d.tabID, err)
		}
		if err := d.sess.Composite.Mount("browser", newEngine); err != nil {
			log.Printf("Doer [tab %d]: mount browser: %v", d.tabID, err)
		}
		d.sess.Navigator.SetGraph(d.sess.Composite)
		d.handler.sendGoto(d.tabID, action.Path)
		d.updateStep(fmt.Sprintf("navigating to %s", action.Path))
		log.Printf("Doer [tab %d]: goto %s", d.tabID, action.Path)

		select {
		case <-d.sess.GetSchemaReady():
			return fmt.Sprintf("Navigated to %s. Page is loaded.", action.Path)
		case <-time.After(schemaWaitTimeout):
			return fmt.Sprintf("Navigated to %s but page load timed out.", action.Path)
		case <-ctx.Done():
			return "Navigation cancelled."
		}

	case "browser.rescan":
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
			newEngine := mache.NewEngine()
			d.sess.SwapEngine(newEngine)
			if err := d.sess.Composite.Unmount("browser"); err != nil {
				log.Printf("Doer [tab %d]: unmount browser: %v", d.tabID, err)
			}
			if err := d.sess.Composite.Mount("browser", newEngine); err != nil {
				log.Printf("Doer [tab %d]: mount browser: %v", d.tabID, err)
			}
			d.sess.Navigator.SetGraph(d.sess.Composite)
			d.handler.sendRescan(d.tabID, "")
			d.updateStep("rescanning full page")
			log.Printf("Doer [tab %d]: full rescan", d.tabID)
		}
		select {
		case <-d.sess.GetSchemaReady():
			if action.Path != "" {
				return fmt.Sprintf("Zoomed into %s for more detail.", action.Path)
			}
			return "Page rescanned."
		case <-time.After(schemaWaitTimeout):
			return "Rescan timed out."
		case <-ctx.Done():
			return "Rescan cancelled."
		}

	case "browser.switch_tab":
		// Switch to an existing open tab. Parse tab ID from Path.
		var switchTabID int
		if _, err := fmt.Sscanf(action.Path, "%d", &switchTabID); err != nil || switchTabID == 0 {
			return fmt.Sprintf("Invalid tab ID: %s", action.Path)
		}
		d.updateStep(fmt.Sprintf("switching to tab %d", switchTabID))
		d.handler.sendSwitchTab(switchTabID)

		// Update the voice tab so future commands route here.
		d.handler.mu.Lock()
		d.handler.activeVoiceTab = switchTabID
		d.handler.mu.Unlock()

		// The extension will activate the tab and send a DOM_SNAPSHOT.
		// Wait for the new tab's schema to be ready.
		newSess := d.handler.getSession(switchTabID)
		newSess.ResetSchema()
		select {
		case <-newSess.GetSchemaReady():
			return fmt.Sprintf("Switched to tab %d. Page is loaded.", switchTabID)
		case <-time.After(schemaWaitTimeout):
			return fmt.Sprintf("Switched to tab %d but schema timed out.", switchTabID)
		case <-ctx.Done():
			return "Tab switch cancelled."
		}

	default:
		// Click/focus/type/enter — dispatch to extension.
		d.updateStep(fmt.Sprintf("performing %s on %s", action.Action, action.Path))
		d.handler.SendActionToExtension(d.tabID, action.MacheID, action.Action, action.Payload)
		d.mu.Lock()
		actionNotify := d.actionNotifyFn
		d.mu.Unlock()
		if actionNotify != nil {
			actionNotify(action.MacheID, action.Action, action.Payload)
		}
		if action.Payload != "" {
			return fmt.Sprintf("Typed %q into %s.", action.Payload, action.Path)
		}
		return fmt.Sprintf("Done. Performed %s on %s.", action.Action, action.Path)
	}
}

// wireNavigatorCallbacks sets scroll, progress, and list_tabs callbacks on the
// current session's Navigator. Called at goal start and after cross-tab rebind.
func (d *Doer) wireNavigatorCallbacks() {
	d.sess.Navigator.SetScrollFunc(func(scrollCtx context.Context, direction string) error {
		d.updateStep(fmt.Sprintf("scrolling %s", direction))
		return d.handler.scrollVoice(scrollCtx, d.sess, d.tabID, direction)
	})
	d.sess.Navigator.SetProgressFunc(func(toolName string, args map[string]any) {
		p, _ := args["path"].(string)
		if p != "" {
			d.updateStep(fmt.Sprintf("%s %s", toolName, p))
		} else {
			d.updateStep(toolName)
		}
	})
	d.sess.Navigator.SetListTabsFunc(func(ltCtx context.Context) ([]navigator.TabInfo, error) {
		d.updateStep("listing open tabs")
		select {
		case <-d.sess.TabsListedCh:
		default:
		}
		d.handler.sendListTabs()
		select {
		case tabs := <-d.sess.TabsListedCh:
			return tabs, nil
		case <-time.After(5 * time.Second):
			return nil, fmt.Errorf("list_tabs timed out")
		case <-ltCtx.Done():
			return nil, ltCtx.Err()
		}
	})
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
	d.mu.Lock()
	notify := d.resultNotifyFn
	d.mu.Unlock()
	if notify != nil {
		notify(summary)
	}
}

// isInteractiveAction returns true for actions that don't already wait for
// SchemaReady inside dispatchAction (i.e., everything except goto/rescan/switch_tab).
// Also returns false for terminal window creation actions since they don't modify the DOM.
func isInteractiveAction(action string) bool {
	return action != "browser.goto" && action != "browser.rescan" && action != "browser.switch_tab"
}

// buildContinuation creates an enriched intent for the next step in the loop.
// It tells the Navigator what happened and asks it to continue toward the goal.
func buildContinuation(goal string, step int, action *navigator.ActionResult, summary string) string {
	return fmt.Sprintf("[CONTINUATION — Step %d completed]\n"+
		"Original goal: %s\nLast action: %s on %s\nResult: %s\n\n"+
		"The page has updated. Explore the new page structure to continue working on "+
		"the original goal. If the goal is achievable by reading page content, provide "+
		"a text answer. If more actions are needed, take the next step.",
		step+1, goal, action.Action, action.Path, summary)
}
