# Handoff: Navigator → Gemini Live API (Direct Tool Calling)

## Context

Session 2026-03-05. Commits this session:
- `ddd4a4e` feat: improve Navigator data quality and iTerm readability
- `323136c` fix: break infinite recursion in focus Router GetCallers/GetCallees
- `9106eb6` fix: truncate verbose iTerm window/tab descriptions
- `04f6c1d` feat: Navigator testing infrastructure (nav-dump, nav-test, Gemini Live support)

## Completed Work

### Navigator Data Quality (engine.go)
- `selectChildItems()`: supplementary text capped at 1:1 ratio with primary items (min 10) — was dumping ALL text nodes
- `formatOrdinalChildren()`: now shows `[N] tag: text` with parent-child hierarchy indentation via ParentID
- `config.go`: default `num_ctx` bumped 8192 → 32768 (system prompt alone is ~8K tokens)
- 7 test assertions updated for new format, all passing

### iTerm Readability (schema.go)
- Sequential window/tab IDs (w0, w1, t0, t1) instead of raw PTY UUIDs
- Description files added to tab and window directories
- `buildTabLabel()`: strips "Default:" prefix, caps at 50 chars
- Window descriptions capped at 80 chars

### Navigator Testing Infrastructure (cmd/bench)
- `--dump` flag: shows zone tree + formatted children without calling LLM (instant)
- `--site` flag: filter to one site (e.g., `--site hackernews`)
- `NAVIGATOR_MODE=gemini-live` wired up with Live API client
- 11 bench cases (was 5) covering HN, lobsters, github, ecommerce, wikipedia
- Taskfile: `task nav-dump`, `task nav-test`, `task nav-test-gemini`

### Guardrails
- Discovered `XRAY_GUARDRAILS=1` was missing from `.envrc` — all guardrails were disabled during demos. User added it.

## Active Direction: Gemini Live as Navigator

**The key architectural insight**: The Talker/Doer split was a workaround for using a local SLM as Navigator. If Gemini Live IS the Navigator (calling browser tools directly with NON_BLOCKING), the indirection is unnecessary.

### Current Architecture
```
User ↔ Gemini Live (Talker, 5 lightweight tools) → issue_command → Doer → local SLM (qwen3.5:9b) → ls/cat/act tools → browser
```

### Target Architecture
```
User ↔ Gemini Live (voice + browser tools, NON_BLOCKING) → browser
```

### What Needs to Happen

1. **Give Gemini Live the Navigator tools directly** (ls, cat, act) — currently only has Talker tools (check_status, issue_command, cancel_task, open_url, terminal_action)

2. **Use NON_BLOCKING tool behavior** — the Live API supports `behavior: "NON_BLOCKING"` with scheduling (`INTERRUPT`, `WHEN_IDLE`, `SILENT`). This lets the model stay responsive while browser actions execute. See: https://ai.google.dev/gemini-api/docs/live-tools

3. **Wire in Live API v1alpha features** (already in Go SDK):
   - `ThinkingConfig{ThinkingBudget: N}` — helps Navigator reason about tool calls
   - `Proactivity: &ProactivityConfig{ProactiveAudio: ptr(true)}` — model decides when to respond
   - `EnableAffectiveDialog: ptr(true)` — emotion-aware voice responses
   - These are on `LiveConnectConfig`, fields confirmed in genai@v1.47.0

4. **The Doer's settle/schema/guardrail logic stays** — it just runs server-side when a tool executes, not driven by a local SLM

### Key Files
- `internal/api/voice.go` — Talker Live API session, `buildLiveConfig()`, tool execution loop
- `internal/navigator/gemini_live.go` — `GeminiLiveGenerator` (current Navigator Live API wrapper)
- `internal/navigator/agent.go` — Navigator tool registry (`ls`, `cat`, `act`)
- `internal/api/doer.go` — Doer loop (settle detection, schema wait, guardrails)
- `cmd/agentd/main.go` — model selection logic

### Model Constraints
- `gemini-2.5-flash` — supports Live API + function calling (use this)
- `gemini-3.1-flash-lite-preview` — does NOT support Live API (REST only, $0.25/1M input)
- `gemini-2.5-flash-native-audio-preview-12-2025` — native audio model (used for Talker voice)

### User's Looping Issue
User has output from a Navigator run that shows looping behavior. They need to share it in the next session for diagnosis. Likely related to the SLM (qwen3.5:9b) not parsing tool responses correctly, which further motivates moving to Gemini Live as Navigator.

### Testing
```bash
task nav-dump                          # see what Navigator reads (no LLM)
task nav-dump -- --site hackernews     # single site
task nav-test                          # local Ollama SLM
task nav-test-gemini                   # Gemini Live API
```

## Uncommitted Local Changes
- `go.mod` / `go.sum` — `replace` directive for local mache (dev artifact, don't commit)
- `internal/api/websocket.go` — NFS mount API change tied to local mache
