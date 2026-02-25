# Abandoned: Chrome Extension WebAudio Implementation

This directory contains the original WebAudio implementation for X-Ray, which relied on Chrome Manifest V3 (MV3) `offscreen` documents and background service workers to capture microphone input and stream it to the Gemini Live API.

## Why was this abandoned?

We pivoted the architecture from a **"Voice Assistant Chrome Extension"** to a **"System-Level Agent OS"** (a local background daemon) for several critical reasons:

1. **MV3 Ephemerality & State Loss:** Chrome aggressively suspends background service workers every ~30 seconds of inactivity. Keeping a persistent voice session alive required complex keepalive hacks and leaky state management across the popup, background script, and offscreen document.
2. **UX Friction:** Browser microphone permissions and the requirement for user interaction (clicking an extension icon to start the session) broke the illusion of a seamless, always-available "Copilot."
3. **The "Dumb Terminal" Philosophy:** X-Ray's core innovation is `Mache`—a semantic virtual filesystem. By moving the voice streaming, LLM reasoning, and tool execution into a local OS daemon, the Chrome Extension becomes a lightweight "display driver." It only needs to take screenshots (`DOM_SNAPSHOT`) and execute deterministic clicks (`EXECUTE_ACTION`).
4. **Latency:** Capturing raw 16kHz PCM audio directly from the OS hardware (via the Go daemon) and streaming it to Gemini Live drastically reduces latency compared to routing it through WebAudio `AudioContext` and `ScriptProcessorNode` layers in JavaScript.

By abandoning the browser-bound audio stack, X-Ray operates as a true desktop agent that drives Chrome from the outside, proving the power and flexibility of the Semantic Filesystem architecture.
