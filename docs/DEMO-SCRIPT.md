# X-Ray Demo Video Script (4 min)

**Category:** UI Navigator
**Format:** Screen recording with voiceover + live voice interaction
**Setup:** Chrome with X-Ray extension, agentd running via `task demo-video`, YouTube open

---

## ACT 1: The Problem (0:00 - 0:25)

> Every AI browser agent has the same problem: the browser has no API for what you're looking at.
>
> Current approaches either blind-click by coordinates, or burn a vision model call on every single screenshot. That's slow, expensive, and fragile.
>
> X-Ray gives your browser structural vision — instant, deterministic, and free. And it lets anyone control the web with just their voice.

**On screen:** Quick cut — a blank browser, then X-Ray overlay snapping into place on YouTube. The "before/after" moment.

---

## ACT 2: The Golden Path — Voice Navigation (0:25 - 1:40)

> My son has limited mobility. Using a mouse isn't always easy. So I built something he can use.

**Do:** YouTube is open. Overlay visible. Zones highlighted.

**Say to X-Ray:** "Go to YouTube and search for Minecraft speedruns."

**Let it work.** X-Ray navigates: finds the search bar, types, hits enter. Results page loads, overlay updates. Zones snap into place.

**Say to X-Ray:** "Click on the first video."

**Let it work.** Browser navigates to the video. Video plays.

> That's it. He says what he wants, the browser does it. No mouse, no keyboard, no scripting. Just voice.

**If it's fast:** Let the full flow play out naturally — the smoothness IS the demo.
**If it's slow:** Cut between key moments (search → results → video). Narrate: "It's searching... found results... now playing."

---

## ACT 3: How It Works (1:40 - 2:40)

> Here's what makes this possible.

**Show:** The overlay on YouTube results with zones highlighted.

> When this page loaded, X-Ray extracted a 12-dimensional feature vector from every region of the screenshot — color, edges, spatial frequency — the same signals your visual cortex uses. Then it projected those features through the Leech lattice, a 24-dimensional error-correcting code originally designed for deep space communication.

**Show:** Terminal with `ls /browser/` output — the virtual filesystem.

> The result is a virtual filesystem. Every zone on the page — header, search bar, video list, sidebar — becomes a directory you can read, search, and act on.

**Show:** `ls /browser/main/` then `cat /browser/main/_c/1`

> This is what the agent sees. Not raw HTML. Not a screenshot. Structured, navigable content. And it costs zero tokens — no vision model, no API call. Pure math, running locally in Go.

---

## ACT 4: Proof It Works (2:40 - 3:20)

> This isn't just a demo.

**Show:** WebArena task running — browser navigating autonomously. Can be pre-recorded, slightly sped up.

> On WebArena — the standard benchmark for web navigation agents — X-Ray completes tasks in 12 to 60 seconds. The visual understanding is instantaneous. The only cost is Gemini reasoning tokens.

**Show:** Results output with scores.

> Other agents burn a vision model call on every screenshot. X-Ray does it with algebraic geometry. Zero vision cost per page. That's the difference between a demo and something that can actually run at scale.

---

## ACT 5: Architecture (3:20 - 3:50)

**Show:** Architecture diagram.

> Chrome extension captures the page. The Cairn Cartographer maps it algebraically — Leech lattice, error-correcting codes, no cloud API. Gemini Live handles voice, vision, and reasoning. The Go server orchestrates everything. Deployed on Cloud Run.
>
> No Puppeteer. No Playwright. No external vision model. Just math and a microphone.

---

## ACT 6: Close (3:50 - 4:00)

> Zero vision model cost. Real-time voice. Algebraic structural understanding.
>
> X-Ray sees the web the way you do — instantly. And it lets anyone browse — even a seven-year-old with both hands full.

---

## Production Notes

### Before Recording
- [ ] `task demo-video` (sets Gemini REST + Cairn + Sheaf + fast mode)
- [ ] `direnv allow` (confirm no Ollama override in .envrc)
- [ ] Chrome with X-Ray extension loaded
- [ ] YouTube open in a tab
- [ ] `XRAY_GUARDRAILS=1` confirmed
- [ ] Screen resolution: 1920x1080
- [ ] Mic levels tested
- [ ] Do a dry run of "search YouTube for X, click first result" before recording

### Golden Path Test Commands
Test these before recording to confirm they work end-to-end:
1. "Go to YouTube" (if not already there)
2. "Search for Minecraft speedruns"
3. "Click on the first video"
4. "What's the title of this video?"

### Backup Plan
If live voice is flaky during recording:
- Pre-record the YouTube voice interaction separately, use best take
- Screen record a WebArena task for Act 4
- Narrate over both with polished voiceover
- Worst case: record multiple takes, edit together the best moments

### Key Phrases for Judges
- "Zero vision model cost" (differentiator)
- "Leech lattice" / "error-correcting codes" (technical depth)
- "Virtual filesystem" (novel abstraction)
- "12-60 seconds on WebArena" (concrete benchmark)
- "Gemini Live for voice and reasoning" (proves Gemini usage)
- "Deterministic and instant" (reliability)
- "Anyone can browse" (accessibility, human impact)

### What NOT to Say
- Don't apologize for latency
- Don't explain what you didn't build
- Don't compare to specific competitors
- Don't say "hackathon" — present it as a real product
- Don't over-explain the medical situation — one sentence is enough
