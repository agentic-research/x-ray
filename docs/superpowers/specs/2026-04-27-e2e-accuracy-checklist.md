# E2E Accuracy Fix Checklist

**Goal**: Honest, falsifiable measurement of DOM-only + Gemma 4 navigation accuracy.

**Current state**: ~65% on verifiable cases, p50=1.4s (reasoning off). Masked by stale bench expectations + broken captures + test that never asserts.

---

## Pre-flight: Fix broken test infrastructure

- [ ] **1. Test must actually fail on low accuracy**
  - Add `t.Errorf` in e2e_fast_test.go when accuracy < threshold
  - Threshold: 70% accuracy, p50 < 5s
  - Verify: `go test` returns exit code 1 when accuracy is below threshold

- [ ] **2. Filter zero-bounds elements before cap**
  - Elements with bounds `0,0,0,0` are hidden/offscreen — exclude them
  - Sort remaining by visual position (y ascending, then x) before 40-cap
  - Verify: GitHub "Sign in" (bounds 0.9030,0.0185) appears in filtered list; hidden mache-12 (bounds 0,0,0,0) does not

- [ ] **3. Discard json.Unmarshal errors**
  - Line 43 in e2e_fast_test.go silently ignores parse errors
  - Add `t.Fatalf` on unmarshal failure
  - Remove dead `validPaths` variable (lines 207-210)

## Testdata: Recapture broken sites

- [ ] **4. Recapture reddit** — currently shows "blocked by network security" page
  - Use `node scripts/batch_capture.js` or Playwright MCP
  - Verify: page_summary.txt contains real content (>20 interactive elements)

- [ ] **5. Recapture ecommerce** — currently shows eBay "Where's all the stuff?" error
  - May need a different ecommerce site (Amazon?) if eBay blocks headless
  - Verify: page has navigation links, search box, sign in

- [ ] **6. Recapture youtube** — missing chip filters, elements at negative bounds
  - May need to accept cookies / dismiss overlays before capture
  - Verify: Search button, Guide button, Create button all present with valid bounds

## Bench cases: Update expected mache-IDs

- [ ] **7. Add validation script**
  - For each bench case: check `expect_text` appears in site's `page_summary.txt`
  - Check `expect_mache_id` maps to an element containing `expect_text`
  - Script outputs which cases are valid/stale/broken
  - Run: `go run ./cmd/validate-bench`

- [ ] **8. Update bench_cases.json**
  - Run validation script
  - For each stale case: find the mache-ID that matches expect_text in current summary
  - Update expect_mache_id
  - Re-run validation — 100% of cases should pass

- [ ] **9. Add difficulty labels to new cases**
  - Ensure all cases have difficulty: simple/medium/hard
  - Add 5+ medium cases (ambiguous elements, multiple matches)
  - Add 3+ hard cases (deeply nested, dynamic content)

## Re-run and measure

- [ ] **10. Run E2E with fixed data + fixed test**
  - Command: `GOWORK=off NAVIGATOR_FORMAT=openai go test ./internal/cartographer/ -run TestE2E_Fast -v -count=1 -timeout=300s`
  - Record: accuracy, p50, p95, per-site breakdown
  - Must pass the t.Errorf threshold (≥70% accuracy)

- [ ] **11. Run 3x with different seeds**
  - Verify results are stable across runs
  - Report variance

- [ ] **12. Run with --reasoning on for comparison**
  - Same test, but restart llama-server without --reasoning off
  - Record speed difference
  - Report: accuracy delta, speed delta

## Honest claims (fill in after measurements)

After completing all items:

```
Accuracy: __/__ (__%)  [reasoning off]
Accuracy: __/__ (__%)  [reasoning on]
Latency p50: __s / __s [off/on]
Latency p95: __s / __s [off/on]
Sites tested: __ (all with valid captures)
Cases tested: __ (all with validated mache-IDs)
Conditions: DOM-only, no screenshot, llama-server --jinja, Gemma 4 26B-A4B Q4_K_M
Hardware: M3 Max 32GB
```

## What this does NOT prove (scope limits)

- Production accuracy (Gemini 2.5 Flash may differ from Gemma 4)
- Dynamic page accuracy (JS hydration timing)
- Pages with >40 interactive elements (need find→act pattern)
- Non-English pages
- Complex interactions (hover menus, scroll-to-reveal)
- Voice-to-action latency (STT not included)
