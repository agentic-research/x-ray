package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/genai"
)

// voiceMessage is the JSON envelope for text messages on the voice WebSocket.
type voiceMessage struct {
	Type       string `json:"type"`
	Text       string `json:"text,omitempty"`
	MacheID    string `json:"mache_id,omitempty"`
	Action     string `json:"action,omitempty"`
	Payload    string `json:"payload,omitempty"` // text for "type" action
	SampleRate int    `json:"sample_rate,omitempty"`
}

// talkerToolDefinitions returns the Talker's tools: check_status, issue_command, cancel_task.
// These execute instantly (no I/O, no blocking) so the Talker is never muted.
func talkerToolDefinitions() []*genai.Tool {
	return []*genai.Tool{{
		FunctionDeclarations: []*genai.FunctionDeclaration{
			{
				Name:        "check_status",
				Description: "Check what the background navigator is currently doing. Returns the current goal, step, and result if finished. Use this when the user asks 'what are you doing?' or to check if a previous command has completed.",
				Parameters:  &genai.Schema{Type: genai.TypeObject},
			},
			{
				Name:        "issue_command",
				Description: "Send a navigation command to the background executor. Examples: 'click the first story', 'go to reddit.com', 'search for golang tutorials', 'scroll down'. The command will execute in the background while you remain available to chat.",
				Parameters: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"goal":      {Type: genai.TypeString, Description: "Natural language navigation goal"},
						"read_only": {Type: genai.TypeBoolean, Description: "Set true when the user asks a QUESTION about the page (what, which, list, describe, tell me). Leave false/omit when the user wants an ACTION (click, play, open, search, type)."},
					},
					Required: []string{"goal"},
				},
			},
			{
				Name:        "cancel_task",
				Description: "Cancel the current background navigation task. Use when the user says stop, cancel, or nevermind.",
				Parameters:  &genai.Schema{Type: genai.TypeObject},
			},
		},
	}}
}

// executeTalkerTool dispatches a Talker tool call. All tools return instantly.
func (h *Handler) executeTalkerTool(fc *genai.FunctionCall, doer *Doer) string {
	switch fc.Name {
	case "check_status":
		status, goalText, step, result := doer.State().Snapshot()
		switch status {
		case DoerIdle:
			return "No task in progress. Ready for a new command."
		case DoerExecuting:
			return fmt.Sprintf("Working on: %q. Current step: %s", goalText, step)
		case DoerDone:
			if result != nil {
				return fmt.Sprintf("Completed: %s", result.Summary)
			}
			return "Task completed."
		case DoerFailed:
			if result != nil {
				return fmt.Sprintf("Failed: %s", result.Summary)
			}
			return "Task failed."
		}
		return "Unknown status."

	case "issue_command":
		goal, _ := fc.Args["goal"].(string)
		if goal == "" {
			return "Error: goal is required."
		}
		readOnly, _ := fc.Args["read_only"].(bool)
		goalID := fmt.Sprintf("goal-%d", time.Now().UnixMilli())
		doer.Submit(DoerGoal{ID: goalID, Text: goal, ReadOnly: readOnly})
		if readOnly {
			log.Printf("Voice: issue_command (read_only=true): %q", goal)
		}
		return fmt.Sprintf("Command accepted: %q. Working on it in the background.", goal)

	case "cancel_task":
		doer.Cancel()
		return "Task cancelled."

	default:
		return fmt.Sprintf("Unknown tool: %s", fc.Name)
	}
}

// talkerSystemPrompt instructs the Talker to stay conversational and delegate all page work.
var talkerSystemPrompt = `You are a VOICE assistant with a background navigator that can see and interact with the user's browser and terminal sessions.

YOUR TOOLS:
- issue_command(goal, read_only?): Dispatch ANY system-related task to your background navigator. This works for actions AND questions about the browser or terminal. Examples: "click the first story", "go to reddit.com", "check if I'm logged in", "read the main heading", "what's running in my terminal?", "type npm start in the terminal".
  Set read_only=true when the user asks a QUESTION about the environment (e.g., "what's playing?", "what's in the terminal?", "describe the page").
  Leave read_only=false (or omit) when the user wants an ACTION (e.g., "click...", "play...", "open...", "search for...", "type...", "go to...").
- check_status(): Check what the navigator is currently doing. Returns goal, current step, and result if finished.
- cancel_task(): Cancel the current background task.

BEHAVIOR:
1. When the user asks you to do something OR asks a question about their environment (browser or terminal), use issue_command() and briefly acknowledge: "Let me check." Always set read_only appropriately.
2. When a task is running, stay SILENT and wait for the system to notify you of completion — unless the user asks.
3. If the user asks "what are you doing?" or "are you almost done?", call check_status() and report briefly.
4. When the system notifies you a task completed, announce the result naturally.
5. If the user says "stop" or "cancel", use cancel_task() and confirm: "Cancelled."
6. You can answer general knowledge questions directly using Google Search — no need to issue_command for those.
7. Keep ALL responses to ONE short sentence. Never narrate your tool usage.

Your navigator can read the full environment structure (including terminals at /iterm/), so ALWAYS delegate environment questions to it — never say "I can't see the terminal."`

// buildLiveConfig returns the LiveConnectConfig shared by HandleVoice and StartVoiceLoop.
func buildLiveConfig() *genai.LiveConnectConfig {
	tools := append(talkerToolDefinitions(), &genai.Tool{
		GoogleSearch: &genai.GoogleSearch{},
	})
	return &genai.LiveConnectConfig{
		ResponseModalities: []genai.Modality{genai.ModalityAudio},
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{{Text: talkerSystemPrompt}},
		},
		Tools:                    tools,
		InputAudioTranscription:  &genai.AudioTranscriptionConfig{},
		OutputAudioTranscription: &genai.AudioTranscriptionConfig{},
		SessionResumption:        &genai.SessionResumptionConfig{},
	}
}

// applyResumeHandle sets the session resumption handle on a LiveConnectConfig.
// If handle is empty, the config retains its default empty SessionResumption
// (opt-in to resumption updates without restoring a previous session).
func applyResumeHandle(config *genai.LiveConnectConfig, handle string) {
	if handle != "" {
		config.SessionResumption = &genai.SessionResumptionConfig{Handle: handle}
	}
}

// HandleVoice upgrades to WebSocket and proxies audio between the browser and
// Gemini's Live API. Navigation work is delegated to the Doer goroutine;
// the Talker stays responsive with instant check_status/issue_command tools.
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
	sess := h.getSession(tabID)
	log.Printf("Voice: browser connected (tab %d)", tabID)

	// Mutex protects writes to the browser WS.
	var wsMu sync.Mutex

	// Create/get the Doer for this tab.
	doer := h.getOrCreateDoer(tabID, sess)

	doer.SetActionNotifyFn(func(macheID, action, payload string) {
		sendVoiceJSON(conn, &wsMu, voiceMessage{
			Type:    MsgExecuteAction,
			MacheID: macheID,
			Action:  action,
			Payload: payload,
		})
	})
	defer doer.SetActionNotifyFn(nil)

	// Session resumption: reconnect-on-GoAway outer loop.
	// The browser WS stays open; only the Gemini Live session reconnects.
	var resumeHandle string
	for {
		config := buildLiveConfig()
		applyResumeHandle(config, resumeHandle)

		session, err := h.LiveClient.Live.Connect(ctx, h.LiveModel, config)
		if err != nil {
			log.Printf("Voice: Live API connect failed: %v", err)
			sendVoiceJSON(conn, nil, voiceMessage{Type: "error", Text: "Live API connect failed: " + err.Error()})
			return
		}
		if resumeHandle != "" {
			log.Printf("Voice: reconnected with resume handle (tab %d)", tabID)
		} else {
			log.Printf("Voice: Gemini Live session established (tab %d)", tabID)
		}

		// Wire Doer result callback to this session.
		doer.SetResultNotifyFn(func(summary string) {
			if err := session.SendClientContent(genai.LiveClientContentInput{
				Turns: []*genai.Content{{
					Role: "user",
					Parts: []*genai.Part{{Text: fmt.Sprintf(
						"[SYSTEM: Background task completed. Result: %s. Announce this to the user briefly.]",
						summary,
					)}},
				}},
			}); err != nil {
				log.Printf("Voice: result notify SendClientContent error: %v", err)
			}
		})

		// --- goroutine 1: browser → Gemini (audio chunks) ---
		// Each reconnect starts a new sender goroutine tied to this session.
		// sessionCtx is cancelled on GoAway to kill the old sender (Bug #8 fix).
		sessionCtx, sessionCancel := context.WithCancel(ctx)
		senderDone := make(chan struct{})
		go func() {
			defer close(senderDone)
			var audioChunks int
			var audioBytes int
			lastLog := time.Now()
			for {
				msgType, data, err := conn.ReadMessage()
				if err != nil {
					log.Printf("Voice: browser read error: %v", err)
					_ = session.Close()
					return
				}
				// Check if this session was cancelled (GoAway reconnect).
				if sessionCtx.Err() != nil {
					return
				}
				switch msgType {
				case websocket.BinaryMessage:
					audioChunks++
					audioBytes += len(data)
					if time.Since(lastLog) >= 5*time.Second {
						log.Printf("Voice [tab %d]: receiving audio — %d chunks, %d bytes in last 5s", tabID, audioChunks, audioBytes)
						audioChunks = 0
						audioBytes = 0
						lastLog = time.Now()
					}
					if err := session.SendRealtimeInput(genai.LiveRealtimeInput{
						Audio: &genai.Blob{
							Data:     data,
							MIMEType: "audio/pcm;rate=16000",
						},
					}); err != nil {
						log.Printf("Voice: SendRealtimeInput error: %v", err)
						return
					}
				case websocket.TextMessage:
					var cmd voiceMessage
					if err := json.Unmarshal(data, &cmd); err != nil {
						continue
					}
					switch cmd.Type {
					case "mic_stop":
						log.Println("Voice: mic released, sending AudioStreamEnd")
						if err := session.SendRealtimeInput(genai.LiveRealtimeInput{
							AudioStreamEnd: true,
						}); err != nil {
							log.Printf("Voice: AudioStreamEnd error: %v", err)
						}
					case "text_input":
						if cmd.Text == "" {
							continue
						}
						log.Printf("Voice [tab %d]: text input: %s", tabID, cmd.Text)
						if err := session.SendClientContent(genai.LiveClientContentInput{
							Turns: []*genai.Content{
								{Role: "user", Parts: []*genai.Part{{Text: cmd.Text}}},
							},
						}); err != nil {
							log.Printf("Voice: SendClientContent error: %v", err)
						}
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
				break
			}

			if msg.SetupComplete != nil {
				log.Println("Voice: Live session setup complete")
				sendVoiceJSON(conn, &wsMu, voiceMessage{Type: "ready"})
				continue
			}

			if sc := msg.ServerContent; sc != nil {
				// Always forward audio — browser handles its own buffer flush on interruption.
				if sc.ModelTurn != nil {
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

				// Transcriptions.
				if sc.InputTranscription != nil && sc.InputTranscription.Text != "" {
					log.Printf("Voice [tab %d] User: %s", tabID, sc.InputTranscription.Text)
					sendVoiceJSON(conn, &wsMu, voiceMessage{
						Type: "input_transcription", Text: sc.InputTranscription.Text,
					})
				}
				if sc.OutputTranscription != nil && sc.OutputTranscription.Text != "" {
					log.Printf("Voice [tab %d] Talker: %s", tabID, sc.OutputTranscription.Text)
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
				continue
			}

			// ToolCall — Talker tools execute instantly.
			if tc := msg.ToolCall; tc != nil {
				var responses []*genai.FunctionResponse
				for _, fc := range tc.FunctionCalls {
					switch fc.Name {
					case "check_status", "issue_command", "cancel_task":
						result := h.executeTalkerTool(fc, doer)
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
					if err := session.SendToolResponse(genai.LiveToolResponseInput{
						FunctionResponses: responses,
					}); err != nil {
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
				resumeHandle = msg.SessionResumptionUpdate.NewHandle
			}

			// GoAway: server is about to disconnect, reconnect with resume handle.
			if msg.GoAway != nil {
				log.Printf("Voice [tab %d]: GoAway received (time left: %s), reconnecting...", tabID, msg.GoAway.TimeLeft)
				shouldReconnect = true
				break
			}
		}

		sessionCancel() // kill the sender goroutine (Bug #8 fix)
		_ = session.Close()
		doer.SetResultNotifyFn(nil)
		if !shouldReconnect {
			break
		}
		log.Printf("Voice [tab %d]: reconnecting after GoAway...", tabID)
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
	var resumeHandle string
	for {
		config := buildLiveConfig()
		applyResumeHandle(config, resumeHandle)

		session, err := h.LiveClient.Live.Connect(ctx, h.LiveModel, config)
		if err != nil {
			return fmt.Errorf("voice: Live API connect: %w", err)
		}
		if resumeHandle != "" {
			log.Printf("Voice: reconnected with resume handle (native mode)")
		} else {
			log.Println("Voice: Gemini Live session established (native mode)")
		}

		// resultNotifyFn injects a synthetic message so Gemini speaks the Doer's result.
		resultNotifyFn := func(summary string) {
			if err := session.SendClientContent(genai.LiveClientContentInput{
				Turns: []*genai.Content{{
					Role: "user",
					Parts: []*genai.Part{{Text: fmt.Sprintf(
						"[SYSTEM: Background task completed. Result: %s. Announce this to the user briefly.]",
						summary,
					)}},
				}},
			}); err != nil {
				log.Printf("Voice: result notify SendClientContent error: %v", err)
			}
		}

		// resolveDoer returns the Doer for the currently active voice tab.
		resolveDoer := func() *Doer {
			tabID := h.getVoiceTabID()
			sess := h.getVoiceSession()
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
						if err := session.SendRealtimeInput(genai.LiveRealtimeInput{
							AudioStreamEnd: true,
						}); err != nil {
							log.Printf("Voice: AudioStreamEnd error: %v", err)
						}
						continue
					}
					if speaking.Load() != 0 {
						continue
					}
					if err := session.SendRealtimeInput(genai.LiveRealtimeInput{
						Audio: &genai.Blob{
							Data:     chunk,
							MIMEType: "audio/pcm;rate=16000",
						},
					}); err != nil {
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
					if err := session.SendClientContent(genai.LiveClientContentInput{
						Turns: []*genai.Content{
							{Role: "user", Parts: []*genai.Part{{Text: text}}},
						},
					}); err != nil {
						log.Printf("Voice: SendClientContent error: %v", err)
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
					_ = session.Close()
					return ctx.Err()
				}
				_ = session.Close()
				if shouldReconnect {
					break
				}
				sessionCancel()
				return fmt.Errorf("voice: Receive: %w", err)
			}

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
					log.Printf("Voice User: %s", sc.InputTranscription.Text)
				}
				if sc.OutputTranscription != nil && sc.OutputTranscription.Text != "" {
					log.Printf("Voice Talker: %s", sc.OutputTranscription.Text)
				}

				continue
			}

			// ToolCall — Talker tools execute instantly.
			if tc := msg.ToolCall; tc != nil {
				var responses []*genai.FunctionResponse
				for _, fc := range tc.FunctionCalls {
					switch fc.Name {
					case "check_status", "issue_command", "cancel_task":
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
					if err := session.SendToolResponse(genai.LiveToolResponseInput{
						FunctionResponses: responses,
					}); err != nil {
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
				resumeHandle = msg.SessionResumptionUpdate.NewHandle
			}

			// GoAway: server is about to disconnect, reconnect with resume handle.
			if msg.GoAway != nil {
				log.Printf("Voice: GoAway received (time left: %s), reconnecting...", msg.GoAway.TimeLeft)
				shouldReconnect = true
				break
			}
		}

		sessionCancel() // kill the sender goroutine (Bug #8 fix)
		_ = session.Close()
		if !shouldReconnect {
			return nil
		}
		log.Println("Voice: reconnecting after GoAway...")
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
