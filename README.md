# X-Ray: Gemini-Powered Web Navigation via Mache

**Agents shouldn't read raw HTML.**

Powered by [agentic-research/mache](https://github.com/agentic-research/mache), X-Ray uses Gemini to project any webpage into a clean, OS-level filesystem. Agents navigate via `cd` and `ls` — no pixel guessing.

---

## The Problem with the DOM

When building web-navigating agents, the standard approach is to feed them raw HTML or complex DOM structures. But HTML is a delivery mechanism for browsers, not a semantic map for reasoning agents.

Raw HTML ASTs are deeply nested and full of semantic noise. Finding a simple "Checkout" button might require navigating a brittle XPath like `/html/body/div[4]/main/section/div[2]/span/button`. Giving a voice-driven LLM agent tools to find that path is like asking someone to find a book in a library by reading the structural blueprints of the building.

Worse, websites change constantly. A simple layout update breaks these brittle locators. We realized we couldn't just write static schemas to map "The Web."

## The Breakthrough: Dynamic Semantic Projection

We needed a bridge between the physical reality of the DOM and the conceptual intent of the user. We designed a **Two-Stage Agent Architecture**:

### Stage 1: The Cartographer (Vision + Structure)
When a user visits a page, a browser extension injects a tiny `data-mache-id` into every interactive element. It then takes a screenshot and generates a flattened text summary of those tagged elements.

We send the *screenshot* and the *summary* to a Gemini model and ask it to identify the main semantic zones of the page. Because Gemini is natively multimodal, it uses its vision to instantly understand the layout (e.g., "Ah, the top bar is navigation, the left side is filters"). It then outputs a strict JSON schema that maps these visual zones to the tagged IDs.

### Stage 2: The Navigator (Voice + Action)
We feed that generated JSON schema into our Mache Engine. Mache instantly projects a virtual filesystem tailored to that exact page.

Now, our Gemini Live agent doesn't see `div[4]/span/button`. It just runs `ls /` and sees a clean semantic structure:
- `/header/global_nav/`
- `/main/trending_repositories/`
- `/footer/legal/`

When the user says "Click the first trending repository", the Navigator easily finds the target in the clean filesystem using standard tools (`ls`, `cat`) and executes the action (`act`).

## How it Works (Under the Hood)

X-Ray is built with a backend service (Agentd) and a browser extension.

1. **Browser Extension (`ext/`)**: Injects IDs, captures the DOM state, takes screenshots, and communicates with the backend via WebSockets.
2. **Backend Engine (`cmd/agentd/`)**:
   - **Mache Engine (`internal/mache/`)**: Takes the JSON schema and builds an in-memory virtual filesystem tree.
   - **Cartographer Agent (`internal/cartographer/`)**: The Gemini vision model that analyzes the screenshot and DOM summary to generate the semantic map.
   - **Navigator Agent (`internal/navigator/`)**: The Gemini agent equipped with filesystem tools (`ls`, `cat`, `act`) that interprets user intent and navigates the virtual Mache filesystem to execute actions.

## Getting Started

*(Instructions for running the backend and loading the extension will go here)*

### Prerequisites
- Go 1.25+
- A Gemini API Key (`GOOGLE_GEMINI_API_KEY` or `GOOGLE_API_KEY`)

### Running the Backend

1. Clone the repository and navigate to the project root.
2. Set your Gemini API key:
   ```bash
   export GOOGLE_API_KEY="your-api-key"
   ```
3. Run the agent daemon:
   ```bash
   go run cmd/agentd/main.go
   ```
   The server will start listening on `:8080`.

### Loading the Extension

1. Open Chrome or a Chromium-based browser.
2. Navigate to `chrome://extensions/`.
3. Enable "Developer mode" in the top right.
4. Click "Load unpacked" and select the `ext/` directory in this repository.

## Architecture Highlights

* **Zero Hallucinated IDs**: By restricting the Cartographer's output to a flat summary of IDs and relying on visual understanding for layout, X-Ray achieves high accuracy without inventing non-existent DOM pointers.
* **Semantic Filesystem (`mache`)**: Instead of relying on brittle XPaths, the agent interacts with a logical, human-readable directory structure mapped dynamically to the page.

---
*Built for the Gemini Live Agent Challenge.*
