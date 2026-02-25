# Plan: Local SLM + Text-Mode Voice + E2E Testing

## Context

Voice testing requires talking out loud (impractical in public). The Navigator's tool-use loop (ls/cat/act/scroll) is 3-8 Gemini API calls per intent — expensive and slow when iterating locally. Ollama is already running on M3 Max with llama3.2 available.

**Goal:** Keep Gemini for vision (Cartographer) and voice audio (Live API). Make the Navigator's model overrideable via an interface so it can run locally or be mocked in tests. Add text input to the voice WebSocket so we can test the full voice pipeline by typing instead of talking.

---

## Architecture After Changes

```
Cartographer  → Gemini Cloud (vision, structured output)  [unchanged]
Navigator     → ContentGenerator interface                  [NEW: mockable, swappable]
                  ├─ GeminiGenerator (default, wraps genai.Client)
                  └─ Any impl (ollama, mock, local SLM)
Voice Live    → Gemini Cloud (audio streaming)              [unchanged]
Voice Input   → Audio OR Text (SendClientContent)           [NEW: text mode]
```

New env vars:
```
NAVIGATOR_ENDPOINT=http://localhost:11434/v1   # empty = use Gemini cloud
NAVIGATOR_MODEL=llama3.2                       # empty = use GEMINI_MODEL
```

---

## The Interface (core abstraction)

### File: `internal/navigator/model.go` (NEW)

```go
// ContentGenerator abstracts the LLM call so Navigator can use Gemini, ollama, or a mock.
type ContentGenerator interface {
    GenerateContent(ctx context.Context, model string, history []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error)
}

// GeminiGenerator wraps the real genai.Client.
type GeminiGenerator struct {
    Client *genai.Client
}

func (g *GeminiGenerator) GenerateContent(ctx context.Context, model string, history []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
    return g.Client.Models.GenerateContent(ctx, model, history, config)
}

// OllamaGenerator talks to Ollama/OpenAI-compatible endpoints.
// The genai.Client can't be reused here — it emits Gemini-specific wire format
// (contents/parts JSON + /v1beta/models/{model}:generateContent paths).
// Ollama speaks OpenAI format: /v1/chat/completions with messages/content.
type OllamaGenerator struct {
    Endpoint string // e.g. http://localhost:11434/v1
    Model    string // e.g. llama3.2
}

func (o *OllamaGenerator) GenerateContent(ctx context.Context, model string, history []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
    // 1. Map []*genai.Content history → OpenAI messages format
    // 2. Map config.Tools → OpenAI tools format
    // 3. http.Post to o.Endpoint + "/chat/completions"
    // 4. Map response back to *genai.GenerateContentResponse
}
```

This is the key change. Navigator's `Agent` struct changes from:
```go
client *genai.Client  →  generator ContentGenerator
```

And `HandleIntent()` line 130 changes from:
```go
a.client.Models.GenerateContent(...)  →  a.generator.GenerateContent(...)
```

`ExecuteTool()` is UNCHANGED — it never calls the model, only the Engine.

---

## Step 0: Write Tests First (sequential — establishes contracts)

All tests written before any implementation. Agents get test files as their spec.

### File: `internal/navigator/model_test.go` (NEW)

Tests the interface contract and mock:

```go
// mockGenerator records calls and returns canned responses.
type mockGenerator struct {
    calls    []mockCall
    response *genai.GenerateContentResponse
    err      error
}

func TestGeminiGeneratorImplementsInterface(t *testing.T)
  // Compile-time check: var _ ContentGenerator = &GeminiGenerator{}

func TestMockGeneratorRecordsCalls(t *testing.T)
  // Verify mock captures model, history, config

func TestHandleIntentWithMockGenerator(t *testing.T)
  // Wire mock that returns a tool call → verify ExecuteTool dispatches correctly
  // Then mock returns text → verify final response
  // NO API CALLS — pure mock
```

### File: `internal/navigator/local_test.go` (NEW)

Tests the full tool chain with reddit fixture (no API):

```go
func TestExecuteToolWithRedditFixture(t *testing.T)
  // Load testdata/reddit schema + summary → engine
  // Call ExecuteTool(ls, cat, act) — pure engine calls, no model needed
  // Verify: ls("/") → ls zone → cat children → act on post

func TestRedditScrollSequence(t *testing.T)
  // Build engine, inject mock scrollFn
  // Execute: scroll down → scroll down → scroll up → act
  // Verify correct mache_id in ActionResult
```

### File: `internal/api/voice_text_test.go` (NEW)

Tests text input through voice WebSocket:

```go
func TestVoiceTextInputMessageParsing(t *testing.T)
  // Verify {"type":"text_input","text":"click the first story"}
  // parses correctly from a TextMessage frame

func TestVoiceTextInputRequiresSession(t *testing.T)
  // Text input only forwarded after SetupComplete
```

### File: `internal/api/e2e_scroll_test.go` (NEW)

E2E scroll+click sequence test against reddit fixture:

```go
func TestE2EScrollSequence(t *testing.T)
  // 1. Build engine from reddit fixture (schema + summary)
  // 2. Wire mock scrollFn + mock ContentGenerator
  // 3. Execute: navigate("scroll down 3 times then click 3rd post")
  // 4. Verify: EXECUTE_ACTION dispatched with correct mache_id
```

### File: `cmd/agentd/main_test.go` (NEW)

Tests client initialization env var routing:

```go
func TestNavigatorEndpointFromEnv(t *testing.T)
  // Set NAVIGATOR_ENDPOINT → separate client config created

func TestNavigatorFallsBackToGemini(t *testing.T)
  // No env var → uses default Gemini client
```

---

## Step 1: Implementation (parallel — each agent confined to one file)

### Agent A: `internal/navigator/model.go` + `internal/navigator/agent.go`

Create the interface file and update Agent to use it:

- `model.go`: `ContentGenerator` interface + `GeminiGenerator` wrapper + `OllamaGenerator` (OpenAI-compatible)
- `agent.go`: Change `client *genai.Client` → `generator ContentGenerator`
- `NewAgent()` signature: `NewAgent(gen ContentGenerator, model string, engine *mache.Engine)`
- `HandleIntent()` line 130: `a.generator.GenerateContent(...)` instead of `a.client.Models.GenerateContent(...)`

~80 lines new (OllamaGenerator needs genai↔OpenAI format translation), ~5 lines changed.

### Agent B: `cmd/agentd/main.go` + `internal/api/websocket.go`

Update wiring to use the interface:

**main.go:**
```go
var navGen navigator.ContentGenerator = &navigator.GeminiGenerator{Client: client}
navModel := model

if ep := os.Getenv("NAVIGATOR_ENDPOINT"); ep != "" {
    navModel = os.Getenv("NAVIGATOR_MODEL")
    if navModel == "" { navModel = "llama3.2" }
    // OllamaGenerator speaks OpenAI wire format — genai.Client can't be reused here.
    navGen = &navigator.OllamaGenerator{Endpoint: ep, Model: navModel}
    log.Printf("Navigator: using local model %s at %s", navModel, ep)
}

handler := api.NewHandler(cart, navGen, liveClient, navModel, liveModel)
```

**websocket.go:**
- `Handler` struct: replace `Client *genai.Client` + `Model string` with `NavGen navigator.ContentGenerator` + `NavModel string`
- `getSession()`: `navigator.NewAgent(h.NavGen, h.NavModel, engine)`
- `NewHandler()`: updated signature

~15 lines changed across both files.

### ~~Agent C: `internal/api/voice.go`~~ ✅ DONE

`text_input` case added to TextMessage handler. Uses `session.SendClientContent()` with `[]*genai.Content{{Role: "user", Parts: []*genai.Part{{Text: cmd.Text}}}}`. Committed: `a84f11f`.

### ~~Agent D: `static/voice.html`~~ ✅ DONE

Text input row added (input + Send button). Enabled on session ready. Enter/click sends `{"type":"text_input","text":"..."}`. Spacebar PTT skips when text input focused. Committed: `a84f11f`.

---

## Step 2: Reddit E2E Fixture

### File: `testdata/reddit/schema.json` (NEW)

Pre-captured Cartographer output so E2E tests don't need Gemini. Generate once:
```bash
go run ./cmd/gate testdata/reddit/page.html testdata/reddit/page.png > testdata/reddit/schema.json
```

---

## Step 3: Verify

```bash
# All tests (existing + new)
go test ./...

# Specific new tests
go test -run TestGeminiGeneratorImplementsInterface ./internal/navigator/...
go test -run TestHandleIntentWithMockGenerator ./internal/navigator/...
go test -run TestExecuteToolWithRedditFixture ./internal/navigator/...
go test -run TestE2EScrollSequence ./internal/api/...

# Local model (manual, requires ollama)
NAVIGATOR_ENDPOINT=http://localhost:11434/v1 NAVIGATOR_MODEL=llama3.2 go run ./cmd/agentd

# Build
go build ./...
```

---

## Execution Order & Parallelism

```
Step 0 (sequential):  Write interface (model.go)
                      Write all test files (5 files)
                      Generate testdata/reddit/schema.json
                      ↓
Step 1 (parallel):    Agent A: navigator/model.go + agent.go (interface impl)
                      Agent B: cmd/agentd/main.go + websocket.go (wiring)
                      Agent C: internal/api/voice.go (text_input)       ✅ DONE
                      Agent D: static/voice.html (text UI)              ✅ DONE
                      ↓
Step 2 (sequential):  go test ./... — verify everything passes
```

---

## Files Changed

| File | Change | Agent |
|------|--------|-------|
| `internal/navigator/model.go` | NEW — ContentGenerator interface + GeminiGenerator | Step 0 / Agent A |
| `internal/navigator/model_test.go` | NEW — interface contract + mock tests | Step 0 |
| `internal/navigator/local_test.go` | NEW — reddit fixture tool chain tests | Step 0 |
| `internal/api/voice_text_test.go` | NEW — text_input voice tests | Step 0 |
| `internal/api/e2e_scroll_test.go` | NEW — scroll sequence E2E tests | Step 0 |
| `cmd/agentd/main_test.go` | NEW — env var config tests | Step 0 |
| `testdata/reddit/schema.json` | NEW — pre-captured schema fixture | Step 0 |
| `internal/navigator/agent.go` | MODIFY — use ContentGenerator interface | Agent A |
| `cmd/agentd/main.go` | MODIFY — NAVIGATOR_ENDPOINT/MODEL, GeminiGenerator wiring | Agent B |
| `internal/api/websocket.go` | MODIFY — NavGen/NavModel in Handler | Agent B |
| `internal/api/voice.go` | ✅ DONE — text_input → SendClientContent | Agent C |
| `static/voice.html` | ✅ DONE — text input field in voice UI | Agent D |

**Already changed (bug fixes, not in original plan):**
- `ext/background.js` — mic permission check, MIC_GRANTED handler, content script auto-injection, pendingSnapshots dedup, SCHEMA_READY_EVENT
- `ext/popup.js` — simplified mic handler, SCHEMA_READY listener
- `ext/mic-setup.html` + `ext/mic-setup.js` — NEW, standalone mic permission grant window

**No changes to:** `cartographer/` (stays on Gemini), `mache/engine.go`, `interfaces.go` (IntentHandler unchanged — it already abstracts Navigator).
