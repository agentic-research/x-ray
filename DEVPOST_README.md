# X-Ray: See Through the Web

> **The web is a graph. Browsers render it as pixels. X-Ray renders it as a filesystem.**

X-Ray is a hybrid edge-cloud browser agent that projects the chaotic, modern web — React, SPAs, Shadow DOMs, virtual scroll — into a clean, deterministic POSIX filesystem. A local 7B-parameter model navigates Reddit flawlessly using `ls`, `cat`, and `act`. No pixel guessing. No brittle DOM paths. No API costs for execution.

**3.5 seconds. Two tool calls. One correct click on Reddit.**

---

## Inspiration

I've spent the last year watching AI agents fail at the web — not in interesting ways, but in stupid ways. An agent tries to click a button. It parses 10,000 lines of minified HTML. It guesses a CSS selector. The selector breaks because React re-rendered. The agent retries. Three more wrong guesses. Timeout.

This is the same problem I wrote about in [*The IDE Solved This Twenty Years Ago*](https://jamestexas.medium.com/the-ide-solved-this-twenty-years-ago-876edc7cec76?source=friends_link&sk=524725415db295b95ed414172c0572bc): we keep giving agents raw, unstructured data and wondering why they fail. The IDE solved this for humans twenty years ago — it built a graph-aware layer between the developer and the code. Syntax highlighting, symbol resolution, red squigglies before you save. The filesystem stayed flat, but the *interface* became structured.

The web agent problem is identical. An LLM already knows what a "trending post" *means* (semantics), but when you dump a raw DOM into its context window, it loses all sense of *where* things are and how they relate to each other (topology). Finding a simple link might require navigating `html > body > div[4] > main > shreddit-app > div > div > article:nth-child(5) > shreddit-post > a.block`. Giving an AI tools to find that path is like asking someone to locate a book by reading the library's structural blueprints.

**The thesis**: don't build smarter agents. Build smarter interfaces. Project the complex graph into the medium agents already understand — the filesystem — and even a tiny model can navigate it.

This is [Mache](https://github.com/agentic-research/mache): the Agent-Computer Interface. X-Ray is Mache applied to the web.

---

## What It Does

X-Ray turns any webpage into a navigable filesystem. The Chrome extension builds an in-memory element registry, draws colored Set-of-Mark bounding boxes over every interactive element, and captures a screenshot — giving the Cartographer (Gemini) visual anchors that tie spatial zones to element IDs:

![Set-of-Mark overlay on Reddit](overlay.jpg)

When a user visits Reddit, the agent sees:

```
/
├── header/
│   └── nav/                    # Global navigation
├── main/
│   └── feed/
│       ├── description         # "Main content feed of Reddit posts"
│       ├── children            # [1] "First post title"
│       │                       # [2] "Second post title"
│       │                       # ...
│       └── _c/
│           ├── 1/              # Ordinal child — no raw IDs exposed
│           │   ├── mache_id    # Internal element reference
│           │   ├── tag         # "a"
│           │   └── text        # "First post title"
│           ├── 2/
│           └── ...
├── sidebar/
│   └── recent_posts/           # Sidebar widgets
└── footer/
    └── links/                  # Legal links
```

The user says: *"Click the 5th post."*

The local model runs two commands:
1. `cat("/main/feed/children")` — reads the ordinal list
2. `act("/main/feed/_c/5", "click")` — clicks the element

**3.5 seconds. Zero cloud API calls for execution. Works on Reddit, Hacker News, GitHub, Wikipedia, eBay.**

### Voice Mode

X-Ray supports real-time voice interaction via the **Gemini Live API**. The user speaks naturally, Gemini handles voice activity detection and conversational flow, and X-Ray's filesystem tools execute actions in the browser — all streamed bidirectionally over WebSocket with sub-second latency.

For example: the user says, *"Scroll down and find the post about Python."* Gemini Live understands the intent, X-Ray's tools silently execute `scroll("down")` and `cat("/main/feed/children")` in the background, and Gemini replies aloud: *"I found it — clicking now"* — as the browser navigates to the post. The user never sees a terminal, a command, or a loading spinner. It just works.

---

## How I Built It

### The Two-Stage Architecture

**Stage 1: The Cartographer (Cloud Vision)**

When the user clicks the X-Ray extension icon, the Chrome extension:
- Builds an in-memory registry of interactive elements (no DOM mutation — SPA-safe)
- Draws colored Set-of-Mark bounding boxes over every element
- Captures a screenshot with the overlay visible
- Sends the screenshot + a structured element summary to the backend

Gemini 2.5 Flash receives both the visual screenshot and the text summary. Because it's natively multimodal, it instantly understands the spatial layout ("the top bar is navigation, the main area is a feed of posts, the right side is a sidebar"). It outputs a strict JSON schema mapping visual zones to DOM elements, including:
- **Primary items**: the specific clickable elements in each list zone (post titles, not metadata links)
- **CSS `item_selector`**: a structural query for discovering new content after scroll

**Stage 2: The Navigator (Local Edge Execution)**

The JSON schema feeds into the Mache Engine, which builds an in-memory virtual filesystem. Now the execution loop is trivially simple — a local 7B model (Qwen 2.5 Coder) uses four bash-like tools:

| Tool | Description |
|------|-------------|
| `ls(path)` | List directory contents |
| `cat(path)` | Read a file |
| `act(path, action)` | Click or focus an element |
| `scroll(direction)` | Scroll to load more content |

The model sees the full filesystem tree upfront (pre-filled via a tree dump), reads the children file, and acts. Two calls. No hallucinated paths. No wasted iterations.

### The Hybrid Edge-Cloud Split

This is the key architectural insight: **the hard part and the easy part require different tools.**

| Capability | Where | Why |
|-----------|-------|-----|
| Visual page mapping | Gemini Cloud | Requires multimodal spatial reasoning |
| Voice interaction | Gemini Live API | Requires real-time audio streaming |
| Navigation execution | Local 7B SLM | Simple text-based filesystem traversal |

Gemini's vision is so good at building the filesystem abstraction that even a 7-billion-parameter local model can navigate it blindfolded. The complex visual web → cloud. The simple filesystem traversal → edge. Zero latency, high privacy, no API costs for the execution loop.

### Context-Limit Bypassing

When the local model hits a limit (e.g., a feed has 300 posts but only 25 are visible), the agent calls `scroll("down")`. The browser scrolls, evaluates the Cartographer's CSS selector via native `querySelectorAll`, discovers fresh elements, and updates the children file — all without another LLM call. The agent re-reads the file and continues.

This hybrid keeps the LLM for pattern recognition and delegates execution to browser-native APIs.

### SPA Compatibility (The Registry)

Traditional browser agents mutate the DOM (adding `data-` attributes), which triggers React/Vue re-renders and breaks SPAs like Reddit. X-Ray uses an **in-memory element registry** — two JavaScript Maps (`elementID → Element` and `Element → elementID`) — with zero DOM mutation. The page never knows it's being observed.

---

## Challenges I Ran Into

### Reddit's Web Components Are a Nightmare

Reddit uses deeply nested Web Components (`<shreddit-post>`, `<shreddit-comment-tree>`), virtual scroll containers, and dynamically loaded content. Every standard approach breaks:

- **DOM paths are meaningless** — elements shift with every scroll
- **CSS selectors are fragile** — `article.w-full a.block` matches both post titles AND subreddit name links, filling the children file with noise
- **Static element IDs are ephemeral** — React re-renders assign new IDs to the same visual elements

**How I solved it: CSS Selector Unions.**

The Cartographer (Gemini) visually identifies primary items and writes a CSS selector. The browser evaluates it. But sometimes Gemini finds 25 items visually while the CSS selector only matches 1 (brittle selector). And sometimes the CSS selector finds 30 items while Gemini only listed 3 (lazy visual pass).

The fix: **union, don't overwrite.** Both lists are merged, deduplicated, with stale IDs filtered out. The LLM's visual intelligence handles the weird SPA edge cases, and the CSS selector handles infinite scroll and lazy LLM edge cases. Best of both worlds.

I also tightened the Cartographer prompt to require selectors that match **exactly one element per item** (the title link, not metadata), using child combinators (`>`) instead of broad descendant selectors.

### Small Models Guess Zone Names

The initial tree dump was a flat `ls("/")` showing top-level directories. A 4B model would see `main/` and guess the zone was called `/main/story_list` (from the system prompt example) when the actual path was `/main/feed`. It would then loop 8 times on 404 errors.

**Fix:** Pre-fill the full filesystem tree (3 levels deep, skipping `_c/` internals) into the model's conversation history. The model sees every zone name upfront and never guesses.

### The "arguments" vs "parameters" Wire Format

Different model families output function calls in different JSON formats. Gemma uses `{"name": "ls", "parameters": {"path": "/"}}`. Qwen uses `{"name": "ls", "arguments": {"path": "/"}}`. The regex parser now accepts both, making the Navigator model-agnostic.

---

## Accomplishments I'm Proud Of

- **3.5-second Reddit navigation** with a local 7B model — two tool calls, correct click, zero cloud API costs for execution
- **Zero DOM mutation** — works on React, Vue, and Web Component-heavy SPAs without triggering re-renders
- **Set-of-Mark visual grounding** — colored bounding boxes visible in screenshots give the Cartographer spatial anchors
- **The filesystem abstraction works** — projecting complex UI into POSIX paths makes even tiny models effective navigators
- **CSS selector unions** — a novel approach that combines LLM visual intelligence with browser-native `querySelectorAll` for robust item discovery
- **Model-agnostic Navigator** — tested with Gemini Flash, Gemma 12B, Qwen 2.5 Coder 7B, and Llama 3.2 3B. The filesystem abstraction is the constant; the model is a variable
- **Voice mode via Gemini Live API** — real-time bidirectional audio with server-side tool execution, audio suppression during tool loops, and sub-second latency

---

## What I Learned

The core thesis from the [Mache article](https://jamestexas.medium.com/the-ide-solved-this-twenty-years-ago-876edc7cec76?source=friends_link&sk=524725415db295b95ed414172c0572bc) held up in practice: **the interface matters more than the model.** When I gave a 0.6B model the raw filesystem, it failed completely — tried to `cat` directories, confused file paths with directory paths, never learned from errors. When I gave a 7B model the same filesystem with a pre-filled tree dump and ordinal paths, it solved the task in two calls.

The difference wasn't intelligence. It was interface design.

---

## What's Next for X-Ray

- **FUSE mount** — expose the virtual filesystem as a real mount point so any terminal tool (`grep`, `find`, `tree`) works natively against a live webpage. Developers could write simple bash scripts to automate web tasks without complex browser automation frameworks like Playwright
- **Multi-page workflows** — chain navigations across pages ("search for X, click the first result, add to cart")
- **Write actions** — form filling, text input, drag-and-drop via the same filesystem metaphor
- **Mache for the web** — generalize the schema format so any web agent framework can consume X-Ray's output
- **Fine-tuned Navigator** — train a purpose-built tiny model on filesystem navigation traces, potentially bringing the execution loop down to sub-1B parameters

---

## Built With

- **Google Gemini 2.5 Flash** — Cartographer vision + structured output
- **Google Gemini Live API** — Real-time voice interaction
- **Google Cloud Run** — Backend hosting
- **Go** — Backend (agentd)
- **Chrome Extension (Manifest V3)** — DOM analysis, Set-of-Mark overlay, action execution
- **Mache** — Virtual filesystem engine
- **Ollama + Qwen 2.5 Coder 7B** — Local Navigator execution

---

## Try It

```bash
git clone https://github.com/agentic-research/x-ray.git
cd x-ray
export GEMINI_API_KEY="your-key"

# Cloud-only (Gemini for everything):
task run

# Hybrid edge-cloud (local Navigator):
export NAVIGATOR_ENDPOINT=http://localhost:11434/v1
export NAVIGATOR_MODEL=qwen2.5-coder:7b
export NAVIGATOR_FORMAT=gemma
task run
```

Load the `ext/` directory as an unpacked Chrome extension, visit any page, and click the X-Ray icon.

---

*Built for the [Gemini Live Agent Challenge](https://ai.google.dev/competition/gemini-live-agent-challenge) — UI Navigator category.*
