package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/genai"
)

// browserFrame is a message read from the browser WebSocket, pushed to the
// session-scoped sender via a channel so that only one goroutine ever calls
// conn.ReadMessage (Gorilla forbids concurrent readers).
type browserFrame struct {
	msgType int
	data    []byte
}

// maxNonGoAwayRetries is the maximum number of consecutive non-GoAway Receive
// errors before giving up. Resets on successful Receive or GoAway.
const maxNonGoAwayRetries = 3

// liveReconnectState tracks retry state for Gemini Live session reconnection.
type liveReconnectState struct {
	ResumeHandle string
	Retries      int
}

// shouldReconnect decides whether to reconnect after a Receive error and waits
// with backoff if so. Returns true if the caller should reconnect, false if it
// should give up. Handles both resume-handle and fresh-session reconnection.
// Known Gemini Live issue: 1011 during tool execution drops the session.
func (s *liveReconnectState) shouldReconnect(ctx context.Context, label string) bool {
	if s.Retries < maxNonGoAwayRetries {
		s.Retries++
		if s.ResumeHandle != "" {
			log.Printf("Voice%s: reconnecting with resume handle (attempt %d/%d)...",
				label, s.Retries, maxNonGoAwayRetries)
		} else {
			log.Printf("Voice%s: reconnecting with fresh session (attempt %d/%d)...",
				label, s.Retries, maxNonGoAwayRetries)
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(time.Duration(s.Retries) * time.Second):
		}
		return true
	}
	// Max retries exceeded — reset and try a completely fresh session.
	log.Printf("Voice%s: max retries (%d) exceeded, creating fresh session",
		label, maxNonGoAwayRetries)
	s.ResumeHandle = ""
	s.Retries = 0
	return true
}

// liveConnector abstracts Live.Connect for testability.
type liveConnector func(ctx context.Context, model string, config *genai.LiveConnectConfig) (*genai.Session, error)

// voiceMessage is the JSON envelope for text messages on the voice WebSocket.
type voiceMessage struct {
	Type       string `json:"type"`
	Text       string `json:"text,omitempty"`
	MacheID    string `json:"mache_id,omitempty"`
	Action     string `json:"action,omitempty"`
	Payload    string `json:"payload,omitempty"` // text for "type" action
	SampleRate int    `json:"sample_rate,omitempty"`
}

// talkerToolDefinitions returns the Talker's tools: check_interaction, create_interaction, cancel_interaction.
// These execute instantly (no I/O, no blocking) so the Talker is never muted.
func talkerToolDefinitions() []*genai.Tool {
	return []*genai.Tool{{
		FunctionDeclarations: []*genai.FunctionDeclaration{
			{
				Name:        "check_interaction",
				Description: "Check what the background navigator is currently doing. Returns the current intent, step, and result if finished. Use this when the user asks 'what are you doing?' or to check if a previous command has completed.",
				Parameters: &genai.Schema{
					Type:       genai.TypeObject,
					Properties: map[string]*genai.Schema{},
				},
			},
			{
				Name:        "create_interaction",
				Description: "Send a navigation command to the background executor. Examples: 'click the first story', 'go to reddit.com', 'search for golang tutorials', 'scroll down'. The command will execute in the background while you remain available to chat.",
				Parameters: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"intent":      {Type: genai.TypeString, Description: "Natural language navigation goal"},
						"read_only":   {Type: genai.TypeBoolean, Description: "Set true when the user asks a QUESTION about the page (what, which, list, describe, tell me). Leave false/omit when the user wants an ACTION (click, play, open, search, type)."},
						"context":     {Type: genai.TypeString, Description: "Optional: the user's overarching intent behind this command, so the navigator can adapt if the direct approach fails."},
						"previous_id": {Type: genai.TypeString, Description: "Optional: ID of the previous interaction for chaining multi-step tasks."},
					},
					Required: []string{"intent"},
				},
			},
			{
				Name:        "cancel_interaction",
				Description: "Cancel the current background navigation task. Use when the user says stop, cancel, or nevermind.",
				Parameters: &genai.Schema{
					Type:       genai.TypeObject,
					Properties: map[string]*genai.Schema{},
				},
			},
			{
				Name:        "open_url",
				Description: "Open a URL in a NEW browser tab. Use when no tab is active or user says 'open [website]'.",
				Parameters: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"url": {Type: genai.TypeString, Description: "URL to open (e.g. 'https://crunchyroll.com'). Add https:// if omitted."},
					},
					Required: []string{"url"},
				},
			},
			{
				Name:        "terminal_action",
				Description: "Execute a simple terminal command INSTANTLY (bypasses the background queue). Use for: opening windows/tabs, typing short commands, sending special keys. For complex multi-step terminal tasks that need filesystem navigation, use create_interaction instead.",
				Parameters: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"action": {Type: genai.TypeString, Description: "One of: new_window, new_tab, type, enter, focus"},
						"text":   {Type: genai.TypeString, Description: "For 'type': the text to send (include \\n for Enter). For 'enter': special key name (e.g. ctrl-c, ctrl-d)."},
					},
					Required: []string{"action"},
				},
			},
			{
				Name:        "screen_share",
				Description: "Toggle screen sharing so you can SEE the user's browser page. Call with enabled=true when the user says 'look at my screen', 'can you see this', 'take a look', etc. Call with enabled=false when done or the user says 'stop looking'. While enabled, you receive live page screenshots as video frames after every page capture.",
				Parameters: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"enabled": {Type: genai.TypeBoolean, Description: "true to start receiving screenshots, false to stop"},
					},
					Required: []string{"enabled"},
				},
			},
		},
	}}
}

// executeOpenURL opens a new browser tab via the extension. App-scoped (no doer needed).
func (h *Handler) executeOpenURL(fc *genai.FunctionCall) string {
	url, _ := fc.Args["url"].(string)
	if url == "" {
		return "Error: url is required."
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = "https://" + url
	}
	h.sendCreateTab(url)
	return fmt.Sprintf("Opening %s in a new tab. The page will load in a few seconds.", url)
}

// executeTerminalAction handles the terminal_action tool — direct bridge calls
// that bypass the Doer queue entirely for instant terminal operations.
func (h *Handler) executeTerminalAction(fc *genai.FunctionCall) string {
	if h.termBridge == nil {
		return "No terminal available. iTerm2 is not connected."
	}
	action, _ := fc.Args["action"].(string)
	text, _ := fc.Args["text"].(string)

	switch action {
	case "new_window":
		_, err := h.termBridge.Act("", "new_window", "")
		if err != nil {
			return fmt.Sprintf("Error opening window: %v", err)
		}
		return "Opened a new terminal window. It is now the active session."
	case "new_tab":
		_, err := h.termBridge.Act("", "new_tab", "")
		if err != nil {
			return fmt.Sprintf("Error opening tab: %v", err)
		}
		return "Opened a new tab. It is now the active session."
	case "type":
		_, err := h.termBridge.Act("active_session", "type", text)
		if err != nil {
			return fmt.Sprintf("Error typing: %v", err)
		}
		return fmt.Sprintf("Typed %q in the active terminal.", text)
	case "enter":
		_, err := h.termBridge.Act("active_session", "enter", text)
		if err != nil {
			return fmt.Sprintf("Error sending key: %v", err)
		}
		return fmt.Sprintf("Sent %s to the active terminal.", text)
	case "focus":
		_, err := h.termBridge.Act("active_session", "focus", "")
		if err != nil {
			return fmt.Sprintf("Error focusing: %v", err)
		}
		return "Focused the active terminal window."
	default:
		return fmt.Sprintf("Unknown terminal action: %s", action)
	}
}

// executeScreenShare toggles video frame streaming to the Talker's Live session.
func (h *Handler) executeScreenShare(fc *genai.FunctionCall) string {
	enabled, _ := fc.Args["enabled"].(bool)
	h.videoEnabled.Store(enabled)
	if enabled {
		log.Printf("Voice: screen_share enabled")
		// Send an immediate frame so the model gets instant visual context.
		tid := h.getVoiceTabID()
		if tid != 0 {
			if s := h.getSession(tid); s != nil {
				if img, mime := s.GetScreenshot(); len(img) > 0 {
					select {
					case h.videoFrameCh <- videoFrame{Data: img, MIME: mime}:
					default:
					}
				}
			}
		}
		return "Screen sharing enabled. You will now receive page screenshots as video frames after every page capture. Describe what you see."
	}
	log.Printf("Voice: screen_share disabled")
	return "Screen sharing disabled. You will no longer receive page screenshots."
}

// executeTalkerTool dispatches a Talker tool call. All tools return instantly.
func (h *Handler) executeTalkerTool(fc *genai.FunctionCall, doer *Doer) string {
	if fc.Name == "open_url" {
		return h.executeOpenURL(fc)
	}
	if fc.Name == "terminal_action" {
		return h.executeTerminalAction(fc)
	}
	if fc.Name == "screen_share" {
		return h.executeScreenShare(fc)
	}
	if doer == nil {
		return "No active browser tab or terminal session. Use open_url to open a website first."
	}
	switch fc.Name {
	case "check_interaction":
		status, intent, step, result := doer.State().Snapshot()
		switch status {
		case StatusIdle:
			return "No task in progress. Ready for a new command."
		case StatusInProgress:
			return fmt.Sprintf("Working on: %q. Current step: %s", intent, step)
		case StatusCompleted:
			if result != nil {
				return fmt.Sprintf("Completed: %s", result.Summary)
			}
			return "Task completed."
		case StatusFailed:
			if result != nil {
				return fmt.Sprintf("Failed: %s", result.Summary)
			}
			return "Task failed."
		case StatusCancelled:
			if result != nil {
				return fmt.Sprintf("Cancelled: %s", result.Summary)
			}
			return "Task was cancelled."
		}
		return "Unknown status."

	case "create_interaction":
		intent, _ := fc.Args["intent"].(string)
		if intent == "" {
			return "Error: intent is required."
		}
		readOnly, _ := fc.Args["read_only"].(bool)
		taskContext, _ := fc.Args["context"].(string)
		previousID, _ := fc.Args["previous_id"].(string)
		ixID := fmt.Sprintf("ix-%d", time.Now().UnixMilli())
		started := doer.Submit(Interaction{ID: ixID, Intent: intent, ReadOnly: readOnly, Context: taskContext, PreviousID: previousID})
		if readOnly {
			log.Printf("Voice: create_interaction (read_only=true): %q", intent)
		}
		// Wait for the Doer to pick up the interaction. If it fails fast
		// (model 404, bad config), we catch it here instead of speaking
		// an optimistic message about a dead interaction.
		<-started
		if status, _, _, result := doer.State().Snapshot(); status == StatusFailed && result != nil {
			log.Printf("Voice: create_interaction early failure: %s", result.Summary)
			return fmt.Sprintf("Command failed immediately: %s. Tell the user what went wrong.", result.Summary)
		}
		return fmt.Sprintf("Command accepted: %q. Tell the user what you're about to do in a natural way, e.g. \"I'll look up the issues for this repo\" or \"Let me open that page for you.\" Be specific to the task. (interaction_id: %s)", intent, ixID)

	case "cancel_interaction":
		doer.Cancel()
		return "Task cancelled."

	default:
		return fmt.Sprintf("Unknown tool: %s", fc.Name)
	}
}

// talkerSystemPrompt instructs the Talker to stay conversational and delegate all page work.
var talkerSystemPrompt = `You are a VOICE assistant with a background navigator that can see and interact with the user's browser and terminal sessions.

YOUR TOOLS:
- create_interaction(intent, read_only?): Dispatch ANY system-related task to your background navigator. This works for actions AND questions about the browser or terminal. Examples: "click the first story", "go to reddit.com", "check if I'm logged in", "read the main heading", "what's running in my terminal?", "type npm start in the terminal".
  Set read_only=true when the user asks a QUESTION about the environment (e.g., "what's playing?", "what's in the terminal?", "describe the page").
  Leave read_only=false (or omit) when the user wants an ACTION (e.g., "click...", "play...", "open...", "search for...", "type...", "go to...").
- check_interaction(): Check what the navigator is currently doing. Returns intent, current step, and result if finished.
- cancel_interaction(): Cancel the current background task.
- open_url(url): Open a URL in a NEW browser tab. Use when no tab exists or user explicitly says "open [website]".
- terminal_action(action, text?): Execute a simple terminal command INSTANTLY — no background queue.
  Actions: "new_window" (open terminal), "new_tab", "type" (send text, include \n for Enter), "enter" (special keys like ctrl-c), "focus" (bring terminal to front).
  Use this for quick terminal operations. For complex multi-step terminal tasks (e.g., "find the process and kill it"), use create_interaction instead.

BEHAVIOR:
1. When the user asks you to do something OR asks a question about their environment (browser or terminal), call create_interaction() IMMEDIATELY without speaking first. Do NOT say anything before the tool call — just call it silently.
2. After the tool returns "Command accepted", say ONE short phrase describing what you're about to do, specific to the task. Examples: "I'll look up the issues for this repo.", "Let me open that page.", "Checking the terminal output." Do NOT guess or predict the answer — the result is not ready yet.
3. CRITICAL: You will receive "[SYSTEM: Background task completed. Result: ...]" when the real result is ready. ONLY THEN announce the result. Until that notification arrives, you do NOT know the answer. NEVER fabricate a response.
4. If the user asks "what are you doing?" or "are you almost done?", call check_interaction() and report the current step briefly.
5. If the user says "stop" or "cancel", use cancel_interaction() and confirm: "Cancelled."
6. You can answer general knowledge questions directly using Google Search — no need to create_interaction for those.
7. Match verbosity to the request. Use one short sentence for simple actions ("Clicking the button"), but give structured multi-sentence responses when reading data back to the user.
8. If you get "No active browser tab", use open_url() to open a website first, then create_interaction() after it loads.
9. SAFETY: For irreversible actions (buy, submit, delete, send, post, confirm, checkout, pay), ALWAYS confirm before dispatching: "I'll click 'Submit Order' — should I go ahead?"

Your navigator can read the full environment structure (including terminals at /iterm/), so ALWAYS delegate environment questions to it — never say "I can't see the terminal."

VISION: You receive page screenshots as video frames whenever the browser captures a new page. You can see the page layout, overlay zones, and mache-ID labels. Use this visual context to give more informed responses — but still delegate actions to create_interaction().`

// buildLiveConfig returns the LiveConnectConfig shared by HandleVoice and StartVoiceLoop.
func buildLiveConfig(language, voice string) *genai.LiveConnectConfig {
	tools := append(talkerToolDefinitions(), &genai.Tool{
		GoogleSearch: &genai.GoogleSearch{},
	})
	cfg := &genai.LiveConnectConfig{
		ResponseModalities: []genai.Modality{genai.ModalityAudio},
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{{Text: talkerSystemPrompt}},
		},
		Tools:                    tools,
		InputAudioTranscription:  &genai.AudioTranscriptionConfig{},
		OutputAudioTranscription: &genai.AudioTranscriptionConfig{},
		SessionResumption:        &genai.SessionResumptionConfig{},
		ContextWindowCompression: &genai.ContextWindowCompressionConfig{
			SlidingWindow: &genai.SlidingWindow{},
		},
		// Proactive audio: model can stay silent on irrelevant/ambient input.
		Proactivity: &genai.ProactivityConfig{
			ProactiveAudio: genai.Ptr(true),
		},
		// Affective dialog: adapt tone to match user's expression.
		EnableAffectiveDialog: genai.Ptr(true),
		// Thinking: give model a budget to reason before responding.
		ThinkingConfig: &genai.ThinkingConfig{
			IncludeThoughts: false,
			ThinkingBudget:  genai.Ptr(int32(1024)),
		},
	}

	// Language and voice selection.
	if language != "" || voice != "" {
		cfg.SpeechConfig = &genai.SpeechConfig{}
		if language != "" {
			cfg.SpeechConfig.LanguageCode = language
		}
		if voice != "" {
			cfg.SpeechConfig.VoiceConfig = &genai.VoiceConfig{
				PrebuiltVoiceConfig: &genai.PrebuiltVoiceConfig{
					VoiceName: voice,
				},
			}
		}
	}

	return cfg
}

// applyResumeHandle sets the session resumption handle on a LiveConnectConfig.
// If handle is empty, the config retains its default empty SessionResumption
// (opt-in to resumption updates without restoring a previous session).
func applyResumeHandle(config *genai.LiveConnectConfig, handle string) {
	if handle != "" {
		config.SessionResumption = &genai.SessionResumptionConfig{Handle: handle}
	}
}

// connectWithBackoff wraps a Live.Connect call with exponential backoff.
// Up to 3 attempts with delays of 1s then 2s. Respects ctx cancellation.
func connectWithBackoff(ctx context.Context, connect liveConnector, model string, config *genai.LiveConnectConfig) (*genai.Session, error) {
	const maxAttempts = 3
	var lastErr error
	for attempt := range maxAttempts {
		session, err := connect(ctx, model, config)
		if err == nil {
			return session, nil
		}
		lastErr = err
		if attempt == maxAttempts-1 {
			break
		}
		delay := time.Duration(1<<uint(attempt)) * time.Second // 1s, 2s
		log.Printf("Voice: Live.Connect attempt %d/%d failed: %v (retry in %s)", attempt+1, maxAttempts, err, delay)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
	return nil, fmt.Errorf("Live.Connect failed after %d attempts: %w", maxAttempts, lastErr)
}

// HandleVoice upgrades to WebSocket and proxies audio between the browser and
// Gemini's Live API. Navigation work is delegated to the Doer goroutine;
// the Talker stays responsive with instant check_interaction/create_interaction tools.
//
// Query param: ?tab=<tabId> associates this voice session with a tab's schema.
func (h *Handler) HandleVoice(w http.ResponseWriter, r *http.Request) {
	// Parse tab ID from query string.
	tabID := 0
	if s := r.URL.Query().Get("tab"); s != "" {
		if id, err := strconv.Atoi(s); err == nil {
			tabID = id
		}
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Voice: WebSocket upgrade failed: %v", err)
		return
	}
	defer func() { _ = conn.Close() }()

	ctx := r.Context()
	log.Printf("Voice: browser connected (tab %d)", tabID)

	// Mutex protects writes to the browser WS.
	var wsMu sync.Mutex

	// actionNotifyFn forwards Doer actions to the browser voice WS.
	actionNotifyFn := func(macheID, action, payload string) {
		sendVoiceJSON(conn, &wsMu, voiceMessage{
			Type:    MsgExecuteAction,
			MacheID: macheID,
			Action:  action,
			Payload: payload,
		})
	}

	// Single reader goroutine: browser WebSocket → channel.
	// Lives for the entire HandleVoice lifetime (not per-session) so that only
	// one goroutine ever calls conn.ReadMessage (Gorilla forbids concurrent readers).
	browserCh := make(chan browserFrame, 8)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			msgType, data, err := conn.ReadMessage()
			if err != nil {
				log.Printf("Voice: browser read error: %v", err)
				return
			}
			browserCh <- browserFrame{msgType: msgType, data: data}
		}
	}()

	// Session resumption: reconnect-on-GoAway outer loop.
	// The browser WS stays open; only the Gemini Live session reconnects.
	var rs liveReconnectState
	var inputBuf, outputBuf strings.Builder
	for {
		config := buildLiveConfig(h.VoiceLanguage, h.VoiceName)
		applyResumeHandle(config, rs.ResumeHandle)

		session, err := connectWithBackoff(ctx, h.LiveClient.Live.Connect, h.LiveModel, config)
		if err != nil {
			log.Printf("Voice: Live API connect failed: %v", err)
			sendVoiceJSON(conn, nil, voiceMessage{Type: "error", Text: "Live API connect failed: " + err.Error()})
			return
		}
		if rs.ResumeHandle != "" {
			log.Printf("Voice: reconnected with resume handle (tab %d)", tabID)
		} else {
			log.Printf("Voice: Gemini Live session established (tab %d)", tabID)
		}

		// Mutex protects concurrent writes to the Gemini Live session (Bug fix).
		var sessionMu sync.Mutex

		// State-aware result delivery: if the model is speaking, queue the
		// result for delivery on TurnComplete. If idle, deliver immediately.
		var modelSpeaking atomic.Bool
		pendingResult := make(chan string, 1)

		sendResultToSession := func(msg string) {
			sessionMu.Lock()
			err := session.SendClientContent(genai.LiveClientContentInput{
				Turns: []*genai.Content{{
					Role:  "user",
					Parts: []*genai.Part{{Text: msg}},
				}},
			})
			sessionMu.Unlock()
			if err != nil {
				log.Printf("Voice: result notify SendClientContent error: %v", err)
			}
		}

		deliverPendingResult := func() {
			select {
			case notification := <-pendingResult:
				sendResultToSession(notification)
			default:
			}
		}

		// Wire Doer result callback to this session.
		resultNotifyFn := func(summary string) {
			msg := fmt.Sprintf(
				"[SYSTEM: Background task completed. Result: %s. Announce this to the user briefly.]",
				summary,
			)
			if !modelSpeaking.Load() {
				// Model is idle — deliver immediately.
				sendResultToSession(msg)
				return
			}
			// Model is speaking — queue for delivery on TurnComplete.
			select {
			case <-pendingResult:
			default:
			}
			select {
			case pendingResult <- msg:
			default:
			}
		}

		// resolveDoer dynamically resolves the Doer for the active voice tab.
		// Falls back to tab 0 ("system" session) when no browser tab is open
		// but iTerm is available — allows terminal-only voice commands.
		resolveDoer := func() *Doer {
			tid := h.getVoiceTabID()
			if tid == 0 {
				// Fall back to the original tab ID from the query param.
				tid = tabID
			}
			if tid == 0 && h.termBridge == nil {
				return nil
			}
			sess := h.getSession(tid)
			doer := h.getOrCreateDoer(tid, sess)
			doer.SetResultNotifyFn(resultNotifyFn)
			doer.SetActionNotifyFn(actionNotifyFn)
			return doer
		}

		// --- goroutine 1: browser → Gemini (audio chunks) ---
		// Reads from browserCh (fed by the single reader goroutine above).
		// sessionCtx is cancelled on GoAway/error to kill the sender cleanly.
		sessionCtx, sessionCancel := context.WithCancel(ctx)
		senderDone := make(chan struct{})
		go func() {
			defer close(senderDone)
			var audioChunks int
			var audioBytes int
			lastLog := time.Now()
			for {
				select {
				case <-sessionCtx.Done():
					return
				case <-readerDone:
					// Browser WS closed — tear down the Gemini session.
					_ = session.Close()
					return
				case frame := <-browserCh:
					switch frame.msgType {
					case websocket.BinaryMessage:
						audioChunks++
						audioBytes += len(frame.data)
						if time.Since(lastLog) >= 5*time.Second {
							log.Printf("Voice [tab %d]: receiving audio — %d chunks, %d bytes in last 5s", tabID, audioChunks, audioBytes)
							audioChunks = 0
							audioBytes = 0
							lastLog = time.Now()
						}
						sessionMu.Lock()
						err := session.SendRealtimeInput(genai.LiveRealtimeInput{
							Audio: &genai.Blob{
								Data:     frame.data,
								MIMEType: "audio/pcm;rate=16000",
							},
						})
						sessionMu.Unlock()
						if err != nil {
							log.Printf("Voice: SendRealtimeInput error: %v", err)
							return
						}
					case websocket.TextMessage:
						var cmd voiceMessage
						if err := json.Unmarshal(frame.data, &cmd); err != nil {
							continue
						}
						switch cmd.Type {
						case "mic_stop":
							log.Println("Voice: mic released, sending AudioStreamEnd")
							sessionMu.Lock()
							err := session.SendRealtimeInput(genai.LiveRealtimeInput{
								AudioStreamEnd: true,
							})
							sessionMu.Unlock()
							if err != nil {
								log.Printf("Voice: AudioStreamEnd error: %v", err)
							}
						case "text_input":
							if cmd.Text == "" {
								continue
							}
							log.Printf("Voice [tab %d]: text input: %s", tabID, cmd.Text)
							sessionMu.Lock()
							err := session.SendClientContent(genai.LiveClientContentInput{
								Turns: []*genai.Content{
									{Role: "user", Parts: []*genai.Part{{Text: cmd.Text}}},
								},
							})
							sessionMu.Unlock()
							if err != nil {
								log.Printf("Voice: SendClientContent error: %v", err)
							}
						}
					}
				case vf := <-h.videoFrameCh:
					// Page screenshot → Gemini Live as a video frame.
					log.Printf("Voice [tab %d]: sending video frame (%d bytes, %s)", tabID, len(vf.Data), vf.MIME)
					sessionMu.Lock()
					err := session.SendRealtimeInput(genai.LiveRealtimeInput{
						Video: &genai.Blob{Data: vf.Data, MIMEType: vf.MIME},
					})
					sessionMu.Unlock()
					if err != nil {
						log.Printf("Voice: SendRealtimeInput video error: %v", err)
					}
				}
			}
		}()

		// --- goroutine 2 (this goroutine): Gemini → browser ---
		shouldReconnect := false
		for {
			msg, err := session.Receive()
			if err != nil {
				log.Printf("Voice: Receive error: %v", err)
				shouldReconnect = rs.shouldReconnect(ctx, fmt.Sprintf(" [tab %d]", tabID))
				break
			}
			rs.Retries = 0 // reset on successful receive

			if msg.SetupComplete != nil {
				log.Println("Voice: Live session setup complete")
				sendVoiceJSON(conn, &wsMu, voiceMessage{Type: "ready"})
				continue
			}

			if sc := msg.ServerContent; sc != nil {
				// Always forward audio — browser flushes its playback buffer on "interrupted".
				if sc.ModelTurn != nil {
					modelSpeaking.Store(true)
					for _, part := range sc.ModelTurn.Parts {
						if part.InlineData != nil && len(part.InlineData.Data) > 0 {
							wsMu.Lock()
							_ = conn.WriteMessage(websocket.BinaryMessage, part.InlineData.Data)
							wsMu.Unlock()
						}
						if part.Text != "" {
							sendVoiceJSON(conn, &wsMu, voiceMessage{
								Type: "model_text", Text: part.Text,
							})
						}
					}
				}

				// Transcriptions: accumulate chunks, forward each to browser
				// for real-time display, but only log complete utterances.
				if sc.InputTranscription != nil && sc.InputTranscription.Text != "" {
					inputBuf.WriteString(sc.InputTranscription.Text)
					sendVoiceJSON(conn, &wsMu, voiceMessage{
						Type: "input_transcription", Text: sc.InputTranscription.Text,
					})
				}
				if sc.OutputTranscription != nil && sc.OutputTranscription.Text != "" {
					outputBuf.WriteString(sc.OutputTranscription.Text)
					sendVoiceJSON(conn, &wsMu, voiceMessage{
						Type: "output_transcription", Text: sc.OutputTranscription.Text,
					})
				}

				if sc.Interrupted {
					sendVoiceJSON(conn, &wsMu, voiceMessage{Type: "interrupted"})
				}
				if sc.GenerationComplete {
					sendVoiceJSON(conn, &wsMu, voiceMessage{Type: "generation_complete"})
				}
				if sc.TurnComplete {
					sendVoiceJSON(conn, &wsMu, voiceMessage{Type: "turn_complete"})
				}
				// Flush buffered transcriptions on turn boundary.
				if sc.TurnComplete || sc.Interrupted {
					modelSpeaking.Store(false)
					if inputBuf.Len() > 0 {
						log.Printf("Voice [tab %d] User: %s", tabID, strings.TrimSpace(inputBuf.String()))
						inputBuf.Reset()
					}
					if outputBuf.Len() > 0 {
						log.Printf("Voice [tab %d] Talker: %s", tabID, strings.TrimSpace(outputBuf.String()))
						outputBuf.Reset()
					}
				}
				// Only deliver pending results on TurnComplete (model finished naturally).
				// On Interrupted (user spoke), keep the result queued — it'll deliver
				// after the model responds to the user. User can also check_interaction().
				if sc.TurnComplete {
					deliverPendingResult()
				}
				continue
			}

			// ToolCall — Talker tools execute instantly.
			if tc := msg.ToolCall; tc != nil {
				var responses []*genai.FunctionResponse
				for _, fc := range tc.FunctionCalls {
					switch fc.Name {
					case "check_interaction", "create_interaction", "cancel_interaction", "open_url", "terminal_action", "screen_share":
						result := h.executeTalkerTool(fc, resolveDoer())
						log.Printf("Voice: Talker tool %s → %q", fc.Name, result)
						responses = append(responses, &genai.FunctionResponse{
							ID:       fc.ID,
							Name:     fc.Name,
							Response: map[string]any{"output": result},
						})
					default:
						log.Printf("Voice: skipping server-side tool %s (tab %d)", fc.Name, tabID)
						continue
					}
				}
				if len(responses) > 0 {
					sessionMu.Lock()
					err := session.SendToolResponse(genai.LiveToolResponseInput{
						FunctionResponses: responses,
					})
					sessionMu.Unlock()
					if err != nil {
						log.Printf("Voice: SendToolResponse error: %v", err)
						break
					}
				}
				continue
			}

			if msg.ToolCallCancellation != nil {
				log.Printf("Voice: tool call cancelled: %v", msg.ToolCallCancellation)
				continue
			}

			if msg.UsageMetadata != nil {
				log.Printf("Voice [tab %d]: tokens: %d total (%d prompt, %d response)",
					tabID,
					msg.UsageMetadata.TotalTokenCount,
					msg.UsageMetadata.PromptTokenCount,
					msg.UsageMetadata.ResponseTokenCount)
			}

			// Session resumption: store handle for reconnect.
			if msg.SessionResumptionUpdate != nil && msg.SessionResumptionUpdate.Resumable {
				rs.ResumeHandle = msg.SessionResumptionUpdate.NewHandle
			}

			// GoAway: server is about to disconnect, reconnect with resume handle.
			if msg.GoAway != nil {
				log.Printf("Voice [tab %d]: GoAway received (time left: %s), reconnecting...", tabID, msg.GoAway.TimeLeft)
				shouldReconnect = true
				rs.Retries = 0 // GoAway is expected, reset transient counter
				break
			}
		}

		sessionCancel()
		<-senderDone // wait for sender goroutine to fully exit before reconnecting
		_ = session.Close()
		if d := resolveDoer(); d != nil {
			d.SetResultNotifyFn(nil)
		}
		if !shouldReconnect {
			break
		}
		log.Printf("Voice [tab %d]: reconnecting...", tabID)
	}

	log.Printf("Voice: session ended (tab %d)", tabID)
}

// StartVoiceLoop runs the native voice mode using sox mic/speaker pipes.
// mic delivers PCM chunks from the Recorder. speaker receives PCM chunks to play.
// textIn delivers typed text intents from stdin. Runs until ctx is cancelled.
func (h *Handler) StartVoiceLoop(ctx context.Context, mic <-chan []byte, speaker chan<- []byte, textIn <-chan string) error {
	// Echo gate: suppress mic input while Gemini is speaking to prevent feedback.
	// Set to 1 when audio flows to the speaker, cleared 1s after the last chunk.
	var speaking atomic.Int32
	var speakingTimer *time.Timer

	markSpeaking := func() {
		speaking.Store(1)
		if speakingTimer != nil {
			speakingTimer.Stop()
		}
		speakingTimer = time.AfterFunc(1000*time.Millisecond, func() {
			speaking.Store(0)
		})
	}

	// VAD interruption: suppress buffered audio when the user speaks over the model.
	var interrupted atomic.Bool

	// Session resumption: reconnect-on-GoAway outer loop.
	var rs liveReconnectState
	var inputBuf, outputBuf strings.Builder
	for {
		config := buildLiveConfig(h.VoiceLanguage, h.VoiceName)
		applyResumeHandle(config, rs.ResumeHandle)

		session, err := connectWithBackoff(ctx, h.LiveClient.Live.Connect, h.LiveModel, config)
		if err != nil {
			return fmt.Errorf("voice: Live API connect: %w", err)
		}
		if rs.ResumeHandle != "" {
			log.Printf("Voice: reconnected with resume handle (native mode)")
		} else {
			log.Println("Voice: Gemini Live session established (native mode)")
		}

		// Mutex protects concurrent writes to the Gemini Live session (Bug fix).
		var sessionMu sync.Mutex

		// State-aware result delivery (native mode): if the model is speaking,
		// queue the result for delivery on TurnComplete. If idle, deliver immediately.
		var modelSpeakingNative atomic.Bool
		pendingResultNative := make(chan string, 1)

		sendResultToSessionNative := func(msg string) {
			sessionMu.Lock()
			err := session.SendClientContent(genai.LiveClientContentInput{
				Turns: []*genai.Content{{
					Role:  "user",
					Parts: []*genai.Part{{Text: msg}},
				}},
			})
			sessionMu.Unlock()
			if err != nil {
				log.Printf("Voice: result notify SendClientContent error: %v", err)
			}
		}

		deliverPendingResultNative := func() {
			select {
			case notification := <-pendingResultNative:
				sendResultToSessionNative(notification)
			default:
			}
		}

		// Wire Doer result callback — queue if speaking, deliver if idle.
		resultNotifyFn := func(summary string) {
			msg := fmt.Sprintf(
				"[SYSTEM: Background task completed. Result: %s. Announce this to the user briefly.]",
				summary,
			)
			if !modelSpeakingNative.Load() {
				sendResultToSessionNative(msg)
				return
			}
			select {
			case <-pendingResultNative:
			default:
			}
			select {
			case pendingResultNative <- msg:
			default:
			}
		}

		// resolveDoer returns the Doer for the currently active voice tab.
		// Falls back to tab 0 ("system" session) when no browser tab is open
		// but iTerm is available — allows terminal-only voice commands.
		resolveDoer := func() *Doer {
			tabID := h.getVoiceTabID()
			if tabID == 0 && h.termBridge == nil {
				return nil
			}
			var sess *TabSession
			if tabID != 0 {
				sess = h.getVoiceSession()
			}
			if sess == nil {
				sess = h.getSession(tabID) // creates tab-0 system session
			}
			doer := h.getOrCreateDoer(tabID, sess)
			doer.SetResultNotifyFn(resultNotifyFn)
			return doer
		}

		// --- goroutine: mic + text → Gemini ---
		// sessionCtx is cancelled on GoAway to kill the old sender (Bug #8 fix).
		sessionCtx, sessionCancel := context.WithCancel(ctx)
		sendDone := make(chan struct{})
		go func() {
			defer close(sendDone)
			for {
				select {
				case <-sessionCtx.Done():
					return
				case chunk, ok := <-mic:
					if !ok {
						return
					}
					if chunk == nil {
						sessionMu.Lock()
						err := session.SendRealtimeInput(genai.LiveRealtimeInput{
							AudioStreamEnd: true,
						})
						sessionMu.Unlock()
						if err != nil {
							log.Printf("Voice: AudioStreamEnd error: %v", err)
						}
						continue
					}
					if speaking.Load() != 0 {
						continue
					}
					sessionMu.Lock()
					err := session.SendRealtimeInput(genai.LiveRealtimeInput{
						Audio: &genai.Blob{
							Data:     chunk,
							MIMEType: "audio/pcm;rate=16000",
						},
					})
					sessionMu.Unlock()
					if err != nil {
						log.Printf("Voice: SendRealtimeInput error: %v", err)
						return
					}
				case text, ok := <-textIn:
					if !ok {
						continue
					}
					if text == "" {
						continue
					}
					log.Printf("Voice: text input: %s", text)
					sessionMu.Lock()
					err := session.SendClientContent(genai.LiveClientContentInput{
						Turns: []*genai.Content{
							{Role: "user", Parts: []*genai.Part{{Text: text}}},
						},
					})
					sessionMu.Unlock()
					if err != nil {
						log.Printf("Voice: SendClientContent error: %v", err)
					}
				case vf := <-h.videoFrameCh:
					// Page screenshot → Gemini Live as a video frame.
					log.Printf("Voice: sending video frame (%d bytes, %s)", len(vf.Data), vf.MIME)
					sessionMu.Lock()
					err := session.SendRealtimeInput(genai.LiveRealtimeInput{
						Video: &genai.Blob{Data: vf.Data, MIMEType: vf.MIME},
					})
					sessionMu.Unlock()
					if err != nil {
						log.Printf("Voice: SendRealtimeInput video error: %v", err)
					}
				}
			}
		}()

		// --- main loop: Gemini → speaker ---
		shouldReconnect := false
		for {
			msg, err := session.Receive()
			if err != nil {
				if ctx.Err() != nil {
					sessionCancel()
					<-sendDone
					_ = session.Close()
					return ctx.Err()
				}
				log.Printf("Voice: Receive error: %v", err)
				shouldReconnect = rs.shouldReconnect(ctx, "")
				if !shouldReconnect {
					sessionCancel()
					<-sendDone
					_ = session.Close()
					return ctx.Err()
				}
				break
			}
			rs.Retries = 0 // reset on successful receive

			if msg.SetupComplete != nil {
				log.Println("Voice: Live session setup complete (native mode)")
				continue
			}

			if sc := msg.ServerContent; sc != nil {
				if sc.Interrupted {
					interrupted.Store(true)
					log.Println("Voice: interrupted by user, suppressing buffered audio")
				}

				if sc.ModelTurn != nil {
					modelSpeakingNative.Store(true)
					for _, part := range sc.ModelTurn.Parts {
						if part.InlineData != nil && len(part.InlineData.Data) > 0 {
							if interrupted.Load() {
								interrupted.Store(false)
								log.Println("Voice: new model turn, resuming audio")
							}
							markSpeaking()
							select {
							case speaker <- part.InlineData.Data:
							case <-ctx.Done():
								sessionCancel()
								_ = session.Close()
								return ctx.Err()
							}
						}
					}
				}

				if sc.InputTranscription != nil && sc.InputTranscription.Text != "" {
					inputBuf.WriteString(sc.InputTranscription.Text)
				}
				if sc.OutputTranscription != nil && sc.OutputTranscription.Text != "" {
					outputBuf.WriteString(sc.OutputTranscription.Text)
				}
				// Flush buffered transcriptions on turn boundary.
				if sc.TurnComplete || sc.Interrupted {
					modelSpeakingNative.Store(false)
					if inputBuf.Len() > 0 {
						log.Printf("Voice User: %s", strings.TrimSpace(inputBuf.String()))
						inputBuf.Reset()
					}
					if outputBuf.Len() > 0 {
						log.Printf("Voice Talker: %s", strings.TrimSpace(outputBuf.String()))
						outputBuf.Reset()
					}
				}
				// Only deliver pending results on TurnComplete (model finished naturally).
				// On Interrupted (user spoke), keep the result queued — it'll deliver
				// after the model responds to the user. User can also check_interaction().
				if sc.TurnComplete {
					deliverPendingResultNative()
				}

				continue
			}

			// ToolCall — Talker tools execute instantly.
			if tc := msg.ToolCall; tc != nil {
				var responses []*genai.FunctionResponse
				for _, fc := range tc.FunctionCalls {
					switch fc.Name {
					case "check_interaction", "create_interaction", "cancel_interaction", "open_url", "terminal_action", "screen_share":
						result := h.executeTalkerTool(fc, resolveDoer())
						log.Printf("Voice: Talker tool %s → %q", fc.Name, result)
						responses = append(responses, &genai.FunctionResponse{
							ID:       fc.ID,
							Name:     fc.Name,
							Response: map[string]any{"output": result},
						})
					default:
						log.Printf("Voice: skipping server-side tool %s", fc.Name)
						continue
					}
				}
				if len(responses) > 0 {
					sessionMu.Lock()
					err := session.SendToolResponse(genai.LiveToolResponseInput{
						FunctionResponses: responses,
					})
					sessionMu.Unlock()
					if err != nil {
						log.Printf("Voice: SendToolResponse error: %v", err)
					}
				}
				continue
			}

			if msg.ToolCallCancellation != nil {
				log.Printf("Voice: tool call cancelled: %v", msg.ToolCallCancellation)
				continue
			}

			if msg.UsageMetadata != nil {
				log.Printf("Voice: tokens: %d total (%d prompt, %d response)",
					msg.UsageMetadata.TotalTokenCount,
					msg.UsageMetadata.PromptTokenCount,
					msg.UsageMetadata.ResponseTokenCount)
			}

			// Session resumption: store handle for reconnect.
			if msg.SessionResumptionUpdate != nil && msg.SessionResumptionUpdate.Resumable {
				rs.ResumeHandle = msg.SessionResumptionUpdate.NewHandle
			}

			// GoAway: server is about to disconnect, reconnect with resume handle.
			if msg.GoAway != nil {
				log.Printf("Voice: GoAway received (time left: %s), reconnecting...", msg.GoAway.TimeLeft)
				shouldReconnect = true
				rs.Retries = 0 // GoAway is expected, reset transient counter
				break
			}
		}

		sessionCancel()
		<-sendDone // wait for sender goroutine to fully exit before reconnecting
		_ = session.Close()
		if !shouldReconnect {
			return nil
		}
		log.Println("Voice: reconnecting...")
	}
}

// sendVoiceJSON marshals a voiceMessage and writes it as a text frame. If mu
// is non-nil it acquires the lock first (used when called from non-reader goroutines).
func sendVoiceJSON(conn *websocket.Conn, mu *sync.Mutex, msg voiceMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Voice: marshal error: %v", err)
		return
	}
	if mu != nil {
		mu.Lock()
		defer mu.Unlock()
	}
	if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
		log.Printf("Voice: send error: %v", err)
	}
}
