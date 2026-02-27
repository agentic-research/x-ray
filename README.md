# X-Ray: Pluggable Universal Agent OS

[![Gemini Live Agent Challenge](https://img.shields.io/badge/Gemini%20Live%20Agent%20Challenge-UI%20Navigator-4285F4?logo=google&logoColor=white)](https://ai.google.dev/competition/gemini-live-agent-challenge)
[![CI](https://github.com/agentic-research/x-ray/actions/workflows/ci.yml/badge.svg)](https://github.com/agentic-research/x-ray/actions/workflows/ci.yml)

> **"Topology is the missing half of semantics."**

X-Ray is a pluggable, voice-driven agent OS that unifies browsers and terminals under a single semantic virtual filesystem. A **CompositeGraph** — like Linux's `fstab` — mounts domain-specific plugins at named paths. The **Navigator** LLM only ever uses three tools (`ls`, `cat`, `act`), and Mache routes those commands to the right backend:

- **`/browser/`** — driven by a Chrome CDP plugin. A **Cartographer** (Gemini Vision or a local VLM) projects the DOM into the filesystem.
- **`/iterm/`** — driven by a zero-latency Unix Domain Socket bridge. Terminal sessions are already structured text — no vision model needed.

For pixel-rendered content like `<canvas>` and WebGL, a pure-Go **Canny edge detection** pipeline detects UI regions invisible to DOM parsing. Voice mode uses **Gemini Live** with a Talker/Doer swarm split for hands-free, real-time cross-domain control — tell the agent to spin up a server in `/iterm/`, then switch to `/browser/` to test the UI.

The entire ACI can run 100% air-gapped on local Apple Silicon: swap Gemini for a local VLM (Cartographer) and a local SLM (Navigator) via environment variables.

Powered by [`agentic-research/mache`](https://github.com/agentic-research/mache) -- an agent-computer interface that projects structured data into virtual filesystems.

## Quick Start

### Prerequisites

| Requirement | Notes |
|-------------|-------|
| **Go 1.25+** | [go.dev/dl](https://go.dev/dl/) |
| **Task** (task runner) | [taskfile.dev/installation](https://taskfile.dev/installation/) |
| **Chrome** | Or any Chromium-based browser |
| **Gemini API Key** | [ai.google.dev](https://ai.google.dev/) |
| **Ollama** *(optional)* | Only needed for local Navigator ([ollama.com](https://ollama.com/)) |
| **sox** *(optional)* | Only needed for native voice mode (`brew install sox`) |

### 1. Set your API key

Create a `.envrc` file in the project root:

```bash
export GEMINI_API_KEY="your-gemini-api-key"
```

If you use [direnv](https://direnv.net/), run `direnv allow`. Otherwise the daemon loads `.envrc` automatically via godotenv.

### 2. Build and run

```bash
# Build the binary (builds + codesigns on macOS)
task build

# Run in WebSocket-only mode (extension connects over ws://localhost:8080/ws)
task run

# Run in voice daemon mode (native mic/speaker via sox)
task demo
```

Or without Task:

```bash
go build -o bin/agentd ./cmd/agentd
./bin/agentd           # WebSocket-only
./bin/agentd --voice   # Voice daemon mode
```

The server listens on `:8080` by default (override with `PORT` env var).

### 3. Load the Chrome extension

1. Open `chrome://extensions/` in Chrome.
2. Enable **Developer mode** (top-right toggle).
3. Click **Load unpacked** and select the `ext/` directory.
4. Grant screenshot-capture permission when prompted.

### 4. Use it

1. Navigate to any webpage in Chrome.
2. Click the X-Ray extension icon -- this captures a screenshot + DOM summary and sends it to the backend.
3. The Cartographer analyzes the page and generates a semantic filesystem.
4. Navigate via voice, text, or curl:

```bash
# Voice: use the daemon (task demo) or the extension popup mic toggle

# Text via HTTP
curl -X POST http://localhost:8080/navigate \
  -H "Content-Type: application/json" \
  -d '{"intent": "click the first story"}'

# Standalone voice UI in browser
open http://localhost:8080/voice-ui
```

### Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `GEMINI_API_KEY` | *(required)* | Gemini API key |
| `GEMINI_MODEL` | `gemini-2.5-flash` | Model for Cartographer and cloud Navigator |
| `GEMINI_LIVE_MODEL` | `gemini-2.5-flash-native-audio-preview-12-2025` | Model for Gemini Live voice sessions |
| `NAVIGATOR_ENDPOINT` | *(unset -- uses Gemini)* | OpenAI-compatible endpoint for local Navigator (e.g., `http://localhost:11434/v1`) |
| `NAVIGATOR_MODEL` | `functiongemma:270m` | Model name when using a local Navigator endpoint |
| `NAVIGATOR_FORMAT` | `openai` | `gemma` for native Gemma function calling, `openai` for OpenAI-compatible format |
| `CARTOGRAPHER_ENDPOINT` | *(unset -- uses Gemini)* | OpenAI-compatible vision endpoint for local Cartographer (e.g., `http://localhost:11434/v1`) |
| `CARTOGRAPHER_MODEL` | `llava:13b` | Model name when using a local Cartographer endpoint |
| `PORT` | `8080` | HTTP server port |
| `XRAY_DB` | `~/.xray/schemas.db` | Path to the SQLite schema cache |

---

## How It Works

### The Problem: LLMs Understand Semantics, but Lack Topology

When building web-navigating agents, the standard approach is to feed them raw HTML or rely on pixel-coordinate guessing. But HTML is a 1D delivery mechanism for browsers, not a semantic map for reasoning agents.

An LLM already knows what a "Checkout" button *means* (semantics), but when you dump a 10,000-line DOM tree into its context window, it loses all spatial awareness of *where* it is or how it relates to the elements around it (topology).

### The Fix: Two-Stage Agent Architecture

**Stage 1 -- The Cartographer (Vision + Structure):** When a user visits a page, the Chrome extension builds an in-memory registry of interactive elements (zero DOM mutation -- SPA-safe), draws a Set-of-Mark overlay with colored bounding boxes and ID labels, captures a scaled screenshot, and generates a flattened text summary enriched with DOM breadcrumb paths. This payload goes to Gemini, which outputs a strict JSON schema projecting visual zones onto registered DOM elements. For list zones, it identifies primary items and a CSS `item_selector` that the browser evaluates natively after scrolling -- discovering fresh content without another LLM call.

**Stage 2 -- The Navigator (Voice + Action):** That JSON schema feeds into the Mache Engine, which mounts a virtual filesystem tailored to the page. The agent runs `ls /` and sees a clean structure like `/header/global_nav/`, `/main/trending_repositories/`, `/footer/legal/`. Children are addressed by ordinal number (`_c/1`, `_c/2`, ...). When the user says "Click the first trending repository," the Navigator traverses the filesystem and executes the action. Because Gemini handles the heavy multimodal reasoning in Stage 1, the execution loop is simple enough for a local 7B SLM to drive the browser flawlessly.

### The Virtual Filesystem (CompositeGraph)

Mache acts like Linux's `fstab`. The Navigator LLM only ever uses three simple tools (`ls`, `cat`, `act`), and Mache routes those commands to high-speed, domain-specific plugins:

```
/
├── browser/          ← Chrome CDP plugin (Cartographer vision model)
│   ├── header/nav/
│   ├── main/feed/
│   └── footer/
└── iterm/            ← Unix Domain Socket bridge (no vision model needed)
    └── windows/
        └── 0/
            └── sessions/
                └── {id}/
                    ├── buffer    # last 100 lines of terminal output
                    ├── title     # shell title / running command
                    └── cwd       # current working directory
```

The `/iterm/` mount does **not** use the Cartographer vision model — terminal data is already perfectly structured text. The Bridge connects via iTerm2's Unix Domain Socket, reads session metadata directly, and exposes it as graph nodes. When the Navigator calls `act("/iterm/.../sessions/abc", "type", "ls -la")`, the text is sent directly to the terminal — zero latency.

### Cross-Domain Swarm

The Talker/Doer swarm can seamlessly jump between domains in a single conversation:

1. `ls("/iterm/windows/0/sessions/")` — see running terminal sessions
2. `act("/iterm/.../sessions/abc", "type", "npm start")` — start a server
3. `cat("/iterm/.../sessions/abc/buffer")` — wait for "listening on :3000"
4. `act("/browser/main/url_bar", "type", "http://localhost:3000")` — test in browser
5. `ls("/browser/main/")` — explore the rendered UI

Voice makes this hands-free: "spin up the dev server in my terminal, then check if the homepage loads."

### Navigator Tools

| Tool | Description |
|------|-------------|
| `ls(path)` | List directory contents |
| `cat(path)` | Read a file (description, children, buffer) |
| `act(path, action)` | Click, focus, type, or press Enter — routes to browser or terminal |
| `scroll(direction)` | Scroll to load more content |
| `goto(url)` | Navigate the browser to a new URL |
| `rescan(path?)` | Rescan the page -- full or targeted (magnifying glass) |
| `list_tabs()` | List all open browser tabs (ID, title, URL) |
| `switch_tab(tab_id)` | Switch to an existing open tab (faster than goto) |

## Voice Mode

X-Ray supports hands-free voice navigation via Gemini's Live API. The user speaks natural commands ("click the first story", "scroll down", "open the settings page") and Gemini executes them in real-time against the semantic filesystem.

1. **Push-to-talk**: Hold the mic button (or press Enter in daemon mode) and speak a navigation intent.
2. **Gemini Live** receives the audio, transcribes it, and issues tool calls against the Mache engine.
3. **Audio suppression**: While executing tool calls, narration is muted server-side. Only the final result is spoken aloud.
4. **Text fallback**: Type commands into the same Live session for corrections or precise instructions.

Voice mode is available through three interfaces:

| Interface | Command | Notes |
|-----------|---------|-------|
| **Voice daemon** (recommended) | `task demo` or `./bin/agentd --voice` | Native mic/speaker via sox, no browser audio permissions |
| **Chrome extension** | Popup mic toggle | Uses browser audio APIs |
| **Standalone UI** | `http://localhost:8080/voice-ui?tab=<tabId>` | Browser-based voice UI |

### Magnifying Glass Rescan

A full-page rescan captures the same 800px-wide screenshot the Cartographer always sees -- fine for top-level zones, but too coarse for internal controls (play buttons, volume sliders, scrubbers inside a video player).

When the Navigator calls `rescan("/main/player")`, X-Ray resolves the zone's bounding box via CDP, crops the screenshot to just that region, runs the Cartographer on the zoomed-in image, and merges the new sub-zones into the existing filesystem. The agent retries `ls("/main/player")` and now sees `controls/`, `progress_bar/`, `volume/` -- sub-zones that were invisible at full-page scale.

## Architecture

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the full system diagram, data flow, and component descriptions.

### Key Design Decisions

- **Zero Hallucinated IDs**: The Cartographer is constrained to select from the in-memory element registry. Structured JSON output with a strict schema prevents the model from inventing non-existent DOM pointers.
- **Set-of-Mark Visual Grounding**: Colored bounding boxes with ID labels are drawn over every registered element before screenshot capture, giving the Cartographer spatial anchors that tie visual zones to element IDs.
- **Ordinal Children Paths**: Child entries use ordinal numbers (`_c/1`, `_c/2`) instead of raw mache IDs, so the Navigator never confuses "click the 6th post" with `mache-6`.
- **CSS Selector Unions**: Primary items from the Cartographer's visual pass are unioned with browser-resolved CSS selector results (deduplicated, stale IDs filtered). Neither source alone is reliable -- together they handle both SPA edge cases and infinite scroll.
- **Accessibility Tree Enrichment**: The extension captures the browser's computed accessibility tree via `chrome.debugger` + CDP. Each summary line is enriched with `AXRole` and `AXName`.
- **DOM Breadcrumb Injection**: Each summary line includes a `Path` field with 2-3 levels of DOM ancestry and CSS classes. The Cartographer uses these structural patterns to synthesize CSS selectors per zone.
- **Canvas Blindspot Detection**: Pure-Go Canny edge detection (Sobel + NMS + hysteresis) runs on the screenshot before the Cartographer call, detecting rectangular UI regions inside `<canvas>` and WebGL elements that are invisible to DOM parsing. Detected regions get `cv-N` IDs and are clicked via CDP `Input.dispatchMouseEvent` at pixel coordinates instead of `el.click()`.
- **Self-Healing Rescan**: When the Navigator can't find an element, it calls `rescan()` to capture a fresh screenshot and regenerate the schema -- adapting to dynamic page changes without manual intervention.
- **Temperature 0.1**: Both Cartographer and Navigator run at near-deterministic temperature for reproducible results.

## Project Structure

```
x-ray/
├── .github/workflows/   # CI: test, lint, bench (amd64 + arm64)
├── cmd/
│   ├── agentd/          # Main backend server (WebSocket + HTTP)
│   ├── bench/           # Navigation accuracy benchmark
│   └── gate/            # Offline accuracy gate test
├── internal/
│   ├── api/             # WebSocket handler, voice handler, message types, edge detection
│   ├── audio/           # sox-based mic/speaker for voice daemon
│   ├── cartographer/    # Stage 1: Gemini Vision or local VLM → semantic schema
│   ├── iterm/           # iTerm2 bridge: Unix Domain Socket → graph.Graph
│   ├── mache/           # Virtual filesystem engine (browser graph backend)
│   └── navigator/       # Stage 2: Gemini Tool-Use → browser/terminal actions
├── ext/                 # Chrome extension (content.js, background.js, CDP AX tree)
├── static/              # Standalone voice UI (voice.html)
├── testdata/            # Captured page snapshots for gate tests + benchmarks
├── deploy/              # Dockerfile + Cloud Run deploy script
└── docs/                # Architecture documentation
```

## Testing

```bash
# Run the full test suite
task test

# Run a single test
task test -- -run TestExecuteToolLs

# Accuracy gate tests (require GEMINI_API_KEY)
task gate           # Mock dummy page
task gate-real      # All captured real pages (HN, GitHub, Wikipedia, Lobsters, eBay)

# Navigation accuracy benchmark
task bench
```

All 61 unit tests use mock interfaces -- no Gemini API key or FUSE required. Gate tests and benchmarks call the real Cartographer and require `GEMINI_API_KEY`.

To test with a local Navigator model:

```bash
NAVIGATOR_ENDPOINT=http://localhost:11434/v1 \
NAVIGATOR_MODEL=qwen2.5-coder:7b \
NAVIGATOR_FORMAT=openai \
task bench
```

### Configuration / Environment Variables

X-Ray is highly configurable via environment variables. You can set these in your shell or use an `.envrc` file (see `.envrc.example`).

| Variable | Default Value | Description |
|----------|---------------|-------------|
| `GOOGLE_GEMINI_API_KEY` | *(None)* | API key for Gemini models |
| `GEMINI_MODEL` | `gemini-2.5-flash` | The primary Gemini model for base operations |
| `GEMINI_LIVE_MODEL` | `gemini-2.5-flash-native-audio-preview-12-2025` | The model used for Voice Mode via Live API |
| `CARTOGRAPHER_ENDPOINT` | *(None)* | URL for a local Vision Language Model |
| `CARTOGRAPHER_MODEL` | `llava:13b` | Local Vision Language Model to use |
| `NAVIGATOR_ENDPOINT` | *(None)* | URL for a local Small Language Model |
| `NAVIGATOR_MODEL` | `functiongemma:270m` | Local SLM for Navigator actions |
| `NAVIGATOR_FORMAT` | `openai` | API format for Navigator (`gemma` or `openai`) |
| `PORT` | `8080` | Port for the HTTP server |
| `XRAY_DB` | `~/.xray/schemas.db` | Path to SQLite schema cache |

### 100% Air-Gapped / Local Mode

The entire ACI can run completely offline on local Apple Silicon. While X-Ray uses Gemini Pro/Flash by default for the cloud swarm, every model is pluggable via environment variables:

```bash
# Fully local — no cloud API calls (defaults: llava:13b + qwen2.5-coder:7b)
task demo-local

# Or override models:
CARTOGRAPHER_MODEL=qwen2-vl:7b NAVIGATOR_MODEL=gemma:12b task demo-local
```

The `/iterm/` terminal bridge is always local (Unix Domain Socket) — no model required.

## Task Commands

| Command | Description |
|---------|-------------|
| `task run` | Build, codesign, and run agentd |
| `task demo` | Build and run the voice daemon (Talker/Doer swarm) |
| `task demo-local` | Fully air-gapped: local VLM + local SLM, no cloud API |
| `task build` | Build and codesign the binary |
| `task test` | Run all tests (`go test -race -v ./...`) |
| `task bench` | Run navigation accuracy benchmark |
| `task gate` | Run accuracy gate on mock dummy page |
| `task gate-real` | Run accuracy gate on all captured real pages |
| `task lint` | Run golangci-lint |
| `task fmt` | Format with gofumpt |
| `task vet` | Run go vet |
| `task tidy` | Run go mod tidy |

## CI

GitHub Actions runs on every push and PR to `main`:

- **Test**: `go test -race -v ./...` on Ubuntu and macOS
- **Lint**: `go vet` + `golangci-lint`
- **Bench**: Navigation accuracy benchmark on amd64 and arm64 (push to main + manual dispatch)

No Gemini API key or FUSE required for test/lint. The bench job requires a `GEMINI_API_KEY` repository secret and skips gracefully if not configured.

## Deployment

X-Ray deploys to Google Cloud Run. See [`deploy/`](deploy/) for the Dockerfile and deploy script.

```bash
export GCP_PROJECT_ID="your-project"
export GOOGLE_API_KEY="your-key"
./deploy/deploy.sh
```

---

*Built for the [Gemini Live Agent Challenge](https://ai.google.dev/competition/gemini-live-agent-challenge) -- UI Navigator category.*
