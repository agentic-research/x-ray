package api

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
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
func (h *Handler) HandleVoice(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Voice: WebSocket upgrade failed: %v", err)
		return
	}
	defer func() { _ = conn.Close() }()

	ctx := r.Context()
	log.Println("Voice: browser connected")

	// Wait for schema to be ready (extension must send DOM_SNAPSHOT first).
	if !h.Engine.HasSchema() {
		sendVoiceJSON(conn, nil, voiceMessage{Type: "waiting", Text: "Waiting for page schema... Click the X-Ray extension icon first."})
		log.Println("Voice: waiting for schema...")
		for !h.Engine.HasSchema() {
			time.Sleep(500 * time.Millisecond)
			// Check if the browser disconnected while waiting.
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				log.Println("Voice: browser disconnected while waiting for schema")
				return
			}
		}
		log.Println("Voice: schema ready, proceeding")
		sendVoiceJSON(conn, nil, voiceMessage{Type: "schema_ready", Text: "Schema loaded. Connecting to Gemini..."})
	}

	// Open Gemini Live session with voice-optimized prompt.
	session, err := h.Client.Live.Connect(ctx, h.LiveModel, &genai.LiveConnectConfig{
		ResponseModalities: []genai.Modality{genai.ModalityAudio},
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{{Text: voiceSystemPrompt}},
		},
		Tools:                    navigator.ToolDefinitions(),
		InputAudioTranscription:  &genai.AudioTranscriptionConfig{},
		OutputAudioTranscription: &genai.AudioTranscriptionConfig{},
	})
	if err != nil {
		log.Printf("Voice: Live API connect failed: %v", err)
		sendVoiceJSON(conn, nil, voiceMessage{Type: "error", Text: "Live API connect failed: " + err.Error()})
		return
	}
	defer func() { _ = session.Close() }()
	log.Println("Voice: Gemini Live session established")

	// Mutex protects writes to the browser WS (two goroutines: gemini→browser
	// and tool-response paths both write).
	var wsMu sync.Mutex

	// --- goroutine 1: browser → Gemini (audio chunks) ---
	go func() {
		for {
			msgType, data, err := conn.ReadMessage()
			if err != nil {
				log.Printf("Voice: browser read error: %v", err)
				_ = session.Close()
				return
			}
			switch msgType {
			case websocket.BinaryMessage:
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
				var cmd struct {
					Type string `json:"type"`
				}
				if json.Unmarshal(data, &cmd) == nil && cmd.Type == "mic_stop" {
					log.Println("Voice: mic released, sending AudioStreamEnd")
					if err := session.SendRealtimeInput(genai.LiveRealtimeInput{
						AudioStreamEnd: true,
					}); err != nil {
						log.Printf("Voice: AudioStreamEnd error: %v", err)
					}
				}
			}
		}
	}()

	// --- goroutine 2 (this goroutine): Gemini → browser ---
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
			// Forward audio parts as binary frames.
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
				sendVoiceJSON(conn, &wsMu, voiceMessage{
					Type: "input_transcription", Text: sc.InputTranscription.Text,
				})
			}
			if sc.OutputTranscription != nil && sc.OutputTranscription.Text != "" {
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

		// ToolCall — execute ls/cat/act locally.
		if tc := msg.ToolCall; tc != nil {
			var responses []*genai.FunctionResponse
			for _, fc := range tc.FunctionCalls {
				log.Printf("Voice: tool call %s(%v)", fc.Name, fc.Args)
				result, action := h.Navigator.ExecuteTool(fc)
				log.Printf("Voice: tool result: %q", result)

				// If act() returned an action, dispatch to the extension.
				if action != nil {
					h.SendActionToExtension(action.MacheID, action.Action)
					sendVoiceJSON(conn, &wsMu, voiceMessage{
						Type:    MsgExecuteAction,
						MacheID: action.MacheID,
						Action:  action.Action,
					})
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
			continue
		}
	}

	log.Println("Voice: session ended")
}

const voiceSystemPrompt = `You navigate web pages via a semantic filesystem. You are a VOICE agent — be silent while working, only speak to announce results.

Tools: ls(path), cat(path), act(path, action)

STRICT RULES:
1. NEVER speak while using tools. Do all exploration silently.
2. Only speak AFTER you have completed the action or determined you cannot.
3. Responses must be ONE short sentence. Example: "Done, clicked the first story."
4. Maximum 5 tool calls per request. If you can't find it by then, say "I couldn't find that element."
5. NEVER narrate your process. No "I'm now looking at..." or "Let me check...". SILENCE until done.

NAVIGATION PATTERN:
- ls("/") to see zones, then ls the relevant zone.
- If a zone has "children" file: cat it, then act("/zone/_c/mache-ID", "click")
- If a zone has NO children (only description + mache_id): the zone itself IS the clickable element. Use act("/zone/path", "click") directly.
- Zone descriptions tell you what they contain. Use them to pick the right zone fast.

Example — "click the first story":
  ls("/") → ls("/main/news_feed") → cat("/main/news_feed/children") → act("/main/news_feed/_c/mache-13", "click")
  Then say: "Done."

Example — "click next page" (pagination zone has no children):
  ls("/") → ls("/main") → act("/main/pagination", "click")
  Then say: "Done, clicked next page."`

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
