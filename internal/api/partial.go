package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"

	"github.com/gorilla/websocket"
	"github.com/jamesgardner/x-ray/internal/mache"
)

// StaleZoneInfo describes a cached zone that needs regeneration.
type StaleZoneInfo struct {
	ZonePath string
	Bounds   [4]float64
}

// PartialRegenResult contains results for merging into the engine.
type PartialRegenResult struct {
	MergeSchemaJSON  string        // JSON for Engine.MergeSchema()
	UpdatedMounts    []mache.Mount // new mounts to cache via PutZone
	InvalidatedPaths []string      // old zone paths to remove from cache
}

// RegenerateStaleZones regenerates only the stale zones.
// For each stale zone: crop screenshot to zone bounds,
// filter summary to zone region, run Cartographer.
func RegenerateStaleZones(
	ctx context.Context,
	cart SchemaGenerator,
	staleZones []StaleZoneInfo,
	screenshot []byte,
	summary string,
) (*PartialRegenResult, error) {
	if len(staleZones) == 0 {
		return nil, nil
	}

	var allMounts []mache.Mount
	var invalidated []string

	for _, zone := range staleZones {
		// Crop screenshot to zone bounds (unless bounds are zero/degenerate).
		zoneScreenshot := screenshot
		if zone.Bounds != [4]float64{} {
			cropped, err := mache.CropScreenshot(screenshot, zone.Bounds)
			if err != nil {
				log.Printf("Partial regen: crop failed for %s: %v (using full screenshot)", zone.ZonePath, err)
			} else if cropped != nil {
				zoneScreenshot = cropped
			}
		}

		// Filter summary to zone region.
		zoneSummary := summary
		if zone.Bounds != [4]float64{} {
			filtered := mache.FilterSummaryByBounds(summary, zone.Bounds, 0.05)
			if filtered != "" {
				zoneSummary = filtered
			}
		}

		// Run Cartographer for this zone.
		schemaJSON, err := cart.GenerateSchema(ctx, zoneScreenshot, "image/jpeg", zoneSummary)
		if err != nil {
			return nil, fmt.Errorf("cartographer failed for zone %s: %w", zone.ZonePath, err)
		}

		var output mache.CartographerOutput
		if err := json.Unmarshal([]byte(schemaJSON), &output); err != nil {
			return nil, fmt.Errorf("parse cartographer output for zone %s: %w", zone.ZonePath, err)
		}

		allMounts = append(allMounts, output.Mounts...)
		invalidated = append(invalidated, zone.ZonePath)
	}

	// Build a merged schema JSON from all regenerated mounts.
	mergeOutput := mache.CartographerOutput{Mounts: allMounts}
	mergeJSON, err := json.Marshal(mergeOutput)
	if err != nil {
		return nil, fmt.Errorf("marshal merged schema: %w", err)
	}

	return &PartialRegenResult{
		MergeSchemaJSON:  string(mergeJSON),
		UpdatedMounts:    allMounts,
		InvalidatedPaths: invalidated,
	}, nil
}

// attemptPartialRegen tries to regenerate only stale zones. Returns true if successful.
// On failure, returns false so the caller can fall through to full regeneration.
func (h *Handler) attemptPartialRegen(
	ctx context.Context,
	conn *websocket.Conn,
	msg InboundMessage,
	sess *TabSession,
	key, cached string,
	staleZones map[string]string,
) bool {
	h.sendMessage(conn, OutboundMessage{
		Type: MsgStatus, TabID: msg.TabID, Message: fmt.Sprintf("Regenerating %d stale zones...", len(staleZones)), Stage: "cartographer",
	})

	// Build StaleZoneInfo list from the cached schema.
	staleInfos := extractStaleZoneInfos(cached, staleZones)

	// Decode screenshot.
	var screenshotBytes []byte
	if msg.Screenshot != "" {
		var err error
		screenshotBytes, err = base64.StdEncoding.DecodeString(msg.Screenshot)
		if err != nil {
			log.Printf("Partial regen: failed to decode screenshot: %v", err)
			return false
		}
	}

	result, err := RegenerateStaleZones(ctx, h.Cartographer, staleInfos, screenshotBytes, msg.Summary)
	if err != nil {
		log.Printf("Partial regen failed for %q (tab %d): %v", key, msg.TabID, err)
		return false
	}

	// Validate and repair: if zone anchors are hallucinated but children are valid,
	// swap the anchor instead of falling through to expensive full regen.
	if bad := mache.ValidateSchema(result.MergeSchemaJSON, msg.Summary); len(bad) > 0 {
		repaired, count := mache.RepairSchema(result.MergeSchemaJSON, msg.Summary)
		if count > 0 {
			log.Printf("Partial regen: repaired %d hallucinated zone anchors: %v", count, bad)
			result.MergeSchemaJSON = repaired
			// Re-parse updated mounts for cache storage.
			var fixedOutput mache.CartographerOutput
			if err := json.Unmarshal([]byte(repaired), &fixedOutput); err == nil {
				result.UpdatedMounts = fixedOutput.Mounts
			}
		} else {
			log.Printf("Partial regen: hallucinated IDs (unrepairable): %v — falling through to full regen", bad)
			return false
		}
	}

	// MergeSchema with regenerated sub-zones.
	if err := sess.Engine.MergeSchema(result.MergeSchemaJSON); err != nil {
		log.Printf("Partial regen: engine merge failed: %v", err)
		return false
	}

	// Update cache: invalidate old stale zones, store new ones.
	for _, path := range result.InvalidatedPaths {
		h.schemas.InvalidateZone(key, path)
	}
	for _, m := range result.UpdatedMounts {
		h.schemas.PutZone(key, m)
	}

	log.Printf("Partial regen succeeded for %q (tab %d): %d zones regenerated",
		key, msg.TabID, len(result.UpdatedMounts))

	// Reconstruct full schema JSON for the SCHEMA_READY message.
	fullJSON, ok := h.schemas.GetAllZones(key)
	if !ok {
		log.Printf("Partial regen: failed to reconstruct full schema after merge")
		return false
	}

	// Update session URL.
	sess.schemaMu.Lock()
	sess.CurrentURL = msg.URL
	sess.schemaMu.Unlock()

	saveLog("schema", msg.URL, fullJSON)
	saveLog("summary", msg.URL, msg.Summary)

	// Resolve selectors + finalize.
	h.resolveAndFinalize(ctx, conn, sess, msg, fullJSON)
	return true
}

// extractStaleZoneInfos builds StaleZoneInfo entries from the cached schema
// for each stale zone path.
func extractStaleZoneInfos(cachedJSON string, staleZones map[string]string) []StaleZoneInfo {
	var output mache.CartographerOutput
	if err := json.Unmarshal([]byte(cachedJSON), &output); err != nil {
		// Can't parse: return stale zones with zero bounds.
		infos := make([]StaleZoneInfo, 0, len(staleZones))
		for path := range staleZones {
			infos = append(infos, StaleZoneInfo{ZonePath: path})
		}
		return infos
	}

	// Index cached mounts by path.
	mountByPath := make(map[string]mache.Mount, len(output.Mounts))
	for _, m := range output.Mounts {
		mountByPath[m.VirtualPath] = m
	}

	infos := make([]StaleZoneInfo, 0, len(staleZones))
	for path := range staleZones {
		info := StaleZoneInfo{ZonePath: path}
		if m, ok := mountByPath[path]; ok {
			info.Bounds = m.Bounds
		}
		infos = append(infos, info)
	}
	return infos
}
