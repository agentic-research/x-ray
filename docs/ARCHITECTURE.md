# X-Ray Architecture

## System Overview

X-Ray is a voice-driven UI navigator built on three core ideas:

1. **Pages are filesystems.** Every web page is projected into a virtual filesystem (via [mache](https://github.com/agentic-research/mache)) where zones are directories and elements are files.
2. **Vision without VLMs.** CairnCartographer segments pages using Leech lattice error-correcting codes — pure math, deterministic, ~100ms.
3. **Voice is the interface.** Gemini Live API streams audio bidirectionally with real-time tool execution.

```mermaid
graph TB
    subgraph Chrome["Chrome Extension (ext/)"]
        CS["content.js<br/>DOM registry · overlays"]
        BG["background.js<br/>WS client · CDP pipe · tab lifecycle"]
        SP["sidepanel.html<br/>LOG tab · agent events"]
        TM["terminal.js<br/>ghostty-web · mache shell"]
        CS <-->|messages| BG
        BG -->|port| SP
        BG -->|port| TM
    end

    subgraph Server["Go Server (cmd/agentd)"]
        WS["WebSocket Handler<br/><i>internal/api/websocket.go</i>"]
        CAP["Capture Orchestrator<br/><i>internal/api/capture.go</i><br/>summary → overlay → CDP → enrich"]
        CDP["CDP Proxy<br/><i>internal/cdp/</i>"]
        SH["Shell Commands<br/><i>internal/api/shell.go</i>"]
        CART["CairnCartographer<br/><i>internal/cartographer/</i><br/>Leech Λ₂₄ tokenization"]
        MACHE["Mache Engine<br/><i>internal/mache/</i><br/>HotSwapGraph · NavFS"]
        NAV["Navigator Agent<br/><i>internal/navigator/</i><br/>12 tools · multi-step"]
        TALK["Talker<br/><i>voice.go</i><br/>Gemini Live · always responsive"]
        DOER["Doer<br/><i>doer.go</i><br/>goal executor · guardrails"]

        WS --> CAP
        WS --> CDP
        WS --> SH
        CAP --> CART
        CART -->|schema| MACHE
        MACHE --> NAV
        TALK -->|issue_command| DOER
        DOER --> NAV
        NAV -->|AGENT_SHELL| WS
    end

    subgraph GCP["Google Cloud"]
        CR["Cloud Run<br/>internal ingress only"]
        AR["Artifact Registry"]
        SM["Secret Manager<br/>Gemini API key"]
        CR --> SM
    end

    subgraph Gemini["Gemini API"]
        LIVE["Gemini Live API<br/>bidirectional audio stream"]
        FLASH["Gemini Flash<br/>Navigator tool-use"]
    end

    BG <-->|WebSocket| WS
    CS <-->|CDP events| CDP
    TALK <-->|audio + tools| LIVE
    NAV <-->|generate content| FLASH
    Server -.->|ko + Terraform| CR
    AR -.->|container image| CR
```

## Data Flow

### Page Capture Pipeline

```mermaid
sequenceDiagram
    participant BG as background.js
    participant CS as content.js
    participant GO as Go Server
    participant CDP as Chrome CDP

    BG->>GO: PAGE_READY
    GO->>BG: REQUEST_SUMMARY
    BG->>CS: CAPTURE_SNAPSHOT
    CS-->>BG: summary + URL
    BG-->>GO: SUMMARY_RESPONSE

    GO->>BG: DRAW_OVERLAY
    BG->>CS: DRAW_OVERLAY
    CS-->>BG: done
    BG-->>GO: OVERLAY_DRAWN

    GO->>BG: CDP_ATTACH
    BG->>CDP: chrome.debugger.attach
    CDP-->>BG: attached
    BG-->>GO: CDP_ATTACHED

    par Parallel CDP calls
        GO->>CDP: Page.getLayoutMetrics
        GO->>CDP: DOM.getDocument
        GO->>CDP: Runtime.evaluate (page text)
        GO->>CDP: Accessibility.getFullAXTree
    end

    GO->>CDP: Page.captureScreenshot
    GO->>CDP: DOM.querySelectorAll [data-mache-id]
    GO->>CDP: LayerTree.enable (optional)

    GO->>BG: CDP_DETACH

    Note over GO: Enrich summary with AX + layers
    GO->>GO: Cartographer → Mache Engine
    GO->>BG: SCHEMA_READY
```

### Voice Pipeline (Talker/Doer)

```mermaid
sequenceDiagram
    actor User as User (mic)
    participant GL as Gemini Live
    participant T as Talker
    participant D as Doer
    participant N as Navigator
    participant E as Extension
    participant TM as Sidebar Terminal

    User->>GL: speech audio
    GL->>T: transcribed intent
    T->>D: issue_command(intent)

    D->>D: wait for schema ready
    D->>N: HandleIntent(intent)

    loop Multi-step tool loop
        N->>N: ls / cat / grep
        N-->>TM: ⚡ agent:~$ ls /content
        N->>N: decide action
        N-->>TM: ⚡ agent:~$ act click /content/mache-42
        N->>E: EXECUTE_ACTION
        E-->>N: action result
        N->>N: rescan if needed
    end

    D-->>T: goal complete + summary
    T->>GL: result text
    GL->>User: spoken response
```

## Key Components

### Chrome Extension (`ext/`)

| File | Role |
|------|------|
| `content.js` | DOM element registry, semantic overlays, data-mache-id attributes, action execution |
| `background.js` | Service worker: WS client, CDP dumb pipe, tab lifecycle, message routing |
| `sidepanel.html` | Two tabs: LOG (agent events) and TERMINAL (ghostty-web shell) |
| `terminal.js` | Pseudo-shell over mache VFS + live agent tool call rendering |

### Go Server (`internal/`)

| Package | Role |
|---------|------|
| `api/` | WebSocket handler, capture orchestrator, Talker/Doer, shell commands |
| `cdp/` | CDP proxy (command/response multiplexer), capture helpers (screenshot, AX, layers) |
| `cartographer/` | CairnCartographer (Leech lattice), TropicalCartographer (max-plus), LLM cartographer |
| `navigator/` | Agent with 12 tools, tool registry, NavFS (virtual filesystem over graph) |
| `mache/` | Engine wrapper around mache's HotSwapGraph |
| `config/` | YAML + env config, Ollama params |

### Navigator Tools

| Tool | Description |
|------|-------------|
| `ls(path)` | List zones/elements at a path |
| `cat(path)` | Read element details (tag, bounds, AX role, text) |
| `stat(path)` | Quick metadata without full content |
| `grep(pattern)` | Search across the filesystem |
| `act(action, path, value?)` | Click, type, select, check, focus |
| `scroll(direction)` | Scroll up/down, reports new viewport position |
| `goto(url)` | Navigate to URL |
| `rescan(mache_id?)` | Re-capture page (full or zoomed to element) |
| `list_tabs()` | List open browser tabs |
| `switch_tab(id)` | Switch to a different tab |
| `new_tab(url)` | Open URL in new tab |
| `new_window(url)` | Open URL in new window |

### CairnCartographer

Segments pages without any VLM call:

1. **Fuse** 12 visual features (position, size, color) + 12 semantic DOM features → 24D vector per element
2. **Scale** by √8 and decode to nearest Leech lattice Λ₂₄ point (Construction A: same-parity coords, Golay bits, sum ≡ 0 or 4 mod 8)
3. **Cluster** by lattice point identity → zones
4. Optional sheaf cohomology (H⁰ folding) and curvature weighting for hierarchical structure

Result: deterministic zone segmentation in ~100ms, stable across page loads.

### CDP Proxy (`internal/cdp/`)

The extension acts as a **dumb pipe** for Chrome DevTools Protocol:

- Go sends `CDP_SEND` → extension calls `chrome.debugger.sendCommand` → returns `CDP_RESULT`
- Events flow back via `CDP_EVENT` → per-tab subscriber channels
- Attach/Detach lifecycle managed per-capture
- `Proxy.sender` protected by `sync.RWMutex` for safe WS reconnection
- Event subscriptions are per-tab (`sync.Map`) to avoid cross-tab race conditions

### Sidebar Terminal

The ghostty-web terminal serves two purposes:

1. **User shell**: manually explore the mache VFS (`ls`, `cd`, `cat`, `tree`)
2. **Agent live feed**: Navigator tool calls render in real-time as shell commands with a `⚡ agent:~$` prompt, auto-switching to the terminal tab when the agent starts working

## Deployment

- **Container**: built with [ko](https://ko.build/) (no Dockerfile)
- **Infrastructure**: Terraform → Cloud Run + Artifact Registry + Secret Manager
- **Ingress**: `internal-and-cloud-load-balancing` (not exposed to public internet)
- **Access**: `gcloud run services proxy` for authenticated local tunnel

## Concurrency Model

- Per-tab `captureSem` (channel semaphore) serializes captures, respects context cancellation
- Connection-scoped context cancels all goroutines on WS disconnect
- `HotSwapGraph` provides lock-free reads with atomic pointer swap on schema updates
- Doer runs as a background goroutine per goal; Talker remains responsive during execution
