# Next Steps

## Immediate (Pre-Submission)

### 1. Validate primary_items on real sites
- [ ] HN — confirm Cartographer returns story title mache_ids in `primary_items`
- [ ] Amazon search results — product title links
- [ ] Reddit — post title links
- [ ] GitHub trending — repo name links
- [ ] Wikipedia — verify non-list zones return empty `primary_items`

### 2. Gate test coverage
- [ ] Add captured snapshots for 2-3 non-HN sites to `testdata/`
- [ ] Extend `cmd/gate/` to verify `primary_items` presence in Cartographer output
- [ ] Verify ordinal counting ("click the 3rd item") resolves correctly end-to-end

### 3. Voice demo recording
- [ ] Record a clean demo: open HN, voice-navigate ("what's on this page?", "click the 5th story", "go back", "click the 2nd story")
- [ ] Record a second demo on a different site (Amazon or GitHub) to show generalization
- [ ] Capture latency numbers for the submission writeup

## Short-Term (Post-Submission Polish)

### 4. Schema caching
- Cache Cartographer output by URL + layout hash
- Skip Stage 1 on cache hit — instant filesystem construction
- Invalidate on DOM mutation (extension can detect via MutationObserver)

### 5. Deeper child traversal
- Current: BFS depth 2 from zone root, max 30 children
- Some sites nest content deeper (e.g., table rows inside tbody)
- Consider adaptive depth based on `primary_items` — if none found at depth 2, go deeper

### 6. Scroll + pagination
- Navigator can detect "no more items" and trigger scroll or pagination click
- Cartographer already maps pagination zones — wire Navigator to use them
- Handle infinite scroll pages (detect new elements after scroll, re-snapshot)

## Medium-Term (If We Advance)

### 7. Multi-tab support
- Track schemas per tab (keyed by tab ID)
- Navigator can reference multiple tabs: "go back to the Amazon tab"
- Extension already sends tab ID — backend needs per-tab engine instances

### 8. Form filling
- Current: click and focus actions only
- Add `type(path, text)` tool for input fields
- Cartographer should identify form zones with field labels
- Voice: "fill in my email as james@example.com"

### 9. Visual feedback
- Highlight the element the agent is about to click (brief CSS overlay)
- Show the semantic filesystem in a sidebar or popup
- Display agent reasoning in real-time ("I'm looking at the story list...")

### 10. Error recovery
- If `act()` fails (element not found, page changed), trigger re-snapshot
- Navigator should detect stale schemas: "This zone seems empty, let me refresh"
- Retry budget: 1 re-snapshot per intent, then report failure to user
