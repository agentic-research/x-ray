# ADR 2: Dynamic Semantic Projection for UI Navigation

**Status:** Proposed
**Date:** 2026-02-22
**Author:** James
**Deadline:** 2026-03-16 (22 days)

## Context
Following the initial proposal to use Mache to project the DOM into a filesystem for the Gemini Live Agent Challenge, we identified a critical UX flaw: raw HTML ASTs are deeply nested and full of semantic noise (`/html/body/div[4]/span/button`). Navigating this directly is slow and token-heavy for an LLM.

However, Mache's architecture decouples parsing from projection via its Schema layer.
Instead of hardcoding a static HTML schema, we can leverage Gemini's multimodal capabilities (Vision + Text) to dynamically generate a Mache topology schema on the fly. This maps a messy DOM into a clean, intent-based filesystem (e.g., `/checkout/submit_button`) tailored specifically to the current page state.

## Decision
Implement a Two-Stage Agent Architecture using dynamic schema generation.

## Architecture
Plaintext
┌──────────────────────────────────────────────────────────────┐
│                      Browser Extension                       │
│  1. Inject data-mache-id into all interactive nodes          │
│  2. Capture screenshot + raw HTML string                     │
└──────────────────────┬───────────────────────────────────────┘
                       │ WebSocket (Screenshot + HTML)
                       ▼
┌──────────────────────────────────────────────────────────────┐
│                 ADK Agent App (Cloud Run)                    │
│                                                              │
│  ┌────────────────────────────────────────────────────────┐  │
│  │ STAGE 1: The Cartographer (Gemini Pro)                 │  │
│  │ - Inputs: Screenshot + DOM HTML                        │  │
│  │ - Task: Identify semantic zones (nav, cart, main)      │  │
│  │ - Output: Mache Topology Schema (JSON) mapping raw     │  │
│  │   DOM paths/IDs to a clean semantic filesystem.        │  │
│  └───────────────────┬────────────────────────────────────┘  │
│                      │ Generated Schema                      │
│                      ▼                                       │
│  ┌────────────────────────────────────────────────────────┐  │
│  │ Mache Engine                                           │  │
│  │ - Parses raw HTML via Tree-sitter                      │  │
│  │ - Applies the dynamic schema                           │  │
│  │ - Mounts: /page/nav/, /page/cart/, /page/main/         │  │
│  └───────────────────┬────────────────────────────────────┘  │
│                      │ Semantic Filesystem                   │
│                      ▼                                       │
│  ┌────────────────────────────────────────────────────────┐  │
│  │ STAGE 2: The Navigator (Gemini Live / Voice)           │  │
│  │ - Inputs: User voice intent ("Checkout my cart")       │  │
│  │ - Tools: ls, cat, act(path)                            │  │
│  │ - Execution: `act('/page/cart/checkout_btn', 'click')` │  │
│  └───────────────────┬────────────────────────────────────┘  │
│                      │ {action: "click", target: "id-123"}   │
└──────────────────────┼───────────────────────────────────────┘
                       │ WebSocket
                       ▼
┌──────────────────────────────────────────────────────────────┐
│                      Browser Extension                       │
│  3. Execute: document.querySelector('[data-mache-id="id-123"]').click()
└──────────────────────────────────────────────────────────────┘

## Why This is the Winning Path
- **Massive Token Reduction:** The Navigator agent doesn't need to read 2MB of raw DOM. It just runs `ls /page/` and sees exactly what it needs to see.
- **Resilience to Redesigns:** If a website completely changes its underlying framework (React to Vue, changing DOM depth), The Cartographer agent simply generates a new schema based on the new visual layout. The Navigator agent's logic remains unbroken.
- **Showcases Gemini's Cross-Modal Reasoning:** It perfectly demonstrates why multimodal models are necessary. Vision is used to understand layout and intent; Text/Code generation is used to write the formal Mache schema.
- **Action Grounding:** By injecting `data-mache-id` before generating the schema, the semantic filesystem inherently holds the exact pointer needed to execute the browser action flawlessly.

## Risks & Mitigations
- **Latency Penalty (High):** Two LLM calls per major page load.
  - *Mitigation:* Cache schemas by URL/layout hash. Only trigger Stage 1 (Cartographer) on cache miss or if Stage 2 (Navigator) reports an element is missing.
- **Schema Generation Hallucination (Medium):** Gemini might generate invalid Mache JSON schemas.
  - *Mitigation:* Provide a strict JSON schema definition to the Gemini API (Structured Outputs) and feed Mache validation errors back for a 1-shot retry.
- **Token Limits for Stage 1 (Medium):** Feeding raw DOM + Vision might hit context limits.
  - *Mitigation:* Extension must heavily sanitize HTML (strip `<svg>`, `<script>`, `<style>`) before sending it to Stage 1.

## Consequences
- **Development focus shifts:** We spend less time trying to write a universal "DOM-to-Filesystem" Mache schema, and more time perfecting the prompt/structured output for the "Cartographer" agent.
- **Demo Impact:** The architecture diagram becomes a core part of the pitch. Showing the raw DOM transforming into a clean filesystem in real-time is highly visual and impressive.
