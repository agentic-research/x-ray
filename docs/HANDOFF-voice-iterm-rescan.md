# HANDOFF: Voice + iTerm + Targeted Rescan

**Date:** 2026-03-04
**Commits:** `d1dfd9e` through `1631c84` (4 commits)
**Context:** Gemini Live Agent Challenge hackathon, deadline 2026-03-16

---

## What Was Done

### 1. Schema-Ready Gate (d1dfd9e)
The Doer now waits for Cartographer schema before the Navigator's first tool-use loop. Prevents hallucination on fresh page loads (Reddit demo bug: "The future of AI" instead of actual 2nd post). Gate is skipped for tab 0 (iTerm-only sessions).

### 2. JSON Function Call Parsing Overhaul (1ff3ea7)
Replaced brittle dual-regex parsing (`gemmaFnCallRe`, `gemmaFnCallNoParamsRe`) with `json.Unmarshal` into `llmFunctionCall` struct. Handles: no params (`{"name":"iterm.new_window"}`), empty params `{}`, nested JSON payloads, both `"parameters"` and `"arguments"` keys, markdown-wrapped JSON. 5 tests in `model_test.go`.

### 3. iTerm Voice Flow (1ff3ea7 + 1631c84)
- `resolveDoer()` now allows tab 0 when `termBridge != nil`
- Schema gate skipped when `tabID == 0` (no browser = no schema to wait for)
- `active_session` blind spot race fixed: after `new_window`/`new_tab`, force-set `b.active = newSession` before rebuild (iTerm may report agentd terminal as focused)
- Tool result messages now guide Navigator to `/iterm/active_session`

### 4. Schema Cache for Rescans (47dc38d)
Previously: every click → `sendRescan(tabID, "")` → `Schema CACHE BYPASS (rescan)` → full Cartographer rebuild (~2.5s). Now: rescans try cache validation first (zone IDs + bounds check). Cache hit = skip rebuild. Partial stale = rebuild only changed zones via `attemptPartialRegen`. Full stale = full rebuild.

### 5. Pagination Loop Prevention (47dc38d)
COMPLETENESS CHECK prompts updated in Doer (`buildContinuation`), Navigator (`agent.go`), and Planner. Key changes:
- Agent has permission to stop when target data is found
- Must track visited pages in scratchpad (`visited: page 1, 2`)
- Never click Previous or revisit a checked page
- Must `cat /tasks/active/scratch` before writing to avoid duplicates

### 6. Result Validation with Escape Hatch (d1dfd9e)
Doer retries weak Navigator responses ("I couldn't find...") but allows definitive conclusions through ("does not exist", "DONE:", "not present on").

---

## What Still Needs Doing

### HIGH: Targeted Auto-Rescan After Clicks (Lines 303 + 338 in doer.go)

**The gap:** After interactive actions (click/type), the Doer calls `sendRescan(d.tabID, "")` — always full-page. The entire magnifying glass pipeline is built and working but not wired into auto-rescans.

**The infrastructure (all exists, all tested):**
- `parseActionPath(action.Path)` → extracts zone path (e.g., `/browser/main/feed`)
- `sess.SetRescanPath(zonePath)` → tells `handleDOMSnapshot` this is a targeted rescan
- `sendRescan(tabID, action.MacheID)` → extension crops screenshot to element + 50px padding
- `captureGo` → `BuildClip(box)` → `filterSummaryByClip()` → focused summary
- `handleDOMSnapshot` → `[FOCUSED RESCAN: ...]` hint to Cartographer → `MergeSchema` (graft, not replace)
- Per-zone caching: `PutZone()` / `InvalidateZone()`

**The fix (pseudocode for doer.go:303 and 338):**
```go
// Instead of:
d.handler.sendRescan(d.tabID, "")

// Do:
zonePath, _ := parseActionPath(action.Path)
if zonePath != "" && action.MacheID != "" {
    d.sess.SetRescanPath(zonePath)
    d.handler.sendRescan(d.tabID, action.MacheID)
} else {
    d.handler.sendRescan(d.tabID, "")
}
```

**Caveat:** Clicking "Next" (footer zone) changes the main content zone, not the footer. Targeted rescan would magnify the wrong zone. The partial regen path (now enabled for rescans in snapshot.go) handles this case correctly — it identifies which zones are stale via cache validation and only rebuilds those. The targeted rescan is best for cases like expanding a dropdown or scrolling within a single zone.

**Recommendation:** Wire up targeted rescan for DOM mutation rescans (line 303) where the mutation is near the clicked element. For the settle timeout path (line 338), keep full rescan since the lack of a mutation signal means we don't know what changed.

---

### HIGH: SLM Latency for iTerm Commands

**Problem:** `iterm.new_window` is a simple bridge API call, but it routes through the 9B SLM (qwen3.5:9b) which takes ~11s per inference. The full round-trip for "open a new terminal and type hello world" is ~25s.

**Options (pick one):**
1. **Direct dispatch for known iTerm tools** — In the Doer, pattern-match goals like "open new terminal" / "type X in terminal" and call the bridge directly without the Navigator/SLM. Falls back to SLM for ambiguous commands.
2. **Lighter model for iTerm-only sessions** — When `tabID == 0` and only iTerm is mounted, use a smaller/faster model or even a rule-based dispatcher.
3. **Pre-parse in Talker** — The Gemini Talker already understands the intent. Instead of `issue_command("open terminal")`, it could emit a structured `iterm_command` tool call that the Doer dispatches directly.

**Key file:** `internal/api/doer.go:executeGoal()` — the SLM call is at `d.sess.Navigator.HandleIntent()`.

---

### MEDIUM: Focus Mount Underutilized

**What it is:** `focus/` is a dynamic router (`internal/focus/focus.go`) that maps to `browser/` or `iterm/` based on the frontmost macOS app (via `osascript`). Mounted at `internal/api/websocket.go:128`.

**Current state:** The Navigator's system prompt mentions it (agent.go:709) but the agent rarely uses it. It could be valuable for:
- "What am I looking at?" → `ls focus/` → see browser or terminal content
- "Click this" → `act focus/main/feed/_c/1 click` → works regardless of which app

**What's needed:** Maybe nothing code-wise — the prompt already mentions it. But in practice, the LLM gravitates toward explicit `/browser/` or `/iterm/` paths. Could add a rule: "When the user doesn't specify browser or terminal, use /focus/ to auto-detect."

---

### MEDIUM: iTerm Session Blind Spot Improvements

**Current:** The bridge filters out `selfSession` (agentd's own terminal) to prevent the agent from reading/writing to its own process. After `new_window`, active is force-set to the new session.

**Remaining issues:**
- If the user manually switches focus back to the agentd terminal, `active_session/` goes empty again (next `reconcileSessions` resets active to selfSession → "")
- No feedback when the agent accidentally targets its own session
- The 500ms sleep after `CreateTab` is fragile — could poll for session appearance instead

---

### LOW: URL Fragment (#hash) Handling

`CacheKey()` in `schemacache.go` strips URL fragments. This is correct (same page content). However, some SPAs use fragments for routing (`#/page/2`). If this becomes an issue, `CacheKey` would need to include fragments for specific sites.

---

## Key Files

| File | What | Lines of Interest |
|------|------|-------------------|
| `internal/api/doer.go` | Doer goal loop, schema gate, rescan trigger | 189 (schema gate), 303/338 (rescan), 566 (parseActionPath), 657 (buildContinuation) |
| `internal/api/snapshot.go` | Schema cache lookup, partial regen, targeted rescan | 44-104 (cache logic), 106-110 (rescan path), 178-193 (focused hint) |
| `internal/api/partial.go` | Partial zone regeneration | Entire file (~212 lines) |
| `internal/api/voice.go` | Talker/Doer voice pipeline, resolveDoer | 394 (resolveDoer browser), 735 (resolveDoer native) |
| `internal/api/schemacache.go` | Per-zone cache, CacheKey, zone validation | 112-133 (CacheKey), 731 (appendUniqueStr) |
| `internal/navigator/model.go` | JSON function call parsing (struct-based) | jsonBlockRe, llmFunctionCall struct, parseResponse() |
| `internal/navigator/agent.go` | Navigator system prompt (scratchpad, pagination, focus) | 696-814 |
| `internal/navigator/tools.go` | iTerm tools (new_window, new_tab, rescan) | 450-494 |
| `internal/iterm/bridge.go` | iTerm2 bridge (Act, reconcile, blind spot) | 321-353 (new_window/tab), 157-216 (reconcileSessions) |
| `internal/iterm/schema.go` | Graph projection for /iterm/ mount | ProjectToGraph, active_session alias |
| `internal/focus/focus.go` | Dynamic app router for /focus/ mount | Router.resolvePath() |
| `internal/api/planner.go` | Planner completeness check | 117-122 |

## Tests

| File | Coverage |
|------|----------|
| `internal/navigator/model_test.go` | 5 JSON parsing tests (no params, empty, nested, Qwen args, markdown) |
| `internal/api/doer_test.go` | 4 new tests (tab 0 schema skip, weak retry, definitive skip, progress) + 14 existing |
| `internal/iterm/*_test.go` | Graph projection, session resolution, ANSI stripping |

## Architecture Quick Reference

```
User speaks → Gemini Live (Talker) → issue_command tool
    → resolveDoer(tabID) → Doer.Submit(goal)
        → Doer.executeGoal():
            1. Schema gate (wait for Cartographer, skip for tab 0)
            2. Navigator.HandleIntent() → SLM (qwen3.5:9b)
            3. SLM returns function call → tool dispatch
            4. Interactive action? → wait for page settle → rescan
            5. Loop until text response or max steps
        → Result validation (retry weak, allow definitive)
    → Talker speaks result to user

Rescan pipeline:
    sendRescan(tabID, macheID)
        → extension: captureAndSend(isRescan=true, targetMacheID)
        → server: captureGo() → handleDOMSnapshot()
            → Cache lookup (zone IDs + bounds validation)
            → CACHE HIT: reuse schema (skip Cartographer)
            → PARTIAL STALE: attemptPartialRegen (per-zone crop+rebuild)
            → FULL STALE: full Cartographer rebuild
            → If targeted (rescanPath != ""): MergeSchema, not ApplySchema

iTerm pipeline:
    iterm.new_window → Bridge.Act("new_window")
        → client.CreateTab() → reconcileSessions() → force active → rebuildGraph()
    act("/iterm/active_session", "type", "hello")
        → Bridge.Act("type") → resolveSessionID("active_session") → b.active
        → client.SendText(sessionID, "hello")
```
