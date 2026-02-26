package api

import (
	"encoding/json"
	"testing"
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

func TestSessionFreshHasNoSchema(t *testing.T) {
	h := NewHandler(&stubCartographer{}, nil, nil, "test", "test-live", "")
	sess := h.getSession(99)

	if sess.Engine.HasSchema() {
		t.Error("fresh session should not have schema")
	}
}

func TestSessionApplySchemaWorks(t *testing.T) {
	h := NewHandler(&stubCartographer{}, nil, nil, "test", "test-live", "")
	sess := h.getSession(99)

	schema := `{"mounts":[{"virtual_path":"/main","mache_id":"mache-1","description":"main"}]}`
	if err := sess.Engine.ApplySchema(schema); err != nil {
		t.Fatalf("ApplySchema failed: %v", err)
	}

	if !sess.Engine.HasSchema() {
		t.Error("session should have schema after apply")
	}
}
