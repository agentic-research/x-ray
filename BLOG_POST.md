# Mache X-Ray: Navigating the Web with Semantic Filesystems

When we started the Gemini Live Agent Challenge, we had a simple premise: LLMs shouldn't have to read raw HTML. HTML is a delivery mechanism for browsers, not a semantic map for reasoning agents.

Our first attempt at a solution was Mache—a system designed to project structured data (like code ASTs or JSON) into a virtual filesystem. For a coding agent, navigating `cd /functions/getAuth` is fundamentally better than parsing a 10,000-line syntax tree.

But when we tried to point Mache at the web (the DOM), we hit a wall.

### The Problem with the DOM
Raw HTML ASTs are deeply nested and full of semantic noise. A simple "Checkout" button might live at `/html/body/div[4]/main/section/div[2]/span/button`. Giving a voice-driven Gemini agent a `cd` and `ls` tool and asking it to find that path is like asking someone to find a book in a library by reading the structural blueprints of the building.

Worse, websites change constantly. A simple layout update breaks brittle XPaths. We realized we couldn't just write a static schema to map "The Web" into Mache.

### The Breakthrough: Dynamic Semantic Projection
We needed a bridge between the physical reality of the DOM and the conceptual intent of the user. We needed a **Cartographer**.

We designed a Two-Stage Agent Architecture:

1. **Stage 1: The Cartographer (Vision + Structure)**
   When a user visits a page, a browser extension injects a tiny `data-mache-id` into every interactive element. It then takes a screenshot and generates a flattened text summary of those tagged elements.

   We send the *screenshot* and the *summary* to a Gemini model and ask it one question: "What are the 5 main semantic zones of this page?"

   Because Gemini is natively multimodal, it uses its vision to instantly understand the layout (e.g., "Ah, the top bar is navigation, the left side is filters"). It then outputs a strict JSON schema that maps these visual zones to the tagged IDs.

2. **Stage 2: The Navigator (Voice + Action)**
   We feed that generated JSON schema into Mache. Mache instantly projects a virtual filesystem tailored to that exact page.

   Now, our voice-driven Gemini Live agent doesn't see `div[4]/span/button`. It just runs `ls /` and sees:
   - `/header/global_nav/`
   - `/main/trending_repositories/`
   - `/footer/legal/`

   When the user says "Click the first trending repository", the Navigator easily finds the target in the clean filesystem and executes the action.

### The 48-Hour Gate
To prove this wasn't just a toy demo, we built a 48-hour gate test. The criteria were strict:
- Test against 5 real, complex pages (Amazon, GitHub, Reddit, HackerNews, Wikipedia).
- Sub-10 second latency for generating the semantic map.
- **Zero hallucinated IDs.** The LLM could not invent pointers.

By restricting the LLM payload to just a flat summary of IDs and letting it rely entirely on its visual understanding for the layout, we dropped processing time from over 3 minutes down to under 10 seconds. More importantly, we achieved zero hallucinations.

We didn't just build a web scraper. We built a system that lets an AI *see* a website the way a human does, and interact with it using the precise, deterministic tooling of a filesystem.
