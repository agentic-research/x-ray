# X-Ray: One-Pager

## What It Is

X-Ray is a voice-driven web navigation agent. It lets users control any webpage by speaking natural language commands ("click the 3rd story," "go to my cart," "scroll down"). It works on any site, with no per-site configuration.

## How It Works

```
User speaks → Gemini Live → Filesystem tools → Browser action
              (voice AI)    (ls, cat, act)      (click, focus)
```

**Two-stage architecture:**

1. **The Cartographer** — When the user opens a page, a Chrome extension tags every interactive element with a unique ID and captures a screenshot. Gemini Vision analyzes the screenshot and maps the page into 3-7 semantic zones (e.g., `/header/nav`, `/main/story_list`, `/footer/links`). For list zones, it also identifies the primary items (story titles, product cards).

2. **The Navigator** — The user's voice intent is processed by Gemini Live, which navigates a virtual filesystem built from the Cartographer's output using standard POSIX tools (`ls`, `cat`, `act`). It resolves the target element and sends a click/focus action back to the browser.

## Key Technical Decisions

| Decision | Why |
|----------|-----|
| Filesystem as interface | LLMs reason well over directory trees; token-efficient vs raw DOM |
| Vision for schema generation | Layout understanding requires seeing the page, not parsing HTML |
| Structured JSON output | Gemini cannot hallucinate IDs — constrained to the pre-tagged set |
| LLM-powered item grouping | Cartographer identifies primary items in lists, replacing fragile heuristics |
| Temperature 0.1 | Near-deterministic: same page + intent = same action |

## Architecture

```
Chrome Extension          Agentd Backend              Gemini API
┌─────────────┐     ┌──────────────────────┐     ┌────────────┐
│ Tag IDs     │────>│ Cartographer (Vision) │────>│ Gemini     │
│ Screenshot  │     │ Mache Engine (FS)     │<────│ 2.5 Flash  │
│ Execute act │<────│ Navigator (Tools)     │────>│            │
└─────────────┘     │ Voice proxy (Live)    │<───>│ Gemini     │
                    └──────────────────────┘     │ Live API   │
                                                  └────────────┘
```

## Performance

| Metric | Value |
|--------|-------|
| Schema generation (Cartographer) | ~3-5s |
| Filesystem construction | <1ms |
| Intent resolution (Navigator) | ~2-4s |
| End-to-end (voice command → browser action) | <10s |
| Hallucinated IDs | 0 (structured output constraint) |

## What Makes It Different

- **No per-site rules.** The Cartographer generates a fresh schema for every page using vision — no CSS selectors, no XPaths, no site-specific configuration.
- **Ordinal counting works.** "Click the 5th product" resolves correctly because the Cartographer identifies which elements are the primary items in a list, not just which elements exist.
- **Deterministic execution.** The agent doesn't guess pixel coordinates or hope an element is visible. It resolves a `data-mache-id` and dispatches a real DOM click.
- **Voice-native.** Built on Gemini Live API with real-time audio streaming. Not a text-to-speech wrapper — the agent speaks and listens natively.
- **Built on Mache.** X-Ray reuses [agentic-research/mache](https://github.com/agentic-research/mache), an existing agent-computer interface for projecting structured data into virtual filesystems. Mache was built for code ASTs, JSON, and SQLite — X-Ray extends it to the live DOM via dynamic schema generation.

## Stack

- **Backend:** Go, Gemini GenAI SDK, WebSocket
- **Extension:** Chrome Manifest V3 (3 files, no dependencies)
- **Hosting:** Google Cloud Run
- **Core dependency:** [agentic-research/mache](https://github.com/agentic-research/mache) (agent-computer interface via semantic filesystem projection)
