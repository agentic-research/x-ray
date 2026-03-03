package api

import (
	"context"
	"log"
	"strings"
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
	captureOK := false
	defer func() {
		if !captureOK {
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
		return
	}
	defer func() {
		dCtx, dCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer dCancel()
		_ = h.cdpProxy.Detach(dCtx, tabID)
	}()

	// 3a. Layout metrics.
	pageWidth, pageHeight, err := cdp.LayoutMetrics(ctx, h.cdpProxy, tabID, h.CDPMaxHeight)
	if err != nil {
		log.Printf("captureGo: LayoutMetrics failed (tab %d): %v", tabID, err)
		h.sendOverlayCleanup(conn, tabID, sess)
		return
	}

	// 3b. Document root.
	rootNodeID, err := cdp.DocumentRoot(ctx, h.cdpProxy, tabID)
	if err != nil {
		log.Printf("captureGo: DocumentRoot failed (tab %d): %v", tabID, err)
		h.sendOverlayCleanup(conn, tabID, sess)
		return
	}

	// 3c. Element box model (magnifying glass mode).
	var box *cdp.BoxModel
	if targetMacheID != "" {
		box, err = cdp.ElementBoxModel(ctx, h.cdpProxy, tabID, rootNodeID, targetMacheID)
		if err != nil {
			log.Printf("captureGo: ElementBoxModel failed for %s (tab %d): %v — falling back to full page",
				targetMacheID, tabID, err)
		}
	}

	// 3d. Build clip and capture screenshot.
	clip := cdp.BuildClip(pageWidth, pageHeight, box, h.CDPTargetWidth)
	screenshot, err := cdp.CaptureScreenshot(ctx, h.cdpProxy, tabID, clip)
	if err != nil {
		log.Printf("captureGo: CaptureScreenshot failed (tab %d): %v", tabID, err)
		screenshot = "" // proceed with empty screenshot
	}

	// 3e. Full AX tree.
	axNodes, axErr := cdp.FullAXTree(ctx, h.cdpProxy, tabID)
	if axErr != nil {
		log.Printf("captureGo: FullAXTree failed (tab %d): %v — proceeding without AX", tabID, axErr)
	}

	// 3e2. Page text (visible innerText for grep-searchable content).
	pageText, ptErr := cdp.PageText(ctx, h.cdpProxy, tabID)
	if ptErr != nil {
		log.Printf("captureGo: PageText failed (tab %d): %v — proceeding without page text", tabID, ptErr)
	}

	// 3f. Mache → backend node ID mapping.
	macheToBackend, err := cdp.MacheBackendMap(ctx, h.cdpProxy, tabID, rootNodeID)
	if err != nil {
		log.Printf("captureGo: MacheBackendMap failed (tab %d): %v", tabID, err)
		macheToBackend = nil
	}

	// 3g. Join AX to mache IDs.
	var axMap map[string]cdp.AXInfo
	if axErr == nil && macheToBackend != nil {
		axMap = cdp.JoinAXToMache(axNodes, macheToBackend)
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
