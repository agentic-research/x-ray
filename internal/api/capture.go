package api

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/agentic-research/x-ray/internal/cdp"
	"github.com/agentic-research/x-ray/internal/config"
	"github.com/agentic-research/x-ray/internal/mache"
	"github.com/gorilla/websocket"
)

// captureGo is the Go-driven replacement for JS captureAndSend().
// It orchestrates: summary request → overlay → CDP screenshot + AX + layers → enrich → feed DOM_SNAPSHOT.
// Serialized per-tab via captureMu to prevent concurrent goroutines from racing on shared channels
// (wrong summary paired with wrong screenshot).
func (h *Handler) captureGo(parentCtx context.Context, tabID int, isRescan bool, targetMacheID string) {
	// Backend mode: route through BrowserBackend (CF Worker) instead of extension.
	h.mu.Lock()
	backend := h.backend
	h.mu.Unlock()
	if backend != nil {
		h.captureViaBackend(parentCtx, tabID, isRescan, backend)
		return
	}
	h.captureGoRetry(parentCtx, tabID, isRescan, targetMacheID, 0)
}

const maxCaptureRetries = 3

func (h *Handler) captureGoRetry(parentCtx context.Context, tabID int, isRescan bool, targetMacheID string, attempt int) {
	sess := h.getSession(tabID)

	// Serialize captures per tab. If a second PAGE_READY arrives while we're mid-capture,
	// it blocks here until the first finishes. This prevents channel cross-talk where
	// goroutine B steals goroutine A's SUMMARY_RESPONSE from the shared SummaryCh.
	// Using a channel semaphore instead of sync.Mutex so we can respect context cancellation:
	// if the parent context is cancelled while waiting in line, we exit cleanly.
	select {
	case sess.captureSem <- struct{}{}:
		defer func() { <-sess.captureSem }()
	case <-parentCtx.Done():
		return
	}

	// On any early-return failure, unblock Planner/Doer waiters so they don't
	// hang for 30s on GetSchemaReady(). The Planner already handles the "no
	// schema" case gracefully ("proceeding anyway").
	// Exception: chrome-extension:// failures are transient (new-tab page) —
	// don't signal ready, schedule a retry instead.
	captureOK := false
	transientFailure := false
	defer func() {
		if !captureOK && !transientFailure {
			sess.SignalSchemaReady()
		}
	}()

	ctx, cancel := context.WithTimeout(parentCtx, config.Dur(h.Timeouts.Capture))
	defer cancel()

	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()
	if conn == nil {
		log.Printf("captureGo: no extension connection (tab %d)", tabID)
		return
	}

	// 1. REQUEST_SUMMARY → wait for SUMMARY_RESPONSE.
	// Drain any stale summary response.
	select {
	case <-sess.SummaryCh:
	default:
	}

	h.sendMessage(conn, OutboundMessage{Type: MsgRequestSummary, TabID: tabID})

	var summaryResp SummaryResponse
	select {
	case summaryResp = <-sess.SummaryCh:
	case <-time.After(config.Dur(h.Timeouts.Summary)):
		log.Printf("captureGo: summary request timed out (tab %d)", tabID)
		h.sendMessage(conn, OutboundMessage{
			Type: MsgStatus, TabID: tabID, Message: "Summary request timed out", Stage: "error",
		})
		return
	case <-ctx.Done():
		return
	}

	if summaryResp.Summary == "" {
		log.Printf("captureGo: empty summary from content script (tab %d)", tabID)
		return
	}

	// Skip capture for non-http pages (e.g., chrome-extension:// briefly shown on new tabs).
	// Treat as transient — schedule retry instead of signaling schema ready with empty tree.
	if summaryResp.URL != "" && !strings.HasPrefix(summaryResp.URL, "http://") && !strings.HasPrefix(summaryResp.URL, "https://") {
		log.Printf("captureGo: skipping non-http URL %q (tab %d)", summaryResp.URL, tabID)
		if attempt < maxCaptureRetries {
			transientFailure = true
			log.Printf("captureGo: non-http URL — retry %d/%d in 2s (tab %d)", attempt+1, maxCaptureRetries, tabID)
			go func() {
				select {
				case <-time.After(2 * time.Second):
				case <-parentCtx.Done():
					return
				}
				h.captureGoRetry(parentCtx, tabID, isRescan, targetMacheID, attempt+1)
			}()
		}
		return
	}

	// 2. DRAW_OVERLAY_CMD → wait for OVERLAY_DRAWN.
	select {
	case <-sess.OverlayDrawnCh:
	default:
	}

	h.sendMessage(conn, OutboundMessage{Type: MsgDrawOverlayCmd, TabID: tabID})

	select {
	case <-sess.OverlayDrawnCh:
	case <-time.After(config.Dur(h.Timeouts.Overlay)):
		log.Printf("captureGo: overlay draw timed out (tab %d), continuing", tabID)
	case <-ctx.Done():
		return
	}

	// 3. CDP Attach → screenshot + AX + layers → Detach.
	if err := h.cdpProxy.Attach(ctx, tabID); err != nil {
		log.Printf("captureGo: CDP attach failed (tab %d): %v", tabID, err)
		h.sendMessage(conn, OutboundMessage{
			Type: MsgStatus, TabID: tabID, Message: "CDP attach failed: " + err.Error(), Stage: "error",
		})
		// Still remove overlay and draw human overlay.
		h.sendOverlayCleanup(conn, tabID, sess)

		// chrome-extension:// failures are transient — Chrome briefly shows its
		// new-tab page before navigating to the real URL. Don't signal schema
		// ready (which would let Planner/Doer proceed with an empty tree).
		// Instead, schedule a retry: the real page will load shortly.
		errMsg := err.Error()
		if strings.Contains(errMsg, "chrome-extension://") || strings.Contains(errMsg, "chrome://") {
			if attempt < maxCaptureRetries {
				transientFailure = true
				log.Printf("captureGo: transient extension URL failure — retry %d/%d in 2s (tab %d)", attempt+1, maxCaptureRetries, tabID)
				go func() {
					select {
					case <-time.After(2 * time.Second):
					case <-parentCtx.Done():
						return
					}
					h.captureGoRetry(parentCtx, tabID, isRescan, targetMacheID, attempt+1)
				}()
			} else {
				log.Printf("captureGo: exhausted %d retries for transient URL (tab %d) — signaling ready with empty tree", maxCaptureRetries, tabID)
			}
		}
		return
	}
	defer func() {
		dCtx, dCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer dCancel()
		_ = h.cdpProxy.Detach(dCtx, tabID)
	}()

	// Phase 1: independent CDP calls in parallel.
	var (
		pageWidth, pageHeight float64
		layoutErr             error
		rootNodeID            int
		rootErr               error
		pageText              string
		ptErr                 error
	)
	// AX tree result captured via closure (unexported type).
	axResult := cdp.CaptureAXAsync(ctx, h.cdpProxy, tabID)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		pageWidth, pageHeight, layoutErr = cdp.LayoutMetrics(ctx, h.cdpProxy, tabID, h.CDPMaxHeight)
	}()
	go func() {
		defer wg.Done()
		rootNodeID, rootErr = cdp.DocumentRoot(ctx, h.cdpProxy, tabID)
	}()
	go func() {
		defer wg.Done()
		pageText, ptErr = cdp.PageText(ctx, h.cdpProxy, tabID)
	}()
	wg.Wait()
	if layoutErr != nil {
		log.Printf("captureGo: LayoutMetrics failed (tab %d): %v", tabID, layoutErr)
		h.sendOverlayCleanup(conn, tabID, sess)
		return
	}
	if rootErr != nil {
		log.Printf("captureGo: DocumentRoot failed (tab %d): %v", tabID, rootErr)
		h.sendOverlayCleanup(conn, tabID, sess)
		return
	}
	if ptErr != nil {
		log.Printf("captureGo: PageText failed (tab %d): %v — proceeding without page text", tabID, ptErr)
	}

	// Phase 2: depends on Phase 1 results.

	// Element box model (magnifying glass mode).
	var box *cdp.BoxModel
	if targetMacheID != "" {
		var boxErr error
		box, boxErr = cdp.ElementBoxModel(ctx, h.cdpProxy, tabID, rootNodeID, targetMacheID)
		if boxErr != nil {
			log.Printf("captureGo: ElementBoxModel failed for %s (tab %d): %v — falling back to full page",
				targetMacheID, tabID, boxErr)
		}
	}

	// Build clip and capture screenshot.
	clip := cdp.BuildClip(pageWidth, pageHeight, box, h.CDPTargetWidth)
	screenshot, err := cdp.CaptureScreenshot(ctx, h.cdpProxy, tabID, clip)
	if err != nil {
		log.Printf("captureGo: CaptureScreenshot failed (tab %d): %v", tabID, err)
		screenshot = "" // proceed with empty screenshot
	}

	// 3f. Mache → backend node ID mapping.
	macheToBackend, err := cdp.MacheBackendMap(ctx, h.cdpProxy, tabID, rootNodeID)
	if err != nil {
		log.Printf("captureGo: MacheBackendMap failed (tab %d): %v", tabID, err)
		macheToBackend = nil
	}

	// 3g. Join AX to mache IDs.
	var axMap map[string]cdp.AXInfo
	if macheToBackend != nil {
		axMap = axResult.JoinToMache(macheToBackend)
	}

	// 3h. Layer tree.
	var layerMap map[string]cdp.LayerInfo
	if macheToBackend != nil {
		layerMap = cdp.CaptureLayerTree(ctx, h.cdpProxy, tabID, macheToBackend, config.Dur(h.Timeouts.LayerTree))
	}

	// 4. Remove machine overlay, draw human overlay.
	h.sendOverlayCleanup(conn, tabID, sess)

	// 5. Enrich summary with AX + layers.
	enrichedSummary := summaryResp.Summary
	if len(axMap) > 0 {
		enrichedSummary = cdp.EnrichSummaryWithAX(enrichedSummary, axMap)
	}
	if len(layerMap) > 0 {
		enrichedSummary = cdp.EnrichSummaryWithLayers(enrichedSummary, layerMap)
	}

	// 5b. On targeted rescan, filter summary to elements intersecting the clip.
	// This avoids sending 1,500 elements to the Cartographer when the screenshot
	// only shows a small cropped region.
	if box != nil && pageWidth > 0 && pageHeight > 0 {
		before := strings.Count(enrichedSummary, "\n")
		enrichedSummary = filterSummaryByClip(enrichedSummary, clip, pageWidth, pageHeight)
		after := strings.Count(enrichedSummary, "\n")
		log.Printf("captureGo: rescan filter kept %d/%d summary lines (tab %d)", after, before, tabID)
	}

	log.Printf("captureGo: captured tab %d — %d AX, %d layers, screenshot=%d chars",
		tabID, len(axMap), len(layerMap), len(screenshot))

	// 6. Synthesize InboundMessage and feed into existing handleDOMSnapshot pipeline.
	captureOK = true
	syntheticMsg := InboundMessage{
		Type:       MsgDOMSnapshot,
		TabID:      tabID,
		URL:        summaryResp.URL,
		Summary:    enrichedSummary,
		Screenshot: screenshot,
		IsRescan:   isRescan,
		PageText:   pageText,
	}
	h.handleDOMSnapshot(parentCtx, conn, syntheticMsg)
}

// filterSummaryByClip keeps only summary lines whose Bounds intersect the
// normalized clip rectangle. Lines without parseable bounds are kept as-is.
func filterSummaryByClip(summary string, clip cdp.ScreenshotClip, pageW, pageH float64) string {
	// Normalize clip to [0,1] range.
	clipBounds := [4]float64{
		clip.X / pageW,
		clip.Y / pageH,
		clip.Width / pageW,
		clip.Height / pageH,
	}

	lines := strings.Split(summary, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		elBounds, ok := mache.ParseBoundsFromLine(line)
		if !ok {
			// No parseable bounds — keep the line (could be a header or metadata).
			kept = append(kept, line)
			continue
		}
		if mache.BoundsOverlap(clipBounds, elBounds) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// captureViaBackend implements the capture pipeline using a BrowserBackend (e.g., CF Worker).
// It replaces the extension WebSocket orchestration with HTTP calls to the backend.
func (h *Handler) captureViaBackend(parentCtx context.Context, tabID int, isRescan bool, backend BrowserBackend) {
	sess := h.getSession(tabID)

	select {
	case sess.captureSem <- struct{}{}:
		defer func() { <-sess.captureSem }()
	case <-parentCtx.Done():
		return
	}

	captureOK := false
	defer func() {
		if !captureOK {
			sess.SignalSchemaReady()
		}
	}()

	ctx, cancel := context.WithTimeout(parentCtx, config.Dur(h.Timeouts.Capture))
	defer cancel()

	// Resolve CF session ID for this tab.
	type sessionMapper interface {
		SessionForTab(int) string
	}
	var sessionID string
	if sm, ok := backend.(sessionMapper); ok {
		sessionID = sm.SessionForTab(tabID)
	}
	if sessionID == "" {
		log.Printf("captureViaBackend: no session for tab %d", tabID)
		return
	}

	// 1. Request summary from backend.
	summaryResp, err := backend.RequestSummary(ctx, sessionID)
	if err != nil {
		log.Printf("captureViaBackend: summary failed (tab %d): %v", tabID, err)
		return
	}
	if summaryResp.Summary == "" {
		log.Printf("captureViaBackend: empty summary (tab %d)", tabID)
		return
	}

	// 2. Layout metrics.
	pageWidth, pageHeight, err := backend.LayoutMetrics(ctx, sessionID)
	if err != nil {
		log.Printf("captureViaBackend: layout metrics failed (tab %d): %v", tabID, err)
		return
	}

	// 3. Screenshot.
	screenshot, err := backend.CaptureScreenshot(ctx, sessionID, pageWidth, pageHeight)
	if err != nil {
		log.Printf("captureViaBackend: screenshot failed (tab %d): %v", tabID, err)
		screenshot = ""
	}

	// 4. AX tree enrichment (backend joins AX to mache IDs and returns enriched summary).
	enrichedSummary := summaryResp.Summary
	axEnriched, axErr := backend.FullAXTree(ctx, sessionID)
	if axErr != nil {
		log.Printf("captureViaBackend: AX tree failed (tab %d): %v — proceeding without", tabID, axErr)
	} else if axEnriched != "" {
		enrichedSummary = axEnriched
	}

	// 5. Page text.
	pageText, ptErr := backend.PageText(ctx, sessionID)
	if ptErr != nil {
		log.Printf("captureViaBackend: page text failed (tab %d): %v", tabID, ptErr)
	}

	log.Printf("captureViaBackend: captured tab %d — screenshot=%d chars, summary=%d lines",
		tabID, len(screenshot), strings.Count(enrichedSummary, "\n"))

	// 6. Feed into existing handleDOMSnapshot pipeline.
	captureOK = true
	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()
	syntheticMsg := InboundMessage{
		Type:       MsgDOMSnapshot,
		TabID:      tabID,
		URL:        summaryResp.URL,
		Summary:    enrichedSummary,
		Screenshot: screenshot,
		IsRescan:   isRescan,
		PageText:   pageText,
	}
	h.handleDOMSnapshot(parentCtx, conn, syntheticMsg)
}

// sendOverlayCleanup removes the machine overlay and draws the human-friendly overlay.
func (h *Handler) sendOverlayCleanup(conn *websocket.Conn, tabID int, sess *TabSession) {
	// Remove machine overlay.
	select {
	case <-sess.OverlayRemovedCh:
	default:
	}
	h.sendMessage(conn, OutboundMessage{Type: MsgRemoveOverlayCmd, TabID: tabID})

	// Draw human-friendly overlay.
	h.sendMessage(conn, OutboundMessage{Type: MsgDrawHumanOverlay, TabID: tabID})
}
