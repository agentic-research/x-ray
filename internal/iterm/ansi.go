package iterm

import "regexp"

// ansiRe matches ANSI escape sequences:
//   - CSI sequences: \x1b[ ... (letter)  (e.g., colors, cursor movement)
//   - OSC sequences: \x1b] ... (\x07 or \x1b\\)  (e.g., window titles)
//   - Simple escapes: \x1b followed by a single character
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[^[\]]`)

// StripANSI removes ANSI escape sequences from terminal output.
func StripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}
