package api

import (
	"context"
	"log"
	"time"

	"github.com/agentic-research/x-ray/internal/cdp"
	"github.com/gorilla/websocket"
)

// Orchestration timeouts for Go-driven capture.
const (
	summaryTimeout   = 10 * time.Second
	overlayTimeout   = 5 * time.Second
	captureGoTimeout = 30 * time.Second
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
	sess.captureMu.Lock()
	defer sess.captureMu.Unlock()

	ctx, cancel := context.WithTimeout(parentCtx, captureGoTimeout)
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
	case <-time.After(summaryTimeout):
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
	case <-time.After(overlayTimeout):
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
		dCtx, dCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer dCancel()
		_ = h.cdpProxy.Detach(dCtx, tabID)
	}()

	// 3a. Layout metrics.
	pageWidth, pageHeight, err := cdp.LayoutMetrics(ctx, h.cdpProxy, tabID)
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
	clip := cdp.BuildClip(pageWidth, pageHeight, box)
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
		layerMap = cdp.CaptureLayerTree(ctx, h.cdpProxy, tabID, macheToBackend)
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

	log.Printf("captureGo: captured tab %d — %d AX, %d layers, screenshot=%d chars",
		tabID, len(axMap), len(layerMap), len(screenshot))

	// 6. Synthesize InboundMessage and feed into existing handleDOMSnapshot pipeline.
	syntheticMsg := InboundMessage{
		Type:       MsgDOMSnapshot,
		TabID:      tabID,
		URL:        summaryResp.URL,
		Summary:    enrichedSummary,
		Screenshot: screenshot,
		IsRescan:   isRescan,
	}
	h.handleDOMSnapshot(conn, syntheticMsg)
}

// verifyCapture runs the Go CDP capture pipeline in the background alongside the JS path
// and logs differences. The JS output is still used for the actual pipeline; this is diagnostic only.
func (h *Handler) verifyCapture(tabID int, jsMsg InboundMessage) {
	ctx, cancel := context.WithTimeout(context.Background(), captureGoTimeout)
	defer cancel()

	sess := h.getSession(tabID)

	// Wait briefly for the overlay to be removed by JS before we attach CDP.
	time.Sleep(500 * time.Millisecond)

	if err := h.cdpProxy.Attach(ctx, tabID); err != nil {
		log.Printf("Verify: CDP attach failed (tab %d): %v", tabID, err)
		return
	}
	defer func() {
		dCtx, dCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer dCancel()
		_ = h.cdpProxy.Detach(dCtx, tabID)
	}()

	// Run the same CDP steps as captureGo but only compare, don't feed into pipeline.
	pageWidth, pageHeight, err := cdp.LayoutMetrics(ctx, h.cdpProxy, tabID)
	if err != nil {
		log.Printf("Verify: LayoutMetrics failed (tab %d): %v", tabID, err)
		return
	}

	rootNodeID, err := cdp.DocumentRoot(ctx, h.cdpProxy, tabID)
	if err != nil {
		log.Printf("Verify: DocumentRoot failed (tab %d): %v", tabID, err)
		return
	}

	clip := cdp.BuildClip(pageWidth, pageHeight, nil)
	goScreenshot, err := cdp.CaptureScreenshot(ctx, h.cdpProxy, tabID, clip)
	if err != nil {
		log.Printf("Verify: CaptureScreenshot failed (tab %d): %v", tabID, err)
		goScreenshot = ""
	}

	axNodes, axErr := cdp.FullAXTree(ctx, h.cdpProxy, tabID)
	macheToBackend, _ := cdp.MacheBackendMap(ctx, h.cdpProxy, tabID, rootNodeID)

	var goAXCount int
	if axErr == nil && macheToBackend != nil {
		axMap := cdp.JoinAXToMache(axNodes, macheToBackend)
		goAXCount = len(axMap)
	}

	var goLayerCount int
	if macheToBackend != nil {
		layerMap := cdp.CaptureLayerTree(ctx, h.cdpProxy, tabID, macheToBackend)
		goLayerCount = len(layerMap)
		_ = sess // keep linter happy
	}

	// Compare dimensions (screenshot length as proxy for image size).
	jsLen := len(jsMsg.Screenshot)
	goLen := len(goScreenshot)

	log.Printf("Verify (tab %d): JS screenshot=%d chars, Go screenshot=%d chars (delta=%d)",
		tabID, jsLen, goLen, goLen-jsLen)
	log.Printf("Verify (tab %d): Go AX mappings=%d, Go layers=%d",
		tabID, goAXCount, goLayerCount)

	if jsLen > 0 && goLen > 0 {
		ratio := float64(goLen) / float64(jsLen)
		if ratio < 0.8 || ratio > 1.2 {
			log.Printf("Verify WARNING (tab %d): screenshot size mismatch >20%% (ratio=%.2f)", tabID, ratio)
		}
	}
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
