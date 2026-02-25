# X-Ray: Gemini-Powered Web Navigation via Mache

[![Gemini Live Agent Challenge](https://img.shields.io/badge/Gemini%20Live%20Agent%20Challenge-UI%20Navigator-4285F4?logo=google&logoColor=white)](https://ai.google.dev/competition/gemini-live-agent-challenge)
[![CI](https://github.com/agentic-research/x-ray/actions/workflows/ci.yml/badge.svg)](https://github.com/agentic-research/x-ray/actions/workflows/ci.yml)

> **"Topology is the missing half of semantics."**

Powered by [`agentic-research/mache`](https://github.com/agentic-research/mache) — an agent-computer interface that projects structured data into virtual filesystems — X-Ray uses Gemini's multimodal vision to project any webpage into a clean, semantic filesystem. Agents navigate the DOM deterministically via `ls` and `cat` — no pixel guessing, no fragile HTML parsing.

## Table of Contents

- [The Problem](#the-problem-llms-understand-semantics-but-lack-topology)
- [The Fix](#the-fix-dynamic-semantic-projection)
  - [Stage 1: The Cartographer](#stage-1-the-cartographer-vision--structure)
  - [Stage 2: The Navigator](#stage-2-the-navigator-voice--action)
- [Voice Mode](#voice-mode)
  - [Voice Daemon Mode](#voice-daemon-mode)
- [Project Structure](#project-structure)
- [Getting Started](#getting-started)
- [Testing](#testing)
- [Benchmarks](#benchmarks)
- [Task Commands](#task-commands)
- [CI](#ci)
- [Architecture](#architecture)
- [Deployment](#deployment)

## The Problem: LLMs Understand Semantics, but Lack Topology

When building web-navigating agents, the standard approach is to feed them raw HTML or rely on pixel-coordinate guessing. But HTML is a 1D delivery mechanism for browsers, not a semantic map for reasoning agents.

An LLM already knows what a "Checkout" button *means* (semantics), but when you dump a 10,000-line DOM tree into its context window, it loses all spatial awareness of *where* it is or how it relates to the elements around it (topology).

Finding a simple button might require navigating a brittle path like `/html/body/div[4]/main/section/div[2]/span/button`. Giving a voice-driven LLM tools to find that path is like asking someone to locate a specific book by reading the library's structural blueprints.

Worse, websites change constantly. A simple layout update breaks these locators, and static schemas can't map the entire internet.

## The Fix: Dynamic Semantic Projection

X-Ray bridges the physical reality of the DOM and the conceptual intent of the user using a Two-Stage Agent Architecture:

### Stage 1: The Cartographer (Vision + Structure)

When a user visits a page, the X-Ray Chrome extension builds an in-memory registry of interactive elements (zero DOM mutation — SPA-safe), draws a Set-of-Mark overlay with colored bounding boxes and ID labels, captures a scaled screenshot (JPEG, quality 60) with the overlay visible, and generates a flattened text summary — each line enriched with DOM breadcrumb paths for structural context.

This payload goes to Gemini. Because Gemini is natively multimodal, it instantly understands the visual layout (e.g., "The top bar is navigation, the left side is filters") — aided by the Set-of-Mark bounding boxes visible in the screenshot. It outputs a strict JSON schema projecting these visual zones onto the registered DOM elements. For list zones, it identifies both **primary items** (the main clickable element in each repeating item) and a **CSS `item_selector`** — a structural query derived from DOM breadcrumb paths in the summary. After scrolling, the browser evaluates this selector via `querySelectorAll` to discover fresh content without another LLM call.

### Stage 2: The Navigator (Voice + Action)

That JSON schema feeds into the Mache Engine, which instantly mounts a virtual, in-memory filesystem tailored to that exact page.

Now, the agent doesn't see `div[4]/span/button`. It runs `ls /` and sees a clean, human-readable structure:

- `/header/global_nav/`
- `/main/trending_repositories/`
- `/footer/legal/`

Children are addressed by ordinal number (`_c/1`, `_c/2`, ...) — the model never sees raw mache IDs, eliminating confusion between item numbers and element IDs. When the user says, "Click the first trending repository," the Navigator traverses the filesystem using standard POSIX tools (`ls`, `cat`) and safely executes the action (`act`). The Navigator can run on Gemini cloud or a local 7B SLM (Qwen 2.5 Coder) for zero-latency, zero-cost execution. Because Gemini handles the heavy multimodal spatial reasoning in Stage 1, the execution loop is reduced to simple filesystem traversal — allowing a lightweight 7B model to drive the browser flawlessly.

## Voice Mode

X-Ray supports hands-free voice navigation via Gemini's Live API. The user speaks natural commands ("click the first story", "scroll down", "open the settings page") and Gemini executes them in real-time against the semantic filesystem.

### How it works

1. **Push-to-talk**: Hold the mic button (or spacebar) and speak a navigation intent.
2. **Gemini Live** receives the audio, transcribes it, and issues tool calls (`ls`, `cat`, `act`, `scroll`, `goto`, `rescan`) against the Mache engine — the same tools the text Navigator uses.
3. **Audio suppression**: While executing tool calls, Gemini's narration is muted server-side. Only the final result is spoken aloud ("Done, clicked the first story.").
4. **Text fallback**: A text input field lets you type commands into the same Live session — useful for corrections or precise instructions.

### Navigator tools

| Tool | Description |
|------|-------------|
| `ls(path)` | List directory contents |
| `cat(path)` | Read a file (description, children) |
| `act(path, action)` | Click or focus an element |
| `scroll(direction)` | Scroll to load more content |
| `goto(url)` | Navigate the browser to a new URL |
| `rescan(path?)` | Rescan the page — full or targeted (magnifying glass) |

### Magnifying glass rescan

A full-page rescan captures the same 800px-wide screenshot the Cartographer always sees — fine for top-level zones, but too coarse for internal controls (play buttons, volume sliders, scrubbers inside a video player).

When the Navigator calls `rescan("/main/player")`, X-Ray:
1. Resolves the mache-id for that zone
2. Uses CDP `DOM.getBoxModel` to get the element's bounding box
3. Crops the screenshot to just that region (with 50px padding for context)
4. Runs the Cartographer on the cropped image with a hint: *"You are zoomed into `/main/player`. Output absolute paths like `/main/player/controls`."*
5. Merges the new sub-zones into the existing filesystem via `Engine.MergeSchema` — no destructive replace

The agent retries `ls("/main/player")` and now sees `controls/`, `progress_bar/`, `volume/` — sub-zones that were invisible at full-page scale.

### Non-blocking schema

The voice connection opens immediately so the mic is hot from the moment the user clicks. If Gemini fires a tool call before the Cartographer finishes generating the schema, the tool execution blocks on a Go channel until the schema arrives — the user never sees an empty page.

### Voice daemon mode

For native mic/speaker voice interaction without the browser UI:

```bash
go run ./cmd/agentd --voice
```

This mode:
- Opens Chrome automatically if no browser is connected (cold-start)
- Uses `sox` for native mic capture and speaker playback — no browser audio permissions needed
- Connects to Gemini Live API with the same tool set as the browser voice UI
- Falls back to querying the active tab if the extension hasn't reported one yet

### Using voice mode

Voice mode is available through three interfaces:

1. **Voice daemon** (recommended): `go run ./cmd/agentd --voice` — native mic/speaker, no browser UI
2. **Chrome extension**: Popup mic toggle
3. **Standalone UI**: `http://localhost:8080/voice-ui?tab=<tabId>`

The extension handles mic permissions via a dedicated setup window (`mic-setup.html`) since Chrome MV3 offscreen documents cannot trigger permission prompts directly.

## Project Structure

```
x-ray/
├── .github/workflows/   # CI: test, lint, bench (amd64 + arm64)
├── cmd/
│   ├── agentd/          # Main backend server (WebSocket + HTTP)
│   ├── bench/           # Navigation accuracy benchmark
│   └── gate/            # Offline accuracy gate test
├── internal/
│   ├── api/             # WebSocket handler, voice handler, message types
│   ├── cartographer/    # Stage 1: Gemini Vision → semantic schema
│   ├── mache/           # Virtual filesystem engine
│   └── navigator/       # Stage 2: Gemini Tool-Use → browser actions
├── ext/                 # Chrome extension (content.js, background.js, CDP AX tree)
├── static/              # Standalone voice UI (voice.html)
├── testdata/            # Captured page snapshots for gate tests + benchmarks
├── deploy/              # Dockerfile + Cloud Run deploy script
└── docs/                # Architecture documentation
```

## Getting Started

### Prerequisites

- **Go 1.25+** — [go.dev/dl](https://go.dev/dl/)
- **Task** (task runner) — [taskfile.dev/installation](https://taskfile.dev/installation/)
- **Chrome** or Chromium-based browser
- **Gemini API Key** — [ai.google.dev](https://ai.google.dev/)

### 1. Environment Setup

Create a `.envrc` file in the project root:

```bash
export GEMINI_API_KEY="your-gemini-api-key"
# Optional: override the default model (gemini-2.5-flash)
# export GEMINI_MODEL="gemini-2.5-pro"
```

If you use [direnv](https://direnv.net/), run `direnv allow`. Otherwise the backend loads `.envrc` automatically via godotenv.

### 2. Running the Backend

```bash
task run
```

This builds, codesigns (macOS), and starts the server on `:8080`.

### 3. Loading the Chrome Extension

1. Open Chrome and navigate to `chrome://extensions/`.
2. Enable **Developer mode** (top right toggle).
3. Click **Load unpacked** and select the `ext/` directory.
4. Grant the extension permission to capture screenshots when prompted.

After making changes to extension code, click the reload icon on `chrome://extensions/` and refresh the target tab.

### 4. Using X-Ray

1. Navigate to any webpage in Chrome.
2. Click the X-Ray extension icon — this captures a screenshot + DOM summary and sends it to the backend.
3. The Cartographer analyzes the page and generates a semantic schema.
4. Navigate via text, voice, or curl:

```bash
# Text via HTTP
curl -X POST http://localhost:8080/navigate \
  -H "Content-Type: application/json" \
  -d '{"intent": "click the first story"}'

# Voice via extension popup mic toggle or standalone UI
open http://localhost:8080/voice-ui
```

## Testing

All commands use [Task](https://taskfile.dev) as the task runner. Install it first:

```bash
# macOS
brew install go-task

# Linux / other
sh -c "$(curl --location https://taskfile.dev/install.sh)" -- -d -b ~/.local/bin
```

### Running the full test suite

```bash
task test
```

This runs `go test -race -v ./...`. You can also run `go test` directly — X-Ray has no CGO/FUSE dependency (it only imports `mache/api` types).

### Running a single test

```bash
task test -- -run TestExecuteToolLs
```

### Test coverage

The test suite covers three packages with 61 tests:

| Package | What's tested |
|---------|---------------|
| `internal/mache/` | Schema parsing, directory listing, file reading, mache-id resolution, children grouping (primary items + heuristic), BFS traversal, zone membership via parent chains, AX-enriched summary parsing, Path/breadcrumb parsing, dynamic CSS selector resolution (`ZoneSelectors`, `LoadChildren` with `resolvedItems`), backward compat |
| `internal/navigator/` | `ExecuteTool` dispatch (ls/cat/act/scroll), default action, error paths, unknown tool, scroll with/without callback, default direction; full HN-style integration chain (ls → cat children → act on child) |
| `internal/api/` | Session creation/isolation/concurrency, message serialization, action queuing; WebSocket integration (snapshot → schema flow, action routing, reconnect flush, multi-tab isolation); voice protocol serialization |

The WebSocket integration tests use mock `SchemaGenerator`/`IntentHandler` interfaces — no Gemini API key required.

### Accuracy gate tests

```bash
task gate           # Mock dummy page
task gate-real      # All captured real pages (HN, GitHub, Wikipedia, Lobsters, eBay)
```

Gate tests require a `GEMINI_API_KEY` — they call the real Cartographer against captured page snapshots.

### Lint and format

```bash
task lint           # golangci-lint
task fmt            # gofumpt (stricter than gofmt)
task vet            # go vet
```

## Benchmarks

The automated navigation benchmark runs the full pipeline (Cartographer → Engine → Navigator) against captured testdata snapshots and verifies the correct element gets clicked.

```bash
task bench
```

Schemas are cached per site so multiple intents on the same page share one Cartographer call. The Navigator respects `NAVIGATOR_ENDPOINT`/`NAVIGATOR_MODEL`/`NAVIGATOR_FORMAT` env vars for testing with local models:

```bash
# Local Navigator (Gemma/Qwen via Ollama)
NAVIGATOR_ENDPOINT=http://localhost:11434/v1 \
NAVIGATOR_MODEL=qwen2.5-coder:7b \
NAVIGATOR_FORMAT=gemma \
task bench
```

## Task Commands

| Command | Description |
|---------|-------------|
| `task run` | Build, codesign, and run agentd |
| `task build` | Build and codesign the binary |
| `task test` | Run all tests (`go test -race -v ./...`) |
| `task bench` | Run navigation accuracy benchmark |
| `task gate` | Run accuracy gate on mock dummy page |
| `task gate-real` | Run accuracy gate on all captured real pages |
| `task lint` | Run golangci-lint |
| `task fmt` | Format with gofumpt |
| `task vet` | Run go vet |
| `task tidy` | Run go mod tidy |
| `task setup` | Install system dependencies (fuse-t on macOS) |

## CI

GitHub Actions runs on every push and PR to `main`:

- **Test**: `go test -race -v ./...` on Ubuntu and macOS
- **Lint**: `go vet` + `golangci-lint`
- **Bench**: Navigation accuracy benchmark on amd64 and arm64 (push to main + manual dispatch)

No Gemini API key or FUSE required for test/lint — all 61 tests use mock interfaces. The bench job requires a `GEMINI_API_KEY` repository secret and skips gracefully if not configured.

## Architecture

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the full system diagram, data flow, and component descriptions.

### Highlights

- **Zero Hallucinated IDs**: The Cartographer is constrained to select from the in-memory element registry. Structured JSON output with a strict schema prevents the model from inventing non-existent DOM pointers.
- **Set-of-Mark Visual Grounding**: Colored bounding boxes with ID labels are drawn over every registered element before screenshot capture, giving the Cartographer spatial anchors that tie visual zones to element IDs.
- **Ordinal Children Paths**: Child entries use ordinal numbers (`_c/1`, `_c/2`) instead of raw mache IDs, so the Navigator never confuses "click the 6th post" with `mache-6`.
- **CSS Selector Unions**: Primary items from the Cartographer's visual pass are *unioned* with browser-resolved CSS selector results (deduplicated, stale IDs filtered). Neither source alone is reliable — together they handle both SPA edge cases and infinite scroll.
- **Accessibility Tree Enrichment**: The extension captures the browser's computed accessibility tree via `chrome.debugger` + CDP (`Accessibility.getFullAXTree`). Each summary line is enriched with `AXRole` and `AXName` — the semantic truth the browser computes from implicit roles, CSS visibility, and ARIA attributes.
- **DOM Breadcrumb Injection**: Each summary line includes a `Path` field with 2-3 levels of DOM ancestry and CSS classes (e.g., `div.post > h3.title > a`). The Cartographer uses these structural patterns to synthesize CSS selectors per zone.
- **Dynamic CSS Selectors**: For list zones, the Cartographer outputs an `item_selector` — a CSS query that the browser evaluates natively via `querySelectorAll` after scrolling. This discovers fresh content without another LLM call, replacing stale hardcoded IDs.
- **Semantic Filesystem**: Instead of brittle XPaths or coordinate guessing, the agent interacts with a logical directory structure mapped dynamically to the page in under 10 seconds.
- **LLM-Powered Item Grouping**: For list zones, the Cartographer identifies primary items (story titles, product cards) so ordinal counting ("click the 3rd story") works correctly across any site.
- **Scroll Support**: The Navigator's `scroll` tool triggers page scrolling, CSS selector re-evaluation, and children file refresh — enabling "click the 15th post" workflows that span beyond the initial viewport.
- **Self-Healing Rescan**: When the Navigator can't find an element, it calls `rescan()` to capture a fresh screenshot and regenerate the schema — adapting to dynamic page changes without manual intervention.
- **Magnifying Glass Rescan**: Targeted `rescan("/path")` crops the screenshot to a specific zone's bounding box via CDP, runs the Cartographer on the zoomed-in image, and merges sub-zones into the existing filesystem. Discovers fine-grained controls (video player buttons, form fields) invisible at full-page scale.
- **Schema Cache with Bypass**: DOM snapshots are cached by URL to avoid redundant Cartographer calls. Rescan operations bypass the cache automatically via the `IsRescan` flag.
- **Proactive Navigation**: The `goto(url)` tool lets the Navigator open new pages ("go to Reddit", "take me home") — the filesystem updates automatically after navigation.
- **Cold-Start Browser Open**: The voice daemon detects when no browser is connected and opens Chrome automatically, so the user can start with just `go run ./cmd/agentd --voice`.
- **Temperature 0.1**: Both Cartographer and Navigator run at near-deterministic temperature for reproducible results.

## Deployment

X-Ray deploys to Google Cloud Run. See [`deploy/`](deploy/) for the Dockerfile and deploy script.

```bash
# Set required env vars
export GCP_PROJECT_ID="your-project"
export GOOGLE_API_KEY="your-key"

# Build and deploy
./deploy/deploy.sh
```

---

*Built for the [Gemini Live Agent Challenge](https://ai.google.dev/competition/gemini-live-agent-challenge) — UI Navigator category.*
