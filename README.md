# X-Ray: Voice-Driven UI Navigator

[![Gemini Live Agent Challenge](https://img.shields.io/badge/Gemini%20Live%20Agent%20Challenge-UI%20Navigator-4285F4?logo=google&logoColor=white)](https://geminiliveagentchallenge.devpost.com/)
[![CI](https://github.com/agentic-research/x-ray/actions/workflows/ci.yml/badge.svg)](https://github.com/agentic-research/x-ray/actions/workflows/ci.yml)

> Navigate any UI by voice -- browser, terminal, structured data.

X-Ray is a voice-driven agent that navigates web pages and terminals through a semantic virtual filesystem. Instead of pixel-coordinate guessing or raw HTML dumps, X-Ray projects every UI into a clean filesystem that an LLM can traverse with three tools: `ls`, `cat`, `act`.

**Built for the [Gemini Live Agent Challenge](https://geminiliveagentchallenge.devpost.com/) -- UI Navigator category.**

### How it works

1. **You speak.** Gemini Live streams your voice in real-time (always-on mic, natural interruptions).
2. **The Cartographer sees.** A Chrome extension captures the page via CDP (screenshot + accessibility tree + DOM). The **CairnCartographer** segments it into zones using Leech lattice visual tokenization -- pure math, no VLM call, deterministic, sub-second.
3. **The Navigator acts.** The LLM calls `ls("/browser/header/")`, reads zone contents with `cat`, and executes actions with `act` -- clicking, typing, scrolling. [Mache](https://github.com/agentic-research/mache) routes every command to the right backend.

The sidebar includes a **ghostty-web terminal** where you can `ls`, `cd`, `cat`, and `tree` through the page yourself -- browse any website like a filesystem.

### Key features

- **Voice-first**: Gemini Live API with persistent audio sessions, barge-in support, and real-time tool execution
- **Visual precision**: Zero hallucinated element IDs -- CairnCartographer grounds zones in DOM structure, not LLM generation
- **Cross-domain**: Unified filesystem mounts browser (`/browser/`) and terminal (`/iterm/`) -- tell the agent to start a server in the terminal, then test the UI in the browser
- **No VLM required**: CairnCartographer uses error-correcting codes (Leech lattice, Golay, sheaf cohomology) instead of a vision model. The entire stack can run 100% air-gapped on local Apple Silicon with a local SLM via Ollama
- **Google Cloud native**: Go backend, GenAI SDK, deployed to Cloud Run via ko + Terraform

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
task build   # build + codesign (macOS)
task run     # WebSocket mode — extension connects to ws://localhost:8080/ws
task demo    # voice daemon mode — native mic/speaker via sox
```

The server listens on `:8080` by default (override with `PORT` env var).

### 3. Load the Chrome extension

1. Open `chrome://extensions/` in Chrome.
2. Enable **Developer mode** (top-right toggle).
3. Click **Load unpacked** and select the `ext/` directory.
4. The side panel opens with two tabs: **LOG** (agent activity) and **TERMINAL** (ghostty-web shell).

### 4. Use it

Navigate to any page, click the extension icon, then:

- **Voice**: `task demo` starts a native voice session. Speak naturally -- "click the first story", "scroll down", "open the settings page".
- **Terminal**: Switch to the TERMINAL tab in the side panel and explore the page as a filesystem:
  ```
  x-ray:~$ ls /
  header/  main/  sidebar/  footer/
  x-ray:~$ cd main
  x-ray:/main$ ls
  trending_repositories/  feed/
  x-ray:/main$ cat trending_repositories/_c/1
  ```
- **Standalone voice UI**: `http://localhost:8080/voice-ui`

## Architecture

```
                          ┌─────────────────────┐
                          │   Gemini Live API    │
                          │   (voice + tools)    │
                          └────────┬────────────┘
                                   │
                    ┌──────────────┴──────────────┐
                    │        Go Backend            │
                    │  ┌──────────┐ ┌───────────┐  │
                    │  │Cartograph│ │ Navigator  │  │
                    │  │  (Cairn) │ │(tool loop) │  │
                    │  └─────┬────┘ └─────┬─────┘  │
                    │        │   Mache    │         │
                    │        │  Engine    │         │
                    │  ┌─────┴────────────┴─────┐  │
                    │  │   CompositeGraph        │  │
                    │  │  /browser/  /iterm/     │  │
                    │  └──────┬──────────┬──────┘  │
                    └─────────┼──────────┼─────────┘
                              │          │
                    ┌─────────┴──┐  ┌────┴────────┐
                    │Chrome (CDP)│  │Terminal (UDS)│
                    │ extension  │  │   bridge     │
                    └────────────┘  └─────────────┘
```

### Two-stage pipeline

**Stage 1 -- Cartographer (what's on the page?):** The Chrome extension builds an in-memory registry of interactive elements (zero DOM mutation), captures a screenshot + accessibility tree via CDP, and sends them to the backend. The **CairnCartographer** projects each element into a 24-dimensional feature vector (spatial bounds, pixel samples, DOM structure, semantic role), quantizes via the Leech lattice, and groups elements sharing a lattice point into zones. Optional sheaf folding (H⁰ cohomology) merges redundant zones; curvature detection (H¹, SO(2) transport) annotates zone boundaries.

**Stage 2 -- Navigator (what should we do?):** The schema mounts as a virtual filesystem via Mache. The Navigator LLM sees a clean directory tree and uses `ls`, `cat`, `act` to traverse and interact. Because Stage 1 does the heavy multimodal lifting, the Navigator can be a small model -- `gemini-3.1-flash-lite-preview` or even a local 7B SLM via Ollama.

### The virtual filesystem

Mache acts like Linux's `fstab`. The Navigator uses three tools, and Mache routes them to the right backend:

```
/
├── browser/          <- Chrome CDP plugin (Cartographer zones the page)
│   ├── header/nav/
│   ├── main/feed/
│   │   └── _c/1     <- ordinal children (not raw element IDs)
│   └── footer/
└── iterm/            <- Terminal bridge (no vision model needed)
    └── windows/0/sessions/{id}/
        ├── buffer    # last 100 lines of output
        ├── title     # running command
        └── cwd       # working directory
```

### Navigator tools

| Tool | Description |
|------|-------------|
| `ls(path)` | List directory contents |
| `cat(path)` | Read a file (description, children, buffer) |
| `act(path, action)` | Click, focus, type, or press Enter |
| `scroll(direction)` | Scroll to load more content |
| `goto(url)` | Navigate the browser to a new URL |
| `rescan(path?)` | Rescan the page -- full or targeted (magnifying glass) |
| `list_tabs()` | List all open browser tabs |
| `switch_tab(tab_id)` | Switch to an existing tab |

### Cartographers

| Mode | How it works | Speed | Requires |
|------|-------------|-------|----------|
| **Cairn** (default) | Leech lattice Λ₂₄ visual tokenization + sheaf folding | ~100ms | Nothing (pure math) |
| **Tropical** | Max-plus tropical geometry + neighbor-joining tree | ~200ms | Nothing (pure math) |
| **Gemini** | Gemini Vision API zones the screenshot | ~2s | `GEMINI_API_KEY` |
| **Ollama** | Local VLM (llava, qwen2-vl) | ~5s | Ollama running |

## Deployment

X-Ray deploys to Google Cloud Run via [ko](https://ko.build/) (no Dockerfile needed) and Terraform. The service is **not exposed to the public internet** -- ingress is restricted to internal + Cloud Load Balancing.

```bash
# Set GCP vars in .envrc (loaded by direnv)
export GCP_PROJECT_ID="your-project"
export KO_DOCKER_REPO="us-central1-docker.pkg.dev/$GCP_PROJECT_ID/x-ray/agentd"
export TF_VAR_project_id="$GCP_PROJECT_ID"

# One command: ko builds the Go binary into a container, Terraform deploys it
task deploy

# Proxy Cloud Run to localhost:8080 for the extension (authenticated, no public exposure)
task deploy-proxy
```

See [`deploy/main.tf`](deploy/main.tf) for the full Terraform configuration.

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `GEMINI_API_KEY` | *(required for cloud)* | Gemini API key |
| `CARTOGRAPHER_MODE` | `cairn` | `cairn`, `tropical`, or unset for Gemini Vision |
| `CAIRN_SHEAF` | `0` | Enable H⁰ sheaf-based zone folding |
| `CAIRN_CURVATURE` | `0` | Enable H¹ contour detection |
| `NAVIGATOR_MODEL` | `gemini-3.1-flash-lite-preview` | Navigator model |
| `NAVIGATOR_ENDPOINT` | *(unset -- uses Gemini)* | OpenAI-compatible endpoint for local Navigator |
| `NAVIGATOR_FORMAT` | `openai` | `gemma` for Gemma function calling, `openai` for OpenAI-compatible |
| `CARTOGRAPHER_ENDPOINT` | *(unset)* | OpenAI-compatible vision endpoint for local Cartographer |
| `CARTOGRAPHER_MODEL` | `llava:13b` | Model when using local Cartographer |
| `PORT` | `8080` | HTTP server port |

### 100% air-gapped mode

The entire stack runs offline on Apple Silicon. CairnCartographer is pure math, and the Navigator uses a local SLM:

```bash
task demo-local   # local VLM + local SLM, no cloud API
```

## Testing

```bash
task test          # full test suite (go test -race -v ./...)
task gate          # accuracy gate on mock page
task gate-real     # accuracy gate on captured real pages (HN, GitHub, Wikipedia, Lobsters, eBay)
task bench         # navigation accuracy benchmark
```

## Task Commands

| Command | Description |
|---------|-------------|
| `task run` | Build and run agentd |
| `task demo` | Voice daemon (native mic/speaker via sox) |
| `task demo-video` | Demo preset for recording (Cairn + sheaf + curvature + Flash Lite) |
| `task demo-local` | Fully air-gapped: local VLM + local SLM |
| `task deploy` | Build with ko + deploy to Cloud Run via Terraform |
| `task deploy-proxy` | Proxy Cloud Run to localhost:8080 (authenticated) |
| `task deploy-proof` | Print Cloud Run service info for GCP deployment proof |
| `task test` | Run all tests |
| `task bench` | Navigation accuracy benchmark |
| `task gate-real` | Accuracy gate on captured real pages |

## Project Structure

```
x-ray/
├── cmd/
│   ├── agentd/             # Main backend server
│   ├── bench/              # Navigation accuracy benchmark
│   └── gate/               # Offline accuracy gate test
├── internal/
│   ├── api/                # WebSocket handler, voice, shell commands, edge detection
│   ├── audio/              # sox-based mic/speaker for voice daemon
│   ├── cartographer/       # Cairn (Leech lattice), Tropical (max-plus), Ollama/Gemini VLM
│   ├── cdp/                # Chrome DevTools Protocol proxy
│   ├── iterm/              # Terminal bridge (Unix Domain Socket)
│   ├── mache/              # Virtual filesystem engine (browser graph backend)
│   └── navigator/          # Gemini Live + REST tool-use agent
├── ext/                    # Chrome extension (content.js, background.js, ghostty-web terminal)
├── static/                 # Standalone voice UI
├── deploy/                 # Terraform + ko Cloud Run deployment
└── docs/                   # Architecture documentation
```

---

*Created for the [Gemini Live Agent Challenge](https://geminiliveagentchallenge.devpost.com/) #GeminiLiveAgentChallenge*
