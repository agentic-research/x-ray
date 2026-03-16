*This post was created for the purposes of entering the Gemini Live Agent Challenge hackathon. #GeminiLiveAgentChallenge*

# Building X-Ray: Teaching a Browser to Understand Itself

My son is seven. He's got a PICC line, a wound vac, and an NG tube — he's being treated for an NTM infection at Children's Hospital Colorado. Using a mouse isn't always easy for him. I wanted to build something where he could just say "play BB Blocks on YouTube" and the browser would do it.

That's the pitch. Here's what I actually built, and what I learned along the way.

## The Problem

Every AI browser agent I've seen works roughly the same way: take a screenshot, send it to a vision model, ask "where should I click?", get back pixel coordinates, click there, hope it worked. If it didn't, take another screenshot and try again.

This is expensive (a vision model call per action), slow (round-trip to the API each time), and fragile (pixel coordinates shift when the page reflows). I'd been working on [Mache](https://github.com/agentic-research/mache), a tool that projects structured data into filesystems for AI agents. Code ASTs become directories. SQLite tables become files. And then it hit me: the DOM is an AST too.

## The Idea

What if instead of asking "where do I click?", an agent could just browse the page like a filesystem?

```
$ ls /browser/main/
feed/          sidebar/       search/        text_index

$ cat /browser/main/feed/children
[1] a: Mistral Releases Leanstral
[2] a: Meta's renewed commitment to jemalloc
[3] a: The "small web" is bigger than you might think

$ act /browser/main/feed/_c/1 click
Executing click on mache-13
```

No coordinates. No guessing. The agent reads what's on the page, picks the element by content, and clicks it by ID. Deterministic.

## How It Works

X-Ray has three layers:

**1. The Chrome Extension** captures the page. It tags every interactive element with a stable `mache-ID`, records DOM ancestry paths, and enriches elements with accessibility metadata via CDP. The raw DOM summary is about 10KB — down from 200KB+ of raw HTML.

**2. The Cairn Cartographer** maps the page into zones. It extracts visual features from the screenshot (luminance, edge density, spatial frequency) and uses the Leech lattice — a 24-dimensional error-correcting code — to segment the page into 3-7 semantic regions. Header, main content, sidebar, footer. No vision model call needed. Pure math, running locally in Go.

**3. The Navigator** is a Gemini 2.5 Flash agent with four tools: `ls`, `cat`, `grep`, and `act`. It traverses the virtual filesystem to resolve intents. "Click the post about Nvidia" becomes: `grep("nvidia")` → finds `[mache-73] Nvidia Launches Vera CPU` → `act("mache-73", "click")`. Two tool calls, under 5 seconds.

Voice comes from **Gemini Live API**. The Talker handles conversation. The Doer runs navigation in the background. You speak, the browser moves, the voice tells you what happened.

## What Actually Worked

On Hacker News, saying "click the post about Nvidia" greps the page, finds the right link, and navigates the browser. It's fast and reliable. The filesystem abstraction means the agent doesn't need to understand HN's table-based layout — it just reads the children file and picks element #1.

The offline replay pipeline turned out to be surprisingly useful. I captured frozen page state (screenshot + DOM summary) and built a test harness that replays Navigator decisions without a browser. This cut iteration time from 55 seconds (live) to 7 seconds (replay). When the Navigator kept clicking "hide" instead of story titles, I could see exactly why in the tool trace and fix the prompt.

Site-specific shortcuts emerged naturally from the replay data. The analyze tool showed that YouTube search works best via direct URL (`youtube.com/results?search_query=X`) rather than typing into the search bar. These patterns are extracted from empirical data, not hardcoded.

## What Didn't Work (Yet)

YouTube is hard. The autocomplete dropdown, the SPA routing, the dynamic content loading — every step has a race condition. The Navigator would type a search query, YouTube would show suggestions, and the agent would click a suggestion instead of pressing Enter. Auto-submitting search inputs helped, but complex multi-step flows on dynamic sites are still unreliable.

The Gemini Live voice session drops occasionally (`websocket: close 1008 policy violation`), requiring automatic reconnection. ProactiveAudio — a feature that lets Gemini stay silent on irrelevant input — was causing complete silence during navigation because the model interpreted background task notifications as "irrelevant." Turning it off fixed voice output.

Full-page CDP screenshots were 7-15MB. Gemini returned malformed function calls when the image was too large. Capping to viewport-only and resizing to 768px (one Gemini tile, 258 tokens) fixed the reliability issue.

## The Stack

- **Go** for the server, cartographer, navigator, and CDP proxy
- **Chrome Extension** (Manifest V3) for DOM capture and action execution
- **Gemini 2.5 Flash** for the Navigator's tool-calling loop
- **Gemini Live API** for real-time voice
- **Google Cloud Run** for deployment (via ko + Terraform)
- **Mache** for the virtual filesystem engine
- **Leech lattice** for algebraic visual tokenization (the Cairn cartographer)

## What I'd Do Next

The system learns from its own failures. The replay → analyze → site primer pipeline already extracts winning navigation patterns from test data. The next step is closing that loop: log every tool chain during real browsing, analyze patterns automatically, update site primers without human intervention. The system gets better the more you use it.

I'd also separate the voice into two sessions — a conversational layer that's always responsive, and a navigation layer that runs tools in the background. Right now there's a gap between "I'll click that for you" and "Done" where the user hears nothing. A kid needs continuous engagement, not silence.

But for now: my son can say "click the post about Nvidia" and the browser navigates. That's a start.

---

*X-Ray is open source at [github.com/agentic-research/x-ray](https://github.com/agentic-research/x-ray). Built with Gemini 2.5 Flash, Gemini Live API, and Google Cloud Run for the Gemini Live Agent Challenge.*
