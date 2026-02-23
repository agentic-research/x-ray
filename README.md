# X-Ray: Gemini-Powered Web Navigation via Mache

[![Gemini Live Agent Challenge](https://img.shields.io/badge/Gemini%20Live%20Agent%20Challenge-UI%20Navigator-4285F4?logo=google&logoColor=white)](https://ai.google.dev/competition/gemini-live-agent-challenge)

> **"Topology is the missing half of semantics."**

Powered by [`agentic-research/mache`](https://github.com/agentic-research/mache) — an agent-computer interface that projects structured data into virtual filesystems — X-Ray uses Gemini's multimodal vision to project any webpage into a clean, semantic filesystem. Agents navigate the DOM deterministically via `ls` and `cat` — no pixel guessing, no fragile HTML parsing.

## The Problem: LLMs Understand Semantics, but Lack Topology

When building web-navigating agents, the standard approach is to feed them raw HTML or rely on pixel-coordinate guessing. But HTML is a 1D delivery mechanism for browsers, not a semantic map for reasoning agents. 

An LLM already knows what a "Checkout" button *means* (semantics), but when you dump a 10,000-line DOM tree into its context window, it loses all spatial awareness of *where* it is or how it relates to the elements around it (topology). 

Finding a simple button might require navigating a brittle path like `/html/body/div[4]/main/section/div[2]/span/button`. Giving a voice-driven LLM tools to find that path is like asking someone to locate a specific book by reading the library's structural blueprints. 

Worse, websites change constantly. A simple layout update breaks these locators, and static schemas can't map the entire internet.

## The Fix: Dynamic Semantic Projection

X-Ray bridges the physical reality of the DOM and the conceptual intent of the user using a Two-Stage Agent Architecture:
