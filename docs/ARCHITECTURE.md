# X-Ray Architecture

## System Diagram

```mermaid
graph TB
    subgraph Chrome["Chrome Extension (ext/)"]
        CS[content.js<br/>Tag IDs, DOM summary,<br/>Execute actions]
        BG[background.js<br/>WebSocket client,<br/>Screenshot capture]
        POP[popup.html/js<br/>Snapshot + Mic toggle]
        OFF[offscreen.js<br/>Voice audio bridge]
        CS <--> BG
        POP --> BG
        OFF <--> BG
    end

    subgraph Agentd["Agentd Backend (cmd/agentd)"]
        WS[WebSocket Handler<br/>internal/api/]
        VOICE[Voice Handler<br/>/voice endpoint]
        CART[Cartographer<br/>Stage 1]
        NAV[Navigator<br/>Stage 2]
        ENG[Mache Engine<br/>Virtual Filesystem]
        SESS[Per-Tab Sessions<br/>map tab_id → session]

        WS --> SESS
        VOICE --> SESS
        SESS --> CART
        SESS --> NAV
        CART --> ENG
        NAV --> ENG
    end

    subgraph GCP["Google Cloud"]
        GEMINI[Gemini 2.5 Flash<br/>Vision + Tool-Use]
        LIVE[Gemini Live API<br/>Native Audio]
        CR[Cloud Run<br/>Hosting]
    end

    BG <-->|"ws://host/ws<br/>DOM_SNAPSHOT, EXECUTE_ACTION"| WS
    OFF <-->|"ws://host/voice?tab=N<br/>PCM audio + JSON"| VOICE
    CART -->|"Screenshot + Summary<br/>→ Semantic JSON"| GEMINI
    NAV -->|"ls / cat / act<br/>Tool-use loop"| GEMINI
    VOICE <-->|"Audio stream<br/>+ Tool calls"| LIVE
    Agentd -.->|"Deployed on"| CR
```

## Data Flow (Per Interaction)

```mermaid
sequenceDiagram
    participant U as User
    participant E as Extension
    participant A as Agentd
    participant C as Cartographer
    participant M as Mache Engine
    participant N as Navigator
    participant G as Gemini

    U->>E: Click X-Ray icon
    E->>E: Inject data-mache-id tags
    E->>E: Generate DOM summary
    E->>E: Capture screenshot (PNG)
    E->>A: DOM_SNAPSHOT {tab_id, summary, screenshot}

    A->>C: GenerateSchema(screenshot, summary)
    C->>G: Gemini Vision (structured output)
    G-->>C: JSON schema {mounts: [...]}
    C-->>A: Schema JSON

    A->>M: ApplySchema(json)
    A->>M: LoadChildren(summary)
    A-->>E: SCHEMA_READY {tab_id, schema}

    U->>A: "Click the first story"
    A->>N: HandleIntent(intent)

    loop Tool-use (max 5 iterations)
        N->>G: GenerateContent(history + tools)
        G-->>N: FunctionCall: ls("/")
        N->>M: ListDir("/")
        M-->>N: ["header/", "main/", "footer/"]
        N->>G: ToolResponse("header/ main/ footer/")

        G-->>N: FunctionCall: cat("/main/story_list/children")
        N->>M: ReadFile(path)
        M-->>N: "--- Item 1 ---\n  mache-13 | a | ..."
        N->>G: ToolResponse(content)

        G-->>N: FunctionCall: act("/main/story_list/_c/mache-13", "click")
        N->>M: ResolveMacheID(path)
        M-->>N: "mache-13"
    end

    N-->>A: ActionResult{MacheID: "mache-13", Action: "click"}
    A->>E: EXECUTE_ACTION {tab_id, mache_id, action}
    E->>E: element.click()
```

## Voice Data Flow

```mermaid
sequenceDiagram
    participant B as Browser (offscreen.js)
    participant A as Agentd (/voice)
    participant G as Gemini Live API
    participant E as Extension (content.js)

    B->>A: ws://host/voice?tab=123
    A->>G: Live.Connect(model, tools, audio modality)
    G-->>A: SetupComplete
    A-->>B: {"type":"ready"}

    Note over B,A: Mic ON — always-on, Gemini handles VAD

    loop Audio streaming
        B->>A: Binary frame (16kHz PCM)
        A->>G: SendRealtimeInput(audio)
    end

    G-->>A: InputTranscription: "click the first story"
    A-->>B: {"type":"input_transcription"}

    G-->>A: ToolCall: ls("/")
    A->>A: engine.ListDir("/")
    A->>G: SendToolResponse("header/ main/ footer/")

    Note over A: Audio suppressed during tool loop

    G-->>A: ToolCall: act("/main/story/_c/mache-13", "click")
    A->>A: engine.ResolveMacheID → "mache-13"
    A->>E: EXECUTE_ACTION via extension WS
    E->>E: element.click()
    A->>G: SendToolResponse("Clicked mache-13")

    G-->>A: ServerContent (audio: "Done!")
    A-->>B: Binary frame (24kHz PCM)
    G-->>A: OutputTranscription: "Done, I clicked the first story."
    A-->>B: {"type":"output_transcription"}
```

## Per-Tab Session Architecture

```mermaid
graph LR
    subgraph Handler
        SM["sessions map[int]*TabSession"]
    end

    subgraph Tab_A["Tab A (id: 123)"]
        EA[Engine A]
        NA[Navigator A]
    end

    subgraph Tab_B["Tab B (id: 456)"]
        EB[Engine B]
        NB[Navigator B]
    end

    SM -->|"getSession(123)"| Tab_A
    SM -->|"getSession(456)"| Tab_B

    CART[Cartographer<br/>Shared / Stateless] --> EA
    CART --> EB
```

## Virtual Filesystem Layout

After the Cartographer maps a page and the engine loads children:

```
/
├── header/
│   └── global_nav/
│       ├── mache_id          → "mache-0"
│       └── description       → "Top navigation bar"
├── main/
│   └── story_list/
│       ├── mache_id          → "mache-15"
│       ├── description       → "List of news stories"
│       ├── children           → "--- Item 1 ---\n  mache-13 | a | \"First Story\"..."
│       └── _c/
│           ├── mache-13/
│           │   ├── mache_id  → "mache-13"
│           │   ├── tag       → "a"
│           │   └── text      → "First Story Title"
│           ├── mache-14/
│           └── ...
└── footer/
    └── links/
        ├── mache_id          → "mache-200"
        └── description       → "Footer with legal links"
```

## Audio Formats

| Leg | Format | Sample Rate |
|-----|--------|-------------|
| Browser → Agentd | 16-bit PCM, mono | 16 kHz |
| Agentd → Gemini Live | Same (proxied) | 16 kHz |
| Gemini Live → Agentd | 16-bit PCM, mono | 24 kHz |
| Agentd → Browser | Same (proxied) | 24 kHz |

## Components

### Chrome Extension (`ext/`)

Six files, no build step, no dependencies.

- **`content.js`**: Injected into every page. Tags interactive elements with `data-mache-id`, generates flat text summary, executes browser actions on command.
- **`background.js`**: Service worker. WebSocket to agentd, screenshot capture, offscreen doc lifecycle, per-tab schema tracking.
- **`popup.html/js`**: Extension popup. Snapshot button, mic toggle, session kill button.
- **`offscreen.html/js`**: Persistent voice audio bridge. Mic capture (48→16kHz downsample), PCM streaming, audio playback (24kHz).
- **`manifest.json`**: Manifest V3. Permissions: `activeTab`, `scripting`, `tabs`, `offscreen`.

### WebSocket Handler (`internal/api/`)

- Per-tab session registry (`sessions map[int]*TabSession`)
- Inbound: `DOM_SNAPSHOT` (screenshot + summary + tab_id), `NAVIGATE` (intent + tab_id)
- Outbound: `SCHEMA_READY`, `EXECUTE_ACTION`, `STATUS` — all include `tab_id`
- Voice handler: `/voice?tab=N` — Gemini Live proxy with server-side audio suppression during tool loops
- `POST /navigate` for curl/testing

### Cartographer (`internal/cartographer/`)

Stage 1. Screenshot + DOM summary → semantic JSON schema.

- Gemini Vision with structured output (`ResponseSchema`)
- 3-7 zones, primary items for list zones
- Temperature 0.1, validation + retry on hallucination

### Navigator (`internal/navigator/`)

Stage 2. User intent → browser action via filesystem traversal.

- Three tools: `ls`, `cat`, `act`
- Max 5 tool-use iterations (typical: 3-4)
- Temperature 0.1
- Returns `ActionResult{MacheID, Action, Path}` or text explanation

### Mache Engine (`internal/mache/`)

In-memory virtual filesystem from Cartographer output.

- `ApplySchema()` → directory tree
- `LoadChildren()` → BFS from zone roots, max depth 2, max 30 children
- `ListDir()` / `ReadFile()` / `ResolveMacheID()` — Navigator's tool implementations

## Google Cloud Services

- **Gemini 2.5 Flash** via GenAI Go SDK (`google.golang.org/genai`) — Cartographer + Navigator
- **Gemini Live API** (native audio, `v1alpha`) — real-time voice interaction
- **Cloud Run** — serverless hosting for agentd backend
- **Cloud Build** — container image builds from source
