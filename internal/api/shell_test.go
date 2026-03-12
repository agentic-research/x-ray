package api

import "testing"

func TestFormatToolAsShell(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		args     map[string]any
		want     string
	}{
		// ls
		{name: "ls with path", toolName: "ls", args: map[string]any{"path": "/main/stories"}, want: "ls /main/stories"},
		{name: "ls empty path defaults to /", toolName: "ls", args: map[string]any{}, want: "ls /"},
		{name: "ls nil args", toolName: "ls", args: nil, want: "ls /"},

		// cat
		{name: "cat", toolName: "cat", args: map[string]any{"path": "/main/stories/children"}, want: "cat /main/stories/children"},

		// stat
		{name: "stat", toolName: "stat", args: map[string]any{"path": "/header/nav"}, want: "stat /header/nav"},

		// grep
		{name: "grep with path", toolName: "grep", args: map[string]any{"pattern": "login", "path": "/main"}, want: `grep "login" /main`},
		{name: "grep no path", toolName: "grep", args: map[string]any{"pattern": "submit"}, want: `grep "submit"`},

		// act
		{name: "act click", toolName: "act", args: map[string]any{"path": "/main/stories/_c/1", "action": "click"}, want: "act click /main/stories/_c/1"},
		{name: "act type with payload", toolName: "act", args: map[string]any{"path": "/main/search/_c/1", "action": "type", "payload": "hello world"}, want: `act type /main/search/_c/1 "hello world"`},
		{name: "act type empty payload", toolName: "act", args: map[string]any{"path": "/main/input", "action": "type"}, want: "act type /main/input"},

		// scroll
		{name: "scroll down", toolName: "scroll", args: map[string]any{"direction": "down"}, want: "scroll down"},
		{name: "scroll up", toolName: "scroll", args: map[string]any{"direction": "up"}, want: "scroll up"},

		// goto
		{name: "goto", toolName: "goto", args: map[string]any{"url": "https://example.com"}, want: "goto https://example.com"},

		// rescan
		{name: "rescan full", toolName: "rescan", args: map[string]any{}, want: "rescan"},
		{name: "rescan zoom", toolName: "rescan", args: map[string]any{"mache_id": "mache-42"}, want: "rescan --zoom mache-42"},

		// list_tabs
		{name: "list_tabs", toolName: "list_tabs", args: map[string]any{}, want: "tabs"},

		// switch_tab
		{name: "switch_tab", toolName: "switch_tab", args: map[string]any{"tab_id": float64(5)}, want: "switch_tab 5"},

		// new_tab
		{name: "new_tab", toolName: "new_tab", args: map[string]any{"url": "https://reddit.com"}, want: "new_tab https://reddit.com"},

		// new_window
		{name: "new_window", toolName: "new_window", args: map[string]any{"url": "https://github.com"}, want: "new_window https://github.com"},

		// unknown
		{name: "unknown tool", toolName: "custom_tool", args: map[string]any{}, want: "custom_tool"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatToolAsShell(tt.toolName, tt.args)
			if got != tt.want {
				t.Errorf("formatToolAsShell(%q, %v)\n  got:  %q\n  want: %q", tt.toolName, tt.args, got, tt.want)
			}
		})
	}
}

// TestFormatToolAsShell_ActUsesPayloadNotValue verifies the bug fix:
// the act tool declares its parameter as "payload" (not "value"), and
// formatToolAsShell must use the same key.
func TestFormatToolAsShell_ActUsesPayloadNotValue(t *testing.T) {
	// "value" key should NOT produce quoted text — only "payload" should.
	argsWithValue := map[string]any{"path": "/input", "action": "type", "value": "typed text"}
	got := formatToolAsShell("act", argsWithValue)
	if got != "act type /input" {
		t.Errorf("args with 'value' key should be ignored, got %q", got)
	}

	argsWithPayload := map[string]any{"path": "/input", "action": "type", "payload": "typed text"}
	got = formatToolAsShell("act", argsWithPayload)
	want := `act type /input "typed text"`
	if got != want {
		t.Errorf("args with 'payload' key:\n  got:  %q\n  want: %q", got, want)
	}
}
