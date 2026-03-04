package navigator

import (
	"context"
	"fmt"
	"log"

	"google.golang.org/genai"
)

// liveSession abstracts genai.Session for testing.
type liveSession interface {
	SendClientContent(input genai.LiveClientContentInput) error
	SendToolResponse(input genai.LiveToolResponseInput) error
	Receive() (*genai.LiveServerMessage, error)
	Close() error
}

// liveConnectorFunc creates a new Live session.
type liveConnectorFunc func(ctx context.Context, model string, config *genai.LiveConnectConfig) (liveSession, error)

// GeminiLiveGenerator implements ContentGenerator using the Gemini Live API
// (WebSocket). It maintains a persistent session per HandleIntent call,
// sending only delta turns instead of full history on each iteration.
//
// Key advantages over REST (GeminiGenerator):
//   - Persistent connection — no HTTP overhead per tool-use iteration
//   - Delta-only messaging — session maintains context implicitly
//   - Interrupt support — Close() immediately terminates the session
type GeminiLiveGenerator struct {
	Client *genai.Client // used by the real connector
	Model  string

	// connect creates a Live session. Injected for testing.
	connect liveConnectorFunc

	// Per-intent state (reset by Close).
	session   liveSession
	turnsSent int
}

// defaultConnector wraps the real genai Live.Connect call, adapting *genai.Session
// to the liveSession interface.
func (g *GeminiLiveGenerator) defaultConnector(ctx context.Context, model string, config *genai.LiveConnectConfig) (liveSession, error) {
	sess, err := g.Client.Live.Connect(ctx, model, config)
	if err != nil {
		return nil, err
	}
	return realSession{sess}, nil
}

// realSession wraps *genai.Session to satisfy liveSession.
type realSession struct{ s *genai.Session }

func (r realSession) SendClientContent(input genai.LiveClientContentInput) error {
	return r.s.SendClientContent(input)
}

func (r realSession) SendToolResponse(input genai.LiveToolResponseInput) error {
	return r.s.SendToolResponse(input)
}
func (r realSession) Receive() (*genai.LiveServerMessage, error) { return r.s.Receive() }
func (r realSession) Close() error                               { return r.s.Close() }

// GenerateContent implements ContentGenerator. On the first call it creates a
// Live session and sends the full history. On subsequent calls it sends only
// the new turns (tool responses) as deltas.
func (g *GeminiLiveGenerator) GenerateContent(ctx context.Context, model string, history []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if model == "" {
		model = g.Model
	}

	// First call: create session.
	if g.session == nil {
		if err := g.initSession(ctx, model, config); err != nil {
			return nil, fmt.Errorf("live session init: %w", err)
		}
		// Send full history as initial context.
		if len(history) > 0 {
			err := g.session.SendClientContent(genai.LiveClientContentInput{
				Turns: history,
			})
			if err != nil {
				return nil, fmt.Errorf("send initial history: %w", err)
			}
		}
		g.turnsSent = len(history)
	} else {
		// Subsequent call: send only new turns (delta).
		newTurns := history[g.turnsSent:]
		if err := g.sendDelta(newTurns); err != nil {
			return nil, fmt.Errorf("send delta: %w", err)
		}
		g.turnsSent = len(history)
	}

	// Receive until we get a complete response.
	return g.receiveResponse(ctx)
}

// initSession creates the Live session with system instruction and tools.
func (g *GeminiLiveGenerator) initSession(ctx context.Context, model string, config *genai.GenerateContentConfig) error {
	liveConfig := &genai.LiveConnectConfig{
		ResponseModalities: []genai.Modality{genai.ModalityText},
	}
	if config != nil {
		liveConfig.SystemInstruction = config.SystemInstruction
		liveConfig.Tools = config.Tools
	}

	connector := g.connect
	if connector == nil {
		connector = g.defaultConnector
	}

	session, err := connector(ctx, model, liveConfig)
	if err != nil {
		return err
	}
	g.session = session

	// Wait for SetupComplete.
	for {
		msg, err := g.session.Receive()
		if err != nil {
			_ = g.session.Close()
			g.session = nil
			return fmt.Errorf("waiting for setup: %w", err)
		}
		if msg.SetupComplete != nil {
			log.Printf("Navigator Live: session established (model %s)", model)
			return nil
		}
	}
}

// sendDelta sends only the new turns since the last call.
// Tool responses (FunctionResponse parts) are sent via SendToolResponse.
// Other turns (user messages) are sent via SendClientContent.
func (g *GeminiLiveGenerator) sendDelta(newTurns []*genai.Content) error {
	for _, turn := range newTurns {
		// Check if this turn contains function responses.
		var funcResponses []*genai.FunctionResponse
		for _, part := range turn.Parts {
			if part.FunctionResponse != nil {
				funcResponses = append(funcResponses, part.FunctionResponse)
			}
		}

		if len(funcResponses) > 0 {
			err := g.session.SendToolResponse(genai.LiveToolResponseInput{
				FunctionResponses: funcResponses,
			})
			if err != nil {
				return err
			}
		} else {
			err := g.session.SendClientContent(genai.LiveClientContentInput{
				Turns: []*genai.Content{turn},
			})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// receiveResponse reads from the session until a complete turn or tool call.
func (g *GeminiLiveGenerator) receiveResponse(ctx context.Context) (*genai.GenerateContentResponse, error) {
	var textParts []*genai.Part

	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		msg, err := g.session.Receive()
		if err != nil {
			return nil, fmt.Errorf("receive: %w", err)
		}

		// Tool call → return as FunctionCall parts.
		if tc := msg.ToolCall; tc != nil {
			var parts []*genai.Part
			for _, fc := range tc.FunctionCalls {
				parts = append(parts, &genai.Part{FunctionCall: fc})
			}
			return &genai.GenerateContentResponse{
				Candidates: []*genai.Candidate{{
					Content: &genai.Content{Role: "model", Parts: parts},
				}},
			}, nil
		}

		// Server content → accumulate text parts.
		if sc := msg.ServerContent; sc != nil {
			if sc.ModelTurn != nil {
				for _, part := range sc.ModelTurn.Parts {
					if part.Text != "" {
						textParts = append(textParts, &genai.Part{Text: part.Text})
					}
				}
			}
			if sc.TurnComplete {
				if len(textParts) == 0 {
					textParts = []*genai.Part{{Text: ""}}
				}
				return &genai.GenerateContentResponse{
					Candidates: []*genai.Candidate{{
						Content: &genai.Content{Role: "model", Parts: textParts},
					}},
				}, nil
			}
		}

		// Ignore other message types (ToolCallCancellation, etc.)
	}
}

// Close tears down the Live session. Call between HandleIntent invocations.
func (g *GeminiLiveGenerator) Close() {
	if g.session != nil {
		_ = g.session.Close()
		g.session = nil
	}
	g.turnsSent = 0
}
