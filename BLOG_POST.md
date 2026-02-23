# Mache X-Ray: Navigating the Web with Semantic Filesystems

The standard approach to building web agents is to feed them raw HTML. But HTML is a delivery mechanism for browsers, not a semantic map for reasoning agents. When I started the Gemini Live Agent Challenge, I had a simple premise: LLMs shouldn't have to read it.

I'd already built Mache — a system that projects structured data (code ASTs, JSON, SQLite databases) into virtual filesystems for agent-computer interaction. For a coding agent, navigating `cd /functions/getAuth` is fundamentally better than parsing a 10,000-line syntax tree.

But when I tried to point Mache at the web (the DOM), I hit a wall.

### The Problem with the DOM

Raw HTML ASTs are deeply nested and full of semantic noise. A simple "Checkout" button might live at `/html/body/div[4]/main/section/div[2]/span/button`. Giving a voice-driven Gemini agent a `cd` and `ls` tool and asking it to find that path is like asking someone to find a book in a library by reading the structural blueprints of the building.

Worse, websites change constantly. A simple layout update breaks brittle XPaths. A static schema can't map "The Web" into Mache.

### The Breakthrough: Dynamic Semantic Projection

I needed a bridge between the physical reality of the DOM and the conceptual intent of the user. I needed a **Cartographer**.

I designed a Two-Stage Agent Architecture:

1. **Stage 1: The Cartographer (Vision + Structure)**
   When a user visits a page, a browser extension injects a tiny `data-mache-id` into every interactive element. It then takes a screenshot and generates a flattened text summary of those tagged elements.

   The *screenshot* and the *summary* go to a Gemini model with one question: "What are the 5 main semantic zones of this page?"

   Because Gemini is natively multimodal, it uses its vision to instantly understand the layout (e.g., "Ah, the top bar is navigation, the left side is filters"). It then outputs a strict JSON schema that maps these visual zones to the tagged IDs.

2. **Stage 2: The Navigator (Voice + Action)**
   That generated JSON schema feeds into Mache. Mache instantly projects a virtual filesystem tailored to that exact page.

   Now, the voice-driven Gemini Live agent doesn't see `div[4]/span/button`. It just runs `ls /` and sees:
   - `/header/global_nav/`
   - `/main/trending_repositories/`
   - `/footer/legal/`

   When the user says "Click the first trending repository", the Navigator easily finds the target in the clean filesystem and executes the action.

### The Hard Part: "Click the Third Story"

Getting an LLM to navigate to a zone is the easy part. The hard part is ordinal counting inside zones. When a user says "click the 3rd story," the agent needs to know which elements are stories and which are metadata — domain labels, upvote buttons, timestamps.

The first attempt used a heuristic: elements with empty text (upvote arrows, bullet icons) served as group delimiters. This worked for Hacker News, but it was fragile. Different sites have different patterns.

I solved this by pushing the problem back to the Cartographer. Since it already sees the screenshot and the DOM summary, I added a single instruction: *for list zones, identify the primary clickable element in each repeating item.* Instead of writing brittle heuristics to guess where one item ends and another begins, I just let the vision model use its eyes.

The Cartographer returns an array of `primary_items` — the mache_ids of the story titles, product cards, or search result links — and the engine uses those as group boundaries. The result: the `children` file for a zone shows clean, numbered groups:

```
--- Item 1 ---
  mache-42 | a | "Show HN: I built a database in Go"
  mache-43 | a | "(github.com)"
  mache-44 | span | "142 points"

--- Item 2 ---
  mache-47 | a | "Why We Switched to Postgres"
  mache-48 | a | "(blog.example.com)"
  mache-49 | span | "89 points"
```

When the voice agent counts "the 3rd story," it counts item groups — not individual links. This works across any site with repeating content, because the vision model understands what a "story" or "product" looks like, not just what the HTML structure happens to be.

### The 48-Hour Gate

To prove this wasn't just a toy demo, I built a 48-hour gate test. The criteria were strict:
- Test against 5 real, complex pages (Amazon, GitHub, Reddit, HackerNews, Wikipedia).
- Sub-10 second latency for generating the semantic map.
- **Zero hallucinated IDs.** The LLM could not invent pointers.

By restricting the LLM payload to just a flat summary of IDs and letting it rely entirely on its visual understanding for the layout, processing time dropped from over 3 minutes down to under 10 seconds. More importantly: zero hallucinations.

### Voice: The Final Mile

X-Ray supports full voice interaction via the Gemini Live API. Audio streams from the browser through the Go backend to Gemini Live, tool calls execute locally against the Mache filesystem, and Gemini's spoken response streams back for playback.

The entire pipeline — from "click the 3rd story" to the browser actually clicking it — takes under 10 seconds. The semantic filesystem is what makes this tractable: instead of the voice agent reasoning over raw DOM, it's navigating a handful of well-labeled directories.

This isn't a web scraper. It's a system that lets an AI *see* a website the way a human does, organize what it sees into a semantic map, and interact with it using the precise, deterministic tooling of a filesystem.
