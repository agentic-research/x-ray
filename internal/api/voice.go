package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jamesgardner/x-ray/internal/mache"
	"github.com/jamesgardner/x-ray/internal/navigator"
	"google.golang.org/genai"
)

// voiceMessage is the JSON envelope for text messages on the voice WebSocket.
type voiceMessage struct {
	Type       string `json:"type"`
	Text       string `json:"text,omitempty"`
	MacheID    string `json:"mache_id,omitempty"`
	Action     string `json:"action,omitempty"`
	SampleRate int    `json:"sample_rate,omitempty"`
}

// HandleVoice upgrades to WebSocket and proxies audio between the browser and
// Gemini's Live API. Tool calls (ls/cat/act) are executed locally against the
// Mache engine; act() results are dispatched to the extension WebSocket.
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

	// Mutex protects writes to the browser WS (two goroutines: gemini→browser
	// and tool-response paths both write).
	var wsMu sync.Mutex

	// Wire scroll so voice navigator can request page scrolling via extension WS.
	sess.Navigator.SetScrollFunc(func(scrollCtx context.Context, direction string) error {
		return h.scrollVoice(scrollCtx, sess, tabID, direction)
	})
	defer sess.Navigator.SetScrollFunc(nil)

	// Connect to Gemini Live immediately — don't block on schema.
	// Tool calls will block on sess.SchemaReady if fired before schema arrives.
	if !sess.Engine.HasSchema() {
		log.Printf("Voice: schema not ready yet (tab %d), connecting to Gemini anyway", tabID)
		sendVoiceJSON(conn, &wsMu, voiceMessage{Type: "waiting", Text: "Connecting to voice... schema loading in background"})
	}

	// Open Gemini Live session with voice-optimized prompt.
	// Include Google Search Grounding — Gemini executes searches server-side.
	tools := append(navigator.ToolDefinitions(), &genai.Tool{
		GoogleSearch: &genai.GoogleSearch{},
	})
	session, err := h.LiveClient.Live.Connect(ctx, h.LiveModel, &genai.LiveConnectConfig{
		ResponseModalities: []genai.Modality{genai.ModalityAudio},
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{{Text: voiceSystemPrompt}},
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
				// Log audio activity every 5 seconds so you can see mic data flowing.
				if time.Since(lastLog) >= 5*time.Second {
					log.Printf("Voice [tab %d]: receiving audio — %d chunks, %d bytes in last 5s", tabID, audioChunks, audioBytes)
					audioChunks = 0
					audioBytes = 0
					lastLog = time.Now()
				}
				// Raw PCM audio from browser mic.
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
	//
	// inToolLoop suppresses audio/narration while the model is working through
	// tool calls. The native audio model narrates its thought process despite
	// system-prompt instructions — we mute it server-side and only forward the
	// final spoken result after all tools complete.
	var inToolLoop bool
	var bufferedTranscript string

	for {
		msg, err := session.Receive()
		if err != nil {
			log.Printf("Voice: Receive error: %v", err)
			break
		}

		// SetupComplete — Live session ready.
		if msg.SetupComplete != nil {
			log.Println("Voice: Live session setup complete")
			sendVoiceJSON(conn, &wsMu, voiceMessage{Type: "ready"})
			continue
		}

		// ServerContent — audio data and/or transcription.
		if sc := msg.ServerContent; sc != nil {
			if inToolLoop {
				// Suppress audio and narration while tools are executing.
				// Buffer the final transcript so we can show the result text.
				if sc.OutputTranscription != nil && sc.OutputTranscription.Text != "" {
					bufferedTranscript += sc.OutputTranscription.Text
				}

				// TurnComplete after tool work — flush buffered transcript.
				if sc.TurnComplete {
					inToolLoop = false
					if bufferedTranscript != "" {
						log.Printf("Voice [tab %d] Navigator: %s", tabID, bufferedTranscript)
						sendVoiceJSON(conn, &wsMu, voiceMessage{
							Type: "output_transcription", Text: bufferedTranscript,
						})
						bufferedTranscript = ""
					}
					sendVoiceJSON(conn, &wsMu, voiceMessage{Type: "turn_complete"})
				}
				if sc.Interrupted {
					inToolLoop = false
					bufferedTranscript = ""
					sendVoiceJSON(conn, &wsMu, voiceMessage{Type: "interrupted"})
				}
				continue
			}

			// Normal path (no active tool loop): forward audio and transcription.
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
				log.Printf("Voice [tab %d] Navigator: %s", tabID, sc.OutputTranscription.Text)
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

		// ToolCall — execute ls/cat/act locally against this tab's session.
		if tc := msg.ToolCall; tc != nil {
			// Block until schema is ready — but let goto through without a schema.
			// Don't set inToolLoop until AFTER schema check passes, so that if
			// schema times out, Gemini's error response audio is NOT suppressed.
			needsSchema := false
			for _, fc := range tc.FunctionCalls {
				if fc.Name != "goto" && fc.Name != "rescan" {
					needsSchema = true
					break
				}
			}
			if needsSchema {
				select {
				case <-sess.SchemaReady:
				case <-time.After(schemaWaitTimeout):
					log.Printf("Voice: timed out waiting for schema before tool call (tab %d)", tabID)
					var errResponses []*genai.FunctionResponse
					for _, fc := range tc.FunctionCalls {
						errResponses = append(errResponses, &genai.FunctionResponse{
							ID:       fc.ID,
							Name:     fc.Name,
							Response: map[string]any{"output": "Error: page schema is still loading. Please tell the user to wait a moment."},
						})
					}
					if err := session.SendToolResponse(genai.LiveToolResponseInput{
						FunctionResponses: errResponses,
					}); err != nil {
						log.Printf("Voice: SendToolResponse error: %v", err)
					}
					continue // inToolLoop is false — audio flows normally
				}
			}

			inToolLoop = true

			var responses []*genai.FunctionResponse
			for _, fc := range tc.FunctionCalls {
				// Skip server-side tools (e.g., google_search) — Gemini handles
				// these internally and returns synthesized audio/text directly.
				switch fc.Name {
				case "ls", "cat", "act", "scroll", "goto", "rescan":
					// Our local tools — handle below.
				default:
					log.Printf("Voice: skipping server-side tool %s (tab %d)", fc.Name, tabID)
					continue
				}
				log.Printf("Voice: tool call %s(%v) (tab %d)", fc.Name, fc.Args, tabID)
				result, action := sess.Navigator.ExecuteTool(ctx, fc)
				log.Printf("Voice: tool result: %q", result)

				// Handle actions: goto navigates + re-maps, act dispatches a click.
				if action != nil {
					switch action.Action {
					case "goto":
						// Reset schema state for the new page.
						sess.ResetSchema()
						sess.Engine = mache.NewEngine()
						sess.Navigator.SetEngine(sess.Engine)

						// Navigate browser via extension WebSocket.
						h.sendGoto(tabID, action.Path)
						log.Printf("Voice: goto %s — waiting for new page schema (tab %d)", action.Path, tabID)

						// Block until the new page's schema is ready.
						select {
						case <-sess.SchemaReady:
							result = fmt.Sprintf("Navigated to %s. Page is loaded and mapped. Use ls('/') to see the new page structure.", action.Path)
						case <-time.After(schemaWaitTimeout):
							result = fmt.Sprintf("Navigated to %s but timed out waiting for page to load.", action.Path)
						case <-ctx.Done():
							result = "Navigation cancelled."
						}
					case "rescan":
						if action.Path != "" {
							// Targeted rescan: keep existing engine, zoom into zone.
							sess.ResetSchema()
							sess.RescanPath = action.Path
							h.sendRescan(tabID, action.MacheID)
							log.Printf("Voice: targeted rescan %q (mache_id %s, tab %d)", action.Path, action.MacheID, tabID)
						} else {
							// Full-page rescan: reset everything.
							sess.ResetSchema()
							sess.Engine = mache.NewEngine()
							sess.Navigator.SetEngine(sess.Engine)
							h.sendRescan(tabID, "")
							log.Printf("Voice: full rescan — waiting for fresh schema (tab %d)", tabID)
						}
						select {
						case <-sess.SchemaReady:
							if action.Path != "" {
								result = fmt.Sprintf("Zoomed into %s and discovered sub-zones. Use ls('%s') to see the detailed structure.", action.Path, action.Path)
							} else {
								result = "Page rescanned with fresh screenshot. Schema regenerated. Use ls('/') to see the updated structure."
							}
						case <-time.After(schemaWaitTimeout):
							result = "Rescan timed out waiting for new schema."
						case <-ctx.Done():
							result = "Rescan cancelled."
						}
					default:
						h.SendActionToExtension(tabID, action.MacheID, action.Action)
						sendVoiceJSON(conn, &wsMu, voiceMessage{
							Type:    MsgExecuteAction,
							MacheID: action.MacheID,
							Action:  action.Action,
						})
					}
				}

				responses = append(responses, &genai.FunctionResponse{
					ID:       fc.ID,
					Name:     fc.Name,
					Response: map[string]any{"output": result},
				})
			}

			if err := session.SendToolResponse(genai.LiveToolResponseInput{
				FunctionResponses: responses,
			}); err != nil {
				log.Printf("Voice: SendToolResponse error: %v", err)
				break
			}
			continue
		}

		// ToolCallCancellation — log and move on.
		if msg.ToolCallCancellation != nil {
			log.Printf("Voice: tool call cancelled: %v", msg.ToolCallCancellation)
			inToolLoop = false
			bufferedTranscript = ""
			continue
		}
	}

	log.Printf("Voice: session ended (tab %d)", tabID)
}

// voiceSystemPrompt composes voice-specific behavioral rules with the shared
// NavigatorSystemPrompt. Single source of truth for navigation instructions.
var voiceSystemPrompt = `You are a VOICE agent — be SILENT while working, only speak to announce results.

VOICE RULES:
1. NEVER speak while using tools. Do all exploration silently.
2. Only speak AFTER you have completed the action or determined you cannot.
3. Responses must be ONE short sentence. Example: "Done, clicked the first story."
4. NEVER narrate your process. No "I'm now looking at..." or "Let me check...". SILENCE until done.
5. If you can't find an element, use rescan() to refresh the page map before giving up.
6. When the user's intent implies visiting a website ("find me flights", "show me Reddit"), use goto() proactively.

` + navigator.NavigatorSystemPrompt

// StartVoiceLoop runs the native voice mode using sox mic/speaker pipes.
// mic delivers PCM chunks from the Recorder. speaker receives PCM chunks to play.
// textIn delivers typed text intents from stdin. Runs until ctx is cancelled.
func (h *Handler) StartVoiceLoop(ctx context.Context, mic <-chan []byte, speaker chan<- []byte, textIn <-chan string) error {
	// Connect to Gemini Live with voice prompt + tools + Google Search Grounding.
	tools := append(navigator.ToolDefinitions(), &genai.Tool{
		GoogleSearch: &genai.GoogleSearch{},
	})
	session, err := h.LiveClient.Live.Connect(ctx, h.LiveModel, &genai.LiveConnectConfig{
		ResponseModalities: []genai.Modality{genai.ModalityAudio},
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{{Text: voiceSystemPrompt}},
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
					// PTT button released — send AudioStreamEnd to tell Gemini we are done talking.
					if err := session.SendRealtimeInput(genai.LiveRealtimeInput{
						AudioStreamEnd: true,
					}); err != nil {
						log.Printf("Voice: AudioStreamEnd error: %v", err)
					}
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

	// --- main loop: Gemini → speaker + tool execution ---
	var inToolLoop bool
	var bufferedTranscript string

	// activeCancelTool stores the cancel func for the current tool interaction.
	// Called by Interrupted/ToolCallCancellation handlers to abort in-flight work.
	var activeCancelMu sync.Mutex
	var activeCancelFn context.CancelFunc
	setActiveCancel := func(fn context.CancelFunc) {
		activeCancelMu.Lock()
		defer activeCancelMu.Unlock()
		if activeCancelFn != nil {
			activeCancelFn()
		}
		activeCancelFn = fn
	}
	defer setActiveCancel(nil)

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

		// ServerContent — audio data and/or transcription.
		if sc := msg.ServerContent; sc != nil {
			if inToolLoop {
				if sc.OutputTranscription != nil && sc.OutputTranscription.Text != "" {
					bufferedTranscript += sc.OutputTranscription.Text
				}
				if sc.TurnComplete {
					inToolLoop = false
					if bufferedTranscript != "" {
						log.Printf("Voice Navigator: %s", bufferedTranscript)
						bufferedTranscript = ""
					}
				}
				if sc.Interrupted {
					inToolLoop = false
					bufferedTranscript = ""
					setActiveCancel(nil)
					log.Println("Voice: interrupted by user")
				}
				continue
			}

			// Normal path: forward audio to speaker.
			if sc.ModelTurn != nil {
				for _, part := range sc.ModelTurn.Parts {
					if part.InlineData != nil && len(part.InlineData.Data) > 0 {
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
				log.Printf("Voice Navigator: %s", sc.OutputTranscription.Text)
			}

			if sc.Interrupted {
				log.Println("Voice: interrupted by user")
			}
			continue
		}

		// ToolCall — execute against the active voice tab's session.
		if tc := msg.ToolCall; tc != nil {
			tabID := h.getVoiceTabID()
			sess := h.getVoiceSession()

			inToolLoop = true

			// Per-interaction cancellable context: cancelled on Interrupted.
			toolCtx, toolCancelFn := context.WithCancel(ctx)
			setActiveCancel(toolCancelFn)

			go func(tc *genai.LiveServerToolCall, tabID int, sess *TabSession, toolCtx context.Context, toolCancelFn context.CancelFunc) {
				defer toolCancelFn()

				// Wire scroll for this interaction.
				sess.Navigator.SetScrollFunc(func(scrollCtx context.Context, direction string) error {
					return h.scrollVoice(scrollCtx, sess, tabID, direction)
				})
				defer sess.Navigator.SetScrollFunc(nil)

				// Schema gate — let goto through without a schema.
				needsSchema := false
				for _, fc := range tc.FunctionCalls {
					if fc.Name != "goto" && fc.Name != "rescan" {
						needsSchema = true
						break
					}
				}
				if needsSchema {
					select {
					case <-sess.SchemaReady:
					case <-time.After(schemaWaitTimeout):
						log.Printf("Voice: timed out waiting for schema (tab %d)", tabID)
						var errResponses []*genai.FunctionResponse
						for _, fc := range tc.FunctionCalls {
							errResponses = append(errResponses, &genai.FunctionResponse{
								ID:       fc.ID,
								Name:     fc.Name,
								Response: map[string]any{"output": "Error: page schema is still loading. Please wait."},
							})
						}
						_ = session.SendToolResponse(genai.LiveToolResponseInput{
							FunctionResponses: errResponses,
						})
						return
					case <-toolCtx.Done():
						log.Printf("Voice: schema wait cancelled by interruption")
						return
					}
				}

				var responses []*genai.FunctionResponse
				for _, fc := range tc.FunctionCalls {
					// Skip server-side tools (google_search etc.)
					switch fc.Name {
					case "ls", "cat", "act", "scroll", "goto", "rescan":
					default:
						log.Printf("Voice: skipping server-side tool %s", fc.Name)
						continue
					}

					log.Printf("Voice: tool %s(%v) (tab %d)", fc.Name, fc.Args, tabID)
					result, action := sess.Navigator.ExecuteTool(toolCtx, fc)
					log.Printf("Voice: result: %q", result)

					if action != nil {
						switch action.Action {
						case "goto":
							sess.ResetSchema()
							sess.Engine = mache.NewEngine()
							sess.Navigator.SetEngine(sess.Engine)
							h.sendGoto(tabID, action.Path)
							log.Printf("Voice: goto %s (tab %d)", action.Path, tabID)

							select {
							case <-sess.SchemaReady:
								result = fmt.Sprintf("Navigated to %s. Page loaded. Use ls('/') to explore.", action.Path)
							case <-time.After(schemaWaitTimeout):
								result = fmt.Sprintf("Navigated to %s but timed out waiting for page.", action.Path)
							case <-toolCtx.Done():
								result = "Navigation cancelled."
							}
						case "rescan":
							if action.Path != "" {
								// Targeted rescan: keep existing engine, zoom into zone.
								sess.ResetSchema()
								sess.RescanPath = action.Path
								h.sendRescan(tabID, action.MacheID)
								log.Printf("Voice: targeted rescan %q (mache_id %s, tab %d)", action.Path, action.MacheID, tabID)
							} else {
								// Full-page rescan: reset everything.
								sess.ResetSchema()
								sess.Engine = mache.NewEngine()
								sess.Navigator.SetEngine(sess.Engine)
								h.sendRescan(tabID, "")
								log.Printf("Voice: full rescan — waiting for fresh schema (tab %d)", tabID)
							}
							select {
							case <-sess.SchemaReady:
								if action.Path != "" {
									result = fmt.Sprintf("Zoomed into %s and discovered sub-zones. Use ls('%s') to see the detailed structure.", action.Path, action.Path)
								} else {
									result = "Page rescanned with fresh screenshot. Schema regenerated. Use ls('/') to see the updated structure."
								}
							case <-time.After(schemaWaitTimeout):
								result = "Rescan timed out waiting for new schema."
							case <-toolCtx.Done():
								result = "Rescan cancelled."
							}
						default:
							h.SendActionToExtension(tabID, action.MacheID, action.Action)
						}
					}

					responses = append(responses, &genai.FunctionResponse{
						ID:       fc.ID,
						Name:     fc.Name,
						Response: map[string]any{"output": result},
					})
				}

				if len(responses) > 0 {
					if err := session.SendToolResponse(genai.LiveToolResponseInput{
						FunctionResponses: responses,
					}); err != nil {
						log.Printf("Voice: SendToolResponse error: %v", err)
					}
				}
			}(tc, tabID, sess, toolCtx, toolCancelFn)

			continue
		}

		if msg.ToolCallCancellation != nil {
			log.Printf("Voice: tool call cancelled: %v", msg.ToolCallCancellation)
			inToolLoop = false
			bufferedTranscript = ""
			setActiveCancel(nil)
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
