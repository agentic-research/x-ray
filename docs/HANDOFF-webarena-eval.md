# Handoff: WebArena Evaluation with Cairn Cartographer

**Author**: Previous Claude session (2026-03-02)
**Priority**: Last remaining eval before hackathon deadline (2026-03-16)

## Goal

Run WebArena evaluation suite against X-Ray using the **Cairn cartographer** (mode=cairn) to produce benchmark numbers for the Gemini Live Agent Challenge submission. This is the final eval needed.

## Current State

- `cmd/webarena/` exists but is likely **broken** — expect it to need fixes.
- `cmd/bench/` runs cartographer + navigator benchmarks from `testdata/bench_cases.json` but is NOT the WebArena eval.
- The **standard WebArena docker** (not a custom one) is the way to go. Use the upstream WebArena environment as-is.

## Architecture Context

The pipeline WebArena exercises:

```
WebArena task → Doer.executeGoal() → Navigator.HandleIntent() → act/read/scroll/goto
                    ↓                        ↓
              multi-step loop         schema from Cairn cartographer
              (up to 5 steps)         (mode=cairn, gear=5, scale=10.0)
```

Key files:
- **Config**: `internal/config/config.go` — set `cartographer.mode: "cairn"` in `~/.agentic-research/x-ray/config.yaml`
- **Cairn**: `internal/cartographer/cairn.go` — Leech lattice visual tokenization, no LLM needed
- **Doer**: `internal/api/doer.go` — multi-step goal execution (what WebArena tasks map to)
- **Navigator**: `internal/navigator/` — intent → action resolution (Gemini or local model)
- **CDP capture**: `internal/api/capture.go` + `internal/cdp/capture.go` — screenshot + AX + layers
- **WebArena runner**: `cmd/webarena/` — the eval harness (needs investigation, likely broken)

## Config for Cairn

```yaml
cartographer:
  mode: "cairn"
  gear: 5          # Leech lattice (default, best balance)
  scale: 10.0
  target_width: 800
  max_height: 16384

timeouts:
  schema_wait: 60  # bump for eval reliability
  capture: 60
```

Cairn is **algebraic** (no VLM/LLM call for schema generation) — it uses Leech lattice quantization of visual features + DOM structure. Zero token cost for cartography.

## What Needs to Happen

1. **Get WebArena docker running** — use the standard upstream WebArena docker environment, NOT a custom one.
2. **Fix `cmd/webarena/`** — investigate what's broken, get it connecting to the WebArena environment and dispatching tasks through the Doer pipeline.
3. **Wire Cairn as the cartographer** — ensure the eval uses `mode: "cairn"` so we benchmark the algebraic cartographer, not Gemini VLM.
4. **Run the eval** — collect pass rates, step counts, latencies.
5. **Record results** — we need numbers for the Devpost submission.

## Important Context

- Module path: `github.com/agentic-research/x-ray`
- The Doer has a `maxGoalSteps = 5` limit per task — WebArena tasks that need more steps will fail.
- Timeouts were just centralized into config.yaml (commit `8e19ef9`) — bump `schema_wait` and `capture` to 60s for eval stability.
- The extension Chrome MV3 (`ext/`) must be loaded for CDP capture to work. The eval harness needs to handle Chrome lifecycle.
- Cairn determinism was fixed this session (commit `558bf74`) — zone ordering is now stable.
- `.envrc` loading is now handled by `config.LoadConfig()` / `config.LoadEnv()` — no manual godotenv needed.

## Files to Start With

```
cmd/webarena/          # the eval runner — start here, figure out what's broken
cmd/bench/main.go      # reference for how bench cases are structured
testdata/              # test fixtures, bench cases
internal/api/doer.go   # the multi-step execution loop WebArena exercises
```
