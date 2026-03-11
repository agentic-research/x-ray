package api

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/agentic-research/x-ray/internal/config"
	"github.com/agentic-research/x-ray/internal/guardrails"
	"github.com/agentic-research/x-ray/internal/mache"
	"github.com/agentic-research/x-ray/internal/navigator"
)

// sameOrigin returns true if targetURL shares the same host as the session's
// current URL. If no current URL is set (e.g., tests or fresh sessions), allows
// anything. This prevents the Navigator from escaping to a different site via
// browser.goto — cross-site navigation should use open_url instead.
func sameOrigin(currentURL, targetURL string) bool {
	if currentURL == "" {
		return true
	}
	cur, err1 := url.Parse(currentURL)
	tgt, err2 := url.Parse(targetURL)
	if err1 != nil || err2 != nil {
		return false
	}
	return cur.Host == tgt.Host
}

// InteractionStatus represents the current state of a background interaction.
type InteractionStatus string

const (
	StatusIdle       InteractionStatus = "idle"
	StatusInProgress InteractionStatus = "in_progress"
	StatusCompleted  InteractionStatus = "completed"
	StatusFailed     InteractionStatus = "failed"
	StatusCancelled  InteractionStatus = "cancelled"
)

const (
	maxGoalSteps        = 5
	actionSettleTimeout = 2 * time.Second
)

// Interaction is sent from the Talker to the Doer.
type Interaction struct {
	ID         string // unique interaction ID for correlation
	Intent     string // natural language intent
	ReadOnly   bool   // true → Navigator cannot use act(); must answer with text
	PreviousID string // ID of the previous interaction (for chaining)
	Context    string // higher-level task from the Planner (used in continuations)
}

// InteractionResult is produced when an interaction completes or fails.
type InteractionResult struct {
	InteractionID string
	Status        InteractionStatus
	Summary       string // human-readable for Talker to speak
	Error         string
}

// InteractionState is the shared state the Talker can poll at any time.
type InteractionState struct {
	mu     sync.RWMutex
	Status InteractionStatus
	Intent string
	Step   string // e.g., "reading /main/feed/children"
	Result *InteractionResult
}

// Snapshot returns a thread-safe copy of the current state.
func (ds *InteractionState) Snapshot() (InteractionStatus, string, string, *InteractionResult) {
	ds.mu.RLock()
	defer ds.mu.RUnlock()
	return ds.Status, ds.Intent, ds.Step, ds.Result
}

// Doer runs background navigation work for a tab session.
type Doer struct {
	handler       *Handler
	tabID         int
	sess          *TabSession
	state         *InteractionState
	interactionCh chan Interaction
	mu            sync.Mutex
	cancelFn      context.CancelFunc

	resultNotifyFn     func(summary string)                  // called when interaction completes
	actionNotifyFn     func(macheID, action, payload string) // called when action dispatched
	dispatchOverrideFn func(*navigator.ActionResult) string  // test-only hook; do not use in production
}

// NewDoer creates a Doer for the given tab session.
func NewDoer(handler *Handler, tabID int, sess *TabSession) *Doer {
	return &Doer{
		handler:       handler,
		tabID:         tabID,
		sess:          sess,
		state:         &InteractionState{Status: StatusIdle},
		interactionCh: make(chan Interaction, 1),
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

// State returns the InteractionState for the Talker to poll.
func (d *Doer) State() *InteractionState {
	return d.state
}

// Run is the main loop. Blocks until ctx is cancelled.
func (d *Doer) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ix := <-d.interactionCh:
			d.executeInteraction(ctx, ix)
		}
	}
}

// Submit sends an interaction to the Doer. Cancels any in-flight work first.
func (d *Doer) Submit(ix Interaction) {
	// Cancel current work.
	d.mu.Lock()
	if d.cancelFn != nil {
		d.cancelFn()
	}
	d.mu.Unlock()

	// Drain stale interaction if any.
	select {
	case <-d.interactionCh:
	default:
	}

	d.interactionCh <- ix
}

// Cancel aborts the current interaction.
func (d *Doer) Cancel() {
	d.mu.Lock()
	if d.cancelFn != nil {
		d.cancelFn()
		d.cancelFn = nil
	}
	d.mu.Unlock()

	d.state.mu.Lock()
	d.state.Status = StatusCancelled
	d.state.Intent = ""
	d.state.Step = ""
	d.state.Result = &InteractionResult{Summary: "Cancelled by user.", Status: StatusCancelled}
	d.state.mu.Unlock()
}

func (d *Doer) executeInteraction(parentCtx context.Context, ix Interaction) {
	ixCtx, cancelFn := context.WithCancel(parentCtx)
	d.mu.Lock()
	d.cancelFn = cancelFn
	d.mu.Unlock()
	defer cancelFn()

	// Update state: in progress.
	d.state.mu.Lock()
	d.state.Status = StatusInProgress
	d.state.Intent = ix.Intent
	d.state.Step = "starting"
	d.state.Result = nil
	d.state.mu.Unlock()

	log.Printf("Doer [tab %d]: executing interaction %q", d.tabID, ix.Intent)

	// Populate /interactions/active/ so Navigator can cat intent and write status.
	taskText := ix.Intent
	if ix.Context != "" {
		taskText = ix.Context + "\n\nCurrent sub-goal: " + ix.Intent
	}
	if d.sess.Tasks != nil {
		d.sess.Tasks.StartInteraction(ix.ID, taskText)
	}

	// Initialize guardrails for this interaction.
	gs := guardrails.New(d.tabID)
	if gs.Enabled {
		gs.Log("init", fmt.Sprintf("guardrails enabled for interaction %q", ix.Intent))
	}
	if d.sess.Tasks != nil {
		d.sess.Tasks.SetDedupFunc(gs.IsDuplicate)
		defer d.sess.Tasks.SetDedupFunc(nil)
	}

	// Wire ref validation guardrail.
	if gs.Enabled {
		d.sess.Navigator.SetRefValidateFunc(gs.ValidateActPath)
	}
	defer d.sess.Navigator.SetRefValidateFunc(nil)

	// Wire navigator callbacks (scroll, progress, list_tabs).
	d.wireNavigatorCallbacks(gs)
	defer func() {
		d.sess.Navigator.SetScrollFunc(nil)
		d.sess.Navigator.SetProgressFunc(nil)
		d.sess.Navigator.SetListTabsFunc(nil)
	}()

	// Wait for schema to be ready before first Navigator call.
	// Without this, Navigator sees an empty filesystem on fresh page loads.
	// Skip for tab 0 (system session) — there's no browser to wait for.
	// The Navigator can still use /iterm/ and /interactions/ paths without browser schema.
	if d.tabID != 0 && !d.sess.GetEngine().HasSchema() {
		initialSchemaWait := 15 * time.Second
		if d.handler.NavSpeed == "fast" {
			initialSchemaWait = 3 * time.Second
		}
		d.updateStep("waiting for page to load")
		log.Printf("Doer [tab %d]: waiting for schema before starting (timeout %s)", d.tabID, initialSchemaWait)
		select {
		case <-d.sess.GetSchemaReady():
			log.Printf("Doer [tab %d]: schema ready, proceeding", d.tabID)
		case <-time.After(initialSchemaWait):
			log.Printf("Doer [tab %d]: schema wait timed out, proceeding anyway", d.tabID)
		case <-ixCtx.Done():
			d.finishInteraction(ix.ID, StatusCancelled, "Cancelled while waiting for page.", "cancelled")
			return
		}
	}

	// Multi-step loop: dispatch action, wait for page settle, feed result back.
	// Always include Context so the Navigator knows the overall goal,
	// not just the Planner's 1-sentence sub-command ("contextual amnesia" fix).
	enrichedIntent := ix.Intent
	if ix.Context != "" {
		enrichedIntent = fmt.Sprintf("OVERALL TASK: %s\n\nCurrent step: %s", ix.Context, ix.Intent)
	}
	var lastSummary string
	var consecutiveRescanTimeouts int

	for step := 0; step < maxGoalSteps; step++ {
		if step > 0 {
			d.updateStep(fmt.Sprintf("step %d", step+1))
		} else {
			d.updateStep("exploring page structure")
		}

		// Inject section hints before HandleIntent so the Navigator sees them.
		if url := d.sess.GetCurrentURL(); url != "" {
			if key := CacheKey(url); key != "" {
				sections := d.handler.schemas.GetAllSectionsForURL(key)
				if hints := formatSectionHints(sections); hints != "" {
					d.sess.Navigator.SetSectionHints(hints)
				}
			}
		}

		// Inject overlay screenshot for visual grounding (Gemini cloud only).
		if img, mime := d.sess.GetScreenshot(); len(img) > 0 {
			d.sess.Navigator.SetScreenshot(img, mime)
		}

		action, textResponse, err := d.sess.Navigator.HandleIntent(ixCtx, enrichedIntent, ix.ReadOnly)
		if err != nil {
			if ixCtx.Err() != nil {
				d.finishInteraction(ix.ID, StatusCancelled, "Cancelled.", "cancelled")
			} else {
				d.finishInteraction(ix.ID, StatusFailed, fmt.Sprintf("Failed: %v", err), err.Error())
			}
			return
		}

		// Text response — the navigator answered rather than acted.
		// Read structured status from the interaction graph instead of
		// string-matching the response text.
		if textResponse != "" {
			ixStatus := ""
			if d.sess.Tasks != nil {
				ixStatus = d.sess.Tasks.Status()
			}

			// Navigator explicitly signaled failure — do NOT retry.
			if strings.HasPrefix(ixStatus, "failed:") {
				reason := strings.TrimPrefix(ixStatus, "failed:")
				log.Printf("Doer [tab %d]: Navigator signaled failure: %s", d.tabID, reason)
				d.finishInteraction(ix.ID, StatusFailed, "Not found: "+reason, reason)
				return
			}

			// Completeness guardrail: check if we have all expected items.
			if gs.Enabled && step < maxGoalSteps-1 && d.sess.Tasks != nil {
				if warning := gs.CheckCompleteness(d.sess.Tasks.Scratch()); warning != "" {
					log.Printf("Doer [tab %d]: completeness check, continuing: %s", d.tabID, warning)
					enrichedIntent = warning + "\n\nOriginal task: " + ix.Intent
					continue
				}
			}

			d.finishInteraction(ix.ID, StatusCompleted, textResponse, "")
			return
		}

		if action == nil {
			d.finishInteraction(ix.ID, StatusFailed, "Could not determine what to do.", "no action or response")
			return
		}

		lastSummary = d.dispatchAction(ixCtx, action)

		// Circuit breaker: if rescan keeps timing out (empty tab), bail.
		if action.Action == "browser.rescan" && strings.Contains(lastSummary, "timed out") {
			consecutiveRescanTimeouts++
			if consecutiveRescanTimeouts >= 3 {
				log.Printf("Doer [tab %d]: %d consecutive rescan timeouts — tab appears empty, aborting", d.tabID, consecutiveRescanTimeouts)
				d.finishInteraction(ix.ID, StatusFailed, "Page failed to load after multiple attempts.", "consecutive rescan timeouts")
				return
			}
		} else {
			consecutiveRescanTimeouts = 0
		}

		// Record step in the interaction graph's audit trail.
		if d.sess.Tasks != nil {
			d.sess.Tasks.RecordStep(fmt.Sprintf("%s %s", action.Action, action.Path))
		}

		// Record page visit and scan for item counts.
		// NOTE: Navigator-level tool calls (cat, grep, ls) are already tracked
		// via the progress callback in wireNavigatorCallbacks. We only record
		// page visits and item count extraction here — NOT RecordAction, to
		// avoid double-recording that would corrupt the lookback window for
		// ref validation.
		if gs.Enabled {
			if url := d.sess.GetCurrentURL(); url != "" {
				startPct, endPct := d.sess.Navigator.GetViewport()
				gs.RecordPageVisit(url, startPct, endPct)
			}
			gs.UpdateItemCount(lastSummary)
		}

		// Rebind if the active tab changed (tab 0 cold start, or cross-origin
		// goto opened a new tab via sendCreateTab).
		d.handler.mu.Lock()
		activeTab := d.handler.activeVoiceTab
		d.handler.mu.Unlock()

		if activeTab != 0 && activeTab != d.tabID {
			log.Printf("Doer [tab %d]: tab changed to %d, rebinding", d.tabID, activeTab)
			d.tabID = activeTab
			d.sess = d.handler.getSession(activeTab)
			d.wireNavigatorCallbacks(gs)
			// Carry the interaction into the new session's graph so
			// Navigator can read/write /interactions/active/*.
			if d.sess.Tasks != nil {
				d.sess.Tasks.StartInteraction(ix.ID, taskText)
			}
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
				// Trigger targeted rescan if the action path identifies a zone.
				d.sess.ResetSchema()
				zonePath, _ := parseActionPath(action.Path)
				if zonePath != "" && action.MacheID != "" {
					d.sess.SetRescanPath(zonePath)
					d.handler.sendRescan(d.tabID, action.MacheID)
				} else {
					d.handler.sendRescan(d.tabID, "")
				}
				select {
				case <-d.sess.GetSchemaReady():
				case <-time.After(config.Dur(d.handler.Timeouts.SchemaWait)):
					log.Printf("Doer [tab %d]: schema wait timed out after DOM mutation rescan", d.tabID)
					lastSummary = "Warning: page DOM changed but rescan timed out. Filesystem may be stale."
				case <-ixCtx.Done():
					d.finishInteraction(ix.ID, StatusCancelled, "Cancelled.", "cancelled")
					return
				}
			case <-time.After(d.settleTimeout()):
				// No same-tab navigation or DOM mutation. Check if a new tab was activated.
				d.handler.mu.Lock()
				newTabID := d.handler.activeVoiceTab
				d.handler.mu.Unlock()

				if newTabID != 0 && newTabID != d.tabID {
					// Click opened a new tab — rebind Doer to it.
					log.Printf("Doer [tab %d]: tab changed to %d, rebinding", d.tabID, newTabID)
					d.tabID = newTabID
					d.sess = d.handler.getSession(newTabID)
					d.wireNavigatorCallbacks(gs)
					if d.sess.Tasks != nil {
						d.sess.Tasks.StartInteraction(ix.ID, taskText)
					}
					select {
					case <-d.sess.GetSchemaReady():
						// New tab loaded — continue loop.
					case <-time.After(config.Dur(d.handler.Timeouts.SchemaWait)):
						log.Printf("Doer [tab %d]: schema wait timed out on new tab", d.tabID)
						lastSummary = "Warning: switched to new tab but page load timed out. Filesystem may be stale."
					case <-ixCtx.Done():
						d.finishInteraction(ix.ID, StatusCancelled, "Cancelled.", "cancelled")
						return
					}
				} else {
					// No tab change — rescan for in-page DOM mutations.
					d.sess.ResetSchema()
					d.handler.sendRescan(d.tabID, "")
					select {
					case <-d.sess.GetSchemaReady():
						// Rescan complete — continue loop with fresh VFS.
					case <-time.After(config.Dur(d.handler.Timeouts.SchemaWait)):
						log.Printf("Doer [tab %d]: schema wait timed out after settle rescan", d.tabID)
						lastSummary = "Warning: rescan timed out after action. Filesystem may be stale."
					case <-ixCtx.Done():
						d.finishInteraction(ix.ID, StatusCancelled, "Cancelled.", "cancelled")
						return
					}
				}
			case <-ixCtx.Done():
				d.finishInteraction(ix.ID, StatusCancelled, "Cancelled.", "cancelled")
				return
			}
		}
		// goto/rescan/switch_tab already waited for SchemaReady in dispatchAction.

		// Record successful interactive actions as NavSections for future reuse.
		d.recordNavSection(ix, action)

		// Fast mode: after a successful interactive action (click, type, etc.),
		// finish immediately. Navigation actions (goto, rescan, switch_tab) are
		// NOT terminal — the user's intent likely requires reading/acting on
		// the destination page, so continue the loop.
		if d.handler.NavSpeed == "fast" && isInteractiveAction(action.Action) {
			d.finishInteraction(ix.ID, StatusCompleted,
				fmt.Sprintf("Done: %s on %s", action.Action, action.Path), "")
			return
		}

		if d.handler.NavSpeed == "fast" {
			// Fast mode: tell the navigator exactly where it is, what just
			// happened, and that the page is ready. Without explicit "do not
			// rescan/switch", lite models waste iterations re-verifying.
			curURL := d.sess.GetCurrentURL()
			enrichedIntent = fmt.Sprintf(
				"You are on %s (page is ready, do NOT rescan or switch tabs). "+
					"Completed: %s %s. Now continue: %s",
				curURL, action.Action, action.Path, ix.Intent)
		} else {
			enrichedIntent = buildContinuation(ix.Intent, ix.Context, step, action, lastSummary, gs)
		}
	}

	// Exhausted steps — return whatever we have.
	d.finishInteraction(ix.ID, StatusCompleted, lastSummary, "")
}

func (d *Doer) dispatchAction(ctx context.Context, action *navigator.ActionResult) string {
	// test-only hook — allows tests to control dispatch summaries without real extension
	if d.dispatchOverrideFn != nil {
		return d.dispatchOverrideFn(action)
	}

	switch action.Action {
	case "browser.goto":
		// Fix protocol mismatch: Navigator sometimes upgrades http→https for
		// localhost URLs. Local containers don't have TLS, so downgrade.
		if strings.HasPrefix(action.Path, "https://localhost") {
			action.Path = "http" + action.Path[5:]
			log.Printf("Doer [tab %d]: downgraded https→http for localhost: %s", d.tabID, action.Path)
		}
		currentURL := d.sess.GetCurrentURL()
		if currentURL == "" && d.tabID != 0 {
			// No URL set (CDP attach failed or no snapshot received).
			// Treat as cross-origin so we open a fresh tab.
			log.Printf("Doer [tab %d]: no currentURL set, treating goto as cross-origin", d.tabID)
		}
		if (currentURL == "" && d.tabID != 0) || !sameOrigin(currentURL, action.Path) {
			// Cross-origin (or broken tab): open in a new tab instead of blocking.
			// The domain jail only hard-blocks in Planner/eval mode;
			// voice-mode Doer opens a new tab so the user can navigate freely.
			log.Printf("Doer [tab %d]: cross-origin goto %s (from %s) — opening new tab", d.tabID, action.Path, d.sess.GetCurrentURL())

			// Drain any stale voiceTabCh values before opening the new tab.
			for {
				select {
				case stale := <-d.handler.voiceTabCh:
					log.Printf("Doer [tab %d]: drained stale voiceTabCh value %d", d.tabID, stale)
				default:
					goto drained
				}
			}
		drained:
			d.handler.sendCreateTab(action.Path)
			d.updateStep(fmt.Sprintf("opening %s in new tab", action.Path))

			timeout := time.After(config.Dur(d.handler.Timeouts.SchemaWait))
			for {
				select {
				case newTab := <-d.handler.voiceTabCh:
					if newTab == d.tabID {
						// Stale activation for our own tab — keep waiting.
						log.Printf("Doer [tab %d]: ignoring voiceTabCh for own tab", d.tabID)
						continue
					}
					log.Printf("Doer [tab %d]: new tab %d appeared for %s, waiting on schema", d.tabID, newTab, action.Path)
					newSess := d.handler.getSession(newTab)
					select {
					case <-newSess.GetSchemaReady():
						return fmt.Sprintf("Navigated to %s in a new tab. Page is loaded.", action.Path)
					case <-timeout:
						return fmt.Sprintf("Navigated to %s but page load timed out.", action.Path)
					case <-ctx.Done():
						return "Navigation cancelled."
					}
				case <-timeout:
					return fmt.Sprintf("Navigated to %s but page load timed out.", action.Path)
				case <-ctx.Done():
					return "Navigation cancelled."
				}
			}
		}
		// Idempotent: skip reset if already on this URL.
		if d.sess.GetCurrentURL() == action.Path {
			d.updateStep(fmt.Sprintf("already on %s, waiting for schema", action.Path))
			select {
			case <-d.sess.GetSchemaReady():
				return fmt.Sprintf("Already on %s, page is loaded.", action.Path)
			case <-time.After(config.Dur(d.handler.Timeouts.SchemaWait)):
				return fmt.Sprintf("Already on %s but schema is still loading.", action.Path)
			case <-ctx.Done():
				return "Navigation cancelled."
			}
		}
		d.sess.SetCurrentURL(action.Path)
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

		// Backend mode: navigate + capture via BrowserBackend.
		if backend := d.handler.GetBrowserBackend(); backend != nil {
			return d.dispatchGotoViaBackend(ctx, backend, action.Path)
		}

		d.handler.sendGoto(d.tabID, action.Path)
		d.updateStep(fmt.Sprintf("navigating to %s", action.Path))
		log.Printf("Doer [tab %d]: goto %s", d.tabID, action.Path)

		// When starting from tab 0 (no browser connected), the page opens in a
		// new tab. Wait for either tab 0's schema OR the real tab to appear.
		if d.tabID == 0 {
			timeout := time.After(config.Dur(d.handler.Timeouts.SchemaWait))
			select {
			case <-d.sess.GetSchemaReady():
				return fmt.Sprintf("Navigated to %s. Page is loaded.", action.Path)
			case newTab := <-d.handler.voiceTabCh:
				// Real tab appeared — wait on its schema instead.
				log.Printf("Doer [tab 0]: real tab %d appeared, waiting on its schema", newTab)
				newSess := d.handler.getSession(newTab)
				select {
				case <-newSess.GetSchemaReady():
					return fmt.Sprintf("Navigated to %s. Page is loaded.", action.Path)
				case <-timeout:
					return fmt.Sprintf("Navigated to %s but page load timed out.", action.Path)
				case <-ctx.Done():
					return "Navigation cancelled."
				}
			case <-timeout:
				return fmt.Sprintf("Navigated to %s but page load timed out.", action.Path)
			case <-ctx.Done():
				return "Navigation cancelled."
			}
		}

		select {
		case <-d.sess.GetSchemaReady():
			return fmt.Sprintf("Navigated to %s. Page is loaded.", action.Path)
		case <-time.After(config.Dur(d.handler.Timeouts.SchemaWait)):
			return fmt.Sprintf("Navigated to %s but page load timed out.", action.Path)
		case <-ctx.Done():
			return "Navigation cancelled."
		}

	case "browser.rescan":
		// Backend mode: trigger capture directly.
		if backend := d.handler.GetBrowserBackend(); backend != nil {
			return d.dispatchRescanViaBackend(ctx, backend, action.Path)
		}

		if action.Path != "" {
			// Targeted rescan: keep existing engine, zoom into zone.
			d.sess.ResetSchema()
			d.sess.SetRescanPath(action.Path)
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
		case <-time.After(config.Dur(d.handler.Timeouts.SchemaWait)):
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
		case <-time.After(config.Dur(d.handler.Timeouts.SchemaWait)):
			return fmt.Sprintf("Switched to tab %d but schema timed out.", switchTabID)
		case <-ctx.Done():
			return "Tab switch cancelled."
		}

	default:
		// Click/focus/type/enter — dispatch to extension or backend.
		d.updateStep(fmt.Sprintf("performing %s on %s", action.Action, action.Path))

		if backend := d.handler.GetBrowserBackend(); backend != nil {
			return d.dispatchActionViaBackend(ctx, backend, action)
		}

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

// dispatchGotoViaBackend navigates via BrowserBackend and triggers capture.
func (d *Doer) dispatchGotoViaBackend(ctx context.Context, backend BrowserBackend, url string) string {
	type sessionMapper interface {
		SessionForTab(int) string
	}
	var sessionID string
	if sm, ok := backend.(sessionMapper); ok {
		sessionID = sm.SessionForTab(d.tabID)
	}
	if sessionID == "" {
		return "No browser session for this tab."
	}

	d.updateStep(fmt.Sprintf("navigating to %s", url))
	log.Printf("Doer [tab %d]: backend goto %s", d.tabID, url)

	if err := backend.Navigate(ctx, sessionID, url); err != nil {
		log.Printf("Doer [tab %d]: backend navigate failed: %v", d.tabID, err)
		return fmt.Sprintf("Navigation failed: %v", err)
	}

	// Trigger capture via backend path.
	go d.handler.captureGo(ctx, d.tabID, false, "")

	select {
	case <-d.sess.GetSchemaReady():
		return fmt.Sprintf("Navigated to %s. Page is loaded.", url)
	case <-time.After(config.Dur(d.handler.Timeouts.SchemaWait)):
		return fmt.Sprintf("Navigated to %s but page load timed out.", url)
	case <-ctx.Done():
		return "Navigation cancelled."
	}
}

// dispatchRescanViaBackend triggers a capture via BrowserBackend.
func (d *Doer) dispatchRescanViaBackend(ctx context.Context, backend BrowserBackend, zonePath string) string {
	if zonePath == "" {
		// Full rescan: reset engine.
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
		d.updateStep("rescanning full page")
		log.Printf("Doer [tab %d]: backend full rescan", d.tabID)
	} else {
		d.sess.ResetSchema()
		d.sess.SetRescanPath(zonePath)
		d.updateStep(fmt.Sprintf("zooming into %s", zonePath))
		log.Printf("Doer [tab %d]: backend targeted rescan %q", d.tabID, zonePath)
	}

	go d.handler.captureGo(ctx, d.tabID, true, "")

	select {
	case <-d.sess.GetSchemaReady():
		if zonePath != "" {
			return fmt.Sprintf("Zoomed into %s for more detail.", zonePath)
		}
		return "Page rescanned."
	case <-time.After(config.Dur(d.handler.Timeouts.SchemaWait)):
		return "Rescan timed out."
	case <-ctx.Done():
		return "Rescan cancelled."
	}
}

// dispatchActionViaBackend dispatches click/type/enter/focus via BrowserBackend.
func (d *Doer) dispatchActionViaBackend(ctx context.Context, backend BrowserBackend, action *navigator.ActionResult) string {
	type sessionMapper interface {
		SessionForTab(int) string
	}
	var sessionID string
	if sm, ok := backend.(sessionMapper); ok {
		sessionID = sm.SessionForTab(d.tabID)
	}
	if sessionID == "" {
		return "No browser session for this tab."
	}

	if err := backend.ExecuteAction(ctx, sessionID, action.MacheID, action.Action, action.Payload); err != nil {
		log.Printf("Doer [tab %d]: backend action failed: %v", d.tabID, err)
		return fmt.Sprintf("Action failed: %v", err)
	}

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

// scrollViaBackend scrolls the page using the BrowserBackend and updates the engine.
func (d *Doer) scrollViaBackend(ctx context.Context, backend BrowserBackend, direction string) error {
	type sessionMapper interface {
		SessionForTab(int) string
	}
	var sessionID string
	if sm, ok := backend.(sessionMapper); ok {
		sessionID = sm.SessionForTab(d.tabID)
	}
	if sessionID == "" {
		return fmt.Errorf("no browser session for tab %d", d.tabID)
	}

	update, err := backend.Scroll(ctx, sessionID, direction)
	if err != nil {
		return fmt.Errorf("scroll failed: %w", err)
	}

	d.sess.Engine.LoadChildren(update.Summary, update.ResolvedItems)
	d.sess.Navigator.SetViewport(update.ScrollY, update.ScrollHeight, update.ViewportHeight)
	log.Printf("Scroll [backend]: tab %d scroll %s (viewport %.0f/%.0f)",
		d.tabID, direction, update.ScrollY, update.ScrollHeight)

	if !update.ScrollMoved || (direction == "down" && update.AtBottom) {
		if update.AtBottom {
			return navigator.ErrAtBottom
		}
		if update.AtTop {
			return navigator.ErrAtTop
		}
	}
	return nil
}

// wireNavigatorCallbacks sets scroll, progress, and list_tabs callbacks on the
// current session's Navigator. Called at goal start and after cross-tab rebind.
func (d *Doer) wireNavigatorCallbacks(gs *guardrails.GoalState) {
	// Backend mode: scroll via BrowserBackend HTTP instead of extension WebSocket.
	if backend := d.handler.GetBrowserBackend(); backend != nil {
		d.sess.Navigator.SetScrollFunc(func(scrollCtx context.Context, direction string) error {
			d.updateStep(fmt.Sprintf("scrolling %s", direction))
			return d.scrollViaBackend(scrollCtx, backend, direction)
		})
	} else {
		d.sess.Navigator.SetScrollFunc(func(scrollCtx context.Context, direction string) error {
			d.updateStep(fmt.Sprintf("scrolling %s", direction))
			return d.handler.scrollVoice(scrollCtx, d.sess, d.tabID, direction)
		})
	}
	d.sess.Navigator.SetProgressFunc(func(toolName string, args map[string]any) {
		p, _ := args["path"].(string)
		iter, _ := args["_iter"].(string)
		prefix := toolName
		if iter != "" {
			prefix = fmt.Sprintf("[%s] %s", iter, toolName)
		}
		if p != "" {
			d.updateStep(fmt.Sprintf("%s %s", prefix, p))
		} else {
			d.updateStep(prefix)
		}
		// Track tool calls for ref validation guardrail.
		if gs != nil && gs.Enabled {
			gs.RecordAction(0, toolName, p, "")
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

// settleTimeout returns how long to wait for DOM mutations after an interactive action.
// Fast mode uses a shorter timeout for lower latency in live voice sessions.
func (d *Doer) settleTimeout() time.Duration {
	if d.handler.NavSpeed == "fast" {
		return 500 * time.Millisecond
	}
	return actionSettleTimeout
}

func (d *Doer) updateStep(step string) {
	d.state.mu.Lock()
	d.state.Step = step
	d.state.mu.Unlock()
}

func (d *Doer) finishInteraction(ixID string, status InteractionStatus, summary, errStr string) {
	d.state.mu.Lock()
	d.state.Status = status
	d.state.Step = ""
	d.state.Result = &InteractionResult{
		InteractionID: ixID,
		Status:        status,
		Summary:       summary,
		Error:         errStr,
	}
	d.state.mu.Unlock()

	// Move the interaction record to history in the graph.
	if d.sess.Tasks != nil {
		d.sess.Tasks.FinishInteraction(summary)
	}

	log.Printf("Doer [tab %d]: interaction %s finished (status=%s): %s", d.tabID, ixID, status, summary)

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

// parseActionPath splits an action path like "/main/feed_4/_c/4" into
// (zonePath="/main/feed_4", ordinal="4"). Returns ("","") if no _c found.
func parseActionPath(path string) (zonePath, ordinal string) {
	parts := strings.SplitN(path, "/_c/", 2)
	if len(parts) != 2 {
		return "", ""
	}
	zonePath = parts[0]
	ordinal = parts[1]
	// Strip any trailing sub-path from ordinal (e.g. "4/text" → "4").
	if idx := strings.IndexByte(ordinal, '/'); idx >= 0 {
		ordinal = ordinal[:idx]
	}
	return zonePath, ordinal
}

// extractElementText reads the text node for the element at the given action path.
// Returns truncated text (max 50 chars) or "".
func (d *Doer) extractElementText(action *navigator.ActionResult) string {
	textPath := "browser" + action.Path + "/text"
	node, err := d.sess.Composite.GetNode(textPath)
	if err != nil || len(node.Data) == 0 {
		return ""
	}
	text := strings.TrimSpace(string(node.Data))
	if len(text) > 50 {
		text = text[:50]
	}
	return text
}

// recordNavSection persists a successful interactive action as a NavSection
// in the schema cache, so future visits to the same page shape can reuse it.
func (d *Doer) recordNavSection(ix Interaction, action *navigator.ActionResult) {
	if !isInteractiveAction(action.Action) {
		return
	}
	zonePath, ordinal := parseActionPath(action.Path)
	if zonePath == "" {
		return
	}

	url := d.sess.GetCurrentURL()
	key := CacheKey(url)
	if key == "" {
		return
	}

	fp := d.handler.schemas.GetZoneFingerprint(key, zonePath)
	if fp == "" {
		return
	}
	sfp := d.handler.schemas.GetZoneStructuralFP(key, zonePath)

	elemText := d.extractElementText(action)
	goalHash := NormalizeGoalHash(ix.Intent)

	section := NavSection{
		GoalHash:     goalHash,
		ZonePath:     zonePath,
		Fingerprint:  fp,
		StructuralFP: sfp,
		Ordinal:      ordinal,
		ElementText:  elemText,
		Action:       action.Action,
		Payload:      action.Payload,
		RecordedAt:   time.Now().Unix(),
	}

	d.handler.schemas.PutSection(key, section)
	if os.Getenv("XRAY_DEBUG") == "1" {
		log.Printf("Doer [tab %d]: recorded NavSection goal=%s zone=%s ordinal=%s action=%s",
			d.tabID, goalHash, zonePath, ordinal, action.Action)
	}
}

// formatSectionHints renders NavSections as text for injection into the
// Navigator's tree dump, giving it hints about previously successful actions.
func formatSectionHints(sections []NavSection) string {
	if len(sections) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("=== Previously successful actions on this page ===\n")
	for _, s := range sections {
		switch s.Action {
		case "type":
			fmt.Fprintf(&sb, "- In %s: type %q into [%s] %q\n", s.ZonePath, s.Payload, s.Ordinal, s.ElementText)
		default:
			fmt.Fprintf(&sb, "- In %s: %s [%s] %q\n", s.ZonePath, s.Action, s.Ordinal, s.ElementText)
		}
	}
	return sb.String()
}

// buildContinuation creates an enriched intent for the next step in the loop.
// It tells the Navigator what happened and asks it to continue toward the overall task.
func buildContinuation(intent, taskContext string, step int, action *navigator.ActionResult, summary string, gs *guardrails.GoalState) string {
	context := intent
	if taskContext != "" {
		context = taskContext
	}
	base := fmt.Sprintf("[CONTINUATION — Step %d completed]\n"+
		"Completed action: %s (last: %s on %s → %s)\n"+
		"Overall task: %s\n\n"+
		"The page has updated. Focus on the OVERALL TASK, not the completed action. "+
		"If the answer is in the page content, use grep or cat to find it and provide a text answer. "+
		"If more actions are needed, take the next step.\n\n"+
		"COMPLETENESS CHECK: If you have found the specific data requested, you may stop and return the answer. "+
		"Only check pagination if: (a) you haven't found the target yet, or (b) the goal explicitly asks for ALL items. "+
		"When paginating: track visited pages in scratchpad (e.g. 'visited: page 1, 2'). NEVER click Previous or revisit a page you already checked. "+
		"Before answering, cat /interactions/active/scratch to collect findings and avoid duplicates.",
		step+1, intent, action.Action, action.Path, summary, context)

	// Inject guardrail pagination data if available.
	if gs != nil && gs.Enabled {
		if visited := gs.VisitedSummary(); visited != "" {
			base += "\n\n" + visited
		}
	}

	return base
}
