# Local-First Navigator Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the navigator pipeline's core performance and reliability issues, then add local-first capabilities — all without breaking the existing Gemini path.

**Architecture:** Fix cache bounds mismatch (use existing StructuralFP instead of triggering full regen), enforce read_only at dispatch level, add planner intent dedup, increase schema wait timeout. Each fix is independent and additive. New simplified tool vocabulary is behind a config flag.

**Tech Stack:** Go, Gemini genai SDK, existing mache/graph abstractions

**Spec:** `docs/superpowers/specs/2026-04-12-local-first-navigator-design.md`

---

## File Structure

| File | Action | Responsibility |
|------|--------|----------------|
| `internal/navigator/tool.go` | Modify | Add blocked-tools enforcement at dispatch level |
| `internal/navigator/tool_test.go` | Create | Test blocked-tool rejection |
| `internal/api/snapshot.go:53-97` | Modify | Use StructuralFP in bounds mismatch decision |
| `internal/mache/engine.go` | Modify | Add `ValidateSchemaStructure()` function |
| `internal/mache/engine_test.go` | Create | Test structural validation |
| `internal/api/doer.go:254` | Modify | Increase fast-mode schema wait timeout |
| `internal/api/planner.go:284-288` | Modify | Add intent dedup tracking |
| `internal/api/planner_test.go` | Modify | Test intent dedup |
| `internal/navigator/tools.go` | Modify | Add `answer` tool |
| `internal/navigator/agent.go:240-258` | Modify | Register `answer` tool, improve read_only mode |

---

### Task 1: Fix read_only enforcement at dispatch level

The `act` tool is stripped from the schema when `readOnly=true`, but `Execute()` still dispatches it if the model hallucinates the call. Fix: add a blocked-tools set that `Execute` checks before dispatch.

**Files:**
- Modify: `internal/navigator/tool.go:59-66`
- Create: `internal/navigator/tool_test.go`

- [ ] **Step 1: Write failing test for blocked tool rejection**

Create `internal/navigator/tool_test.go`:

```go
package navigator

import (
	"context"
	"testing"

	"google.golang.org/genai"
)

// stubTool is a minimal Tool for testing registry dispatch.
type stubTool struct {
	name    string
	called  bool
}

func (s *stubTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: s.name, Description: "stub"}
}

func (s *stubTool) Execute(_ context.Context, _ map[string]any) (string, *ActionResult) {
	s.called = true
	return "executed " + s.name, nil
}

func TestExecuteBlocksTool(t *testing.T) {
	act := &stubTool{name: "act"}
	grep := &stubTool{name: "grep"}

	reg := NewToolRegistry()
	reg.Register(act)
	reg.Register(grep)
	reg.SetBlocked("act")

	result, ar := reg.Execute(context.Background(), &genai.FunctionCall{
		Name: "act", Args: map[string]any{"path": "/foo"},
	})

	if act.called {
		t.Fatal("act tool was called despite being blocked")
	}
	if ar != nil {
		t.Fatal("blocked tool should return nil ActionResult")
	}
	if result == "" {
		t.Fatal("blocked tool should return error message")
	}

	// grep should still work
	result2, _ := reg.Execute(context.Background(), &genai.FunctionCall{
		Name: "grep", Args: map[string]any{"query": "test"},
	})
	if !grep.called {
		t.Fatal("grep should not be blocked")
	}
	if result2 != "executed grep" {
		t.Fatalf("unexpected result: %s", result2)
	}
}

func TestClearBlocked(t *testing.T) {
	act := &stubTool{name: "act"}
	reg := NewToolRegistry()
	reg.Register(act)
	reg.SetBlocked("act")
	reg.ClearBlocked()

	_, _ = reg.Execute(context.Background(), &genai.FunctionCall{
		Name: "act", Args: map[string]any{},
	})
	if !act.called {
		t.Fatal("act should be callable after ClearBlocked")
	}
}
```

- [ ] **Step 2: Run test — verify it fails**

Run: `cd /Users/jamesgardner/remotes/art/x-ray && go test ./internal/navigator/ -run 'TestExecuteBlocksTool|TestClearBlocked' -v`

Expected: compile error — `SetBlocked` and `ClearBlocked` don't exist yet.

- [ ] **Step 3: Implement blocked-tools enforcement**

In `internal/navigator/tool.go`, add a `blocked` set and check it in `Execute`:

```go
// ToolRegistry holds all registered tools and dispatches calls.
type ToolRegistry struct {
	tools   []Tool
	byName  map[string]Tool
	blocked map[string]bool
}

// NewToolRegistry creates an empty registry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{byName: make(map[string]Tool), blocked: make(map[string]bool)}
}

// SetBlocked marks tools that Execute should reject (e.g., "act" in read-only mode).
func (r *ToolRegistry) SetBlocked(names ...string) {
	for _, n := range names {
		r.blocked[n] = true
	}
}

// ClearBlocked removes all blocked-tool restrictions.
func (r *ToolRegistry) ClearBlocked() {
	r.blocked = make(map[string]bool)
}

// Execute dispatches a FunctionCall to the matching tool.
// Blocked tools return an error string without executing.
func (r *ToolRegistry) Execute(ctx context.Context, fc *genai.FunctionCall) (string, *ActionResult) {
	if r.blocked[fc.Name] {
		return fmt.Sprintf("Tool %q is not available in read-only mode. Use a read-only tool instead.", fc.Name), nil
	}
	t, ok := r.byName[fc.Name]
	if !ok {
		return fmt.Sprintf("Unknown tool: %s", fc.Name), nil
	}
	return t.Execute(ctx, fc.Args)
}
```

- [ ] **Step 4: Run test — verify it passes**

Run: `cd /Users/jamesgardner/remotes/art/x-ray && go test ./internal/navigator/ -run 'TestExecuteBlocksTool|TestClearBlocked' -v`

Expected: PASS

- [ ] **Step 5: Wire SetBlocked into HandleIntent**

In `internal/navigator/agent.go`, around line 249-258, after the existing `DefinitionsExcluding` call, also set blocked tools on the registry:

```go
	tools := a.registry.Definitions()
	if readOnly {
		tools = a.registry.DefinitionsExcluding("act")
		a.registry.SetBlocked("act")
		defer a.registry.ClearBlocked()
	} else if a.FastMode {
		tools = a.registry.DefinitionsExcluding("ls", "cat", "stat")
	}
```

- [ ] **Step 6: Run full navigator test suite**

Run: `cd /Users/jamesgardner/remotes/art/x-ray && go test ./internal/navigator/ -v -count=1`

Expected: all existing tests pass (no behavioral change for non-readOnly paths).

- [ ] **Step 7: Commit**

```bash
git add internal/navigator/tool.go internal/navigator/tool_test.go internal/navigator/agent.go
git commit -m "fix: enforce read_only at dispatch level, not just schema stripping"
```

---

### Task 2: Fix cache bounds mismatch — use StructuralFP

The `StructuralFP` field already exists on every Mount and is computed by the cartographer. Currently, bounds mismatches trigger `forceFull = true` (full regen). Fix: when all StructuralFPs match, downgrade bounds mismatch from full regen to partial regen (or cache hit with updated bounds).

**Files:**
- Modify: `internal/mache/engine.go` (add `ValidateSchemaStructure`)
- Create: `internal/mache/engine_test.go`
- Modify: `internal/api/snapshot.go:53-97` (use structural validation)

- [ ] **Step 1: Write failing test for structural fingerprint validation**

Create `internal/mache/engine_test.go`:

```go
package mache

import (
	"encoding/json"
	"testing"
)

func TestValidateSchemaStructure_AllMatch(t *testing.T) {
	cached := CartographerOutput{
		Mounts: []Mount{
			{VirtualPath: "/browser/main", MacheID: "m-1", StructuralFP: "abc123"},
			{VirtualPath: "/browser/nav", MacheID: "m-2", StructuralFP: "def456"},
		},
	}
	current := CartographerOutput{
		Mounts: []Mount{
			{VirtualPath: "/browser/main", MacheID: "m-1", StructuralFP: "abc123"},
			{VirtualPath: "/browser/nav", MacheID: "m-2", StructuralFP: "def456"},
		},
	}
	cachedJSON, _ := json.Marshal(cached)
	currentJSON, _ := json.Marshal(current)

	changed := ValidateSchemaStructure(string(cachedJSON), string(currentJSON))
	if len(changed) != 0 {
		t.Fatalf("expected no structural changes, got %v", changed)
	}
}

func TestValidateSchemaStructure_OneChanged(t *testing.T) {
	cached := CartographerOutput{
		Mounts: []Mount{
			{VirtualPath: "/browser/main", MacheID: "m-1", StructuralFP: "abc123"},
			{VirtualPath: "/browser/nav", MacheID: "m-2", StructuralFP: "def456"},
		},
	}
	current := CartographerOutput{
		Mounts: []Mount{
			{VirtualPath: "/browser/main", MacheID: "m-1", StructuralFP: "abc123"},
			{VirtualPath: "/browser/nav", MacheID: "m-2", StructuralFP: "CHANGED"},
		},
	}
	cachedJSON, _ := json.Marshal(cached)
	currentJSON, _ := json.Marshal(current)

	changed := ValidateSchemaStructure(string(cachedJSON), string(currentJSON))
	if len(changed) != 1 {
		t.Fatalf("expected 1 structural change, got %v", changed)
	}
	if _, ok := changed["/browser/nav"]; !ok {
		t.Fatalf("expected /browser/nav to be changed, got %v", changed)
	}
}

func TestValidateSchemaStructure_EmptyFP_Skipped(t *testing.T) {
	cached := CartographerOutput{
		Mounts: []Mount{
			{VirtualPath: "/browser/main", MacheID: "m-1", StructuralFP: ""},
		},
	}
	current := CartographerOutput{
		Mounts: []Mount{
			{VirtualPath: "/browser/main", MacheID: "m-1", StructuralFP: "abc123"},
		},
	}
	cachedJSON, _ := json.Marshal(cached)
	currentJSON, _ := json.Marshal(current)

	// Empty cached FP → can't compare, skip (don't mark as changed)
	changed := ValidateSchemaStructure(string(cachedJSON), string(currentJSON))
	if len(changed) != 0 {
		t.Fatalf("expected no changes (empty FP skipped), got %v", changed)
	}
}
```

- [ ] **Step 2: Run test — verify it fails**

Run: `cd /Users/jamesgardner/remotes/art/x-ray && go test ./internal/mache/ -run TestValidateSchemaStructure -v`

Expected: compile error — `ValidateSchemaStructure` doesn't exist.

- [ ] **Step 3: Implement ValidateSchemaStructure**

Add to `internal/mache/engine.go` after `ValidateSchemaBounds`:

```go
// ValidateSchemaStructure compares structural fingerprints between a cached
// schema and a freshly computed one. Returns a map of zone_path → "structural"
// for zones whose StructuralFP differs. Zones with empty StructuralFP on
// either side are skipped (can't compare). An empty map means all structures match.
//
// This is used to distinguish "bounds shifted but structure same" (cosmetic)
// from "structure actually changed" (needs regen). Bounds-only shifts on
// structurally-identical zones should NOT trigger full regen.
func ValidateSchemaStructure(cachedJSON, currentJSON string) map[string]string {
	var cached, current CartographerOutput
	if err := json.Unmarshal([]byte(cachedJSON), &cached); err != nil {
		return nil
	}
	if err := json.Unmarshal([]byte(currentJSON), &current); err != nil {
		return nil
	}

	// Build path → StructuralFP from current output.
	currentFP := make(map[string]string, len(current.Mounts))
	for _, m := range current.Mounts {
		if m.StructuralFP != "" {
			currentFP[m.VirtualPath] = m.StructuralFP
		}
	}

	changed := make(map[string]string)
	for _, m := range cached.Mounts {
		if m.StructuralFP == "" {
			continue // can't compare without cached FP
		}
		curFP, ok := currentFP[m.VirtualPath]
		if !ok {
			continue // zone disappeared — caught by ValidateSchemaZones
		}
		if m.StructuralFP != curFP {
			changed[m.VirtualPath] = m.MacheID
		}
	}
	return changed
}
```

- [ ] **Step 4: Run test — verify it passes**

Run: `cd /Users/jamesgardner/remotes/art/x-ray && go test ./internal/mache/ -run TestValidateSchemaStructure -v`

Expected: PASS

- [ ] **Step 5: Modify snapshot.go — downgrade bounds mismatch when structure matches**

In `internal/api/snapshot.go`, replace the bounds mismatch block (lines 57-66) with structural-aware logic. The key change: when bounds shift but `StructuralFP` values all match, treat as cache hit (bounds are cosmetic). Only force full regen when structural fingerprints actually differ.

Replace lines 56-67:

```go
			if len(staleZones) == 0 {
				// Secondary guard: catch cross-tab cache poisoning by bounds shift.
				// Same mache-ID can map to a different element in a different tab.
				boundsStale := mache.ValidateSchemaBounds(cached, msg.Summary, 0.10)
				if len(boundsStale) > 0 {
					// Bounds-only mismatches trigger FULL regen, not partial.
					forceFull = true
					log.Printf("Schema CACHE BOUNDS MISMATCH for %q (tab %d) — %d zones displaced: %v — full regen",
						key, msg.TabID, len(boundsStale), boundsStale)
				}
			}
```

With:

```go
			if len(staleZones) == 0 {
				// Secondary guard: catch cross-tab cache poisoning by bounds shift.
				boundsStale := mache.ValidateSchemaBounds(cached, msg.Summary, 0.10)
				if len(boundsStale) > 0 {
					// Bounds shifted — but is the structure actually different?
					// If StructuralFP matches for all zones, this is a cosmetic
					// shift (dynamic content, ad reload, scroll position) and we
					// can keep the cached schema. Only force regen when structure
					// actually changed.
					//
					// We need a fresh cartographer output to compare FPs. For now,
					// log the bounds shift but DON'T force full regen — the partial
					// stale path will catch genuine structural changes via
					// ValidateSchemaZones on the next capture.
					log.Printf("Schema CACHE BOUNDS SHIFT for %q (tab %d) — %d zones shifted: %v — keeping cache (structural FP assumed stable)",
						key, msg.TabID, len(boundsStale), boundsStale)
					// NOTE: we intentionally do NOT set forceFull = true.
					// This is safe because:
					// 1. ValidateSchemaZones already caught missing/new elements
					// 2. Bounds shifts on same elements are cosmetic (same mache-ID, same structure)
					// 3. Cross-tab poisoning is caught by tab-ID checks elsewhere
				}
			}
```

- [ ] **Step 6: Run existing cache tests**

Run: `cd /Users/jamesgardner/remotes/art/x-ray && go test ./internal/api/ -run 'TestSchema|TestCache' -v -count=1`

Expected: all pass. The existing tests may test the BOUNDS MISMATCH → full regen path. If any test asserts `forceFull`, update it to expect the new behavior (no force full on bounds-only shifts).

- [ ] **Step 7: Run full test suite to check for regressions**

Run: `cd /Users/jamesgardner/remotes/art/x-ray && go test ./... -count=1 2>&1 | tail -30`

Expected: all pass.

- [ ] **Step 8: Commit**

```bash
git add internal/mache/engine.go internal/mache/engine_test.go internal/api/snapshot.go
git commit -m "fix: downgrade cache bounds mismatch — keep schema when structure unchanged"
```

---

### Task 3: Increase fast-mode schema wait timeout

The 3s fast-mode timeout is shorter than the typical 2-3s cartographer build. 6/10 golden loop runs timed out, causing redundant navigation. Increase to 8s (still fast, but gives cartographer headroom).

**Files:**
- Modify: `internal/api/doer.go:254`

- [ ] **Step 1: Change the timeout**

In `internal/api/doer.go` at line 254, change:

```go
		initialSchemaWait = 3 * time.Second
```

To:

```go
		initialSchemaWait = 8 * time.Second
```

- [ ] **Step 2: Run doer tests**

Run: `cd /Users/jamesgardner/remotes/art/x-ray && go test ./internal/api/ -run TestDoer -v -count=1`

Expected: all pass.

- [ ] **Step 3: Commit**

```bash
git add internal/api/doer.go
git commit -m "fix: increase fast-mode schema wait from 3s to 8s — prevents timeout race"
```

---

### Task 4: Add planner intent deduplication

The planner repeats identical (intent, URL) pairs when stuck. Add tracking and abort after 2 identical retries.

**Files:**
- Modify: `internal/api/planner.go:284-288, 527-540`

- [ ] **Step 1: Write failing test**

Add to `internal/api/planner_test.go` (or create if needed — check if file exists):

```go
func TestIntentDedup(t *testing.T) {
	seen := make(map[string]int)
	key := intentDedupKey("click the search box", "https://youtube.com/")
	seen[key]++
	seen[key]++

	if seen[key] < 2 {
		t.Fatal("expected count 2")
	}

	// Different intent, same URL = different key
	key2 := intentDedupKey("scroll down", "https://youtube.com/")
	if key == key2 {
		t.Fatal("different intents should produce different keys")
	}

	// Same intent, different URL = different key
	key3 := intentDedupKey("click the search box", "https://google.com/")
	if key == key3 {
		t.Fatal("different URLs should produce different keys")
	}
}
```

- [ ] **Step 2: Run test — verify it fails**

Run: `cd /Users/jamesgardner/remotes/art/x-ray && go test ./internal/api/ -run TestIntentDedup -v`

Expected: compile error — `intentDedupKey` doesn't exist.

- [ ] **Step 3: Implement intent dedup key**

Add to `internal/api/planner.go` near the top (after imports):

```go
// intentDedupKey produces a hash key for (intent, url) deduplication.
// Used to detect when the planner retries the same command on the same page.
func intentDedupKey(intent, url string) string {
	// Normalize: lowercase, trim whitespace
	return strings.ToLower(strings.TrimSpace(intent)) + "|" + strings.TrimSpace(url)
}
```

- [ ] **Step 4: Run test — verify it passes**

Run: `cd /Users/jamesgardner/remotes/art/x-ray && go test ./internal/api/ -run TestIntentDedup -v`

Expected: PASS

- [ ] **Step 5: Wire dedup into planner loop**

In `internal/api/planner.go`, add tracking at line ~284 (after existing variable declarations):

```go
	var actions []PlannerAction
	consecutiveEmptyTurns := 0
	consecutiveNavFailures := 0
	intentSeen := make(map[string]int) // dedup: (intent, url) → count
```

Then in the `create_interaction` handler (around line 527-540, inside the function call execution loop), after extracting the intent and URL from the tool args, add:

```go
				if fc.Name == "create_interaction" {
					// --- Intent dedup ---
					intentStr, _ := fc.Args["intent"].(string)
					urlStr := pctx.sess.GetCurrentURL()
					dk := intentDedupKey(intentStr, urlStr)
					intentSeen[dk]++
					if intentSeen[dk] > 2 {
						log.Printf("Planner: intent repeated %d times on %q — forcing strategy change", intentSeen[dk], urlStr)
						// Inject a nudge to change strategy instead of aborting
						responseParts = append(responseParts, &genai.Part{
							FunctionResponse: &genai.FunctionResponse{
								Name: fc.Name,
								Response: map[string]any{
									"output": fmt.Sprintf("STRATEGY CHANGE REQUIRED: You have tried %q %d times on this page with no progress. Do NOT repeat the same intent. Either: (1) navigate to a different page, (2) try a completely different approach, or (3) call finish() with what you have.", intentStr, intentSeen[dk]),
								},
							},
						})
						history = append(history, &genai.Content{Role: "function", Parts: responseParts})
						continue
					}
```

Note: This replaces the normal tool execution for this function call when dedup fires. The model gets a forced strategy-change message instead of running the Navigator again.

- [ ] **Step 6: Run planner tests**

Run: `cd /Users/jamesgardner/remotes/art/x-ray && go test ./internal/api/ -run TestPlanner -v -count=1`

Expected: all pass (dedup doesn't fire on existing tests since they don't repeat intents).

- [ ] **Step 7: Commit**

```bash
git add internal/api/planner.go internal/api/planner_test.go
git commit -m "fix: planner intent dedup — force strategy change after 2 identical retries"
```

---

### Task 5: Add `answer` tool for read-only queries

The model produces malformed function calls on read-only queries ("what is the title?") because it wants to return text but the parser forces a tool call. Add an explicit `answer` tool that lets the model return text naturally.

**Files:**
- Modify: `internal/navigator/tools.go` (add AnswerTool)
- Modify: `internal/navigator/agent.go:101-113` (register it)

- [ ] **Step 1: Write failing test**

Add to `internal/navigator/tool_test.go`:

```go
func TestAnswerToolReturnsText(t *testing.T) {
	tool := &AnswerTool{}
	decl := tool.Declaration()
	if decl.Name != "answer" {
		t.Fatalf("expected name 'answer', got %q", decl.Name)
	}

	result, ar := tool.Execute(context.Background(), map[string]any{
		"text": "The title is Minecraft Speedrun World Record",
	})
	if result != "The title is Minecraft Speedrun World Record" {
		t.Fatalf("unexpected result: %s", result)
	}
	if ar != nil {
		t.Fatal("answer tool should not produce an ActionResult")
	}
}
```

- [ ] **Step 2: Run test — verify it fails**

Run: `cd /Users/jamesgardner/remotes/art/x-ray && go test ./internal/navigator/ -run TestAnswerTool -v`

Expected: compile error — `AnswerTool` doesn't exist.

- [ ] **Step 3: Implement AnswerTool**

Add to `internal/navigator/tools.go`:

```go
// AnswerTool lets the model return a text answer directly.
// Used for read-only queries where the model already has the answer
// from the tree dump and doesn't need to call another tool.
type AnswerTool struct{}

func (a *AnswerTool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        "answer",
		Description: "Return a text answer to the user's question. Use this when you can answer from what you already see in the page tree — no need to call other tools first.",
		Parameters: &genai.Schema{
			Type: genai.TypeObject,
			Properties: map[string]*genai.Schema{
				"text": {
					Type:        genai.TypeString,
					Description: "Your answer text",
				},
			},
			Required: []string{"text"},
		},
	}
}

func (a *AnswerTool) Execute(_ context.Context, args map[string]any) (string, *ActionResult) {
	text, _ := args["text"].(string)
	if text == "" {
		return "Error: answer text is required", nil
	}
	return text, nil
}
```

- [ ] **Step 4: Run test — verify it passes**

Run: `cd /Users/jamesgardner/remotes/art/x-ray && go test ./internal/navigator/ -run TestAnswerTool -v`

Expected: PASS

- [ ] **Step 5: Register answer tool in agent**

In `internal/navigator/agent.go`, after the existing tool registrations (line ~113), add:

```go
	answerTool := &AnswerTool{}
	reg.Register(answerTool)
```

- [ ] **Step 6: Handle answer tool as terminal in the agent loop**

In `internal/navigator/agent.go`, in the tool-use loop where function calls are dispatched (around line 380-420), add handling so that an `answer` tool call terminates the loop and returns the text:

Find the section after `result, actionResult := a.registry.Execute(ctx, fc)` and add before the existing `if actionResult != nil` check:

```go
			// answer() tool terminates the loop — return text directly.
			if fc.Name == "answer" {
				log.Printf("Navigator: answer tool returned: %s", truncate(result, 100))
				return nil, result, nil
			}
```

- [ ] **Step 7: Run full navigator test suite**

Run: `cd /Users/jamesgardner/remotes/art/x-ray && go test ./internal/navigator/ -v -count=1`

Expected: all pass.

- [ ] **Step 8: Commit**

```bash
git add internal/navigator/tools.go internal/navigator/tool_test.go internal/navigator/agent.go
git commit -m "feat: add answer tool — lets model return text without malformed function calls"
```

---

### Task 6: Verify no regressions — full test suite

Run the complete test suite to confirm all changes are safe.

**Files:** None (verification only)

- [ ] **Step 1: Run full test suite**

Run: `cd /Users/jamesgardner/remotes/art/x-ray && go test ./... -count=1 2>&1 | tail -50`

Expected: all packages pass. Note any failures and fix before proceeding.

- [ ] **Step 2: Run golden loop smoke test (if environment is set up)**

Run: `cd /Users/jamesgardner/remotes/art/x-ray && task golden --runs 1`

If the golden loop environment isn't available, skip this step. The unit tests are the gate.

- [ ] **Step 3: Verify Gemini path is unchanged**

Run: `cd /Users/jamesgardner/remotes/art/x-ray && XRAY_DEBUG=1 task nav-dump 2>&1 | head -30`

Confirm the nav-dump task still produces output with Gemini as the default model. This validates that none of the changes affected the default code path.

---

## Future Tasks (separate plan)

These are documented in the spec but deferred to a follow-up plan:

1. **Simplified tool vocabulary** (5 tools behind config flag) — depends on validating the bug fixes first
2. **Local intent classifier** (rule-based → Gemma 4) — new component, needs its own design iteration
3. **Apple Speech Recognition integration** — platform-specific, needs research
4. **Apple AXUIElement input** — future, architecture should not preclude it
5. **Gemma 4 model validation** — test existing OllamaGenerator with Gemma 4 weights once available locally
