# X-Ray: Gemini-Powered Web Navigation via Mache

[![Gemini Live Agent Challenge](https://img.shields.io/badge/Gemini%20Live%20Agent%20Challenge-UI%20Navigator-4285F4?logo=google&logoColor=white)](https://ai.google.dev/competition/gemini-live-agent-challenge)

**Agents shouldn't read raw HTML.**

Powered by [`agentic-research/mache`](https://github.com/agentic-research/mache), X-Ray uses Gemini's multimodal vision to project any webpage into a clean, semantic filesystem. Agents navigate the DOM deterministically via `ls` and `cat` — no pixel guessing, no fragile XPaths.

## The Problem: The DOM is Not an Interface

When building web-navigating agents, the standard approach is to feed them raw HTML or complex DOM structures. But HTML is a delivery mechanism for browsers, not a semantic map for reasoning agents.

Raw HTML ASTs are deeply nested and full of visual noise. Finding a simple "Checkout" button might require navigating a brittle path like `/html/body/div[4]/main/section/div[2]/span/button`. Giving a voice-driven LLM tools to find that path is like asking someone to locate a book by reading the library's architectural blueprints.

Worse, websites change constantly. A simple layout update breaks these locators. We realized we couldn't just write static schemas to map the entire internet.

## The Fix: Dynamic Semantic Projection

We needed a bridge between the physical reality of the DOM and the conceptual intent of the user. X-Ray solves this using a Two-Stage Agent Architecture:

### Stage 1: The Cartographer (Vision + Structure)

When a user visits a page, the X-Ray Chrome extension injects a tiny `data-mache-id` into every interactive element. It captures a viewport screenshot and generates a flattened text summary of those tagged IDs.

We send this payload to Gemini. Because Gemini is natively multimodal, it instantly understands the visual layout (e.g., "The top bar is navigation, the left side is filters"). It outputs a strict JSON schema projecting these visual zones onto the tagged DOM nodes.

### Stage 2: The Navigator (Voice + Action)

We feed that JSON schema into our Mache Engine, which instantly mounts a virtual, in-memory filesystem tailored to that exact page.

Now, our Gemini Live agent doesn't see `div[4]/span/button`. It runs `ls /` and sees a clean, human-readable structure:

- `/header/global_nav/`
- `/main/trending_repositories/`
- `/footer/legal/`

When the user says, "Click the first trending repository," the Navigator traverses the filesystem using standard POSIX tools (`ls`, `cat`) and safely executes the action (`act`).

## Project Structure

```
x-ray/
├── cmd/
│   ├── agentd/          # Main backend server (WebSocket + HTTP)
│   └── gate/            # Offline accuracy gate test
├── internal/
│   ├── api/             # WebSocket handler, message types
│   ├── cartographer/    # Stage 1: Gemini Vision → semantic schema
│   ├── mache/           # Virtual filesystem engine
│   └── navigator/       # Stage 2: Gemini Tool-Use → browser actions
├── ext/                 # Chrome extension (content.js, background.js)
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

## Architecture

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the full system diagram, data flow, and component descriptions.

### Highlights

- **Zero Hallucinated IDs**: The Cartographer is constrained to select from the pre-tagged `data-mache-id` set. Structured JSON output with a strict schema prevents the model from inventing non-existent DOM pointers.
- **Semantic Filesystem**: Instead of brittle XPaths or coordinate guessing, the agent interacts with a logical directory structure mapped dynamically to the page in under 10 seconds.
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

## License

MIT

---

*Built for the [Gemini Live Agent Challenge](https://ai.google.dev/competition/gemini-live-agent-challenge) — UI Navigator category.*
