package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/png" // register PNG decoder for format-agnostic image.Decode
	"log"
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

	// --- Schema cache lookup (bypassed on rescan) ---
	key := CacheKey(msg.URL)
	var schemaJSON string
	var fromCache bool

	if msg.IsRescan {
		log.Printf("Schema CACHE BYPASS (rescan) for %q (tab %d)", key, msg.TabID)
	} else if key != "" {
		if cached, ok := h.schemas.Get(key); ok {
			staleZones := mache.ValidateSchemaZones(cached, msg.Summary)
			if len(staleZones) == 0 {
				// Secondary guard: catch cross-tab cache poisoning by bounds shift.
				// Same mache-ID can map to a different element in a different tab.
				boundsStale := mache.ValidateSchemaBounds(cached, msg.Summary, 0.10)
				if len(boundsStale) > 0 {
					log.Printf("Schema CACHE BOUNDS MISMATCH for %q (tab %d) — %d zones displaced: %v",
						key, msg.TabID, len(boundsStale), boundsStale)
				}
				for path, id := range boundsStale {
					staleZones[path] = id
				}
			}
			if len(staleZones) == 0 {
				schemaJSON = cached
				fromCache = true
				log.Printf("Schema CACHE HIT for %q (tab %d) — skipping Cartographer", key, msg.TabID)
				h.sendMessage(conn, OutboundMessage{
					Type: MsgStatus, TabID: msg.TabID, Message: "Using cached schema", Stage: "cartographer",
				})
			} else {
				// Count total zones in the cached schema.
				totalZones := countCachedZones(cached)

				// Partial regen: only some zones are stale.
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
			log.Printf("Schema CACHE MISS for %q (tab %d)", key, msg.TabID)
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

		// --- Overlay color readback: classify overlay pixels for masking ---
		var overlayMap *OverlayMap
		var decodedImg image.Image
		if len(screenshotBytes) > 0 {
			if img, _, err := image.Decode(bytes.NewReader(screenshotBytes)); err == nil {
				decodedImg = img
				overlayMap = ClassifyOverlay(img, 900)
				log.Printf("Overlay coverage: %.1f%% (tab %d)", overlayMap.CoverageRatio()*100, msg.TabID)
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
				log.Printf("Edge detection: found %d cv regions (tab %d)", len(cvRegions), msg.TabID)
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

		log.Printf("Cartographer generated schema (tab %d) in %s: %s", msg.TabID, time.Since(cartStart), schemaJSON)

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
	sess.Navigator.SetGraph(sess.Composite)

	// Signal that schema is ready — unblocks any waiting handleNavigate or voice tool call.
	sess.SignalSchemaReady()

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
