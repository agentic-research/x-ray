package navigator

import (
	"context"
	"errors"
	"sync"
	"testing"

	"google.golang.org/genai"
)

// Compile-time interface compliance.
var _ ContentGenerator = (*GeminiLiveGenerator)(nil)

// mockLiveSession implements liveSession for testing.
type mockLiveSession struct {
	mu        sync.Mutex
	sent      []any // records sent messages (LiveClientContentInput or LiveToolResponseInput)
	responses []*genai.LiveServerMessage
	respIdx   int
	closed    bool
	closeErr  error
}

func (m *mockLiveSession) SendClientContent(input genai.LiveClientContentInput) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, input)
	return nil
}

func (m *mockLiveSession) SendToolResponse(input genai.LiveToolResponseInput) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, input)
	return nil
}

func (m *mockLiveSession) Receive() (*genai.LiveServerMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.respIdx >= len(m.responses) {
		return nil, errors.New("no more messages")
	}
	msg := m.responses[m.respIdx]
	m.respIdx++
	return msg, nil
}

func (m *mockLiveSession) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return m.closeErr
}

// mockLiveConnector returns a factory that produces mockLiveSessions.
func mockLiveConnector(session *mockLiveSession) liveConnectorFunc {
	return func(ctx context.Context, model string, config *genai.LiveConnectConfig) (liveSession, error) {
		return session, nil
	}
}

func TestGeminiLive_TextResponse(t *testing.T) {
	// Model responds with a text turn.
	session := &mockLiveSession{
		responses: []*genai.LiveServerMessage{
			{SetupComplete: &genai.LiveServerSetupComplete{}},
			{ServerContent: &genai.LiveServerContent{
				ModelTurn: &genai.Content{
					Role:  "model",
					Parts: []*genai.Part{{Text: "The page shows a login form."}},
				},
				TurnComplete: true,
			}},
		},
	}

	gen := &GeminiLiveGenerator{
		Model:   "gemini-2.5-flash",
		connect: mockLiveConnector(session),
	}

	history := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{{Text: "What do you see?"}}},
	}
	config := &genai.GenerateContentConfig{
		SystemInstruction: &genai.Content{Parts: []*genai.Part{{Text: "You are a navigator."}}},
	}

	resp, err := gen.GenerateContent(context.Background(), "", history, config)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(resp.Candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(resp.Candidates))
	}
	parts := resp.Candidates[0].Content.Parts
	if len(parts) != 1 || parts[0].Text != "The page shows a login form." {
		t.Errorf("unexpected parts: %+v", parts)
	}
}

func TestGeminiLive_ToolCall(t *testing.T) {
	// Model responds with a tool call.
	session := &mockLiveSession{
		responses: []*genai.LiveServerMessage{
			{SetupComplete: &genai.LiveServerSetupComplete{}},
			{ToolCall: &genai.LiveServerToolCall{
				FunctionCalls: []*genai.FunctionCall{
					{ID: "call-1", Name: "ls", Args: map[string]any{"path": "/"}},
				},
			}},
		},
	}

	gen := &GeminiLiveGenerator{
		Model:   "gemini-2.5-flash",
		connect: mockLiveConnector(session),
	}

	history := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{{Text: "list the root"}}},
	}

	resp, err := gen.GenerateContent(context.Background(), "", history, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(resp.Candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(resp.Candidates))
	}
	parts := resp.Candidates[0].Content.Parts
	if len(parts) != 1 {
		t.Fatalf("expected 1 part (FunctionCall), got %d", len(parts))
	}
	fc := parts[0].FunctionCall
	if fc == nil || fc.Name != "ls" {
		t.Errorf("expected FunctionCall ls, got %+v", parts[0])
	}
}

func TestGeminiLive_DeltaTracking(t *testing.T) {
	// Second call should only send the new tool response, not resend history.
	session := &mockLiveSession{
		responses: []*genai.LiveServerMessage{
			// First call: setup + tool call
			{SetupComplete: &genai.LiveServerSetupComplete{}},
			{ToolCall: &genai.LiveServerToolCall{
				FunctionCalls: []*genai.FunctionCall{
					{ID: "call-1", Name: "ls", Args: map[string]any{"path": "/"}},
				},
			}},
			// Second call: text response
			{ServerContent: &genai.LiveServerContent{
				ModelTurn: &genai.Content{
					Role:  "model",
					Parts: []*genai.Part{{Text: "I see browser/ and iterm/."}},
				},
				TurnComplete: true,
			}},
		},
	}

	gen := &GeminiLiveGenerator{
		Model:   "gemini-2.5-flash",
		connect: mockLiveConnector(session),
	}

	// First call: user message
	history1 := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{{Text: "list root"}}},
	}
	_, err := gen.GenerateContent(context.Background(), "", history1, nil)
	if err != nil {
		t.Fatalf("call 1 error: %v", err)
	}

	// Second call: Navigator appends model tool call + tool response to history
	history2 := append(history1,
		&genai.Content{Role: "model", Parts: []*genai.Part{
			{FunctionCall: &genai.FunctionCall{ID: "call-1", Name: "ls", Args: map[string]any{"path": "/"}}},
		}},
		&genai.Content{Role: "user", Parts: []*genai.Part{
			{FunctionResponse: &genai.FunctionResponse{ID: "call-1", Name: "ls", Response: map[string]any{"output": "browser/\niterm/"}}},
		}},
	)

	resp, err := gen.GenerateContent(context.Background(), "", history2, nil)
	if err != nil {
		t.Fatalf("call 2 error: %v", err)
	}

	// Verify the second call sent deltas: model turn (ClientContent) + tool response.
	session.mu.Lock()
	defer session.mu.Unlock()

	// sent[0] = initial history, sent[1] = model turn delta, sent[2] = tool response delta
	if len(session.sent) < 3 {
		t.Fatalf("expected at least 3 sends, got %d", len(session.sent))
	}
	// The third send should be a LiveToolResponseInput (tool response)
	if _, ok := session.sent[2].(genai.LiveToolResponseInput); !ok {
		t.Errorf("expected third send to be LiveToolResponseInput, got %T", session.sent[2])
	}

	// Final response should have text
	if len(resp.Candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(resp.Candidates))
	}
	text := resp.Candidates[0].Content.Parts[0].Text
	if text != "I see browser/ and iterm/." {
		t.Errorf("unexpected text: %s", text)
	}
}

func TestGeminiLive_ContextCancellation(t *testing.T) {
	// Verify that Close is called when context is cancelled.
	session := &mockLiveSession{
		responses: []*genai.LiveServerMessage{
			{SetupComplete: &genai.LiveServerSetupComplete{}},
			// No more messages — Receive will block, but context cancels.
		},
	}

	gen := &GeminiLiveGenerator{
		Model:   "gemini-2.5-flash",
		connect: mockLiveConnector(session),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	history := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{{Text: "hello"}}},
	}
	_, err := gen.GenerateContent(ctx, "", history, nil)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestGeminiLive_CloseResetsSession(t *testing.T) {
	session := &mockLiveSession{
		responses: []*genai.LiveServerMessage{
			{SetupComplete: &genai.LiveServerSetupComplete{}},
			{ServerContent: &genai.LiveServerContent{
				ModelTurn: &genai.Content{
					Role:  "model",
					Parts: []*genai.Part{{Text: "done"}},
				},
				TurnComplete: true,
			}},
		},
	}

	gen := &GeminiLiveGenerator{
		Model:   "gemini-2.5-flash",
		connect: mockLiveConnector(session),
	}

	history := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{{Text: "hi"}}},
	}
	_, err := gen.GenerateContent(context.Background(), "", history, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Session should be active
	if gen.session == nil {
		t.Fatal("expected session to be set")
	}

	// Close should reset
	gen.Close()
	if gen.session != nil {
		t.Error("expected session to be nil after Close")
	}
	if gen.turnsSent != 0 {
		t.Error("expected turnsSent to be 0 after Close")
	}

	session.mu.Lock()
	closed := session.closed
	session.mu.Unlock()
	if !closed {
		t.Error("expected session.Close to have been called")
	}
}
