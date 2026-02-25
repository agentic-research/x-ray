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
						"goal": {Type: genai.TypeString, Description: "Natural language navigation goal"},
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
		goalID := fmt.Sprintf("goal-%d", time.Now().UnixMilli())
		doer.Submit(DoerGoal{ID: goalID, Text: goal})
		return fmt.Sprintf("Command accepted: %q. Working on it in the background.", goal)

	case "cancel_task":
		doer.Cancel()
		return "Task cancelled."

	default:
		return fmt.Sprintf("Unknown tool: %s", fc.Name)
	}
}

// talkerSystemPrompt instructs the Talker to stay conversational and delegate navigation work.
var talkerSystemPrompt = `You are a VOICE assistant with a background navigator that does the actual page work.

YOUR TOOLS:
- issue_command(goal): Dispatch a navigation task to your background navigator. Examples: "click the first story", "go to reddit.com", "search for golang tutorials".
- check_status(): Check what the navigator is currently doing. Returns goal, current step, and result if finished.
- cancel_task(): Cancel the current background task.

BEHAVIOR:
1. When the user asks you to do something on a web page, use issue_command() and briefly acknowledge: "On it."
2. When a task is running, stay SILENT and wait for the system to notify you of completion — unless the user asks.
3. If the user asks "what are you doing?" or "are you almost done?", call check_status() and give a play-by-play: "I'm currently scanning the navigation bar, almost there!"
4. When the system notifies you a task completed, announce the result naturally: "Done, I clicked the first story."
5. If the user says "stop" or "cancel", use cancel_task() and confirm: "Cancelled."
6. You can answer general knowledge questions directly using Google Search — no need to issue_command for those.
7. Keep ALL responses to ONE short sentence. Never narrate your tool usage.

You do NOT have direct page access. You cannot ls, cat, or click anything yourself.`

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

	// Connect to Gemini Live with Talker tools (NOT navigator tools).
	tools := append(talkerToolDefinitions(), &genai.Tool{
		GoogleSearch: &genai.GoogleSearch{},
	})
	session, err := h.LiveClient.Live.Connect(ctx, h.LiveModel, &genai.LiveConnectConfig{
		ResponseModalities: []genai.Modality{genai.ModalityAudio},
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{{Text: talkerSystemPrompt}},
		},
		Tools:                    tools,
		InputAudioTranscription:  &genai.AudioTranscriptionConfig{},
		OutputAudioTranscription: &genai.AudioTranscriptionConfig{},
	})
	if err != nil {
		log.Printf("Voice: Live API connect failed: %v", err)
		sendVoiceJSON(conn, nil, voiceMessage{Type: "error", Text: "Live API connect failed: " + err.Error()})
		return
	}
	defer func() { _ = session.Close() }()
	log.Printf("Voice: Gemini Live session established (tab %d)", tabID)

	// Wire Doer callbacks: result announcement + action UI forwarding.
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
	defer doer.SetResultNotifyFn(nil)

	doer.SetActionNotifyFn(func(macheID, action, payload string) {
		sendVoiceJSON(conn, &wsMu, voiceMessage{
			Type:    MsgExecuteAction,
			MacheID: macheID,
			Action:  action,
			Payload: payload,
		})
	})
	defer doer.SetActionNotifyFn(nil)

	// --- goroutine 1: browser → Gemini (audio chunks) ---
	go func() {
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
	// No inToolLoop — Talker tools are instant, audio is always forwarded.
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
			// Always forward audio — no suppression.
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
					// Google Search or other server-side tools — skip.
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
	}

	log.Printf("Voice: session ended (tab %d)", tabID)
}

// StartVoiceLoop runs the native voice mode using sox mic/speaker pipes.
// mic delivers PCM chunks from the Recorder. speaker receives PCM chunks to play.
// textIn delivers typed text intents from stdin. Runs until ctx is cancelled.
func (h *Handler) StartVoiceLoop(ctx context.Context, mic <-chan []byte, speaker chan<- []byte, textIn <-chan string) error {
	// Connect to Gemini Live with Talker tools + Google Search Grounding.
	tools := append(talkerToolDefinitions(), &genai.Tool{
		GoogleSearch: &genai.GoogleSearch{},
	})
	session, err := h.LiveClient.Live.Connect(ctx, h.LiveModel, &genai.LiveConnectConfig{
		ResponseModalities: []genai.Modality{genai.ModalityAudio},
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{{Text: talkerSystemPrompt}},
		},
		Tools:                    tools,
		InputAudioTranscription:  &genai.AudioTranscriptionConfig{},
		OutputAudioTranscription: &genai.AudioTranscriptionConfig{},
	})
	if err != nil {
		return fmt.Errorf("voice: Live API connect: %w", err)
	}
	defer func() { _ = session.Close() }()
	log.Println("Voice: Gemini Live session established (native mode)")

	// resultNotifyFn injects a synthetic message so Gemini speaks the Doer's result.
	// Defined once (captures the stable Gemini session); wired to whatever Doer is active.
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
	// The tab may change when the extension connects or the user switches tabs,
	// so we resolve it fresh on each tool call instead of locking at startup.
	resolveDoer := func() *Doer {
		tabID := h.getVoiceTabID()
		sess := h.getVoiceSession()
		doer := h.getOrCreateDoer(tabID, sess)
		doer.SetResultNotifyFn(resultNotifyFn)
		return doer
	}

	// Echo gate: suppress mic input while Gemini is speaking to prevent feedback.
	// Set to 1 when audio flows to the speaker, cleared 300ms after the last chunk.
	var speaking atomic.Int32
	var speakingTimer *time.Timer

	markSpeaking := func() {
		speaking.Store(1)
		if speakingTimer != nil {
			speakingTimer.Stop()
		}
		// Clear the flag 300ms after the last audio chunk — gives time for the
		// speaker to finish playing before the mic re-opens.
		speakingTimer = time.AfterFunc(300*time.Millisecond, func() {
			speaking.Store(0)
		})
	}

	// --- goroutine: mic + text → Gemini ---
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case chunk, ok := <-mic:
				if !ok {
					return
				}
				if chunk == nil {
					// PTT button released — send AudioStreamEnd.
					if err := session.SendRealtimeInput(genai.LiveRealtimeInput{
						AudioStreamEnd: true,
					}); err != nil {
						log.Printf("Voice: AudioStreamEnd error: %v", err)
					}
					continue
				}
				// Echo gate: drop mic data while Gemini is speaking.
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
	// No inToolLoop — Talker tools are instant, audio is always forwarded.
	for {
		msg, err := session.Receive()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("voice: Receive: %w", err)
		}

		if msg.SetupComplete != nil {
			log.Println("Voice: Live session setup complete (native mode)")
			continue
		}

		if sc := msg.ServerContent; sc != nil {
			// Forward audio to speaker; mark echo gate so mic is suppressed.
			if sc.ModelTurn != nil {
				for _, part := range sc.ModelTurn.Parts {
					if part.InlineData != nil && len(part.InlineData.Data) > 0 {
						markSpeaking()
						select {
						case speaker <- part.InlineData.Data:
						case <-ctx.Done():
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

			if sc.Interrupted {
				log.Println("Voice: interrupted by user")
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
