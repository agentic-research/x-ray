# Research: Claude Code as Agent Runtime

**Date:** 2026-03-04
**Status:** Research complete, not yet implemented
**Verdict:** Promising but brittle — revisit after mache integration

## Concept

Replace the custom Navigator tool loop (agent.go, 20-iteration Gemini/Ollama loop) with Claude Code (`claude -p`) as the agent runtime. Mache FUSE mount provides the sandboxed filesystem.

```
User <-> Gemini Live (voice) <-> CC -p (tool loop) <-> Mache NFS mount (web page as FS)
                                                   <-> MCP server (act, scroll, goto, rescan)
```

## What CC Gives Us For Free

- **Read, Edit, Grep (with -C context!), Glob, Bash** — native tools on real files
- **Planning mode** with multi-step reasoning
- **Subagent parallelism** via Agent tool
- **Self-correction and retry** — battle-tested over millions of sessions
- **No tools to maintain** — only need 3-4 custom MCP tools for browser actions

## What We'd Build

1. **Mache NFS mount adapter** (~100 LOC) — mount existing MemoryStore as NFS
2. **MCP server** (~150 LOC) — expose act/scroll/goto/rescan as MCP tools
3. **Doer rewrite** (~200 LOC delta) — replace `Navigator.HandleIntent()` with `claude -p` invocation
4. **Delete ~1500 LOC** — navigator/agent.go, tools.go, model.go, navfs.go

## What We'd Delete

| File | LOC | Purpose | Replaced By |
|------|-----|---------|-------------|
| navigator/agent.go | ~400 | 20-iteration tool loop | CC's agent loop |
| navigator/tools.go | ~400 | 12 custom tools (ls, cat, grep, stat, act, scroll, goto, rescan, list_tabs, switch_tab, new_window, new_tab) | CC native tools + MCP |
| navigator/model.go | ~300 | GeminiGenerator, OllamaGenerator, GemmaGenerator adapters | CC handles model selection |
| navigator/navfs.go | ~200 | NavFS adapter over MemoryStore | Mache NFS mount |

## The Grep Problem This Solves

Task 21 failure: `grep("small|ear cups")` matched "small" in unrelated reviews.

- Current grep: line-level match, no context, returns flat strings
- CC's Grep: supports `-C 3` context lines, glob filters, regex, output modes
- With mache mount: reviews are individual directories, model reads each review separately

## CC + Local Model (Key Finding)

**This changes the calculus significantly.** CC supports local model backends via env vars:

```bash
ANTHROPIC_BASE_URL=http://localhost:11434
ANTHROPIC_AUTH_TOKEN=ollama
ANTHROPIC_MODEL=qwen2.5-coder:32b
```

This means the comparison becomes:
- **Current:** Custom tool loop + Qwen via Ollama + NavFS
- **Proposed:** CC's tool loop + Qwen via Ollama + Mache mount
- **Same model. Same cost ($0).** Just better scaffolding and better filesystem.

### Recommended Local Models for CC (community consensus, March 2026)

| Model | Size | Notes |
|-------|------|-------|
| **Qwen 2.5 Coder 32B** | ~20GB Q4 | Gold standard for CC + local. Fits M3 Max 32GB. |
| **GLM 4.7 Flash** | varies | 128k context, strong agentic tool calling |
| **Qwen3-Coder-Next** | varies | Purpose-built for agentic workflows, multi-step planning |
| **Qwen3.5-9B** | ~6-10GB | Already running in x-ray; smaller but fast |

### CC System Prompt Budget

CC sends a ~16k token system prompt defining its behavior. This means:
- **20k context is the sweet spot** — large enough for agentic tasks, fast enough to avoid thinking loops
- Token throughput can drop from 100+ tok/s to ~2 tok/s at very large contexts
- On M3 Max 32GB: Qwen 2.5 Coder 32B (Q4) fits, but context budget is tight

### Daemon / Programmatic Mode

`claude -p` is the programmatic interface (formerly "headless mode"):
- Same tools, same agent loop, same context management
- Callable from Go: `exec.Command("claude", "-p", "--allowedTools", ..., intent)`
- CLAUDE.md provides control: "you're navigating a browser, not a codebase"
- `--allowedTools` constrains which tools are available

### Open Questions (testable in an afternoon)

1. Does Qwen3.5 behave well inside CC's scaffolding, or does the mismatch between CC's prompting and a non-Claude model cause problems?
2. Does CLAUDE.md give enough control to tell it "you're navigating a browser, not a codebase"?
3. What's the cold-start latency of `claude -p` per invocation?
4. Can MCP tools (act/scroll/goto) integrate cleanly?

## Brittleness Concerns

### Why it might be brittle:

1. **Process boundary** — shelling out to `claude -p` per step adds latency, error surface
2. **Version coupling** — CC updates could break integration (tool names, output format, behavior)
3. ~~**Model lock-in** — CC uses Claude API; can't easily swap to local models~~ **RESOLVED: local models work via env vars**
4. ~~**Cost** — Claude API per tool turn vs free local inference~~ **RESOLVED: $0 with local model**
5. **Control** — less control over iteration budget, prompt injection, tool availability
6. **Orchestration gap** — CC doesn't know about page-settle detection, schema wait, DOM mutations; Doer still needs to wrap it
7. **Output parsing** — `claude -p` returns text; need to parse structured responses (action requests, retrieved data)
8. **Sandbox escape** — CC has Bash tool; needs careful allowedTools configuration to prevent unintended system access
9. **Startup latency** — each `claude -p` invocation has cold-start overhead
10. **CC system prompt overhead** — ~16k tokens of CC's own system prompt eats into local model's context budget

### Why it might be OK:

1. **Same model, better loop** — Qwen via CC vs Qwen via custom loop; CC's loop is better
2. **MCP is stable** — custom tools via MCP is a supported, documented pattern
3. **Hackathon scope** — for demo purposes, the quality improvement alone justifies the coupling
4. **Fallback path** — keep OllamaGenerator as fallback if CC is unavailable
5. **Battle-tested** — CC's retry/planning/tool-selection logic is production-grade
6. **Zero cost** — local model eliminates the API cost concern entirely

## Current Tool Loop (for comparison)

```
Navigator (agent.go):
  - 20-iteration max tool loop
  - ContentGenerator interface (Gemini/Ollama/Gemma/GeminiLive)
  - 12 custom tools registered via ToolRegistry
  - Tool results fed back as FunctionResponse in history
  - Exits on: text response, ActionResult, or iteration cap

Doer (doer.go):
  - 5-step max goal loop
  - Enriches intent with TaskContext + step counter
  - Dispatches actions to browser extension
  - Waits for page settle (SchemaReady / DOMMutatedCh)
  - Builds continuation prompts between steps
  - Weak-response detection and retry
```

## Integration Path (if we proceed)

```go
// Doer keeps its 5-step goal loop but calls CC instead of Navigator
for step := range maxGoalSteps {
    // 1. Ensure mache NFS mount reflects current page state
    hotswap.Swap(currentEngine)

    // 2. Run CC with constrained tools
    cmd := exec.Command("claude", "-p",
        "--allowedTools", "Read,Grep,Glob,mcp__xray__act,mcp__xray__scroll,mcp__xray__goto",
        "--systemPrompt", systemPromptFile,
        enrichedIntent,
    )
    cmd.Dir = macheMountPoint  // CC operates inside the mount
    result, _ := cmd.Output()

    // 3. Parse result for actions or answers
    // 4. Dispatch browser actions
    // 5. Wait for page settle, loop
}
```

## Agent SDK (Better Than `claude -p`)

The Claude Agent SDK is the programmatic interface — same tools, agent loop, and context
management as CC, but as a library. Available in **Python and TypeScript** (official),
with a community **Go port**.

### Why Agent SDK > `claude -p`

| Dimension | `claude -p` | Agent SDK |
|-----------|-------------|-----------|
| Interface | Shell out, parse text | Structured async iterator |
| Overhead | Process spawn per call | In-process (Python/TS) or library (Go) |
| Tools | String-based allowedTools | Programmatic + MCP servers |
| Hooks | None | PreToolUse, PostToolUse, Stop, etc. |
| Sessions | Stateless per invocation | Resumable sessions with full context |
| Subagents | Not available | Built-in via `Task` tool |

### Key API (Python)

```python
from claude_agent_sdk import query, ClaudeAgentOptions

async for message in query(
    prompt="Find reviews mentioning small ear cups",
    options=ClaudeAgentOptions(
        allowed_tools=["Read", "Grep", "Glob"],
        mcp_servers={
            "xray": {"command": "./bin/xray-mcp", "args": ["--tab", str(tab_id)]}
        },
    ),
):
    if hasattr(message, "result"):
        print(message.result)
```

### MCP Integration — Native

The SDK natively supports MCP servers. Browser actions become an MCP server:

```python
mcp_servers={
    "xray-browser": {
        "command": "./bin/xray-mcp",
        "args": ["--tab", str(tab_id)]
    }
}
```

This gives CC tools like `mcp__xray_browser__act`, `mcp__xray_browser__scroll`, etc.
No Bash wrapper needed.

### Go Port (Community)

- [schlunsen/claude-agent-sdk-go](https://github.com/schlunsen/claude-agent-sdk-go)
- Supports: Query, SimpleTool, permissions, hooks, MCP
- **Caveat:** Community-maintained, custom MCP server support is TODO
- Could call from Go Doer directly — no Python/TS shim needed

### Local Model + Agent SDK

The SDK authenticates via `ANTHROPIC_API_KEY`. For local models via Ollama:

```bash
ANTHROPIC_BASE_URL=http://localhost:11434
ANTHROPIC_AUTH_TOKEN=ollama
ANTHROPIC_MODEL=qwen2.5-coder:32b
```

Same agent loop, same tools, zero API cost. The SDK's ~16k system prompt eats
into context budget — 20k context sweet spot for local models.

### Architecture With Agent SDK

```
User <-> Gemini Live (voice)
           |
           v
     Doer (Go orchestrator, 5-step goal loop)
           |
           v
     Agent SDK query() — per step
       - cwd = mache NFS mount point
       - allowed_tools = [Read, Grep, Glob, mcp__xray__act, mcp__xray__scroll, ...]
       - mcp_servers = {xray-browser: ./bin/xray-mcp}
       - resume = session_id (maintains context across steps!)
           |
           v
     Mache NFS mount (web page as structured FS)
```

**Session resumption** is huge — the Doer can resume the same agent session across
its 5-step goal loop, preserving all context from prior tool calls. No more
rebuilding continuation prompts from scratch.

## Decision

**Park this for now.** Focus on mache FUSE integration first — that's the foundation
either way. Whether the tool loop is custom or Agent SDK, the navigator needs to
operate on a mache mount. Build the mount, validate it works with the existing loop,
then evaluate whether swapping to Agent SDK is worth the coupling risk.

The mache integration is the independent variable. Agent SDK is the dependent variable.

### Testable in an afternoon

1. `pip install claude-agent-sdk`
2. Mount a page via mache
3. Point Agent SDK at mount with xray MCP server
4. Run a WebArena task
5. Compare accuracy + latency vs current custom loop
