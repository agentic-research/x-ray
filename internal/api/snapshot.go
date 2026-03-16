package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // register PNG decoder for format-agnostic image.Decode
	"log"
	"os"
	"time"

	"github.com/agentic-research/x-ray/internal/mache"
	"github.com/gorilla/websocket"
)

func (h *Handler) handleDOMSnapshot(ctx context.Context, conn *websocket.Conn, msg InboundMessage) {
	sess := h.getSession(msg.TabID)

	// Early tab promotion: if the voice Doer is stranded on tab 0 (no extension
	// was connected at voice-start time), unblock it now — before the slow
	// Cartographer call. The Doer just needs to know "goto worked," and the
	// real tab's schema will be ready independently for the next command.
	h.mu.Lock()
	if h.activeVoiceTab == 0 && msg.TabID != 0 {
		h.activeVoiceTab = msg.TabID
		log.Printf("WebSocket: active voice tab promoted to %d", msg.TabID)
		if oldSess, ok := h.sessions[0]; ok {
			oldSess.SignalSchemaReady()
		}
	}
	h.mu.Unlock()

	// Claim a generation number. If another snapshot starts processing while
	// this one is in-flight (e.g., goto fires twice), the stale generation
	// is discarded at apply time to prevent overwriting a newer schema.
	sess.schemaMu.Lock()
	sess.schemaGen++
	myGen := sess.schemaGen
	sess.schemaMu.Unlock()

	// --- Schema cache lookup ---
	// Rescans (after interactive actions) now try the cache first. If zone IDs
	// and bounds still match the fresh capture, the page structure didn't change
	// and we skip the ~2.5s Cartographer rebuild. Only truly stale pages rebuild.
	key := CacheKey(msg.URL)
	var schemaJSON string
	var fromCache bool

	if key != "" {
		if cached, ok := h.schemas.Get(key); ok {
			staleZones := mache.ValidateSchemaZones(cached, msg.Summary)
			forceFull := false
			if len(staleZones) == 0 {
				// Secondary guard: catch cross-tab cache poisoning by bounds shift.
				// Same mache-ID can map to a different element in a different tab.
				boundsStale := mache.ValidateSchemaBounds(cached, msg.Summary, 0.10)
				if len(boundsStale) > 0 {
					// Bounds-only mismatches trigger FULL regen, not partial.
					forceFull = true
					log.Printf("Schema CACHE BOUNDS MISMATCH for %q (tab %d) — %d zones displaced: %v — full regen",
						key, msg.TabID, len(boundsStale), boundsStale)
				}
			}
			if len(staleZones) == 0 && !forceFull {
				schemaJSON = cached
				fromCache = true
				if msg.IsRescan {
					log.Printf("Schema RESCAN CACHE HIT for %q (tab %d) — structure unchanged", key, msg.TabID)
				} else {
					log.Printf("Schema CACHE HIT for %q (tab %d) — skipping Cartographer", key, msg.TabID)
				}
				h.sendMessage(conn, OutboundMessage{
					Type: MsgStatus, TabID: msg.TabID, Message: "Using cached schema", Stage: "cartographer",
				})
			} else if !forceFull {
				// Some zones stale — try partial regen (works for both rescans and normal loads).
				// This is the "magnifying glass" path: only rebuild changed zones.
				totalZones := countCachedZones(cached)
				if totalZones > 0 && len(staleZones) < totalZones {
					log.Printf("Schema cache PARTIAL STALE for %q (tab %d) — %d/%d zones stale: %v",
						key, msg.TabID, len(staleZones), totalZones, staleZones)

					partialResult := h.attemptPartialRegen(ctx, conn, msg, sess, key, cached, staleZones)
					if partialResult {
						return // partial regen succeeded, schema applied
					}
					// Fall through to full regen on failure.
					log.Printf("Schema: partial regen failed for %q (tab %d), falling through to full regen", key, msg.TabID)
				} else {
					log.Printf("Schema cache STALE for %q (tab %d) — %d/%d zones stale: %v",
						key, msg.TabID, len(staleZones), totalZones, staleZones)
				}
			}
		} else {
			if msg.IsRescan {
				log.Printf("Schema RESCAN CACHE MISS for %q (tab %d)", key, msg.TabID)
			} else {
				log.Printf("Schema CACHE MISS for %q (tab %d)", key, msg.TabID)
			}
		}
	}

	// Check if this is a targeted rescan (magnifying glass mode).
	rescanPath := ""
	if msg.IsRescan {
		rescanPath = sess.ConsumeRescanPath()
	}

	if !fromCache {
		h.sendMessage(conn, OutboundMessage{
			Type: MsgStatus, TabID: msg.TabID, Message: "Generating semantic schema...", Stage: "cartographer",
		})

		// Decode screenshot from base64
		var screenshotBytes []byte
		if msg.Screenshot != "" {
			var err error
			screenshotBytes, err = base64.StdEncoding.DecodeString(msg.Screenshot)
			if err != nil {
				log.Printf("Failed to decode screenshot: %v", err)
			}
		}

		// Detect MIME type from magic bytes.
		mimeType := "image/jpeg"
		if len(screenshotBytes) > 4 && screenshotBytes[0] == 0x89 && screenshotBytes[1] == 'P' {
			mimeType = "image/png"
		}

		// Store screenshot for Navigator visual grounding, resized to 768px max
		// for optimal Gemini token cost (258 tokens = 1 tile at ≤768px).
		// Full-res screenshot is kept in screenshotBytes for edge detection below.
		if len(screenshotBytes) > 0 {
			resized := resizeForGemini(screenshotBytes, 768)
			sess.SetScreenshot(resized, "image/jpeg")
		}

		// --- Overlay color readback: classify overlay pixels for masking ---
		var overlayMap *OverlayMap
		var decodedImg image.Image
		var coverageRatio float64
		if len(screenshotBytes) > 0 {
			if img, _, err := image.Decode(bytes.NewReader(screenshotBytes)); err == nil {
				decodedImg = img
				overlayMap = ClassifyOverlay(img, 900)
				coverageRatio = overlayMap.CoverageRatio() * 100
				if os.Getenv("XRAY_DEBUG") == "1" {
					log.Printf("Overlay coverage: %.1f%% (tab %d)", coverageRatio, msg.TabID)
				}
			} else {
				log.Printf("Overlay classification failed (tab %d): %v", msg.TabID, err)
			}
		}

		// --- Canvas edge detection: find UI regions inside <canvas> / WebGL ---
		// Pass decodedImg to avoid re-decoding the screenshot.
		var localCVRegions []EdgeRegion
		if len(screenshotBytes) > 0 {
			existingBounds := parseBounds(msg.Summary)
			cvRegions, annotatedImg, edgeErr := DetectCanvasRegions(screenshotBytes, existingBounds, overlayMap, decodedImg)
			if edgeErr != nil {
				log.Printf("Edge detection failed (tab %d): %v", msg.TabID, edgeErr)
			} else if len(cvRegions) > 0 {
				screenshotBytes = annotatedImg
				localCVRegions = cvRegions
				if os.Getenv("XRAY_DEBUG") == "1" {
					log.Printf("Edge detection: found %d cv regions (tab %d)", len(cvRegions), msg.TabID)
				}
			}
			sess.SetCVRegions(localCVRegions)
		}

		// For targeted rescan, hint the Cartographer to output absolute sub-zone paths.
		cartSummary := msg.Summary

		// Append cv-N entries so the Cartographer sees canvas-detected regions.
		if len(localCVRegions) > 0 {
			for _, r := range localCVRegions {
				cartSummary += fmt.Sprintf(
					"ID: %s | Color: CV | Bounds: [%.3f, %.3f, %.3f, %.3f] | Parent: none | Tag: canvas | Text: \"[CV detected]\" | Path: canvas\n",
					r.ID, r.X, r.Y, r.W, r.H,
				)
			}
		}

		if rescanPath != "" {
			cartSummary = fmt.Sprintf(
				"[FOCUSED RESCAN: You are zoomed into the component at %s. "+
					"Map its internal sub-zones. CRITICAL: Output your virtual_paths as "+
					"absolute paths starting from the component path, e.g. %s/controls, "+
					"%s/progress_bar. Do NOT output bare paths like /controls.]\n\n%s",
				rescanPath, rescanPath, rescanPath, msg.Summary,
			)
			log.Printf("Schema: focused rescan hint for %q (tab %d)", rescanPath, msg.TabID)
		}

		cartStart := time.Now()
		var err error
		schemaJSON, err = h.Cartographer.GenerateSchema(ctx, screenshotBytes, mimeType, cartSummary)
		if err != nil {
			log.Printf("Cartographer failed after %s: %v", time.Since(cartStart), err)
			h.sendMessage(conn, OutboundMessage{
				Type: MsgStatus, TabID: msg.TabID, Message: "Schema generation failed: " + err.Error(), Stage: "error",
			})
			return
		}

		// Count zones simply by parsing CartographerOutput structure
		var tempOutput mache.CartographerOutput
		numZones := 0
		if err := json.Unmarshal([]byte(schemaJSON), &tempOutput); err == nil {
			numZones = len(tempOutput.Mounts)
		}

		log.Printf("Cartographer: built %d zones in %s (tab %d, %d cv regions, %.1f%% overlay)",
			numZones, time.Since(cartStart), msg.TabID, len(localCVRegions), coverageRatio)
		if os.Getenv("XRAY_DEBUG") == "1" {
			log.Printf("Cartographer generated schema (tab %d): %s", msg.TabID, schemaJSON)
		}

		// Validate and repair: if zone anchors are hallucinated but children are valid,
		// swap the anchor instead of regenerating (avoids the integer-extrapolation trap).
		if bad := mache.ValidateSchema(schemaJSON, msg.Summary); len(bad) > 0 {
			repaired, count := mache.RepairSchema(schemaJSON, msg.Summary)
			if count > 0 {
				log.Printf("Cartographer: repaired %d hallucinated zone anchors: %v", count, bad)
				schemaJSON = repaired
			} else {
				// No repairable zones — fall back to regeneration.
				log.Printf("Cartographer hallucinated IDs (unrepairable): %v — regenerating", bad)
				h.sendMessage(conn, OutboundMessage{
					Type: MsgStatus, TabID: msg.TabID, Message: "Schema had invalid IDs, retrying...", Stage: "cartographer",
				})
				schemaJSON, err = h.Cartographer.GenerateSchema(ctx, screenshotBytes, mimeType, cartSummary)
				if err != nil {
					log.Printf("Cartographer retry failed: %v", err)
					h.sendMessage(conn, OutboundMessage{
						Type: MsgStatus, TabID: msg.TabID, Message: "Schema retry failed: " + err.Error(), Stage: "error",
					})
					return
				}
				log.Printf("Cartographer retry schema: %s", schemaJSON)
				if bad2 := mache.ValidateSchema(schemaJSON, msg.Summary); len(bad2) > 0 {
					log.Printf("Cartographer still hallucinating after retry: %v", bad2)
				}
			}
		}

		// Cache the validated schema using per-zone entries.
		if key != "" {
			var output mache.CartographerOutput
			if err := json.Unmarshal([]byte(schemaJSON), &output); err == nil {
				if rescanPath != "" {
					// Targeted rescan: store sub-zones, invalidate parent.
					for _, m := range output.Mounts {
						h.schemas.PutZone(key, m)
					}
					h.schemas.InvalidateZone(key, rescanPath)
					log.Printf("Schema cached (rescan) for %q: %d sub-zones under %s", key, len(output.Mounts), rescanPath)
				} else {
					// Full scan: replace all zones.
					h.schemas.PutZones(key, output.Mounts)
					log.Printf("Schema cached for %q: %d zones", key, len(output.Mounts))
				}
			} else {
				// Fallback: store as v1 monolithic blob.
				h.schemas.Put(key, schemaJSON)
				log.Printf("Schema cached (v1 fallback) for %q", key)
			}
		}
	}

	// Generation guard: discard if a newer snapshot started processing.
	sess.schemaMu.Lock()
	stale := sess.schemaGen != myGen
	if !stale {
		sess.CurrentURL = msg.URL
	}
	sess.schemaMu.Unlock()
	if stale {
		log.Printf("Schema: generation %d superseded (tab %d), discarding stale Cartographer result", myGen, msg.TabID)
		return
	}

	// Save schema to disk for reference
	saveLog("schema", msg.URL, schemaJSON)
	saveLog("summary", msg.URL, msg.Summary)

	if rescanPath != "" {
		// Targeted rescan: graft new sub-zones into existing filesystem.
		if err := sess.Engine.MergeSchema(schemaJSON); err != nil {
			log.Printf("Engine merge failed: %v", err)
			h.sendMessage(conn, OutboundMessage{
				Type: MsgStatus, TabID: msg.TabID, Message: "Engine merge failed: " + err.Error(), Stage: "error",
			})
			return
		}
		log.Printf("Schema: merged sub-zones under %q (tab %d)", rescanPath, msg.TabID)
	} else {
		if err := sess.Engine.ApplySchema(schemaJSON); err != nil {
			log.Printf("Engine apply failed: %v", err)
			h.sendMessage(conn, OutboundMessage{
				Type: MsgStatus, TabID: msg.TabID, Message: "Engine failed: " + err.Error(), Stage: "error",
			})
			return
		}
	}

	// Resolve CSS selectors via the browser so LoadChildren has the full item list.
	// This is the same mechanism used after scroll, but applied on initial load too.
	h.resolveAndFinalize(ctx, conn, sess, msg, schemaJSON)
}

// resolveAndFinalize resolves CSS selectors, loads children, signals schema ready,
// and sends the SCHEMA_READY message. Used by both the main and partial regen paths.
func (h *Handler) resolveAndFinalize(ctx context.Context, conn *websocket.Conn, sess *TabSession, msg InboundMessage, schemaJSON string) {
	var resolvedItems map[string][]string
	if selectors := sess.Engine.ZoneSelectors(); len(selectors) > 0 {
		// Drain any stale response from a previous snapshot.
		select {
		case <-sess.SelectorsResolved:
		default:
		}
		h.sendMessage(conn, OutboundMessage{
			Type: MsgResolveSelectors, TabID: msg.TabID, Selectors: selectors,
		})
		select {
		case resolvedItems = <-sess.SelectorsResolved:
			log.Printf("Schema: resolved selectors for %d zones (tab %d)", len(resolvedItems), msg.TabID)
		case <-time.After(5 * time.Second):
			log.Printf("Schema: selector resolution timed out for tab %d, using static primary_items", msg.TabID)
		case <-ctx.Done():
			return
		}
	}

	sess.Engine.LoadChildren(msg.Summary, resolvedItems)
	if msg.PageText != "" {
		sess.Engine.SetPageText(msg.PageText)
	}
	sess.Navigator.SetGraph(sess.Composite)

	// Signal that schema is ready — unblocks any waiting handleNavigate or voice tool call.
	sess.SignalSchemaReady()

	// Push screenshot as a video frame to the Talker's Live session (if screen sharing is enabled).
	// Non-blocking: drops the frame if the voice goroutine hasn't consumed the last one.
	if h.videoEnabled.Load() {
		if img, mime := sess.GetScreenshot(); len(img) > 0 {
			select {
			case h.videoFrameCh <- videoFrame{Data: img, MIME: mime}:
			default:
			}
		}
	}

	var schema any
	if err := json.Unmarshal([]byte(schemaJSON), &schema); err != nil {
		log.Printf("Failed to parse schema JSON for response: %v", err)
	}
	h.sendMessage(conn, OutboundMessage{Type: MsgSchemaReady, TabID: msg.TabID, Schema: schema})
}

// countCachedZones returns the number of zones in a cached schema JSON.
func countCachedZones(schemaJSON string) int {
	var output mache.CartographerOutput
	if err := json.Unmarshal([]byte(schemaJSON), &output); err != nil {
		return 0
	}
	return len(output.Mounts)
}

// resizeForGemini downscales a screenshot to maxDim (largest side) and
// re-encodes as JPEG for minimal token cost. At ≤768px both dimensions,
// Gemini uses a single 258-token tile — optimal for speed.
func resizeForGemini(data []byte, maxDim int) []byte {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return data
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= maxDim && h <= maxDim {
		// Already small enough — just re-encode as JPEG for size.
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
			return data
		}
		return buf.Bytes()
	}

	// Scale so largest side = maxDim.
	scale := float64(maxDim) / float64(max(w, h))
	newW := int(float64(w) * scale)
	newH := int(float64(h) * scale)
	if newW < 1 {
		newW = 1
	}
	if newH < 1 {
		newH = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
	for y := range newH {
		srcY := y * h / newH
		for x := range newW {
			srcX := x * w / newW
			dst.Set(x, y, img.At(bounds.Min.X+srcX, bounds.Min.Y+srcY))
		}
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 80}); err != nil {
		return data
	}
	log.Printf("resizeForGemini: %dx%d → %dx%d (%d → %d bytes)",
		w, h, newW, newH, len(data), buf.Len())
	return buf.Bytes()
}
