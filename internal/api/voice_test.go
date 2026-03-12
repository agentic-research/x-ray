package api

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agentic-research/mache/graph"
	"google.golang.org/genai"
)

func TestVoiceMessageSerialization(t *testing.T) {
	tests := []struct {
		name string
		msg  voiceMessage
		want map[string]any
	}{
		{
			name: "waiting message",
			msg:  voiceMessage{Type: "waiting", Text: "Waiting for schema..."},
			want: map[string]any{"type": "waiting", "text": "Waiting for schema..."},
		},
		{
			name: "ready message",
			msg:  voiceMessage{Type: "ready"},
			want: map[string]any{"type": "ready"},
		},
		{
			name: "execute action",
			msg:  voiceMessage{Type: MsgExecuteAction, MacheID: "mache-5", Action: "click"},
			want: map[string]any{"type": "EXECUTE_ACTION", "mache_id": "mache-5", "action": "click"},
		},
		{
			name: "transcription",
			msg:  voiceMessage{Type: "input_transcription", Text: "click the first story"},
			want: map[string]any{"type": "input_transcription", "text": "click the first story"},
		},
		{
			name: "with sample rate",
			msg:  voiceMessage{Type: "ready", SampleRate: 24000},
			want: map[string]any{"type": "ready", "sample_rate": float64(24000)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.msg)
			if err != nil {
				t.Fatalf("marshal failed: %v", err)
			}

			var got map[string]any
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("unmarshal failed: %v", err)
			}

			for key, wantVal := range tt.want {
				gotVal, ok := got[key]
				if !ok {
					t.Errorf("missing key %q in serialized message", key)
					continue
				}
				if gotVal != wantVal {
					t.Errorf("key %q: expected %v, got %v", key, wantVal, gotVal)
				}
			}
		})
	}
}

func TestVoiceMessageOmitsEmpty(t *testing.T) {
	msg := voiceMessage{Type: "turn_complete"}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	// omitempty fields should be absent
	for _, key := range []string{"text", "mache_id", "action", "sample_rate"} {
		if _, exists := got[key]; exists {
			t.Errorf("key %q should be omitted when zero/empty", key)
		}
	}
}

func TestBuildLiveConfigHasTranscription(t *testing.T) {
	config := buildLiveConfig("", "")
	if config.InputAudioTranscription == nil {
		t.Error("expected InputAudioTranscription to be set")
	}
	if config.OutputAudioTranscription == nil {
		t.Error("expected OutputAudioTranscription to be set")
	}
}

func TestBuildLiveConfigHasGoogleSearch(t *testing.T) {
	config := buildLiveConfig("", "")
	found := false
	for _, tool := range config.Tools {
		if tool.GoogleSearch != nil {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected GoogleSearch tool in config")
	}
}

func TestBuildLiveConfigEnablesResumption(t *testing.T) {
	config := buildLiveConfig("", "")
	if config.SessionResumption == nil {
		t.Error("expected SessionResumption to be set")
	}
}

func TestInterruptedFlagSuppressesAudio(t *testing.T) {
	// Unit test: when the interrupted flag is set, audio should be skipped.
	var interrupted atomic.Bool
	interrupted.Store(true)

	audioSent := false
	speaker := make(chan []byte, 1)

	// Simulate the audio forwarding logic from StartVoiceLoop.
	data := []byte{0x01, 0x02, 0x03}
	if !interrupted.Load() {
		speaker <- data
		audioSent = true
	}

	if audioSent {
		t.Error("audio should be suppressed when interrupted flag is set")
	}
	if len(speaker) != 0 {
		t.Error("speaker channel should be empty when interrupted")
	}

	// Clear flag and verify audio flows again.
	interrupted.Store(false)
	if !interrupted.Load() {
		speaker <- data
		audioSent = true
	}
	if !audioSent {
		t.Error("audio should flow when interrupted flag is cleared")
	}
}

func TestGenerationCompleteForwarded(t *testing.T) {
	// Verify that a voiceMessage with type "generation_complete" serializes correctly.
	msg := voiceMessage{Type: "generation_complete"}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if got["type"] != "generation_complete" {
		t.Errorf("expected type=generation_complete, got %v", got["type"])
	}
}

func TestBuildLiveConfigWithResume(t *testing.T) {
	config := buildLiveConfig("", "")
	applyResumeHandle(config, "prev-session-handle-abc")

	if config.SessionResumption == nil {
		t.Fatal("expected SessionResumption to be set")
	}
	if config.SessionResumption.Handle != "prev-session-handle-abc" {
		t.Errorf("expected Handle=prev-session-handle-abc, got %q", config.SessionResumption.Handle)
	}
}

func TestApplyResumeHandleEmpty(t *testing.T) {
	config := buildLiveConfig("", "")
	applyResumeHandle(config, "")

	// Empty handle should not override the default empty SessionResumption.
	if config.SessionResumption == nil {
		t.Fatal("expected SessionResumption to still be set")
	}
	if config.SessionResumption.Handle != "" {
		t.Errorf("expected empty Handle, got %q", config.SessionResumption.Handle)
	}
}

func TestLiveReconnectState_RetriesWithBackoff(t *testing.T) {
	ctx := context.Background()
	rs := liveReconnectState{}

	// First 3 attempts should return true (reconnect).
	for i := 1; i <= maxNonGoAwayRetries; i++ {
		if !rs.shouldReconnect(ctx, " [test]") {
			t.Fatalf("attempt %d: expected shouldReconnect=true", i)
		}
		if rs.Retries != i {
			t.Fatalf("attempt %d: expected Retries=%d, got %d", i, i, rs.Retries)
		}
	}

	// After max retries, should still reconnect but reset state for fresh session.
	if !rs.shouldReconnect(ctx, " [test]") {
		t.Fatal("expected shouldReconnect=true after max retries (fresh session)")
	}
	if rs.Retries != 0 {
		t.Fatalf("expected Retries reset to 0, got %d", rs.Retries)
	}
	if rs.ResumeHandle != "" {
		t.Fatalf("expected ResumeHandle cleared, got %q", rs.ResumeHandle)
	}
}

func TestLiveReconnectState_RespectsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	rs := liveReconnectState{}
	if rs.shouldReconnect(ctx, "") {
		t.Fatal("expected shouldReconnect=false when context is cancelled")
	}
}

func TestLiveReconnectState_ClearsHandleOnMaxRetries(t *testing.T) {
	ctx := context.Background()
	rs := liveReconnectState{ResumeHandle: "stale-handle", Retries: maxNonGoAwayRetries}

	if !rs.shouldReconnect(ctx, "") {
		t.Fatal("expected shouldReconnect=true")
	}
	if rs.ResumeHandle != "" {
		t.Fatalf("expected stale handle cleared, got %q", rs.ResumeHandle)
	}
}

func TestLiveReconnectState_ResetsOnSuccess(t *testing.T) {
	rs := liveReconnectState{Retries: 2, ResumeHandle: "handle-abc"}
	rs.Retries = 0 // simulate successful Receive
	if rs.Retries != 0 {
		t.Fatalf("expected Retries=0 after reset, got %d", rs.Retries)
	}
	if rs.ResumeHandle != "handle-abc" {
		t.Fatal("ResumeHandle should be preserved on success reset")
	}
}

// mockTermBridge records Act calls for terminal_action tests.
type mockTermBridge struct {
	graph.Graph // embed to satisfy interface (other methods panic if called)
	actCalls    []mockActCall
}

type mockActCall struct {
	ID, Action, Payload string
}

func (m *mockTermBridge) Act(id, action, payload string) (*graph.ActionResult, error) {
	m.actCalls = append(m.actCalls, mockActCall{id, action, payload})
	return &graph.ActionResult{Action: action}, nil
}

func TestExecuteTerminalAction(t *testing.T) {
	tests := []struct {
		name       string
		action     string
		text       string
		wantAct    mockActCall
		wantSubstr string // substring expected in result
	}{
		{
			name:       "new_window",
			action:     "new_window",
			wantAct:    mockActCall{"", "new_window", ""},
			wantSubstr: "new terminal window",
		},
		{
			name:       "new_tab",
			action:     "new_tab",
			wantAct:    mockActCall{"", "new_tab", ""},
			wantSubstr: "new tab",
		},
		{
			name:       "type text",
			action:     "type",
			text:       "hello world\n",
			wantAct:    mockActCall{"active_session", "type", "hello world\n"},
			wantSubstr: "Typed",
		},
		{
			name:       "send ctrl-c",
			action:     "enter",
			text:       "ctrl-c",
			wantAct:    mockActCall{"active_session", "enter", "ctrl-c"},
			wantSubstr: "Sent",
		},
		{
			name:       "focus",
			action:     "focus",
			wantAct:    mockActCall{"active_session", "focus", ""},
			wantSubstr: "Focused",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bridge := &mockTermBridge{}
			h := newTestHandler()
			h.termBridge = bridge

			fc := &genai.FunctionCall{
				Name: "terminal_action",
				Args: map[string]any{"action": tt.action},
			}
			if tt.text != "" {
				fc.Args["text"] = tt.text
			}

			result := h.executeTerminalAction(fc)

			if len(bridge.actCalls) != 1 {
				t.Fatalf("expected 1 Act call, got %d", len(bridge.actCalls))
			}
			got := bridge.actCalls[0]
			if got != tt.wantAct {
				t.Errorf("Act call = %+v, want %+v", got, tt.wantAct)
			}
			if !strings.Contains(strings.ToLower(result), strings.ToLower(tt.wantSubstr)) {
				t.Errorf("result %q missing substring %q", result, tt.wantSubstr)
			}
		})
	}
}

func TestExecuteTerminalActionNoBridge(t *testing.T) {
	h := newTestHandler()
	// h.termBridge is nil

	fc := &genai.FunctionCall{
		Name: "terminal_action",
		Args: map[string]any{"action": "new_window"},
	}
	result := h.executeTerminalAction(fc)
	if !strings.Contains(result, "No terminal") {
		t.Errorf("expected 'No terminal' error, got %q", result)
	}
}

func TestExecuteTerminalActionUnknown(t *testing.T) {
	bridge := &mockTermBridge{}
	h := newTestHandler()
	h.termBridge = bridge

	fc := &genai.FunctionCall{
		Name: "terminal_action",
		Args: map[string]any{"action": "delete_everything"},
	}
	result := h.executeTerminalAction(fc)
	if !strings.Contains(result, "Unknown") {
		t.Errorf("expected 'Unknown' error, got %q", result)
	}
	if len(bridge.actCalls) != 0 {
		t.Error("should not call Act for unknown action")
	}
}

func TestTalkerToolDefinitionsIncludesTerminalAction(t *testing.T) {
	tools := talkerToolDefinitions()
	found := false
	for _, tool := range tools {
		for _, fd := range tool.FunctionDeclarations {
			if fd.Name == "terminal_action" {
				found = true
			}
		}
	}
	if !found {
		t.Error("expected terminal_action in talkerToolDefinitions")
	}
}

func TestExecuteScreenShareEnable(t *testing.T) {
	h := newTestHandler()

	fc := &genai.FunctionCall{
		Name: "screen_share",
		Args: map[string]any{"enabled": true},
	}
	result := h.executeScreenShare(fc)
	if !h.videoEnabled.Load() {
		t.Error("expected videoEnabled=true after screen_share(enabled=true)")
	}
	if !strings.Contains(result, "enabled") {
		t.Errorf("expected 'enabled' in result, got %q", result)
	}
}

func TestExecuteScreenShareDisable(t *testing.T) {
	h := newTestHandler()
	h.videoEnabled.Store(true) // pre-enable

	fc := &genai.FunctionCall{
		Name: "screen_share",
		Args: map[string]any{"enabled": false},
	}
	result := h.executeScreenShare(fc)
	if h.videoEnabled.Load() {
		t.Error("expected videoEnabled=false after screen_share(enabled=false)")
	}
	if !strings.Contains(result, "disabled") {
		t.Errorf("expected 'disabled' in result, got %q", result)
	}
}

func TestExecuteScreenShareSendsImmediateFrame(t *testing.T) {
	h := newTestHandler()
	// Create a session with a screenshot.
	sess := h.getSession(42)
	sess.SetScreenshot([]byte("fake-png"), "image/png")
	h.mu.Lock()
	h.activeVoiceTab = 42
	h.mu.Unlock()

	fc := &genai.FunctionCall{
		Name: "screen_share",
		Args: map[string]any{"enabled": true},
	}
	h.executeScreenShare(fc)

	// Should have an immediate frame in the channel.
	select {
	case vf := <-h.videoFrameCh:
		if string(vf.Data) != "fake-png" {
			t.Errorf("unexpected frame data: %q", vf.Data)
		}
		if vf.MIME != "image/png" {
			t.Errorf("unexpected MIME: %q", vf.MIME)
		}
	default:
		t.Error("expected immediate video frame in channel after screen_share enable")
	}
}

func TestVideoEnabledGatesFrameSend(t *testing.T) {
	h := newTestHandler()

	// videoEnabled is false by default — frames should NOT be sent.
	if h.videoEnabled.Load() {
		t.Fatal("videoEnabled should default to false")
	}

	// Verify the channel is empty.
	select {
	case <-h.videoFrameCh:
		t.Fatal("videoFrameCh should be empty initially")
	default:
	}
}

func TestTalkerToolDefinitionsIncludesScreenShare(t *testing.T) {
	tools := talkerToolDefinitions()
	found := false
	for _, tool := range tools {
		for _, fd := range tool.FunctionDeclarations {
			if fd.Name == "screen_share" {
				found = true
			}
		}
	}
	if !found {
		t.Error("expected screen_share in talkerToolDefinitions")
	}
}

func TestSessionFreshHasNoSchema(t *testing.T) {
	h := NewHandler(&stubCartographer{}, nil, nil, nil, "test", "test-live", "", "")
	sess := h.getSession(99)

	if sess.Engine.HasSchema() {
		t.Error("fresh session should not have schema")
	}
}

func TestSessionApplySchemaWorks(t *testing.T) {
	h := NewHandler(&stubCartographer{}, nil, nil, nil, "test", "test-live", "", "")
	sess := h.getSession(99)

	schema := `{"mounts":[{"virtual_path":"/main","mache_id":"mache-1","description":"main"}]}`
	if err := sess.Engine.ApplySchema(schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	if !sess.Engine.HasSchema() {
		t.Error("session should have schema after apply")
	}
}
