package iterm

import "testing"

func TestStripANSI(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "hello world", "hello world"},
		{"color", "\x1b[32mgreen\x1b[0m", "green"},
		{"bold+color", "\x1b[1;31mred bold\x1b[0m text", "red bold text"},
		{"cursor_move", "\x1b[2J\x1b[H", ""},
		{"osc_title", "\x1b]0;my title\x07rest", "rest"},
		{"osc_with_st", "\x1b]0;title\x1b\\rest", "rest"},
		{"mixed", "\x1b[36m$ \x1b[0mgit status\n\x1b[32mOn branch main\x1b[0m", "$ git status\nOn branch main"},
		{"empty", "", ""},
		{"no_escapes", "just plain text\nwith newlines\n", "just plain text\nwith newlines\n"},
		{"prompt_ps1", "\x1b[1;34m~/code\x1b[0m \x1b[1;32m❯\x1b[0m ", "~/code ❯ "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripANSI(tt.in)
			if got != tt.want {
				t.Errorf("StripANSI(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
