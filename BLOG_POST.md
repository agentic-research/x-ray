# X-Ray: Real-Time Voice Browsing with Zero-Cost Page Understanding

*Created for the Gemini Live Agent Challenge #GeminiLiveAgentChallenge*

You can talk to your browser. Say "click the third story" and it clicks it. Say "go to the settings page" and it navigates. Say "what am I looking at?" and it tells you. Real-time, hands-free, voice-first web browsing — powered by Gemini Live.

But the interesting part isn't the voice. It's how the system *sees* the page.

### The Problem: Web Agents Are Blind (or Expensive)

Every web agent today faces the same bottleneck: understanding what's on the screen. The standard approaches are:

- **Feed raw HTML to an LLM.** Slow, expensive, and the model hallucinates element references it can't actually click.
- **Use a Vision Language Model** to look at a screenshot. Better, but $0.003-0.01 per page, 1-3 seconds of latency, and non-deterministic — the same page gets different results every time.

For a real-time voice agent, neither works. You can't wait 3 seconds for the model to figure out the page layout every time you scroll. And you can't afford $0.01 per page view in a system that might rescan dozens of times per session.

### The Solution: Page Understanding Without a Model

X-Ray understands web pages using signal processing and graph algorithms instead of a vision model. Zero API calls. ~50ms. Deterministic.

Here's how it works:

1. **A Chrome extension tags every interactive element** with a stable ID (`data-mache-id`), takes a screenshot, and sends both to a Go backend.

2. **The backend samples the actual pixels** at each element's center (RGB color) and runs FFT analysis on canvas/WebGL regions to detect repeating visual patterns (grids, lists, table rows).

3. **Five different "fibers" of information** are attached to each element: where it is on the page (spatial), what color it is (visual), where it sits in the DOM tree (structural), what kind of element it is (semantic), and what visual patterns surround it (frequency).

4. **A phylogenetic clustering algorithm** (neighbor-joining, borrowed from computational biology) groups these elements into 3-7 semantic zones — "header," "main content," "sidebar," "footer" — based on which elements are most similar across all five dimensions.

5. **The zones become a virtual filesystem.** Instead of raw HTML, the voice agent sees:
   ```
   /header/global_nav/
   /main/story_list/
   /sidebar/trending/
   /footer/legal/
   ```

The voice agent doesn't parse HTML. It runs `ls` and `cat` on a clean filesystem. When you say "click the third story," it navigates to `/main/story_list/_c/3/` and executes the click.

### The 48-Hour Gate

The first commit in this repo — [5329b00](https://github.com/agentic-research/x-ray/commit/5329b00) — landed on February 22, 2026 at 4:06 PM. It was a 9,336-line, 38-file drop with validated test fixtures for five sites (Hacker News, GitHub, Wikipedia, Lobsters, eBay). By the next evening, 44 commits later, the system had a working Chrome extension, WebSocket pipeline, Gemini Live voice integration, per-tab sessions, accessibility tree enrichment, scroll handling, and a Terraform deploy config.

The original Cartographer was a Gemini VLM call — it worked, but at $0.003-0.01 per page with 1-3 seconds of latency. Five days later, the TropicalCartographer replaced it entirely: same accuracy, ~50ms, zero API cost, deterministic. The rest of the time has been hardening concurrency, fixing real bugs found by running it on live sites, and building the sheaf-based cache that makes revisits free.

### Why This Matters for Voice

The Gemini Live API gives you real-time bidirectional audio — you talk, it responds, continuously. But a voice agent that takes 3 seconds to understand each page creates awkward silences that break the conversational flow.

X-Ray uses a **Talker/Doer architecture**:

- **The Talker** (Gemini Live) is always listening and always responsive. It has three instant tools: check what the Doer is working on, issue a new command, or cancel a running task. These execute in microseconds — no I/O, no waiting.

- **The Doer** runs multi-step navigation tasks in the background. It reads the filesystem, plans a path, executes clicks and scrolls, waits for the page to settle, and reports back.

The result: you can interrupt, redirect, or ask questions mid-task without breaking the agent's flow. "Go to Amazon and find headphones" starts executing immediately. Mid-search, you can say "actually, make those wireless" and the Doer adjusts.

### The Cache: Pages Don't Change That Much

Most of a web page stays the same when you scroll or click a tab. The header doesn't move. The sidebar doesn't reorganize. Only the main content area changes.

X-Ray caches zone segmentations per-URL with content fingerprints for each zone. When you revisit a page or scroll within one, only the zones that actually changed get regenerated. The rest load from cache instantly. On a warm cache hit, page understanding is effectively free.

### Canvas and WebGL: Seeing Beyond the DOM

Standard web agents are completely blind to `<canvas>` elements — Google Maps, Figma, data visualizations, video players. There's no DOM inside a canvas. It's just pixels.

X-Ray runs Canny edge detection on the screenshot to find rectangular UI regions inside canvas elements, then applies FFT analysis to characterize their visual structure. These detected regions participate in the same clustering algorithm as regular DOM elements, so the voice agent can interact with canvas-based UIs that are invisible to every other web agent.

### What's Next

The same approach — pixel sampling, FFT, phylogenetic clustering — works on any rectangular region of pixels. The browser is just the first target. A desktop agent that understands native application windows, remote desktop sessions, or game UIs is the same pipeline pointed at a different screenshot source.

X-Ray is open source at [github.com/agentic-research/x-ray](https://github.com/agentic-research/x-ray).

---

*X-Ray uses the Gemini Live API for real-time voice interaction, the Gemini GenAI Go SDK for the Navigator agent, and Google Cloud Run for deployment.*
