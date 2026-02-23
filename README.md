# X-Ray: Gemini-Powered Web Navigation via Mache

[![Gemini Live Agent Challenge](https://img.shields.io/badge/Gemini%20Live%20Agent%20Challenge-UI%20Navigator-4285F4?logo=google&logoColor=white)](https://ai.google.dev/competition/gemini-live-agent-challenge)
[![CI](https://github.com/agentic-research/x-ray/actions/workflows/ci.yml/badge.svg)](https://github.com/agentic-research/x-ray/actions/workflows/ci.yml)

> **"Topology is the missing half of semantics."**

Powered by [`agentic-research/mache`](https://github.com/agentic-research/mache) — an agent-computer interface that projects structured data into virtual filesystems — X-Ray uses Gemini's multimodal vision to project any webpage into a clean, semantic filesystem. Agents navigate the DOM deterministically via `ls` and `cat` — no pixel guessing, no fragile HTML parsing.

## The Problem: LLMs Understand Semantics, but Lack Topology

When building web-navigating agents, the standard approach is to feed them raw HTML or rely on pixel-coordinate guessing. But HTML is a 1D delivery mechanism for browsers, not a semantic map for reasoning agents.

An LLM already knows what a "Checkout" button *means* (semantics), but when you dump a 10,000-line DOM tree into its context window, it loses all spatial awareness of *where* it is or how it relates to the elements around it (topology).

Finding a simple button might require navigating a brittle path like `/html/body/div[4]/main/section/div[2]/span/button`. Giving a voice-driven LLM tools to find that path is like asking someone to locate a specific book by reading the library's structural blueprints.

Worse, websites change constantly. A simple layout update breaks these locators, and static schemas can't map the entire internet.

## The Fix: Dynamic Semantic Projection

X-Ray bridges the physical reality of the DOM and the conceptual intent of the user using a Two-Stage Agent Architecture:

### Stage 1: The Cartographer (Vision + Structure)

When a user visits a page, the X-Ray Chrome extension injects a tiny `data-mache-id` into every interactive element. It captures a viewport screenshot and generates a flattened text summary of those tagged IDs.

This payload goes to Gemini. Because Gemini is natively multimodal, it instantly understands the visual layout (e.g., "The top bar is navigation, the left side is filters"). It outputs a strict JSON schema projecting these visual zones onto the tagged DOM nodes. For list zones, it also identifies the **primary items** — the main clickable element in each repeating item (e.g., story titles, product cards).

### Stage 2: The Navigator (Voice + Action)

That JSON schema feeds into the Mache Engine, which instantly mounts a virtual, in-memory filesystem tailored to that exact page.

Now, the Gemini Live agent doesn't see `div[4]/span/button`. It runs `ls /` and sees a clean, human-readable structure:

- `/header/global_nav/`
- `/main/trending_repositories/`
- `/footer/legal/`

When the user says, "Click the first trending repository," the Navigator traverses the filesystem using standard POSIX tools (`ls`, `cat`) and safely executes the action (`act`).

## Project Structure

```
x-ray/
├── .github/workflows/   # CI: test (Ubuntu + macOS) + lint
├── cmd/
│   ├── agentd/          # Main backend server (WebSocket + HTTP)
│   └── gate/            # Offline accuracy gate test
├── internal/
│   ├── api/             # WebSocket handler, message types, interfaces
│   ├── cartographer/    # Stage 1: Gemini Vision → semantic schema
│   ├── mache/           # Virtual filesystem engine
│   └── navigator/       # Stage 2: Gemini Tool-Use → browser actions
├── ext/                 # Chrome extension (content.js, background.js, CDP AX tree)
├── testdata/            # Captured page snapshots for gate tests
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
4. Send navigation intents via the `/navigate` HTTP endpoint:

```bash
curl -X POST http://localhost:8080/navigate \
  -H "Content-Type: application/json" \
  -d '{"intent": "click the first story"}'
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

The test suite covers three packages with 42 tests:

| Package | What's tested |
|---------|---------------|
| `internal/mache/` | Schema parsing, directory listing, file reading, mache-id resolution, children grouping (primary items + heuristic), BFS traversal, AX-enriched summary parsing, backward compat |
| `internal/navigator/` | `ExecuteTool` dispatch (ls/cat/act), default action, error paths, unknown tool; full HN-style integration chain (ls → cat children → act on child) |
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

## Task Commands

| Command | Description |
|---------|-------------|
| `task run` | Build, codesign, and run agentd |
| `task build` | Build and codesign the binary |
| `task test` | Run all tests (`go test -race -v ./...`) |
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

No Gemini API key or FUSE required — all 42 tests use mock interfaces.

## Architecture

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the full system diagram, data flow, and component descriptions.

### Highlights

- **Zero Hallucinated IDs**: The Cartographer is constrained to select from the pre-tagged `data-mache-id` set. Structured JSON output with a strict schema prevents the model from inventing non-existent DOM pointers.
- **Accessibility Tree Enrichment**: The extension captures the browser's computed accessibility tree via `chrome.debugger` + CDP (`Accessibility.getFullAXTree`). Each summary line is enriched with `AXRole` and `AXName` — the semantic truth the browser computes from implicit roles, CSS visibility, and ARIA attributes.
- **Semantic Filesystem**: Instead of brittle XPaths or coordinate guessing, the agent interacts with a logical directory structure mapped dynamically to the page in under 10 seconds.
- **LLM-Powered Item Grouping**: For list zones, the Cartographer identifies primary items (story titles, product cards) so ordinal counting ("click the 3rd story") works correctly across any site.
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
