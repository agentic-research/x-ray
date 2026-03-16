# X-Ray: Voice-Controlled Browser Navigation with Algebraic Vision

## Inspiration

My seven-year-old son has a PICC line, a wound vac, and an NG tube. Using a mouse isn't always easy. I wanted to build something he could use — say "play BB Blocks on YouTube" and have the browser just do it. No mouse, no keyboard, just his voice.

Every AI browser agent has the same problem: the browser has no API for what you're looking at. Current approaches either blind-click by coordinates or burn a vision model call on every screenshot. That's slow, expensive, and fragile. We built something different.

## What it does

X-Ray lets you control any website with your voice through Gemini Live. Say "click the first story" on Hacker News, and the browser navigates. Say "search for Minecraft" on YouTube, and it types, searches, and finds results. The system understands page structure algebraically — no vision model needed for layout comprehension.

The key innovation: X-Ray maps every web page into a virtual filesystem. Headers, navigation bars, content feeds, sidebars — they all become directories you can read, search, and act on. The agent navigates this filesystem the same way you'd navigate code: `ls`, `cat`, `grep`, `act`.

## How we built it

**Chrome Extension (MV3):** Captures the DOM, registers interactive elements with stable mache-IDs, and executes actions (click, type, scroll) via the Chrome DevTools Protocol.

**Go Server:** Orchestrates the full pipeline — CDP screenshot capture, accessibility tree enrichment, cartography, schema generation, and Navigator tool dispatch.

**Cairn Cartographer:** The algebraic vision system. Extracts 12-dimensional feature vectors from the screenshot (luminance, color-opponent, edge density, spatial frequency) and projects them through the Leech lattice — a 24-dimensional error-correcting code originally designed for deep space communication. The result: instant, deterministic zone segmentation with zero token cost. No VLM, no API call, pure math.

**Gemini Live:** Handles real-time voice interaction. The Talker stays responsive while the Doer runs navigation in the background. Screen sharing sends the current page to Gemini for multimodal reasoning.

**Gemini REST (2.5 Flash):** Powers the Navigator's tool-calling loop. The Navigator reads the virtual filesystem, greps for elements, and dispatches click/type/scroll actions through the extension.

**Virtual Filesystem (mache):** Every page zone becomes a directory with `children`, `description`, `mache_id`, and `text_index` files. The Navigator operates on this abstraction — not raw HTML, not pixel coordinates.

## Architecture

```
Voice Input → Gemini Live (Talker) → Doer (Go orchestrator)
                                        ↓
                                   Navigator (Gemini 2.5 Flash)
                                        ↓
                               Virtual Filesystem (mache)
                                   ↑           ↓
                        Cairn Cartographer   Chrome Extension
                        (Leech lattice)      (CDP + DOM)
```

Deployed on Google Cloud Run via ko + Terraform.

## Challenges we ran into

- **YouTube's autocomplete dropdown** confused the Navigator into clicking suggestions instead of pressing Enter. We solved this by auto-submitting search inputs and using direct search URLs (`youtube.com/results?search_query=X`).

- **Full-page screenshots** were 7-15MB, causing Gemini to return malformed function calls. We capped CDP capture to viewport-only and resize to 768px for optimal Gemini token cost (258 tokens per tile).

- **The CDP "Another debugger attached" error** would cascade-fail all subsequent captures. We added force-detach before every attach.

- **Intent classification**: The Navigator would describe what it saw instead of clicking. We added an action intent guard — when the user says "play" or "click," the Doer rejects text responses and pushes the Navigator to act.

## Accomplishments that we're proud of

- **Zero vision model cost for page understanding.** Cairn maps pages using the Leech lattice — the same error-correcting code used in deep space communication. No VLM, no per-screenshot API call, pure algebraic geometry.

- **Offline replay testing pipeline.** We captured frozen page state (screenshot + DOM summary) and built a replay harness that tests Navigator decisions in ~7 seconds without a browser. This let us iterate on the Navigator prompt 10x faster than live testing.

- **Empirical site primer extraction.** The analyze-replay tool reads test results, ranks tool chains by pass rate, and outputs site-specific shortcuts. The system learns which navigation patterns work from its own data.

- **The click actually works.** On Hacker News: grep("nvidia") → finds mache-73 → el.click() → browser navigates to the article. Voice says "Clicking the post about Nvidia." Under 5 seconds.

## What we learned

- Gemini's `FunctionCallingConfigModeAny` forces tool calls but breaks result reporting — the model needs to be able to answer sometimes.
- ProactiveAudio on Gemini Live causes silence when combined with background task notifications — the model interprets system messages as "irrelevant" and suppresses audio.
- Screenshot resolution has a sweet spot: 768px = 258 tokens (one Gemini tile). Going wider adds tiles and latency without improving Navigator decisions.
- The math friend was right: Tropical cartography produces zones that are maximally different (good for LLM disambiguation), while Cairn produces zones that are geometrically precise (good for visual accuracy). The ideal is a hybrid.

## What's next

- **Live learning loop:** Log every Navigator tool chain during real browsing, analyze patterns, auto-update site primers. The system gets faster the more you use it.
- **Two-session voice:** Separate the conversational layer (always listening, always responding) from the navigation layer (tool execution). Kids need continuous engagement, not silence.
- **Site-specific shortcuts via OpenSearch:** Discover search URL templates from `<link rel="search">` metadata instead of hardcoding per-site rules.
- **Rosary orchestration:** Use our agent orchestrator to run replay tests, analyze results, and update Navigator prompts automatically — a meta-learning loop where the system improves itself.

## Built With

- Go (server, cartographer, navigator, CDP proxy)
- Chrome Extension (MV3, TypeScript)
- Gemini 2.5 Flash (navigator tool calling)
- Gemini Live API (real-time voice)
- Google Cloud Run (deployment via ko + Terraform)
- Leech lattice / algebraic geometry (Cairn cartographer)
- mache (virtual filesystem, graph projection)
