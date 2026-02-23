# 🩻 X-Ray: Gemini-Powered Web Navigation via Mache
**Agents shouldn't read raw HTML.**

Powered by `agentic-research/mache`, X-Ray uses Gemini's multimodal vision to project any webpage into a clean, OS-level filesystem. Agents navigate the DOM deterministically via `cd` and `ls`—no pixel guessing, no fragile XPaths.

## 🛑 The Problem: The DOM is Not an Interface
When building web-navigating agents, the standard approach is to feed them raw HTML or complex DOM structures. But HTML is a delivery mechanism for browsers, not a semantic map for reasoning agents.

Raw HTML ASTs are deeply nested and full of visual noise. Finding a simple "Checkout" button might require navigating a brittle path like `/html/body/div[4]/main/section/div[2]/span/button`. Giving a voice-driven LLM tools to find that path is like asking someone to locate a book by reading the library's architectural blueprints.

Worse, websites change constantly. A simple layout update breaks these locators. We realized we couldn't just write static schemas to map the entire internet.

## 🌉 The Fix: Dynamic Semantic Projection
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

## ⚙️ How it Works (Under the Hood)
X-Ray is built with a Go backend service (Agentd) and a Chrome extension.

- **Browser Extension (`ext/`)**: Injects IDs, captures the DOM state, takes screenshots, and streams payloads to the backend via WebSockets.
- **Backend Engine (`cmd/agentd/`)**:
  - **Mache Engine (`internal/mache/`)**: Takes the JSON schema and builds the in-memory virtual filesystem tree.
  - **Cartographer Agent (`internal/cartographer/`)**: The Gemini vision model that analyzes the screenshot and DOM summary to generate the semantic map.
  - **Navigator Agent (`internal/navigator/`)**: The Gemini agent equipped with filesystem tools (`ls`, `cat`, `act`) that interprets user intent and navigates the virtual Mache filesystem.

## 🚀 Getting Started
### Prerequisites
- Go 1.25+
- A Gemini API Key (`GOOGLE_GEMINI_API_KEY` or `GOOGLE_API_KEY`)

### 1. Running the Backend
Clone the repository and navigate to the project root:

```bash
export GOOGLE_API_KEY="your-api-key"
go run cmd/agentd/main.go
```
The server will start listening for WebSocket connections on `:8080`.

### 2. Loading the Extension
1. Open Chrome or a Chromium-based browser.
2. Navigate to `chrome://extensions/`.
3. Enable **Developer mode** in the top right corner.
4. Click **Load unpacked** and select the `ext/` directory in this repository.

## 🧠 Architecture Highlights
- **Zero Hallucinated IDs**: By restricting the Cartographer's output to a flat summary of IDs and relying entirely on its visual understanding of the layout, X-Ray achieves high accuracy without inventing non-existent DOM pointers.
- **Semantic Filesystem (`mache`)**: Instead of relying on brittle XPaths or abstract coordinate guessing, the agent interacts with a logical, deterministic directory structure mapped dynamically to the page in under 10 seconds.

---
*Built for the Gemini Live Agent Challenge.*
